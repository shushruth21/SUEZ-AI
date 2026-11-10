package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type apiKeySummary struct {
	ID            string   `json:"id"`
	TenantID      string   `json:"tenantId"`
	Label         string   `json:"label"`
	KeyPrefix     string   `json:"keyPrefix"`
	Scopes        []string `json:"scopes"`
	AllowedModels []string `json:"allowedModels"`
	AllowedTools  []string `json:"allowedTools"`
	RateLimitRPM  int      `json:"rateLimitRpm"`
	CreatedBy     string   `json:"createdBy,omitempty"`
	CreatedAt     string   `json:"createdAt"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
	RevokedAt     string   `json:"revokedAt,omitempty"`
	RevokedBy     string   `json:"revokedBy,omitempty"`
	LastUsedAt    string   `json:"lastUsedAt,omitempty"`
	Status        string   `json:"status"`
}

type createAPIKeyRequest struct {
	Label         string   `json:"label"`
	TenantID      string   `json:"tenantId"`
	Scopes        []string `json:"scopes"`
	AllowedModels []string `json:"allowedModels"`
	AllowedTools  []string `json:"allowedTools"`
	RateLimitRPM  int      `json:"rateLimitRpm"`
	ExpiresAt     string   `json:"expiresAt"`
	ExpiresInDays int      `json:"expiresInDays"`
}

type userRecord struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenantId"`
	Subject     string `json:"subject"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
}

type userRequest struct {
	Subject     string `json:"subject"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

// authFailure is the only detail an unauthenticated caller is told. The
// specific reason goes to the log with the request ID.
func (a *controlPlaneApp) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errAuthenticationServiceUnavailable) {
		slog.Error("authentication unavailable", "requestId", requestID(r.Context()), "error", err.Error())
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication service unavailable"})
		return
	}
	slog.Warn("authentication failed", "requestId", requestID(r.Context()), "reason", err.Error(), "client", a.cfg.clientIP(r))
	w.Header().Set("WWW-Authenticate", `Bearer realm="suez"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing credentials"})
}

// withIdentity authenticates the caller and then applies routePermission.
// Handlers behind it may assume the caller holds the route's permission.
func (a *controlPlaneApp) withIdentity(next http.Handler) http.Handler {
	authorized := a.authorize(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := a.resolveIdentityFromRequest(r)
		if err != nil {
			a.writeAuthError(w, r, err)
			return
		}
		authorized.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey, identity)))
	})
}

// authorize is the one place a control-plane request is allowed or refused.
func (a *controlPlaneApp) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := mustIdentity(r.Context())
		permission, known := routePermission(r.Method, path.Clean(r.URL.Path))
		if !known {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if identity.TenantID == "" || !hasPermission(identity, permission) {
			slog.Warn("authorization denied", "requestId", requestID(r.Context()), "tenantId", identity.TenantID, "actor", identity.Actor, "permission", permission, "path", r.URL.Path)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied", "requiredPermission": permission})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *controlPlaneApp) handleAuthResolve(w http.ResponseWriter, r *http.Request) {
	identity, err := a.resolveIdentityFromRequest(r)
	if err != nil {
		a.writeAuthError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity})
}

func (a *controlPlaneApp) resolveAPIKey(ctx context.Context, rawKey string) (identityContext, error) {
	if rawKey == "" {
		return identityContext{}, errors.New("empty api key")
	}
	var (
		apiKeyID, label, tenantID, tenantSlug, tenantName string
		scopes, models, tools                             textArray
		rateLimit                                         int
		expiresAt, revokedAt                              sql.NullTime
	)
	err := a.db.QueryRowContext(ctx, `
		select ak.id, ak.label, t.id, t.slug, t.name, ak.scopes, ak.allowed_models, ak.allowed_tools,
		       ak.rate_limit_rpm, ak.expires_at, ak.revoked_at
		from api_keys ak
		join tenants t on t.id = ak.tenant_id
		where ak.key_hash = $1
		limit 1
	`, hashAPIKey(rawKey)).Scan(&apiKeyID, &label, &tenantID, &tenantSlug, &tenantName, &scopes, &models, &tools, &rateLimit, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return identityContext{}, errors.New("unknown api key")
	}
	if err != nil {
		return identityContext{}, fmt.Errorf("%w: %v", errAuthenticationServiceUnavailable, err)
	}
	if revokedAt.Valid {
		return identityContext{}, errors.New("api key is revoked")
	}
	if expiresAt.Valid && !time.Now().Before(expiresAt.Time) {
		return identityContext{}, errors.New("api key is expired")
	}
	validScopes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if validScope(scope) {
			validScopes = append(validScopes, scope)
		}
	}
	// Best effort and at most once a minute per key; never blocks the request.
	_, _ = a.db.ExecContext(ctx, `
		update api_keys set last_used_at = now()
		where id = $1 and (last_used_at is null or last_used_at < now() - interval '1 minute')
	`, apiKeyID)

	return identityContext{
		TenantID:      tenantID,
		TenantSlug:    tenantSlug,
		TenantName:    tenantName,
		Actor:         label,
		AuthMethod:    "api_key",
		APIKeyID:      apiKeyID,
		Roles:         rolesForScopes(validScopes),
		Scopes:        validScopes,
		Permissions:   permissionsForScopes(validScopes),
		AllowedModels: []string(models),
		AllowedTools:  []string(tools),
		RateLimitRPM:  rateLimit,
		Authenticated: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// rolesForScopes keeps the legacy roles field meaningful for clients and
// policies that read it: only a tenant admin is "admin".
func rolesForScopes(scopes []string) []string {
	for _, scope := range scopes {
		if scope == scopeTenantAdmin {
			return []string{"admin"}
		}
	}
	return []string{"service"}
}

// resolveJWT verifies the token and then binds it to a stored user. Roles and
// permissions come from the users table, never from token claims.
func (a *controlPlaneApp) resolveJWT(ctx context.Context, token string) (identityContext, error) {
	claims, err := a.jwt.verify(ctx, token)
	if err != nil {
		return identityContext{}, err
	}
	subject, err := claims.GetSubject()
	if err != nil || strings.TrimSpace(subject) == "" {
		return identityContext{}, errors.New("token has no subject")
	}
	resolved, err := a.resolveJWTTenant(ctx, claimString(claims, a.jwt.cfg.TenantIDClaim), claimString(claims, a.jwt.cfg.TenantSlugClaim))
	if err != nil {
		return identityContext{}, err
	}

	var userID, role, status string
	err = a.db.QueryRowContext(ctx, `
		select id, role, status from users where tenant_id = $1 and subject = $2 limit 1
	`, resolved.ID, subject).Scan(&userID, &role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return identityContext{}, errors.New("token subject is not a member of the tenant")
	}
	if err != nil {
		return identityContext{}, fmt.Errorf("%w: %v", errAuthenticationServiceUnavailable, err)
	}
	if status != "active" {
		return identityContext{}, errors.New("user is inactive")
	}
	if !validRole(role) {
		return identityContext{}, errors.New("user has an invalid role")
	}
	scopes := roleScopes[role]
	return identityContext{
		TenantID:      resolved.ID,
		TenantSlug:    resolved.Slug,
		TenantName:    resolved.Name,
		Actor:         subject,
		AuthMethod:    "jwt",
		UserID:        userID,
		JWTSubject:    subject,
		Roles:         []string{role},
		Scopes:        scopes,
		Permissions:   permissionsForScopes(scopes),
		Authenticated: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// demoJWTs signs short-lived tokens for the seeded demo users. Only reachable
// in development with JWT_MODE=dev-hs256 and demo credentials enabled.
func (a *controlPlaneApp) demoJWTs() map[string]string {
	out := map[string]string{}
	now := time.Now()
	for _, subject := range []string{"admin@example.com", "requester@example.com", "developer@example.com", "auditor@example.com", "security-admin@example.com"} {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"iss":         a.jwt.cfg.Issuer,
			"aud":         a.jwt.cfg.Audiences[0],
			"sub":         subject,
			"tenant_slug": "demo",
			"iat":         now.Unix(),
			"exp":         now.Add(8 * time.Hour).Unix(),
		})
		if signed, err := token.SignedString(a.jwt.cfg.DevSecret); err == nil {
			out[subject] = signed
		}
	}
	return out
}

func (a *controlPlaneApp) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	keyID := strings.Trim(strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/api-keys"), "/")
	switch {
	case r.Method == http.MethodGet && keyID == "":
		tenantID, ok := sameTenant(identity, r.URL.Query().Get("tenantId"))
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "api keys can only be listed for your own tenant"})
			return
		}
		apiKeys, err := a.listAPIKeys(r.Context(), tenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity, "apiKeys": apiKeys})
	case r.Method == http.MethodPost && keyID == "":
		var req createAPIKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		tenantID, ok := sameTenant(identity, req.TenantID)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "api keys can only be created for your own tenant"})
			return
		}
		req.TenantID = tenantID
		expiresAt, err := validateAPIKeyRequest(&req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if !canGrantScopes(identity, req.Scopes) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "you cannot grant scopes beyond your own permissions"})
			return
		}
		summary, rawKey, err := a.createAPIKey(r.Context(), identity, req, expiresAt)
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "api_key.create", "api_key", summary.Label, "allow", map[string]any{
			"apiKeyId": summary.ID, "scopes": summary.Scopes, "expiresAt": summary.ExpiresAt,
		})
		// The raw key is returned exactly once and is never stored or logged.
		writeJSON(w, http.StatusCreated, map[string]any{"identityContext": identity, "apiKey": summary, "rawApiKey": rawKey})
	case r.Method == http.MethodDelete && keyID != "" && !strings.Contains(keyID, "/"):
		summary, err := a.revokeAPIKey(r.Context(), identity, keyID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "api_key.revoke", "api_key", summary.Label, "allow", map[string]any{"apiKeyId": summary.ID})
		writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity, "apiKey": summary})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func validateAPIKeyRequest(req *createAPIKeyRequest) (*time.Time, error) {
	req.Label = strings.TrimSpace(req.Label)
	if !validLength(req.Label, 1, 128) {
		return nil, errors.New("label must be 1-128 characters")
	}
	if len(req.Scopes) == 0 {
		return nil, errors.New("scopes is required: read-only, gateway-inference, policy-admin, audit-reader, tenant-admin")
	}
	seen := map[string]bool{}
	for _, scope := range req.Scopes {
		if !validScope(scope) {
			return nil, fmt.Errorf("unknown scope %q", scope)
		}
		if seen[scope] {
			return nil, fmt.Errorf("duplicate scope %q", scope)
		}
		seen[scope] = true
	}
	for name, list := range map[string][]string{"allowedModels": req.AllowedModels, "allowedTools": req.AllowedTools} {
		if len(list) > 200 {
			return nil, fmt.Errorf("%s has too many entries", name)
		}
		for _, item := range list {
			if !validLength(item, 1, 256) {
				return nil, fmt.Errorf("%s entries must be 1-256 characters", name)
			}
		}
	}
	if req.RateLimitRPM < 0 || req.RateLimitRPM > 1_000_000 {
		return nil, errors.New("rateLimitRpm must be between 0 and 1000000")
	}
	switch {
	case strings.TrimSpace(req.ExpiresAt) != "":
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ExpiresAt))
		if err != nil || !parsed.After(time.Now()) {
			return nil, errors.New("expiresAt must be a future RFC 3339 timestamp")
		}
		return &parsed, nil
	case req.ExpiresInDays != 0:
		if req.ExpiresInDays < 1 || req.ExpiresInDays > 3650 {
			return nil, errors.New("expiresInDays must be between 1 and 3650")
		}
		expires := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		return &expires, nil
	}
	return nil, nil
}

const apiKeyColumns = `id, tenant_id, label, key_prefix, scopes, allowed_models, allowed_tools, rate_limit_rpm,
	coalesce(created_by, ''), created_at, expires_at, revoked_at, coalesce(revoked_by, ''), last_used_at`

func scanAPIKey(scanner interface{ Scan(...any) error }) (apiKeySummary, error) {
	var (
		item                             apiKeySummary
		scopes, models, tools            textArray
		createdAt                        time.Time
		expiresAt, revokedAt, lastUsedAt sql.NullTime
	)
	if err := scanner.Scan(&item.ID, &item.TenantID, &item.Label, &item.KeyPrefix, &scopes, &models, &tools, &item.RateLimitRPM,
		&item.CreatedBy, &createdAt, &expiresAt, &revokedAt, &item.RevokedBy, &lastUsedAt); err != nil {
		return apiKeySummary{}, err
	}
	item.Scopes, item.AllowedModels, item.AllowedTools = nonNil(scopes), nonNil(models), nonNil(tools)
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	item.Status = "active"
	if expiresAt.Valid {
		item.ExpiresAt = expiresAt.Time.UTC().Format(time.RFC3339)
		if !time.Now().Before(expiresAt.Time) {
			item.Status = "expired"
		}
	}
	if revokedAt.Valid {
		item.RevokedAt = revokedAt.Time.UTC().Format(time.RFC3339)
		item.Status = "revoked"
	}
	if lastUsedAt.Valid {
		item.LastUsedAt = lastUsedAt.Time.UTC().Format(time.RFC3339)
	}
	return item, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (a *controlPlaneApp) listAPIKeys(ctx context.Context, tenantID string) ([]apiKeySummary, error) {
	rows, err := a.db.QueryContext(ctx, `select `+apiKeyColumns+` from api_keys where tenant_id = $1 order by created_at desc`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apiKeys := []apiKeySummary{}
	for rows.Next() {
		item, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		apiKeys = append(apiKeys, item)
	}
	return apiKeys, rows.Err()
}

func (a *controlPlaneApp) createAPIKey(ctx context.Context, identity identityContext, req createAPIKeyRequest, expiresAt *time.Time) (apiKeySummary, string, error) {
	rawKey, err := newAPIKey()
	if err != nil {
		return apiKeySummary{}, "", err
	}
	row := a.db.QueryRowContext(ctx, `
		insert into api_keys (tenant_id, label, key_hash, key_prefix, scopes, allowed_models, allowed_tools, rate_limit_rpm, created_by, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		returning `+apiKeyColumns,
		req.TenantID, req.Label, hashAPIKey(rawKey), rawKey[:12], req.Scopes, nonNil(req.AllowedModels), nonNil(req.AllowedTools), req.RateLimitRPM, identity.Actor, expiresAt)
	item, err := scanAPIKey(row)
	if err != nil {
		return apiKeySummary{}, "", err
	}
	return item, rawKey, nil
}

func (a *controlPlaneApp) revokeAPIKey(ctx context.Context, identity identityContext, keyID string) (apiKeySummary, error) {
	row := a.db.QueryRowContext(ctx, `
		update api_keys set revoked_at = coalesce(revoked_at, now()), revoked_by = coalesce(revoked_by, $3)
		where id = $1 and tenant_id = $2
		returning `+apiKeyColumns, keyID, identity.TenantID, identity.Actor)
	return scanAPIKey(row)
}

func (a *controlPlaneApp) handleUsers(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	userID := strings.Trim(strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/users"), "/")
	switch {
	case r.Method == http.MethodGet && userID == "":
		rows, err := a.db.QueryContext(r.Context(), `
			select id, tenant_id, subject, coalesce(email, ''), coalesce(display_name, ''), role, status, created_at
			from users where tenant_id = $1 order by created_at asc limit 1000`, identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		users := []userRecord{}
		for rows.Next() {
			user, err := scanUser(rows)
			if err != nil {
				writeAppError(w, err)
				return
			}
			users = append(users, user)
		}
		writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity, "users": users})
	case r.Method == http.MethodPost && userID == "":
		var req userRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.Role == "" {
			req.Role = "user"
		}
		if !validLength(req.Subject, 1, 256) || len(req.Email) > 320 || len(req.DisplayName) > 256 || !validRole(req.Role) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject (1-256 characters) and a role of admin, policy-admin, auditor, user or read-only are required"})
			return
		}
		user, err := scanUser(a.db.QueryRowContext(r.Context(), `
			insert into users (tenant_id, subject, email, display_name, role, created_by)
			values ($1, $2, nullif($3, ''), nullif($4, ''), $5, $6)
			returning id, tenant_id, subject, coalesce(email, ''), coalesce(display_name, ''), role, status, created_at`,
			identity.TenantID, strings.TrimSpace(req.Subject), strings.TrimSpace(req.Email), strings.TrimSpace(req.DisplayName), req.Role, identity.Actor))
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "user.create", "user", user.Subject, "allow", map[string]any{"userId": user.ID, "role": user.Role})
		writeJSON(w, http.StatusCreated, map[string]any{"identityContext": identity, "user": user})
	case r.Method == http.MethodPatch && userID != "" && !strings.Contains(userID, "/"):
		var req userRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if (req.Role != "" && !validRole(req.Role)) || (req.Status != "" && req.Status != "active" && req.Status != "inactive") || (req.Role == "" && req.Status == "") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide a valid role and/or a status of active or inactive"})
			return
		}
		user, err := scanUser(a.db.QueryRowContext(r.Context(), `
			update users set role = coalesce(nullif($3, ''), role), status = coalesce(nullif($4, ''), status), updated_at = now()
			where id = $1 and tenant_id = $2
			returning id, tenant_id, subject, coalesce(email, ''), coalesce(display_name, ''), role, status, created_at`,
			userID, identity.TenantID, req.Role, req.Status))
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "user.update", "user", user.Subject, "allow", map[string]any{"userId": user.ID, "role": user.Role, "status": user.Status})
		writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity, "user": user})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func scanUser(scanner interface{ Scan(...any) error }) (userRecord, error) {
	var user userRecord
	var createdAt time.Time
	if err := scanner.Scan(&user.ID, &user.TenantID, &user.Subject, &user.Email, &user.DisplayName, &user.Role, &user.Status, &createdAt); err != nil {
		return userRecord{}, err
	}
	user.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return user, nil
}

// handleAccessGrantRevoke handles DELETE /api/v1/access-grants/{id}. The grant
// row is kept (status revoked) so the audit trail stays intact; the gateway
// only honours grants with status active.
func (a *controlPlaneApp) handleAccessGrantRevoke(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	grantID := strings.Trim(strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/access-grants"), "/")
	if r.Method != http.MethodDelete || grantID == "" || strings.Contains(grantID, "/") {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	grant, err := scanAccessGrant(a.db.QueryRowContext(r.Context(), `
		update access_grants set status = 'revoked', revoked_at = now(), revoked_by = $3
		where id = $1 and tenant_id = $2 and status = 'active'
		returning id, request_id, tenant_id, subject, resource_type, resource_name, granted_by, status, created_at`,
		grantID, identity.TenantID, identity.Actor))
	if err != nil {
		writeAppError(w, err)
		return
	}
	_ = a.insertAudit(r.Context(), identity, "access_grant.revoke", grant.ResourceType, grant.ResourceName, "allow", map[string]any{"grantId": grant.ID, "subject": grant.Subject})
	writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity, "grant": grant})
}
