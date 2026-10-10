package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type contextKey string

const identityContextKey contextKey = "identityContext"

type dependencyResult struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type tenant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
}

type apiKeySummary struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenantId"`
	Label     string `json:"label"`
	CreatedAt string `json:"createdAt"`
}

type accessRequest struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenantId"`
	Subject        string `json:"subject"`
	ResourceType   string `json:"resourceType"`
	ResourceName   string `json:"resourceName"`
	Justification  string `json:"justification"`
	Status         string `json:"status"`
	ReviewedBy     string `json:"reviewedBy,omitempty"`
	DecisionReason string `json:"decisionReason,omitempty"`
	CreatedAt      string `json:"createdAt"`
	ReviewedAt     string `json:"reviewedAt,omitempty"`
}

type accessGrant struct {
	ID           string `json:"id"`
	RequestID    string `json:"requestId,omitempty"`
	TenantID     string `json:"tenantId"`
	Subject      string `json:"subject"`
	ResourceType string `json:"resourceType"`
	ResourceName string `json:"resourceName"`
	GrantedBy    string `json:"grantedBy"`
	Status       string `json:"status"`
	CreatedAt    string `json:"createdAt"`
}

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

type createTenantRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
}

type createAPIKeyRequest struct {
	Label    string `json:"label"`
	TenantID string `json:"tenantId"`
}

type createAccessRequestRequest struct {
	Subject       string `json:"subject"`
	ResourceType  string `json:"resourceType"`
	ResourceName  string `json:"resourceName"`
	Justification string `json:"justification"`
}

type decisionRequest struct {
	Reason string `json:"reason"`
}

type controlPlaneApp struct {
	db         *sql.DB
	httpClient *http.Client
}

type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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

	app := &controlPlaneApp{
		db:         db,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	if err := app.syncPublishedPolicies(context.Background(), ""); err != nil {
		log.Printf("phase 5 policy sync skipped: %v", err)
	}

	port := env("PORT", "8081")
	mux := http.NewServeMux()

	mux.HandleFunc("/", app.handleRoot)
	mux.HandleFunc("/healthz", app.handleHealth)
	mux.HandleFunc("/api/v1/bootstrap/status", app.handleBootstrapStatus)
	mux.HandleFunc("/internal/auth/resolve", app.handleAuthResolve)
	mux.Handle("/api/v1/auth/session", app.withIdentity(http.HandlerFunc(app.handleAuthSession)))
	mux.Handle("/api/v1/tenants", app.withIdentity(http.HandlerFunc(app.handleTenants)))
	mux.Handle("/api/v1/api-keys", app.withIdentity(http.HandlerFunc(app.handleAPIKeys)))
	mux.Handle("/api/v1/access-requests", app.withIdentity(http.HandlerFunc(app.handleAccessRequests)))
	mux.Handle("/api/v1/access-requests/", app.withIdentity(http.HandlerFunc(app.handleAccessRequestActions)))
	mux.Handle("/api/v1/access-grants", app.withIdentity(http.HandlerFunc(app.handleAccessGrants)))
	mux.Handle("/api/v1/models", app.withIdentity(http.HandlerFunc(app.handleModels)))
	mux.Handle("/api/v1/tools", app.withIdentity(http.HandlerFunc(app.handleTools)))
	mux.Handle("/api/v1/policies", app.withIdentity(http.HandlerFunc(app.handlePolicies)))
	mux.Handle("/api/v1/policies/generate", app.withIdentity(http.HandlerFunc(app.handlePolicyGenerate)))
	mux.Handle("/api/v1/policies/", app.withIdentity(http.HandlerFunc(app.handlePolicyActions)))
	mux.Handle("/api/v1/audit-log/traces/", app.withIdentity(http.HandlerFunc(app.handleAuditTrace)))
	mux.Handle("/api/v1/audit-log", app.withIdentity(http.HandlerFunc(app.handleAuditLog)))
	mux.HandleFunc("/internal/tools/http-echo", app.handleHTTPToolEcho)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           withCORS(loggingMiddleware(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("control-plane listening on :%s", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *controlPlaneApp) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "control-plane",
		"status":  "running",
		"routes": []string{
			"/healthz",
			"/api/v1/bootstrap/status",
			"/api/v1/auth/session",
			"/api/v1/tenants",
			"/api/v1/api-keys",
			"/api/v1/access-requests",
			"/api/v1/access-requests/{id}/approve",
			"/api/v1/access-requests/{id}/deny",
			"/api/v1/access-grants",
			"/api/v1/models",
			"/api/v1/tools",
			"/api/v1/policies",
			"/api/v1/policies/generate",
			"/api/v1/policies/{id}/tests",
			"/api/v1/policies/{id}/tests/run",
			"/api/v1/policies/{id}/validate",
			"/api/v1/policies/{id}/publish",
			"/api/v1/policies/{id}/versions",
			"/api/v1/policies/{id}/rollback",
			"/api/v1/audit-log",
			"/api/v1/audit-log/traces/{traceId}",
			"/internal/tools/http-echo",
			"/internal/auth/resolve",
		},
	})
}

func (a *controlPlaneApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	results := []dependencyResult{
		checkTCP("postgres", env("POSTGRES_ADDR", "postgres:5432")),
		checkTCP("redis", env("REDIS_ADDR", "redis:6379")),
		checkHTTP("opa", env("OPA_HEALTH_URL", "http://opa:8181/v1/data/suez/authz/allow")),
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
		Service:      "control-plane",
		Status:       status,
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
		Dependencies: results,
	})
}

func (a *controlPlaneApp) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"services": map[string]string{
			"gateway":      env("GATEWAY_BASE_URL", "http://gateway:8080"),
			"controlPlane": env("CONTROL_PLANE_BASE_URL", "http://control-plane:8081"),
			"opa":          env("OPA_BASE_URL", "http://opa:8181"),
			"policyLlm":    env("POLICY_LLM_BASE_URL", "http://policy-llm:11434"),
			"edge":         env("EDGE_BASE_URL", "https://localhost:5443"),
		},
		"checks": []string{
			"model enforcement via gateway",
			"tool registry and execution",
			"policy-aware authorization with stored policy tests and rollback support",
			"draft generation, validation, tests, and manual publish to OPA",
			"filterable audit logging with trace detail views",
			"https edge routing and file-backed local secret loading",
			"backup and restore scripts for local configuration and audit data",
		},
		"demoCredentials": map[string]string{
			"apiKey":          "suez-demo-admin-key",
			"requesterJwt":    buildUnsignedDemoJWT("requester@example.com", "user"),
			"adminJwt":        buildUnsignedDemoJWT("admin@example.com", "admin"),
			"stubModel":       "demo-model",
			"openaiDemoModel": "openai-gpt-4o-mini",
			"stubTool":        "weather.lookup",
			"httpTool":        "http.echo",
		},
	})
}

func (a *controlPlaneApp) handleAuthResolve(w http.ResponseWriter, r *http.Request) {
	identity, err := a.resolveIdentityFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": identity,
	})
}

func (a *controlPlaneApp) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": mustIdentity(r.Context()),
	})
}

func (a *controlPlaneApp) handleTenants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tenants, err := a.listTenants(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": mustIdentity(r.Context()),
			"tenants":         tenants,
		})
	case http.MethodPost:
		var req createTenantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Slug) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and slug are required"})
			return
		}
		if strings.TrimSpace(req.Environment) == "" {
			req.Environment = "dev"
		}

		created, err := a.createTenant(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		identity := mustIdentity(r.Context())
		_ = a.insertAudit(r.Context(), identity, "tenant.create", "tenant", created.Slug, "allow", map[string]any{
			"tenantId": created.ID,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"tenant":          created,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		tenantID := r.URL.Query().Get("tenantId")
		if tenantID == "" {
			tenantID = identity.TenantID
		}
		apiKeys, err := a.listAPIKeys(r.Context(), tenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"apiKeys":         apiKeys,
		})
	case http.MethodPost:
		var req createAPIKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Label) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "label is required"})
			return
		}
		if req.TenantID == "" {
			req.TenantID = identity.TenantID
		}

		summary, rawKey, err := a.createAPIKey(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_ = a.insertAudit(r.Context(), identity, "api_key.create", "api_key", summary.Label, "allow", map[string]any{
			"apiKeyId":  summary.ID,
			"tenantId":  summary.TenantID,
			"createdBy": identity.Actor,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"apiKey":          summary,
			"rawApiKey":       rawKey,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleAccessRequests(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		requests, err := a.listAccessRequests(r.Context(), identity, strings.TrimSpace(r.URL.Query().Get("status")))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"accessRequests":  requests,
		})
	case http.MethodPost:
		var req createAccessRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.ResourceType) == "" || strings.TrimSpace(req.ResourceName) == "" || strings.TrimSpace(req.Justification) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "resourceType, resourceName, and justification are required"})
			return
		}
		if req.Subject == "" {
			req.Subject = identity.Actor
		}
		if !isAdmin(identity) && req.Subject != identity.Actor {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "non-admin users can only create requests for themselves"})
			return
		}

		created, err := a.createAccessRequest(r.Context(), identity, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"accessRequest":   created,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleAccessRequestActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	identity := mustIdentity(r.Context())
	if !isAdmin(identity) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
		return
	}

	trimmed := strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/access-requests/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	var req decisionRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}

	switch parts[1] {
	case "approve":
		updated, grant, err := a.approveAccessRequest(r.Context(), identity, parts[0], strings.TrimSpace(req.Reason))
		if err != nil {
			writeJSON(w, statusCodeForError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"accessRequest":   updated,
			"grant":           grant,
		})
	case "deny":
		updated, err := a.denyAccessRequest(r.Context(), identity, parts[0], strings.TrimSpace(req.Reason))
		if err != nil {
			writeJSON(w, statusCodeForError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"accessRequest":   updated,
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *controlPlaneApp) handleAccessGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	identity := mustIdentity(r.Context())
	grants, err := a.listAccessGrants(r.Context(), identity)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": identity,
		"accessGrants":    grants,
	})
}

func (a *controlPlaneApp) withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := a.resolveIdentityFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey, identity)))
	})
}

func (a *controlPlaneApp) resolveIdentityFromRequest(r *http.Request) (identityContext, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
			authHeader = "ApiKey " + apiKey
		}
	}
	if authHeader == "" {
		return identityContext{}, errors.New("missing authorization header")
	}

	switch {
	case strings.HasPrefix(strings.ToLower(authHeader), "apikey "):
		return a.resolveAPIKey(strings.TrimSpace(authHeader[len("ApiKey "):]))
	case strings.HasPrefix(strings.ToLower(authHeader), "bearer "):
		token := strings.TrimSpace(authHeader[len("Bearer "):])
		if strings.Count(token, ".") == 2 {
			return a.resolveJWT(token)
		}
		return a.resolveAPIKey(token)
	default:
		return a.resolveAPIKey(authHeader)
	}
}

func (a *controlPlaneApp) resolveAPIKey(rawKey string) (identityContext, error) {
	if rawKey == "" {
		return identityContext{}, errors.New("empty api key")
	}
	hashed := hashAPIKey(rawKey)

	row := a.db.QueryRowContext(context.Background(), `
		select ak.id, ak.label, t.id, t.slug, t.name
		from api_keys ak
		join tenants t on t.id = ak.tenant_id
		where ak.key_hash = $1
		limit 1
	`, hashed)

	var apiKeyID, label, tenantID, tenantSlug, tenantName string
	if err := row.Scan(&apiKeyID, &label, &tenantID, &tenantSlug, &tenantName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identityContext{}, errors.New("invalid api key")
		}
		return identityContext{}, err
	}

	return identityContext{
		TenantID:      tenantID,
		TenantSlug:    tenantSlug,
		TenantName:    tenantName,
		Actor:         label,
		AuthMethod:    "api_key",
		APIKeyID:      apiKeyID,
		Roles:         []string{"admin"},
		Authenticated: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (a *controlPlaneApp) resolveJWT(token string) (identityContext, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identityContext{}, errors.New("invalid jwt format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return identityContext{}, errors.New("invalid jwt payload")
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return identityContext{}, errors.New("invalid jwt claims")
	}

	tenantID := claimString(claims, "tenant_id", "tid")
	tenantSlug := claimString(claims, "tenant_slug", "tenant")
	tenantName := claimString(claims, "tenant_name")
	if tenantSlug != "" {
		if tenant, lookupErr := a.getTenantBySlug(context.Background(), tenantSlug); lookupErr == nil {
			if tenantID == "" {
				tenantID = tenant.ID
			}
			if tenantName == "" {
				tenantName = tenant.Name
			}
		}
	}
	if tenantID == "" && tenantSlug == "" {
		if demo, demoErr := a.getTenantBySlug(context.Background(), "demo"); demoErr == nil {
			tenantID = demo.ID
			tenantSlug = demo.Slug
			tenantName = demo.Name
		}
	}
	if tenantID == "" && tenantSlug == "" {
		return identityContext{}, errors.New("jwt is missing tenant scope")
	}

	subject := claimString(claims, "sub", "preferred_username", "email")
	if subject == "" {
		subject = "jwt-user"
	}

	roles := claimStrings(claims["roles"])
	if len(roles) == 0 {
		if role := claimString(claims, "role"); role != "" {
			roles = []string{role}
		}
	}
	if len(roles) == 0 {
		roles = []string{"user"}
	}

	return identityContext{
		TenantID:      tenantID,
		TenantSlug:    tenantSlug,
		TenantName:    tenantName,
		Actor:         subject,
		AuthMethod:    "jwt",
		JWTSubject:    subject,
		Roles:         roles,
		Claims:        claims,
		Authenticated: time.Now().UTC().Format(time.RFC3339),
		Metadata: map[string]any{
			"jwtValidation": "Phase 1 decodes JWT claims without signature verification for the local runtime.",
		},
	}, nil
}

func (a *controlPlaneApp) listTenants(ctx context.Context) ([]tenant, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, name, slug, environment
		from tenants
		order by created_at asc
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []tenant
	for rows.Next() {
		var item tenant
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Environment); err != nil {
			return nil, err
		}
		tenants = append(tenants, item)
	}
	return tenants, rows.Err()
}

func (a *controlPlaneApp) getTenantBySlug(ctx context.Context, slug string) (tenant, error) {
	row := a.db.QueryRowContext(ctx, `
		select id, name, slug, environment
		from tenants
		where slug = $1
		limit 1
	`, slug)
	var item tenant
	err := row.Scan(&item.ID, &item.Name, &item.Slug, &item.Environment)
	return item, err
}

func (a *controlPlaneApp) createTenant(ctx context.Context, req createTenantRequest) (tenant, error) {
	row := a.db.QueryRowContext(ctx, `
		insert into tenants (name, slug, environment)
		values ($1, $2, $3)
		returning id, name, slug, environment
	`, strings.TrimSpace(req.Name), strings.TrimSpace(req.Slug), strings.TrimSpace(req.Environment))
	var item tenant
	err := row.Scan(&item.ID, &item.Name, &item.Slug, &item.Environment)
	return item, err
}

func (a *controlPlaneApp) listAPIKeys(ctx context.Context, tenantID string) ([]apiKeySummary, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, label, created_at
		from api_keys
		where tenant_id = $1
		order by created_at desc
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apiKeys []apiKeySummary
	for rows.Next() {
		var item apiKeySummary
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Label, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		apiKeys = append(apiKeys, item)
	}
	return apiKeys, rows.Err()
}

func (a *controlPlaneApp) createAPIKey(ctx context.Context, req createAPIKeyRequest) (apiKeySummary, string, error) {
	rawKey, err := newAPIKey()
	if err != nil {
		return apiKeySummary{}, "", err
	}

	row := a.db.QueryRowContext(ctx, `
		insert into api_keys (tenant_id, label, key_hash)
		values ($1, $2, $3)
		returning id, tenant_id, label, created_at
	`, req.TenantID, strings.TrimSpace(req.Label), hashAPIKey(rawKey))

	var item apiKeySummary
	var createdAt time.Time
	if err := row.Scan(&item.ID, &item.TenantID, &item.Label, &createdAt); err != nil {
		return apiKeySummary{}, "", err
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return item, rawKey, nil
}

func (a *controlPlaneApp) createAccessRequest(ctx context.Context, identity identityContext, req createAccessRequestRequest) (accessRequest, error) {
	row := a.db.QueryRowContext(ctx, `
		insert into access_requests (tenant_id, subject, resource_type, resource_name, justification, status, updated_at)
		values ($1, $2, $3, $4, $5, 'pending', now())
		returning id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
	`, identity.TenantID, strings.TrimSpace(req.Subject), strings.TrimSpace(req.ResourceType), strings.TrimSpace(req.ResourceName), strings.TrimSpace(req.Justification))

	item, err := scanAccessRequest(row)
	if err != nil {
		return accessRequest{}, err
	}
	if err := a.insertAudit(ctx, identity, "access_request.create", item.ResourceType, item.ResourceName, "pending", map[string]any{
		"requestId": item.ID,
		"subject":   item.Subject,
		"status":    item.Status,
	}); err != nil {
		return accessRequest{}, err
	}
	return item, nil
}

func (a *controlPlaneApp) listAccessRequests(ctx context.Context, identity identityContext, status string) ([]accessRequest, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if isAdmin(identity) {
		if status == "" {
			rows, err = a.db.QueryContext(ctx, `
				select id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
				from access_requests
				where tenant_id = $1
				order by created_at desc
			`, identity.TenantID)
		} else {
			rows, err = a.db.QueryContext(ctx, `
				select id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
				from access_requests
				where tenant_id = $1 and status = $2
				order by created_at desc
			`, identity.TenantID, status)
		}
	} else if status == "" {
		rows, err = a.db.QueryContext(ctx, `
			select id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
			from access_requests
			where tenant_id = $1 and subject = $2
			order by created_at desc
		`, identity.TenantID, identity.Actor)
	} else {
		rows, err = a.db.QueryContext(ctx, `
			select id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
			from access_requests
			where tenant_id = $1 and subject = $2 and status = $3
			order by created_at desc
		`, identity.TenantID, identity.Actor, status)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []accessRequest
	for rows.Next() {
		item, err := scanAccessRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, item)
	}
	return requests, rows.Err()
}

func (a *controlPlaneApp) approveAccessRequest(ctx context.Context, identity identityContext, requestID, reason string) (accessRequest, accessGrant, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return accessRequest{}, accessGrant{}, err
	}
	defer tx.Rollback()

	current, err := a.getAccessRequestForUpdate(ctx, tx, identity.TenantID, requestID)
	if err != nil {
		return accessRequest{}, accessGrant{}, err
	}
	if current.Status != "pending" {
		return accessRequest{}, accessGrant{}, errors.New("only pending requests can be approved")
	}
	if reason == "" {
		reason = "Approved via control-plane workflow."
	}

	updated, err := scanAccessRequest(tx.QueryRowContext(ctx, `
		update access_requests
		set status = 'approved', reviewed_by = $1, decision_reason = $2, reviewed_at = now(), updated_at = now()
		where id = $3
		returning id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
	`, identity.Actor, reason, requestID))
	if err != nil {
		return accessRequest{}, accessGrant{}, err
	}

	grant, err := scanAccessGrant(tx.QueryRowContext(ctx, `
		insert into access_grants (request_id, tenant_id, subject, resource_type, resource_name, granted_by, status)
		values ($1, $2, $3, $4, $5, $6, 'active')
		on conflict (tenant_id, subject, resource_type, resource_name)
		do update set request_id = excluded.request_id, granted_by = excluded.granted_by, status = 'active'
		returning id, request_id, tenant_id, subject, resource_type, resource_name, granted_by, status, created_at
	`, updated.ID, updated.TenantID, updated.Subject, updated.ResourceType, updated.ResourceName, identity.Actor))
	if err != nil {
		return accessRequest{}, accessGrant{}, err
	}

	if err := insertAuditRecord(ctx, tx, identity.TenantID, identity.Actor, "access_request.approve", updated.ResourceType, updated.ResourceName, "allow", map[string]any{
		"requestId":      updated.ID,
		"subject":        updated.Subject,
		"decisionReason": updated.DecisionReason,
		"grantId":        grant.ID,
	}); err != nil {
		return accessRequest{}, accessGrant{}, err
	}

	if err := tx.Commit(); err != nil {
		return accessRequest{}, accessGrant{}, err
	}
	return updated, grant, nil
}

func (a *controlPlaneApp) denyAccessRequest(ctx context.Context, identity identityContext, requestID, reason string) (accessRequest, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return accessRequest{}, err
	}
	defer tx.Rollback()

	current, err := a.getAccessRequestForUpdate(ctx, tx, identity.TenantID, requestID)
	if err != nil {
		return accessRequest{}, err
	}
	if current.Status != "pending" {
		return accessRequest{}, errors.New("only pending requests can be denied")
	}
	if reason == "" {
		reason = "Denied via control-plane workflow."
	}

	updated, err := scanAccessRequest(tx.QueryRowContext(ctx, `
		update access_requests
		set status = 'denied', reviewed_by = $1, decision_reason = $2, reviewed_at = now(), updated_at = now()
		where id = $3
		returning id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
	`, identity.Actor, reason, requestID))
	if err != nil {
		return accessRequest{}, err
	}

	if err := insertAuditRecord(ctx, tx, identity.TenantID, identity.Actor, "access_request.deny", updated.ResourceType, updated.ResourceName, "deny", map[string]any{
		"requestId":      updated.ID,
		"subject":        updated.Subject,
		"decisionReason": updated.DecisionReason,
	}); err != nil {
		return accessRequest{}, err
	}

	if err := tx.Commit(); err != nil {
		return accessRequest{}, err
	}
	return updated, nil
}

func (a *controlPlaneApp) getAccessRequestForUpdate(ctx context.Context, tx *sql.Tx, tenantID, requestID string) (accessRequest, error) {
	item, err := scanAccessRequest(tx.QueryRowContext(ctx, `
		select id, tenant_id, subject, resource_type, resource_name, justification, status, reviewed_by, decision_reason, created_at, reviewed_at
		from access_requests
		where id = $1 and tenant_id = $2
		for update
	`, requestID, tenantID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return accessRequest{}, errors.New("access request not found")
		}
		return accessRequest{}, err
	}
	return item, nil
}

func (a *controlPlaneApp) listAccessGrants(ctx context.Context, identity identityContext) ([]accessGrant, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if isAdmin(identity) {
		rows, err = a.db.QueryContext(ctx, `
			select id, request_id, tenant_id, subject, resource_type, resource_name, granted_by, status, created_at
			from access_grants
			where tenant_id = $1
			order by created_at desc
		`, identity.TenantID)
	} else {
		rows, err = a.db.QueryContext(ctx, `
			select id, request_id, tenant_id, subject, resource_type, resource_name, granted_by, status, created_at
			from access_grants
			where tenant_id = $1 and subject = $2
			order by created_at desc
		`, identity.TenantID, identity.Actor)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []accessGrant
	for rows.Next() {
		item, err := scanAccessGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, item)
	}
	return grants, rows.Err()
}

func (a *controlPlaneApp) insertAudit(ctx context.Context, identity identityContext, action, resourceType, resourceName, decision string, metadata map[string]any) error {
	return insertAuditRecord(ctx, a.db, identity.TenantID, identity.Actor, action, resourceType, resourceName, decision, metadata)
}

func insertAuditRecord(ctx context.Context, exec dbExecutor, tenantID, actor, action, resourceType, resourceName, decision string, metadata map[string]any) error {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	traceID := ""
	if metadata != nil {
		if raw, ok := metadata["traceId"].(string); ok {
			traceID = strings.TrimSpace(raw)
		}
	}

	_, err = exec.ExecContext(ctx, `
		insert into audit_log (tenant_id, actor, action, resource_type, resource_name, decision, trace_id, metadata)
		values ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8::jsonb)
	`, tenantID, actor, action, resourceType, resourceName, decision, traceID, string(payload))
	return err
}

func mustIdentity(ctx context.Context) identityContext {
	identity, _ := ctx.Value(identityContextKey).(identityContext)
	return identity
}

func isAdmin(identity identityContext) bool {
	for _, role := range identity.Roles {
		if strings.EqualFold(role, "admin") {
			return true
		}
	}
	return false
}

func newAPIKey() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("sk_suez_%s", hex.EncodeToString(buf)), nil
}

func hashAPIKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func claimString(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := claims[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func claimStrings(value any) []string {
	switch typed := value.(type) {
	case []any:
		var values []string
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				values = append(values, text)
			}
		}
		return values
	case []string:
		return typed
	default:
		return nil
	}
}

func scanAccessRequest(scanner interface {
	Scan(dest ...any) error
}) (accessRequest, error) {
	var (
		item           accessRequest
		reviewedBy     sql.NullString
		decisionReason sql.NullString
		createdAt      time.Time
		reviewedAt     sql.NullTime
	)
	err := scanner.Scan(
		&item.ID,
		&item.TenantID,
		&item.Subject,
		&item.ResourceType,
		&item.ResourceName,
		&item.Justification,
		&item.Status,
		&reviewedBy,
		&decisionReason,
		&createdAt,
		&reviewedAt,
	)
	if err != nil {
		return accessRequest{}, err
	}
	item.ReviewedBy = reviewedBy.String
	item.DecisionReason = decisionReason.String
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	if reviewedAt.Valid {
		item.ReviewedAt = reviewedAt.Time.UTC().Format(time.RFC3339)
	}
	return item, nil
}

func scanAccessGrant(scanner interface {
	Scan(dest ...any) error
}) (accessGrant, error) {
	var (
		item      accessGrant
		requestID sql.NullString
		createdAt time.Time
	)
	err := scanner.Scan(
		&item.ID,
		&requestID,
		&item.TenantID,
		&item.Subject,
		&item.ResourceType,
		&item.ResourceName,
		&item.GrantedBy,
		&item.Status,
		&createdAt,
	)
	if err != nil {
		return accessGrant{}, err
	}
	item.RequestID = requestID.String
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return item, nil
}

func buildUnsignedDemoJWT(subject, role string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := fmt.Sprintf(`{"sub":"%s","tenant_slug":"demo","tenant_name":"Demo Tenant","roles":["%s"]}`, subject, role)
	return header + "." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "."
}

func statusCodeForError(err error) int {
	switch err.Error() {
	case "access request not found":
		return http.StatusNotFound
	case "admin role is required":
		return http.StatusForbidden
	case "only pending requests can be approved", "only pending requests can be denied":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
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

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
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
