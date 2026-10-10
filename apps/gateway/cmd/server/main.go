package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

type dependencyResult struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type contextKey string

const identityContextKey contextKey = "identityContext"

type identityContext struct {
	TenantID      string                 `json:"tenantId"`
	TenantSlug    string                 `json:"tenantSlug"`
	TenantName    string                 `json:"tenantName"`
	Actor         string                 `json:"actor"`
	AuthMethod    string                 `json:"authMethod"`
	APIKeyID      string                 `json:"apiKeyId,omitempty"`
	JWTSubject    string                 `json:"jwtSubject,omitempty"`
	Roles         []string               `json:"roles"`
	Claims        map[string]any         `json:"claims,omitempty"`
	Authenticated string                 `json:"authenticatedAt"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type healthResponse struct {
	Service      string             `json:"service"`
	Status       string             `json:"status"`
	TimestampUTC string             `json:"timestampUtc"`
	Dependencies []dependencyResult `json:"dependencies"`
}

type gatewayApp struct {
	db         *sql.DB
	redis      *redis.Client
	httpClient *http.Client
	usageStore contextUsageStore
}

func main() {
	databaseURL := env("DATABASE_URL", "postgres://suez:suez@postgres:5432/suez?sslmode=disable")
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	redisURL := env("REDIS_URL", "redis://redis:6379/0")
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatal(err)
	}
	redisClient := redis.NewClient(redisOptions)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatal(err)
	}
	defer redisClient.Close()

	app := &gatewayApp{
		db:         db,
		redis:      redisClient,
		httpClient: &http.Client{Timeout: 45 * time.Second},
		usageStore: &postgresUsageStore{db: db},
	}

	port := env("PORT", "8080")
	mux := http.NewServeMux()

	mux.HandleFunc("/", app.handleRoot)
	mux.HandleFunc("/healthz", app.handleHealth)
	protected := withIdentityResolver(http.HandlerFunc(app.handleProtected))
	mux.Handle("/v1/identity", protected)
	mux.Handle("/v1/chat/completions", protected)
	mux.Handle("/v1/tools/execute", protected)
	mux.Handle("/v1/observability", protected)
	mux.Handle("/v1/context/usage", protected)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           withCORS(loggingMiddleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on :%s", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *gatewayApp) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "gateway",
		"status":  "running",
		"phase":   "phase-8",
		"routes": []string{
			"/healthz",
			"/v1/identity",
		"/v1/chat/completions",
		"/v1/tools/execute",
		"/v1/observability",
		"/v1/context/usage",
	},
	})
}

func (a *gatewayApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	results := []dependencyResult{
		checkTCP("postgres", env("POSTGRES_ADDR", "postgres:5432")),
		checkTCP("redis", env("REDIS_ADDR", "redis:6379")),
		checkHTTP("opa", env("OPA_HEALTH_URL", "http://opa:8181/v1/data/suez/authz/allow")),
		checkHTTP("control-plane", env("CONTROL_PLANE_HEALTH_URL", "http://control-plane:8081/healthz")),
		checkHTTP("policy-llm", env("POLICY_LLM_URL", "http://policy-llm:11434/api/tags")),
	}

	statusCode := http.StatusOK
	status := "ok"
	for _, result := range results {
		if result.Status != "ok" {
			statusCode = http.StatusServiceUnavailable
			status = "degraded"
			break
		}
	}

	writeJSON(w, statusCode, healthResponse{
		Service:      "gateway",
		Status:       status,
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
		Dependencies: results,
	})
}

func (a *gatewayApp) handleProtected(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/identity":
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": mustIdentity(r.Context()),
		})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/observability":
		a.handleObservability(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/context/usage":
		a.handleContextUsage(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		a.handleChatCompletions(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tools/execute":
		a.handleToolExecute(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	if path := os.Getenv(key + "_FILE"); path != "" {
		if payload, err := os.ReadFile(path); err == nil {
			if value := strings.TrimSpace(string(payload)); value != "" {
				return value
			}
		}
	}
	return fallback
}

func checkTCP(name, target string) dependencyResult {
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		return dependencyResult{Name: name, Target: target, Status: "error", Error: err.Error()}
	}
	_ = conn.Close()
	return dependencyResult{Name: name, Target: target, Status: "ok"}
}

func checkHTTP(name, target string) dependencyResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return dependencyResult{Name: name, Target: target, Status: "error", Error: err.Error()}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return dependencyResult{Name: name, Target: target, Status: "error", Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return dependencyResult{Name: name, Target: target, Status: "error", Error: resp.Status}
	}

	return dependencyResult{Name: name, Target: target, Status: "ok"}
}

func withIdentityResolver(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		xAPIKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if authHeader == "" && xAPIKey == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing authorization header"})
			return
		}

		resolverURL := env("AUTH_RESOLVER_URL", "http://control-plane:8081/internal/auth/resolve")
		payload, err := resolveIdentity(r.Context(), resolverURL, authHeader, xAPIKey)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey, payload)))
	})
}

func resolveIdentity(ctx context.Context, resolverURL, authHeader, xAPIKey string) (identityContext, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolverURL, bytes.NewReader(nil))
	if err != nil {
		return identityContext{}, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if xAPIKey != "" {
		req.Header.Set("X-API-Key", xAPIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return identityContext{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var failure map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&failure); err == nil && failure["error"] != "" {
			return identityContext{}, errors.New(failure["error"])
		}
		return identityContext{}, fmt.Errorf("identity resolution failed with status %d", resp.StatusCode)
	}

	var payload struct {
		IdentityContext identityContext `json:"identityContext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return identityContext{}, err
	}
	return payload.IdentityContext, nil
}

func mustIdentity(ctx context.Context) identityContext {
	identity, _ := ctx.Value(identityContextKey).(identityContext)
	return identity
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-SUEZ-Session-Id")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}
