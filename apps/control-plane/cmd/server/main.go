package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type contextKey string

const identityContextKey contextKey = "identityContext"

type dependencyResult struct {
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type tenant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
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

// identityContext is what authentication produces and what every
// authorization decision reads. It never carries raw token claims.
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

type createTenantRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Environment string `json:"environment"`
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
	policyMu   sync.Mutex
	cfg        runtimeConfig
	jwt        *jwtVerifier
	sealer     *contentSealer
}

type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func main() {
	slog.SetDefault(newLogger())
	cfg, err := loadRuntimeConfig()
	if err != nil {
		log.Fatal(err)
	}
	jwtCfg, err := loadJWTConfig(cfg.Production)
	if err != nil {
		log.Fatalf("configuration is not valid: %v", err)
	}
	sealer, err := newContentSealer(cfg.Production)
	if err != nil {
		log.Fatalf("configuration is not valid: %v", err)
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
	app := &controlPlaneApp{
		db:         db,
		httpClient: httpClient,
		cfg:        cfg,
		jwt:        newJWTVerifier(jwtCfg, &http.Client{Timeout: 5 * time.Second}),
		sealer:     sealer,
	}
	if err := app.syncPublishedPolicies(context.Background(), ""); err != nil {
		slog.Warn("initial policy sync failed; the reconciler will retry", "error", err.Error())
	}
	go app.runPolicyReconciler(context.Background())
	go app.runRetention(context.Background())

	port := env("PORT", "8081")
	mux := http.NewServeMux()

	mux.HandleFunc("/", app.handleRoot)
	mux.HandleFunc("/healthz", app.handleLiveness)
	mux.HandleFunc("/readyz", app.handleHealth)
	mux.HandleFunc("/api/v1/bootstrap/status", app.handleBootstrapStatus)
	mux.HandleFunc("/internal/auth/resolve", cfg.requireInternalToken(app.handleAuthResolve))
	mux.HandleFunc("/internal/tools/http-echo", cfg.requireInternalToken(app.handleHTTPToolEcho))
	mux.HandleFunc("/internal/memory/", cfg.requireInternalToken(app.handleInternalMemory))
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"/api/v1/auth/session", app.handleAuthSession},
		{"/api/v1/tenants", app.handleTenants},
		{"/api/v1/api-keys", app.handleAPIKeys},
		{"/api/v1/api-keys/", app.handleAPIKeys},
		{"/api/v1/users", app.handleUsers},
		{"/api/v1/users/", app.handleUsers},
		{"/api/v1/access-requests", app.handleAccessRequests},
		{"/api/v1/access-requests/", app.handleAccessRequestActions},
		{"/api/v1/access-grants", app.handleAccessGrants},
		{"/api/v1/access-grants/", app.handleAccessGrantRevoke},
		{"/api/v1/models", app.handleModels},
		{"/api/v1/tools", app.handleTools},
		{"/api/v1/policies", app.handlePolicies},
		{"/api/v1/policies/generate", app.handlePolicyGenerate},
		{"/api/v1/policies/", app.handlePolicyActions},
		{"/api/v1/audit-log/traces/", app.handleAuditTrace},
		{"/api/v1/audit-log", app.handleAuditLog},
		{"/api/v1/memory/", app.handleMemory},
	} {
		// Every /api/v1 route goes through withIdentity, which authenticates
		// and applies the routePermission table.
		mux.Handle(route.pattern, app.withIdentity(route.handler))
	}

	handler := cfg.withRequestContext(cfg.withCORS(cfg.withRateLimit(withBodyLimit(mux))))
	server := newHTTPServer(":"+port, handler)

	slog.Info("control-plane listening", "port", port, "production", cfg.Production, "jwtMode", jwtCfg.Mode)
	runHTTPServer(server)
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
		},
	})
}

// handleLiveness answers whether the process is up. It checks nothing else,
// so an unavailable dependency never gets the container restarted.
func (a *controlPlaneApp) handleLiveness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "control-plane", "status": "ok"})
}

// handleHealth is the readiness probe. PostgreSQL and OPA are required;
// the policy LLM is optional (draft generation falls back to a template).
// Addresses and error text stay in the log, not in the response.
func (a *controlPlaneApp) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	results := []dependencyResult{{Name: "postgres", Status: "ok"}}
	if err := a.db.PingContext(ctx); err != nil {
		slog.Error("readiness: postgres", "error", err.Error())
		results[0].Status = "error"
	}
	opa := checkHTTP("opa", env("OPA_HEALTH_URL", "http://opa:8181/health"))
	llm := checkHTTP("policy-llm", env("POLICY_LLM_URL", "http://policy-llm:11434/api/tags"))
	llm.Name = "policy-llm (optional)"
	for _, result := range []dependencyResult{opa, llm} {
		if result.Status != "ok" {
			slog.Warn("readiness: dependency", "name", result.Name, "error", result.Error)
		}
		results = append(results, dependencyResult{Name: result.Name, Status: result.Status})
	}

	statusCode, status := http.StatusOK, "ok"
	if results[0].Status != "ok" || opa.Status != "ok" {
		statusCode, status = http.StatusServiceUnavailable, "unavailable"
	} else if llm.Status != "ok" {
		status = "degraded"
	}
	writeJSON(w, statusCode, healthResponse{
		Service:      "control-plane",
		Status:       status,
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
		Dependencies: results,
	})
}

func (a *controlPlaneApp) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Production {
		// Unauthenticated endpoint: say nothing about the deployment.
		writeJSON(w, http.StatusOK, map[string]any{"service": "control-plane", "status": "running"})
		return
	}
	body := map[string]any{
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
	}
	// This endpoint is unauthenticated, so demo credentials are only handed out
	// when explicitly enabled for a local demo.
	if a.cfg.ExposeDemoCredentials {
		body["demoCredentials"] = map[string]string{
			"apiKey":          "suez-demo-admin-key",
			"stubModel":       "demo-model",
			"openaiDemoModel": "openai-gpt-4o-mini",
			"stubTool":        "weather.lookup",
			"httpTool":        "http.echo",
		}
	}
	if a.cfg.ExposeDemoCredentials && a.jwt != nil && a.jwt.cfg.Mode == jwtModeDevHS256 {
		if demo, ok := body["demoCredentials"].(map[string]string); ok {
			body["demoJwts"] = a.demoJWTs()
			demo["requesterJwt"] = a.demoJWTs()["requester@example.com"]
			demo["adminJwt"] = a.demoJWTs()["admin@example.com"]
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *controlPlaneApp) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": mustIdentity(r.Context()),
	})
}

func (a *controlPlaneApp) handleTenants(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		// Non-admins only ever see their own tenant.
		var (
			tenants []tenant
			err     error
		)
		if hasPermission(identity, permTenantsManage) {
			tenants, err = a.listTenants(r.Context())
		} else {
			var own tenant
			own, err = a.getTenantByID(r.Context(), identity.TenantID)
			if err == nil {
				tenants = []tenant{own}
			} else if errors.Is(err, sql.ErrNoRows) {
				tenants, err = []tenant{}, nil
			}
		}
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"tenants":         tenants,
		})
	case http.MethodPost:
		var req createTenantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Environment) == "" {
			req.Environment = "dev"
		}
		if err := validateTenantRequest(req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		created, err := a.createTenant(r.Context(), req)
		if err != nil {
			writeAppError(w, err)
			return
		}
		// A new tenant needs a first credential. It is a tenant-admin key for
		// the new tenant only, shown once here; the creator keeps no access.
		firstKey, rawKey, err := a.createAPIKey(r.Context(), identity, createAPIKeyRequest{
			TenantID: created.ID, Label: "Initial tenant admin key", Scopes: []string{scopeTenantAdmin},
		}, nil)
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "tenant.create", "tenant", created.Slug, "allow", map[string]any{
			"tenantId": created.ID, "initialApiKeyId": firstKey.ID,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext":  identity,
			"tenant":           created,
			"initialAdminKey":  firstKey,
			"rawInitialApiKey": rawKey,
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
			writeAppError(w, err)
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
		if req.Subject == "" {
			req.Subject = identity.Actor
		}
		if !hasPermission(identity, permAccessReview) && req.Subject != identity.Actor {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "non-admin users can only create requests for themselves"})
			return
		}
		if err := validateAccessRequest(req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		created, err := a.createAccessRequest(r.Context(), identity, req)
		if err != nil {
			writeAppError(w, err)
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
			writeAppError(w, err)
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
			writeAppError(w, err)
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
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": identity,
		"accessGrants":    grants,
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
		return a.resolveAPIKey(r.Context(), strings.TrimSpace(authHeader[len("ApiKey "):]))
	case strings.HasPrefix(strings.ToLower(authHeader), "bearer "):
		token := strings.TrimSpace(authHeader[len("Bearer "):])
		if strings.Count(token, ".") == 2 {
			return a.resolveJWT(r.Context(), token)
		}
		return a.resolveAPIKey(r.Context(), token)
	default:
		return a.resolveAPIKey(r.Context(), authHeader)
	}
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

func (a *controlPlaneApp) getTenantByID(ctx context.Context, id string) (tenant, error) {
	row := a.db.QueryRowContext(ctx, `
		select id, name, slug, environment
		from tenants
		where id::text = $1
		limit 1
	`, id)
	var item tenant
	err := row.Scan(&item.ID, &item.Name, &item.Slug, &item.Environment)
	return item, err
}

// resolveJWTTenant maps the tenant claims onto a stored tenant. With no tenant
// claims at all the local demo tenant is used, as before.
func (a *controlPlaneApp) resolveJWTTenant(ctx context.Context, claimID, claimSlug string) (tenant, error) {
	claimID, claimSlug = strings.TrimSpace(claimID), strings.TrimSpace(claimSlug)
	var (
		found tenant
		err   error
	)
	switch {
	case claimID != "":
		found, err = a.getTenantByID(ctx, claimID)
	case claimSlug != "":
		found, err = a.getTenantBySlug(ctx, claimSlug)
	default:
		return tenant{}, errors.New("token has no tenant claim")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return tenant{}, errors.New("token tenant is unknown")
	}
	if err != nil {
		if dbStatus(err) == http.StatusNotFound {
			return tenant{}, errors.New("token tenant is unknown")
		}
		return tenant{}, fmt.Errorf("%w: %v", errAuthenticationServiceUnavailable, err)
	}
	if claimID != "" && claimSlug != "" && !strings.EqualFold(found.Slug, claimSlug) {
		return tenant{}, errors.New("token tenant id and slug do not match")
	}
	return found, nil
}

// sameTenant returns the caller's tenant when requested is empty or equal to
// it; any other tenant is refused.
func sameTenant(identity identityContext, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == identity.TenantID {
		return identity.TenantID, identity.TenantID != ""
	}
	return "", false
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
	if hasPermission(identity, permAccessReview) {
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
	if hasPermission(identity, permAccessReview) {
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

func statusCodeForError(err error) int {
	if status := dbStatus(err); status != 0 {
		return status
	}
	switch err.Error() {
	case "access request not found", "policy not found":
		return http.StatusNotFound
	case "admin role is required":
		return http.StatusForbidden
	case "only pending requests can be approved", "only pending requests can be denied", "policy validation failed; review the draft before publishing", "policy must include at least one test case before publish", "policy tests must pass before publish":
		return http.StatusConflict
	default:
		if strings.HasPrefix(err.Error(), "cannot rollback to a draft") {
			return http.StatusConflict
		}
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

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to encode response: %v", err)
	}
}
