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

func asIdentity(r *http.Request, roles ...string) *http.Request {
	identity := identityContext{TenantID: testTenant, TenantSlug: "demo", Actor: "someone@example.com", Roles: roles}
	return r.WithContext(context.WithValue(r.Context(), identityContextKey, identity))
}

// The app has no database: every case below must be decided before any query,
// so a nil-pointer panic would also mean the check came too late.
func serve(t *testing.T, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, r)
	return rec
}

func TestNonAdminCannotCreateTenants(t *testing.T) {
	app := &controlPlaneApp{}
	req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/tenants", strings.NewReader(`{"name":"x","slug":"x"}`)), "user")
	if rec := serve(t, app.handleTenants, req); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestNonAdminCannotCreateAPIKeys(t *testing.T) {
	app := &controlPlaneApp{}
	for _, body := range []string{`{"label":"k"}`, `{"label":"k","tenantId":"` + testTenant + `"}`} {
		req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(body)), "user")
		if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", body, rec.Code)
		}
	}
}

func TestAdminCannotCreateAPIKeysInAnotherTenant(t *testing.T) {
	app := &controlPlaneApp{}
	req := asIdentity(httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"label":"k","tenantId":"other-tenant"}`)), "admin")
	if rec := serve(t, app.handleAPIKeys, req); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestAPIKeyListing(t *testing.T) {
	app := &controlPlaneApp{}

	// Non-admins get an empty list without touching the database.
	rec := serve(t, app.handleAPIKeys, asIdentity(httptest.NewRequest(http.MethodGet, "/api/v1/api-keys?tenantId=other-tenant", nil), "user"))
	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin status = %d, want 200", rec.Code)
	}
	var payload struct {
		APIKeys []apiKeySummary `json:"apiKeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload.APIKeys == nil || len(payload.APIKeys) != 0 {
		t.Fatalf("non-admin body = %s, want an empty apiKeys array", rec.Body.String())
	}

	// Admins cannot read another tenant's keys.
	rec = serve(t, app.handleAPIKeys, asIdentity(httptest.NewRequest(http.MethodGet, "/api/v1/api-keys?tenantId=other-tenant", nil), "admin"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant admin status = %d, want 403", rec.Code)
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
	app := &controlPlaneApp{}
	t.Setenv("SUEZ_EXPOSE_DEMO_CREDENTIALS", "")
	rec := serve(t, app.handleBootstrapStatus, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap/status", nil))
	if strings.Contains(rec.Body.String(), "demoCredentials") || strings.Contains(rec.Body.String(), "suez-demo-admin-key") {
		t.Fatalf("demo credentials exposed by default: %s", rec.Body.String())
	}

	t.Setenv("SUEZ_EXPOSE_DEMO_CREDENTIALS", "true")
	rec = serve(t, app.handleBootstrapStatus, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap/status", nil))
	if !strings.Contains(rec.Body.String(), "suez-demo-admin-key") {
		t.Fatalf("demo credentials missing when enabled: %s", rec.Body.String())
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
