package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	Target string `json:"target,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type contextKey string

const identityContextKey contextKey = "identityContext"

// identityContext mirrors what the control plane resolves. The gateway never
// derives permissions itself.
type identityContext struct {
	TenantID      string   `json:"tenantId"`
	TenantSlug    string   `json:"tenantSlug"`
	TenantName    string   `json:"tenantName"`
	Actor         string   `json:"actor"`
	AuthMethod    string   `json:"authMethod"`
	APIKeyID      string   `json:"apiKeyId,omitempty"`
	UserID        string   `json:"userId,omitempty"`
	JWTSubject    string   `json:"jwtSubject,omitempty"`
	Roles         []string `json:"roles"`
	Scopes        []string `json:"scopes"`
	Permissions   []string `json:"permissions"`
	AllowedModels []string `json:"allowedModels,omitempty"`
	AllowedTools  []string `json:"allowedTools,omitempty"`
	RateLimitRPM  int      `json:"rateLimitRpm,omitempty"`
	Authenticated string   `json:"authenticatedAt"`
}

type healthResponse struct {
	Service      string             `json:"service"`
	Status       string             `json:"status"`
	TimestampUTC string             `json:"timestampUtc"`
	Dependencies []dependencyResult `json:"dependencies"`
}

type gatewayApp struct {
	db           *sql.DB
	redis        *redis.Client
	httpClient   *http.Client
	streamClient *http.Client
	usageStore   contextUsageStore
	cfg          runtimeConfig
}

func main() {
	slog.SetDefault(newLogger())
	cfg, err := loadRuntimeConfig()
	if err != nil {
		log.Fatal(err)
	}

	databaseURL := env("DATABASE_URL", "postgres://suez:suez@postgres:5432/suez?sslmode=disable")
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal("database configuration is not valid")
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal("database is not reachable")
	}

	redisOptions, err := redis.ParseURL(env("REDIS_URL", "redis://redis:6379/0"))
	if err != nil {
		log.Fatal("REDIS_URL is not valid")
	}
	redisClient := redis.NewClient(redisOptions)
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatal("redis is not reachable")
	}
	defer redisClient.Close()

	app := &gatewayApp{
		db:         db,
		redis:      redisClient,
		httpClient: &http.Client{Timeout: 45 * time.Second},
		// Streams can outlive any fixed total timeout; they are bounded by the
		// request context, a header timeout and a per-chunk idle timeout.
		streamClient: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 45 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		}},
		usageStore: &postgresUsageStore{db: db},
		cfg:        cfg,
	}

	port := env("PORT", "8080")
	mux := http.NewServeMux()

	mux.HandleFunc("/", app.handleRoot)
	mux.HandleFunc("/healthz", app.handleLiveness)
	mux.HandleFunc("/readyz", app.handleHealth)
	protected := app.withIdentityResolver(http.HandlerFunc(app.handleProtected))
	for _, route := range []string{"/v1/identity", "/v1/chat/completions", "/v1/tools/execute", "/v1/observability", "/v1/context/usage"} {
		mux.Handle(route, protected)
	}
	mux.Handle("/v1/memory/", app.withIdentityResolver(http.HandlerFunc(app.handleMemoryProxy)))

	server := newHTTPServer(":"+port, cfg.withRequestContext(cfg.withCORS(cfg.withRateLimit(withBodyLimit(mux)))))

	slog.Info("gateway listening", "port", port, "production", cfg.Production)
	runHTTPServer(server)
}

func (a *gatewayApp) handleRoot(w http.ResponseWriter, r *http.Request) {
	// The mux sends every unmatched path here; only "/" itself is the banner.
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "gateway",
		"status":  "running",
		"routes": []string{
			"/healthz",
			"/v1/identity",
			"/v1/chat/completions",
			"/v1/tools/execute",
			"/v1/observability",
			"/v1/context/usage",
			"/v1/memory/*",
		},
	})
}

func (a *gatewayApp) handleLiveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "gateway", "status": "ok"})
}

// handleHealth is the readiness probe: the gateway cannot authorize or meter
// a request without PostgreSQL, Redis, OPA and the control plane, so any of
// them being down makes it not ready. Details go to the log only.
func (a *gatewayApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	results := []dependencyResult{{Name: "postgres", Status: "ok"}, {Name: "redis", Status: "ok"}}
	if err := a.db.PingContext(ctx); err != nil {
		slog.Error("readiness: postgres", "error", err.Error())
		results[0].Status = "error"
	}
	if err := a.redis.Ping(ctx).Err(); err != nil {
		slog.Error("readiness: redis", "error", err.Error())
		results[1].Status = "error"
	}
	for _, check := range []dependencyResult{
		checkHTTP("opa", env("OPA_HEALTH_URL", "http://opa:8181/health")),
		checkHTTP("control-plane", env("CONTROL_PLANE_HEALTH_URL", "http://control-plane:8081/healthz")),
	} {
		if check.Status != "ok" {
			slog.Error("readiness: dependency", "name", check.Name, "error", check.Error)
		}
		results = append(results, dependencyResult{Name: check.Name, Status: check.Status})
	}
	statusCode, status := http.StatusOK, "ok"
	for _, result := range results {
		if result.Status != "ok" {
			statusCode, status = http.StatusServiceUnavailable, "unavailable"
		}
	}
	writeJSON(w, statusCode, healthResponse{Service: "gateway", Status: status, TimestampUTC: time.Now().UTC().Format(time.RFC3339), Dependencies: results})
}

// Permission names are defined by the control plane (authz.go there).
const (
	permSessionRead   = "session.read"
	permGatewayInvoke = "gateway.invoke"
	permGatewayTools  = "gateway.tools"
	permUsageRead     = "usage.read"
)

func hasPermission(identity identityContext, permission string) bool {
	for _, held := range identity.Permissions {
		if held == permission {
			return true
		}
	}
	return false
}

// allowedByList applies an API key's model or tool restriction. An empty
// list means the key is not restricted beyond grants and policy.
func allowedByList(list []string, name string) bool {
	if len(list) == 0 {
		return true
	}
	for _, allowed := range list {
		if allowed == name {
			return true
		}
	}
	return false
}

// handleProtected is the gateway's single authorization point: every route
// names the permission it needs before its handler runs.
func (a *gatewayApp) handleProtected(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	routes := map[string]struct {
		method, permission string
		handler            http.HandlerFunc
	}{
		"/v1/identity": {http.MethodGet, permSessionRead, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity})
		}},
		"/v1/observability":    {http.MethodGet, permUsageRead, a.handleObservability},
		"/v1/context/usage":    {http.MethodGet, permUsageRead, a.handleContextUsage},
		"/v1/chat/completions": {http.MethodPost, permGatewayInvoke, a.handleChatCompletions},
		"/v1/tools/execute":    {http.MethodPost, permGatewayTools, a.handleToolExecute},
	}
	route, ok := routes[r.URL.Path]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if r.Method != route.method {
		w.Header().Set("Allow", route.method)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if identity.TenantID == "" || !hasPermission(identity, route.permission) {
		slog.Warn("authorization denied", "requestId", requestID(r.Context()), "tenantId", identity.TenantID, "actor", identity.Actor, "permission", route.permission)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied", "requiredPermission": route.permission})
		return
	}
	if err := a.enforceKeyRateLimit(r.Context(), identity); err != nil {
		if errors.Is(err, errQuotaBackendUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "quota service unavailable"})
			return
		}
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "api key rate limit exceeded"})
		return
	}
	route.handler(w, r)
}

// enforceKeyRateLimit applies the optional per-key requests-per-minute limit.
// Like the model quotas it fails closed when Redis is unavailable.
func (a *gatewayApp) enforceKeyRateLimit(ctx context.Context, identity identityContext) error {
	if identity.APIKeyID == "" || identity.RateLimitRPM <= 0 {
		return nil
	}
	key := "quota:key:" + identity.APIKeyID + ":" + time.Now().UTC().Format("200601021504")
	var count *redis.IntCmd
	if _, err := a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		count = pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, time.Minute+10*time.Second)
		return nil
	}); err != nil {
		return fmt.Errorf("%w: %v", errQuotaBackendUnavailable, err)
	}
	if int(count.Val()) > identity.RateLimitRPM {
		return errors.New("api key rate limit exceeded")
	}
	return nil
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

func (a *gatewayApp) withIdentityResolver(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		xAPIKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if authHeader == "" && xAPIKey == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="suez"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing credentials"})
			return
		}

		resolverURL := env("AUTH_RESOLVER_URL", "http://control-plane:8081/internal/auth/resolve")
		payload, err := resolveIdentity(r.Context(), resolverURL, a.cfg.InternalToken, requestID(r.Context()), authHeader, xAPIKey)
		if err != nil {
			if errors.Is(err, errIdentityServiceUnavailable) {
				slog.Error("identity service unavailable", "requestId", requestID(r.Context()), "error", err.Error())
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "identity service unavailable"})
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="suez"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing credentials"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey, payload)))
	})
}

// authResolverClient bounds every identity lookup; http.DefaultClient has no
// timeout, so a hung control plane would otherwise stall every request.
var authResolverClient = &http.Client{Timeout: 10 * time.Second}

var (
	errIdentityServiceUnavailable = errors.New("identity service unavailable")
	errIdentityRejected           = errors.New("credentials rejected")
)

// resolveIdentity asks the control plane who the caller is. Anything other
// than an explicit 200 or 401 is treated as the service being unavailable, so
// a misconfigured or failing resolver can never produce an identity.
func resolveIdentity(ctx context.Context, resolverURL, internalToken, requestID, authHeader, xAPIKey string) (identityContext, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolverURL, bytes.NewReader(nil))
	if err != nil {
		return identityContext{}, fmt.Errorf("%w: %v", errIdentityServiceUnavailable, err)
	}
	req.Header.Set("X-SUEZ-Internal-Token", internalToken)
	if requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if xAPIKey != "" {
		req.Header.Set("X-API-Key", xAPIKey)
	}

	resp, err := authResolverClient.Do(req)
	if err != nil {
		return identityContext{}, fmt.Errorf("%w: %v", errIdentityServiceUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return identityContext{}, errIdentityRejected
	case resp.StatusCode != http.StatusOK:
		return identityContext{}, fmt.Errorf("%w: resolver returned %d", errIdentityServiceUnavailable, resp.StatusCode)
	}
	var payload struct {
		IdentityContext identityContext `json:"identityContext"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return identityContext{}, fmt.Errorf("%w: %v", errIdentityServiceUnavailable, err)
	}
	if payload.IdentityContext.TenantID == "" || payload.IdentityContext.Actor == "" {
		return identityContext{}, fmt.Errorf("%w: resolver returned an empty identity", errIdentityServiceUnavailable)
	}
	return payload.IdentityContext, nil
}

func mustIdentity(ctx context.Context) identityContext {
	identity, _ := ctx.Value(identityContextKey).(identityContext)
	return identity
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}
