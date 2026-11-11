package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const testTenant = "00000000-0000-0000-0000-000000000001"

// asIdentity attaches an identity whose permissions come from the role, the
// same way resolveJWT builds them.
func asIdentity(r *http.Request, roles ...string) *http.Request {
	identity := identityContext{TenantID: testTenant, TenantSlug: "demo", Actor: "someone@example.com", Roles: roles}
	for _, role := range roles {
		identity.Scopes = append(identity.Scopes, roleScopes[role]...)
	}
	identity.Permissions = permissionsForScopes(identity.Scopes)
	return r.WithContext(context.WithValue(r.Context(), identityContextKey, identity))
}

// serve runs the handler behind the central authorization step, as the real
// router does. The app has no database: every case below must be decided
// before any query, so a nil-pointer panic would also mean the check came too
// late.
func serve(t *testing.T, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	(&controlPlaneApp{}).authorize(handler).ServeHTTP(rec, r)
	return rec
}

func TestNonAdminCannotCreateTenants(t *testing.T) {
	app := &controlPlaneApp{}
	for _, role := range []string{"user", "admin", "auditor", "policy-admin", "read-only"} {
		// Creating tenants needs the platform-admin scope; no tenant role has it.
		req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(`{"name":"x","slug":"xx"}`)), role)
		if rec := serve(t, app.handleTenants, req); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", role, rec.Code)
		}
	}
}

func TestNonAdminCannotManageAPIKeys(t *testing.T) {
	app := &controlPlaneApp{}
	for _, role := range []string{"user", "auditor", "policy-admin", "read-only"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			req := asIdentity(httptest.NewRequest(method, "/api/v1/api-keys", strings.NewReader(`{"label":"k","scopes":["read-only"]}`)), role)
			if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s: status = %d, want 403", role, method, rec.Code)
			}
		}
	}
}

func TestAdminCannotCreateAPIKeysInAnotherTenant(t *testing.T) {
	app := &controlPlaneApp{}
	req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"label":"k","scopes":["read-only"],"tenantId":"other-tenant"}`)), "admin")
	if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	rec := serve(t, app.handleAPIKeys, asIdentity(httptest.NewRequest(http.MethodGet, "/api/v1/api-keys?tenantId=other-tenant", nil), "admin"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant list status = %d, want 403", rec.Code)
	}
}

func TestAPIKeyScopesCannotExceedCreator(t *testing.T) {
	app := &controlPlaneApp{}
	// A tenant admin does not hold platform-admin, so cannot mint it.
	req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"label":"k","scopes":["platform-admin"]}`)), "admin")
	if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"label":"k"}`, `{"label":"k","scopes":["root"]}`, `{"label":"k","scopes":["read-only"],"expiresAt":"2001-01-01T00:00:00Z"}`, `{"label":"k","scopes":["read-only"],"rateLimitRpm":-1}`} {
		req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(body)), "admin")
		if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestSameTenant(t *testing.T) {
	identity := identityContext{TenantID: testTenant}
	cases := []struct {
		requested string
		want      string
		ok        bool
	}{
		{"", testTenant, true},
		{"  ", testTenant, true},
		{testTenant, testTenant, true},
		{"other", "", false},
	}
	for _, c := range cases {
		got, ok := sameTenant(identity, c.requested)
		if got != c.want || ok != c.ok {
			t.Errorf("sameTenant(%q) = %q, %v; want %q, %v", c.requested, got, ok, c.want, c.ok)
		}
	}
	if _, ok := sameTenant(identityContext{}, ""); ok {
		t.Error("an identity without a tenant must not resolve to a tenant")
	}
}

func TestTemplatePolicyEscapesTenantAndTarget(t *testing.T) {
	hostileTenant := `x" ; true } allow if { "a" == "a`
	source := buildTemplatePolicy("policy-1", hostileTenant, "demo\nallow := true", generatePolicyRequest{
		Scope:      "model",
		TargetName: `m" || true || "`,
	})
	wantTenant, _ := json.Marshal(hostileTenant)
	if !strings.Contains(source, "input.identity.tenantId == "+string(wantTenant)) {
		t.Fatalf("tenant id not emitted as an escaped Rego string:\n%s", source)
	}
	if strings.Contains(source, `"x" ;`) || strings.Contains(source, "\nallow := true") {
		t.Fatalf("hostile input broke out of its literal or comment:\n%s", source)
	}
	if !strings.Contains(source, `input.resource.name == "m\" || true || \""`) {
		t.Fatalf("target name not escaped:\n%s", source)
	}
}

func TestBootstrapHidesDemoCredentialsByDefault(t *testing.T) {
	call := func(app *controlPlaneApp) string {
		rec := httptest.NewRecorder()
		app.handleBootstrapStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap/status", nil))
		return rec.Body.String()
	}
	if body := call(&controlPlaneApp{}); strings.Contains(body, "demoCredentials") || strings.Contains(body, "suez-demo-admin-key") {
		t.Fatalf("demo credentials exposed by default: %s", body)
	}
	if body := call(&controlPlaneApp{cfg: runtimeConfig{ExposeDemoCredentials: true}}); !strings.Contains(body, "suez-demo-admin-key") {
		t.Fatalf("demo credentials missing when enabled in development: %s", body)
	}
	// Production says nothing about the deployment, whatever else is set.
	if body := call(&controlPlaneApp{cfg: runtimeConfig{Production: true, ExposeDemoCredentials: true}}); strings.Contains(body, "demo") || strings.Contains(body, "http://") {
		t.Fatalf("production bootstrap leaks details: %s", body)
	}
}

func TestBodyLimitRejectsOversizedRequests(t *testing.T) {
	t.Setenv("MAX_REQUEST_BODY_BYTES", "16")
	var decodeErr error
	handler := withBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		decodeErr = json.NewDecoder(r.Body).Decode(&v)
	}))

	big := `{"label":"` + strings.Repeat("a", 64) + `"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize: status = %d, want 413", rec.Code)
	}

	// Chunked bodies have no Content-Length; the reader itself must stop them.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	req.ContentLength = -1
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if decodeErr == nil {
		t.Fatal("streamed oversize body was decoded")
	}
}

func TestDBStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("ordinary failure"), 0},
		{&pgconn.PgError{Code: "22P02"}, http.StatusNotFound},
		{&pgconn.PgError{Code: "23505"}, http.StatusConflict},
		{&pgconn.PgError{Code: "23503"}, http.StatusBadRequest},
	}
	for _, test := range cases {
		if got := dbStatus(test.err); got != test.want {
			t.Errorf("dbStatus(%v) = %d, want %d", test.err, got, test.want)
		}
	}
}

func TestRequestValidatorsAndHeaderRedaction(t *testing.T) {
	if err := validateTenantRequest(createTenantRequest{Name: "Tenant", Slug: "bad_slug"}); err == nil {
		t.Fatal("invalid tenant slug accepted")
	}
	if err := validateModelRequest(createModelRequest{ExternalName: "m", ModelName: "m", Provider: "other"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	validTool := createToolRequest{
		Name: "tool", Description: "tool", ExecutionMode: "http", TargetURL: "https://example.test/hook", TimeoutMS: 60000,
		RiskTier: "high", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), HTTPHeaders: json.RawMessage(`{"Authorization":"secret"}`),
	}
	if err := validateToolRequest(validTool); err != nil {
		t.Fatalf("valid tool rejected: %v", err)
	}
	validTool.HTTPHeaders = json.RawMessage(`{"Authorization": 3}`)
	if err := validateToolRequest(validTool); err == nil {
		t.Fatal("non-string tool header accepted")
	}
	tool := redactToolHeaders(toolRecord{HTTPHeaders: json.RawMessage(`{"Authorization":"secret","X-Id":"abc"}`)})
	if string(tool.HTTPHeaders) != `{"Authorization":"***","X-Id":"***"}` && string(tool.HTTPHeaders) != `{"X-Id":"***","Authorization":"***"}` {
		t.Fatalf("headers were not redacted: %s", tool.HTTPHeaders)
	}
}

func TestOPAModuleSourceAndTestRunRoute(t *testing.T) {
	source := "package suez.authz\n\npublished_policy_results contains {\"policyId\": \"p\", \"allow\": true} if { true }"
	rewritten, err := opaModuleSource("tenant-a", "policy-b", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rewritten, "package suez.generated.t_tenant_a.p_policy_b") || strings.Contains(rewritten, "package suez.authz") {
		t.Fatalf("package not isolated: %s", rewritten)
	}
	if _, err := opaModuleSource("t", "p", "package suez.authz\npackage suez.authz"); err == nil {
		t.Fatal("multiple package declarations accepted")
	}
	bypasses := map[string]string{
		"real package with trailing comment, decoy in raw string": "package suez.authz # real\nx := `\npackage suez.authz\n`\nallow if { true }",
		"other tenant namespace, decoy in raw string":             "package suez.generated.t_victim.p_x\nx := `\npackage suez.authz\n`",
		"package continued on next line":                          "package suez.authz\n.evil\nallow if { true }",
		"no package":                                              "allow if { true }",
	}
	for name, src := range bypasses {
		if out, err := opaModuleSource("tenant-a", "policy-b", src); err == nil {
			t.Errorf("%s: accepted, rewritten to %q", name, out)
		}
	}
	commented, err := opaModuleSource("tenant-a", "policy-b", "# header\n\npackage suez.authz\nx := 1")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(strings.SplitN(commented, "\n", 4)[2]), "package suez.generated.t_tenant_a.p_policy_b") {
		t.Fatalf("leading comments should be allowed: %q %v", commented, err)
	}
	if !isPolicyTestRunPath([]string{"id", "tests", "run"}) || isPolicyTestRunPath([]string{"id", "tests"}) {
		t.Fatal("tests/run route matcher is wrong")
	}
}
