//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// State shared by the ordered tests below.
var (
	tenantBKey, tenantBID string
	inferenceKey          string
)

func TestA_HealthAndExposure(t *testing.T) {
	for _, base := range []string{controlPlane, gateway} {
		expect(t, base+" liveness", call("GET", base+"/healthz", "", nil), 200)
		ready := call("GET", base+"/readyz", "", nil)
		want(t, base+" readiness", ready, 200)
		for _, leak := range []string{"postgres:5432", "redis:6379", "http://", "e2e-password"} {
			if strings.Contains(ready.raw, leak) {
				t.Errorf("%s readiness leaks %q: %s", base, leak, ready.raw)
			}
		}
	}
	// Internal endpoints need the shared token; without it they do not exist.
	expect(t, "internal resolve without token", call("GET", controlPlane+"/internal/auth/resolve", adminKey, nil), 404)
	expect(t, "internal capture without token", call("POST", controlPlane+"/internal/memory/capture", "", map[string]any{}), 404)
	expect(t, "internal resolve wrong token", call("GET", controlPlane+"/internal/auth/resolve", adminKey, nil, "X-SUEZ-Internal-Token", "guess"), 404)
	expect(t, "internal resolve with token", call("GET", controlPlane+"/internal/auth/resolve", adminKey, nil, "X-SUEZ-Internal-Token", internalTok), 200)

	// No credentials, no access, and no detail about why.
	for _, path := range []string{"/api/v1/models", "/api/v1/audit-log", "/api/v1/memory/sessions", "/api/v1/auth/session"} {
		resp := cp("GET", path, "", nil)
		if resp.status != 401 || resp.str("error") != "invalid or missing credentials" {
			t.Errorf("%s without credentials: %d %s", path, resp.status, resp.raw)
		}
	}
	expect(t, "gateway without credentials", call("POST", gateway+"/v1/chat/completions", "", map[string]any{}), 401)
	if resp := cp("GET", "/api/v1/models", "not-a-real-key", nil); resp.status != 401 || strings.Contains(resp.raw, "unknown") {
		t.Errorf("bad key: %d %s", resp.status, resp.raw)
	}
	// CORS: this stack lists no origins, so none is echoed.
	cors := call("GET", controlPlane+"/healthz", "", nil, "Origin", "https://evil.example")
	if cors.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("CORS header sent to an unlisted origin: %q", cors.header.Get("Access-Control-Allow-Origin"))
	}
	if cors.header.Get("X-Content-Type-Options") != "nosniff" || cors.header.Get("X-Request-Id") == "" {
		t.Errorf("security/correlation headers missing: %v", cors.header)
	}
	// API keys are stored hashed only.
	if stored := queryString(t, `select key_hash from api_keys where key_hash like '%suez-demo-admin-key%' limit 1`); stored != "" {
		t.Errorf("raw api key found in the database")
	}
}

func TestB_JWTVerification(t *testing.T) {
	// A verified token for a subject who is not a tenant member is refused.
	expect(t, "valid signature, unknown subject", cp("GET", "/api/v1/auth/session", userToken("nobody@example.com", "demo"), nil), 401)

	user := cp("POST", "/api/v1/users", adminKey, map[string]any{"subject": "alice@corp.example", "role": "user"})
	want(t, "create user", user, 201)
	userID := user.str("user", "id")

	session := cp("GET", "/api/v1/auth/session", userToken("alice@corp.example", "demo"), nil)
	want(t, "valid token for a member", session, 200)
	if session.str("identityContext", "tenantId") != demoTenant || session.str("identityContext", "actor") != "alice@corp.example" {
		t.Fatalf("identity = %s", session.raw)
	}
	if strings.Contains(session.raw, `"claims"`) {
		t.Errorf("raw token claims are returned in the identity")
	}

	with := func(change func(map[string]any)) map[string]any {
		claims := claimsFor("alice@corp.example", "demo")
		change(claims)
		return claims
	}
	valid := signRS256(idpKey, "e2e-1", claimsFor("alice@corp.example", "demo"))
	parts := strings.Split(valid, ".")
	forgeries := map[string]string{
		"unsigned (alg none)":           b64(map[string]string{"alg": "none", "typ": "JWT"}) + "." + b64(claimsFor("alice@corp.example", "demo")) + ".",
		"unsigned claiming admin":       b64(map[string]string{"alg": "none"}) + "." + b64(with(func(c map[string]any) { c["roles"] = []string{"admin"}; c["sub"] = "admin@example.com" })) + ".",
		"signed with attacker key":      signRS256(attackerKey, "e2e-1", claimsFor("alice@corp.example", "demo")),
		"attacker key, unknown kid":     signRS256(attackerKey, "attacker", claimsFor("alice@corp.example", "demo")),
		"HS256 with public key":         signHS256(idpKey.N.Bytes(), claimsFor("alice@corp.example", "demo")),
		"payload swapped":               parts[0] + "." + b64(with(func(c map[string]any) { c["sub"] = "admin@example.com" })) + "." + parts[2],
		"signature removed":             parts[0] + "." + parts[1] + ".",
		"expired":                       signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() })),
		"no expiry":                     signRS256(idpKey, "e2e-1", with(func(c map[string]any) { delete(c, "exp") })),
		"not yet valid":                 signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["nbf"] = time.Now().Add(10 * time.Minute).Unix() })),
		"wrong issuer":                  signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["iss"] = "http://evil.example" })),
		"wrong audience":                signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["aud"] = "another-api" })),
		"unknown tenant":                signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["tenant_slug"] = "no-such-tenant" })),
		"no tenant claim":               signRS256(idpKey, "e2e-1", with(func(c map[string]any) { delete(c, "tenant_slug") })),
		"tenant id and slug mismatched": signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["tenant_id"] = demoTenant; c["tenant_slug"] = "other" })),
		"no subject":                    signRS256(idpKey, "e2e-1", with(func(c map[string]any) { delete(c, "sub") })),
	}
	for name, token := range forgeries {
		for _, target := range []string{controlPlane + "/api/v1/auth/session", gateway + "/v1/identity"} {
			resp := call("GET", target, "Bearer "+token, nil)
			if resp.status != 401 {
				t.Errorf("%s at %s: status %d, want 401: %s", name, target, resp.status, resp.raw)
			}
		}
	}

	// Roles come from the user record, not from the token.
	selfPromoted := "Bearer " + signRS256(idpKey, "e2e-1", with(func(c map[string]any) { c["roles"] = []string{"admin"}; c["role"] = "admin" }))
	expect(t, "token claiming admin still cannot manage keys", cp("GET", "/api/v1/api-keys", selfPromoted, nil), 403)

	// Deactivating the user cuts access with the same, still-valid token.
	want(t, "deactivate", cp("PATCH", "/api/v1/users/"+userID, adminKey, map[string]any{"status": "inactive"}), 200)
	expect(t, "inactive user", cp("GET", "/api/v1/auth/session", userToken("alice@corp.example", "demo"), nil), 401)
	want(t, "reactivate", cp("PATCH", "/api/v1/users/"+userID, adminKey, map[string]any{"status": "active"}), 200)
	expect(t, "reactivated user", cp("GET", "/api/v1/auth/session", userToken("alice@corp.example", "demo"), nil), 200)
	expect(t, "invalid role refused", cp("PATCH", "/api/v1/users/"+userID, adminKey, map[string]any{"role": "superuser"}), 400)
}

func TestC_ScopedAPIKeys(t *testing.T) {
	createModel(t, adminKey, "stub-small", "stub", "", nil)
	createModel(t, adminKey, "stub-other", "stub", "", nil)

	readOnly, _ := createKey(t, adminKey, "ro-key", []string{"read-only"}, nil)
	inference, inferenceID := createKey(t, adminKey, "inference-key", []string{"gateway-inference"}, nil)
	inferenceKey = inference
	policyAdmin, _ := createKey(t, adminKey, "policy-key", []string{"policy-admin"}, nil)
	auditor, _ := createKey(t, adminKey, "audit-key", []string{"audit-reader"}, nil)
	grant(t, adminKey, "inference-key", "model", "stub-small")
	grant(t, adminKey, "inference-key", "model", "stub-other")
	grant(t, adminKey, "ro-key", "model", "stub-small")

	// The raw key is shown once: listing returns metadata, never the key.
	list := cp("GET", "/api/v1/api-keys", adminKey, nil)
	want(t, "list keys", list, 200)
	if strings.Contains(list.raw, inference) || strings.Contains(list.raw, "key_hash") || strings.Contains(list.raw, "sha256:") {
		t.Errorf("key listing exposes key material: %s", list.raw)
	}
	if stored := queryString(t, `select key_hash from api_keys where id = $1`, inferenceID); !strings.HasPrefix(stored, "sha256:") || strings.Contains(stored, inference) {
		t.Errorf("stored key is not a hash: %q", stored)
	}

	type check struct {
		name, method, url string
		body              any
		want              map[string]int
	}
	chatBody := map[string]any{"model": "stub-small", "messages": []map[string]string{{"role": "user", "content": "scope check"}}}
	keys := map[string]string{"read-only": readOnly, "inference": inference, "policy": policyAdmin, "audit": auditor}
	checks := []check{
		{"read models", "GET", controlPlane + "/api/v1/models", nil, map[string]int{"read-only": 200, "inference": 200, "policy": 200, "audit": 200}},
		{"chat", "POST", gateway + "/v1/chat/completions", chatBody, map[string]int{"read-only": 403, "inference": 200, "policy": 403, "audit": 403}},
		{"run tool", "POST", gateway + "/v1/tools/execute", map[string]any{"name": "weather.lookup", "input": map[string]any{"city": "Paris"}}, map[string]int{"read-only": 403, "policy": 403, "audit": 403}},
		{"create model", "POST", controlPlane + "/api/v1/models", map[string]any{"externalName": "x", "provider": "stub", "modelName": "x"}, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"read audit log", "GET", controlPlane + "/api/v1/audit-log", nil, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 200}},
		{"list policies", "GET", controlPlane + "/api/v1/policies", nil, map[string]int{"read-only": 403, "inference": 403, "policy": 200, "audit": 403}},
		{"create policy", "POST", controlPlane + "/api/v1/policies", map[string]any{}, map[string]int{"read-only": 403, "inference": 403, "policy": 400, "audit": 403}},
		{"list keys", "GET", controlPlane + "/api/v1/api-keys", nil, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"create key", "POST", controlPlane + "/api/v1/api-keys", map[string]any{"label": "x", "scopes": []string{"read-only"}}, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"manage users", "GET", controlPlane + "/api/v1/users", nil, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"approve access", "POST", controlPlane + "/api/v1/access-requests/00000000-0000-0000-0000-000000000000/approve", nil, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"create tenant", "POST", controlPlane + "/api/v1/tenants", map[string]any{"name": "x", "slug": "xx"}, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"memory settings", "PUT", controlPlane + "/api/v1/memory/settings", map[string]any{"captureEnabled": true}, map[string]int{"read-only": 403, "inference": 403, "policy": 403, "audit": 403}},
		{"read memory", "GET", controlPlane + "/api/v1/memory/sessions", nil, map[string]int{"read-only": 403, "inference": 200, "policy": 403, "audit": 403}},
	}
	for _, c := range checks {
		for scope, status := range c.want {
			if resp := call(c.method, c.url, keys[scope], c.body); resp.status != status {
				t.Errorf("%s with %s key: status %d, want %d: %s", c.name, scope, resp.status, status, resp.raw)
			}
		}
	}

	// A key cannot hand out more than its creator holds.
	expect(t, "tenant admin minting platform-admin", cp("POST", "/api/v1/api-keys", adminKeyNoPlatform(t), map[string]any{"label": "esc", "scopes": []string{"platform-admin"}}), 403)
	expect(t, "unknown scope", cp("POST", "/api/v1/api-keys", adminKey, map[string]any{"label": "esc", "scopes": []string{"root"}}), 400)
	expect(t, "no scopes", cp("POST", "/api/v1/api-keys", adminKey, map[string]any{"label": "esc"}), 400)

	// Model restriction on a key.
	restricted, _ := createKey(t, adminKey, "restricted-key", []string{"gateway-inference"}, map[string]any{"allowedModels": []string{"stub-small"}})
	grant(t, adminKey, "restricted-key", "model", "stub-small")
	grant(t, adminKey, "restricted-key", "model", "stub-other")
	expect(t, "restricted key, allowed model", chat(restricted, "stub-small", "allowed", nil), 200)
	expect(t, "restricted key, other model", chat(restricted, "stub-other", "not allowed", nil), 403)

	// Per-key rate limit.
	limited, _ := createKey(t, adminKey, "limited-key", []string{"gateway-inference"}, map[string]any{"rateLimitRpm": 3})
	grant(t, adminKey, "limited-key", "model", "stub-small")
	statuses := []int{}
	for i := 0; i < 5; i++ {
		statuses = append(statuses, chat(limited, "stub-small", fmt.Sprintf("limited %d", i), nil).status)
	}
	if fmt.Sprint(statuses) != "[200 200 200 429 429]" {
		t.Errorf("rate limited key statuses = %v, want [200 200 200 429 429]", statuses)
	}

	// Expiry and revocation.
	shortLived, _ := createKey(t, adminKey, "short-key", []string{"read-only"}, map[string]any{"expiresAt": time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339)})
	expect(t, "key before expiry", cp("GET", "/api/v1/models", shortLived, nil), 200)
	time.Sleep(4 * time.Second)
	expect(t, "key after expiry", cp("GET", "/api/v1/models", shortLived, nil), 401)

	revocable, revocableID := createKey(t, adminKey, "revocable-key", []string{"gateway-inference"}, nil)
	grant(t, adminKey, "revocable-key", "model", "stub-small")
	expect(t, "key before revocation", chat(revocable, "stub-small", "before revoke", nil), 200)
	revoked := cp("DELETE", "/api/v1/api-keys/"+revocableID, adminKey, nil)
	want(t, "revoke", revoked, 200)
	if revoked.str("apiKey", "status") != "revoked" || revoked.str("apiKey", "revokedBy") == "" {
		t.Errorf("revocation not attributed: %s", revoked.raw)
	}
	expect(t, "revoked key at control plane", cp("GET", "/api/v1/models", revocable, nil), 401)
	expect(t, "revoked key at gateway", chat(revocable, "stub-small", "after revoke", nil), 401)

	// Attribution: creation and revocation are in the audit log.
	audit := cp("GET", "/api/v1/audit-log?action=api_key&limit=200", adminKey, nil)
	if !strings.Contains(audit.raw, "api_key.create") || !strings.Contains(audit.raw, "api_key.revoke") {
		t.Errorf("key lifecycle missing from the audit log")
	}
	if strings.Contains(audit.raw, revocable) || strings.Contains(audit.raw, inference) {
		t.Errorf("raw api key present in the audit log")
	}

	// Grant revocation takes effect at the gateway.
	grantID := grant(t, adminKey, "inference-key", "model", "stub-other")
	expect(t, "before grant revoke", chat(inference, "stub-other", "grant check", nil), 200)
	want(t, "revoke grant", cp("DELETE", "/api/v1/access-grants/"+grantID, adminKey, nil), 200)
	expect(t, "after grant revoke", chat(inference, "stub-other", "grant check 2", nil), 403)
}

// adminKeyNoPlatform returns a tenant-admin key without the platform scope.
func adminKeyNoPlatform(t *testing.T) string {
	t.Helper()
	key, _ := createKey(t, adminKey, fmt.Sprintf("tenant-admin-%d", time.Now().UnixNano()), []string{"tenant-admin"}, nil)
	return key
}

func TestD_TenantIsolation(t *testing.T) {
	tenant := cp("POST", "/api/v1/tenants", adminKey, map[string]any{"name": "Tenant B", "slug": "tenant-b"})
	want(t, "create tenant", tenant, 201)
	tenantBID, tenantBKey = tenant.str("tenant", "id"), tenant.str("rawInitialApiKey")
	if tenantBKey == "" {
		t.Fatalf("no initial key for the new tenant: %s", tenant.raw)
	}
	// A tenant admin (without the platform scope) cannot create tenants or see others.
	tenantAdmin := adminKeyNoPlatform(t)
	expect(t, "tenant admin creating a tenant", cp("POST", "/api/v1/tenants", tenantAdmin, map[string]any{"name": "C", "slug": "tenant-c"}), 403)
	if own := cp("GET", "/api/v1/tenants", tenantAdmin, nil); strings.Contains(own.raw, "tenant-b") {
		t.Errorf("tenant admin can see another tenant: %s", own.raw)
	}

	session := cp("GET", "/api/v1/auth/session", tenantBKey, nil)
	if session.str("identityContext", "tenantId") != tenantBID {
		t.Fatalf("tenant B key resolves to %s", session.raw)
	}
	// Tenant B sees none of tenant A's data.
	for _, path := range []string{"/api/v1/models", "/api/v1/tools", "/api/v1/api-keys", "/api/v1/users", "/api/v1/access-grants", "/api/v1/audit-log", "/api/v1/policies"} {
		resp := cp("GET", path, tenantBKey, nil)
		want(t, "tenant B "+path, resp, 200)
		for _, foreign := range []string{"stub-small", "inference-key", "alice@corp.example", demoTenant, "weather.lookup"} {
			if strings.Contains(resp.raw, foreign) {
				t.Errorf("tenant B sees tenant A data (%s) at %s", foreign, path)
			}
		}
	}
	expect(t, "tenant B listing tenant A keys", cp("GET", "/api/v1/api-keys?tenantId="+demoTenant, tenantBKey, nil), 403)
	expect(t, "tenant B minting a tenant A key", cp("POST", "/api/v1/api-keys", tenantBKey, map[string]any{"label": "x", "scopes": []string{"read-only"}, "tenantId": demoTenant}), 403)
	expect(t, "tenant B using tenant A model", chat(tenantBKey, "stub-small", "cross tenant", nil), 404)

	// A JWT for tenant A's user does not work against tenant B and vice versa.
	want(t, "tenant B user", cp("POST", "/api/v1/users", tenantBKey, map[string]any{"subject": "bob@b.example", "role": "admin"}), 201)
	expect(t, "tenant B member in tenant B", cp("GET", "/api/v1/auth/session", userToken("bob@b.example", "tenant-b"), nil), 200)
	expect(t, "tenant B member claiming tenant A", cp("GET", "/api/v1/auth/session", userToken("bob@b.example", "demo"), nil), 401)
	expect(t, "tenant A member claiming tenant B", cp("GET", "/api/v1/auth/session", userToken("alice@corp.example", "tenant-b"), nil), 401)
}

func policyBody(name, rego string) map[string]any {
	return map[string]any{"name": name, "scope": "model", "targetName": "*", "regoPackage": "suez.authz", "regoSource": rego, "enabled": true}
}

func addPolicyTest(t *testing.T, auth, policyID string, expectedAllow bool, reasons []string) {
	t.Helper()
	want(t, "add policy test", cp("POST", "/api/v1/policies/"+policyID+"/tests", auth, map[string]any{
		"name": "t", "action": "chat.completions", "identity": map[string]any{"actor": "x", "roles": []string{"user"}},
		"resource": map[string]any{"type": "model", "name": "m", "enabled": true}, "request": map[string]any{},
		"expectedAllow": expectedAllow, "expectedReasons": reasons,
	}), 201)
}

func TestE_PolicyIsolation(t *testing.T) {
	createModel(t, tenantBKey, "b-model", "stub", "", nil)
	grant(t, tenantBKey, "Initial tenant admin key", "model", "b-model")
	expect(t, "tenant B chat baseline", chat(tenantBKey, "b-model", "baseline b", nil), 200)
	expect(t, "tenant A chat baseline", chat(inferenceKey, "stub-small", "baseline a", nil), 200)
	modulesBefore := opaModules(t)

	// Tenant B drafts a policy that denies its own chat, and also tries to add
	// an allow-everything rule to the base package.
	deny := cp("POST", "/api/v1/policies", tenantBKey, policyBody("b-deny", fmt.Sprintf(`package suez.authz

allow if { true }

published_policy_results contains {"policyId": "__POLICY_ID__", "allow": false, "reason": "b_block"} if {
    input.identity.tenantId == "%s"
    input.action == "chat.completions"
}`, tenantBID)))
	want(t, "tenant B draft", deny, 201)
	denyID := deny.str("policy", "id")
	addPolicyTest(t, tenantBKey, denyID, false, []string{"b_block"})

	// Creating, validating and testing a draft changes nothing that is live.
	want(t, "validate", cp("POST", "/api/v1/policies/"+denyID+"/validate", tenantBKey, nil), 200)
	run := cp("POST", "/api/v1/policies/"+denyID+"/tests/run", tenantBKey, nil)
	want(t, "tests/run is reachable", run, 200)
	if run.str("testRun", "status") != "passed" {
		t.Fatalf("policy tests did not pass: %s", run.raw)
	}
	if after := opaModules(t); after != modulesBefore {
		t.Errorf("validation/testing changed the loaded policy modules:\n before %s\n after  %s", modulesBefore, after)
	}
	expect(t, "tenant B still allowed while only a draft exists", chat(tenantBKey, "b-model", "draft only", nil), 200)
	if allow, _ := opaDecide(demoTenant, "chat.completions", "model"); !allow {
		t.Errorf("tenant A decision changed while tenant B tested a draft")
	}

	// Tenant A cannot touch tenant B's policy in any way.
	for _, action := range []string{"validate", "publish", "rollback", "tests/run"} {
		if resp := cp("POST", "/api/v1/policies/"+denyID+"/"+action, adminKey, nil); resp.status == 200 {
			t.Errorf("tenant A could %s tenant B's policy: %s", action, resp.raw)
		}
	}
	for _, read := range []string{"versions", "tests"} {
		if resp := cp("GET", "/api/v1/policies/"+denyID+"/"+read, adminKey, nil); resp.status == 200 && strings.Contains(resp.raw, denyID) {
			t.Errorf("tenant A could read tenant B's policy %s", read)
		}
	}

	want(t, "tenant B publish", cp("POST", "/api/v1/policies/"+denyID+"/publish", tenantBKey, nil), 200)
	if allow, reasons := opaDecide(tenantBID, "chat.completions", "model"); allow || !strings.Contains(reasons, "b_block") {
		t.Errorf("tenant B policy not enforced for tenant B: allow=%v reasons=%s", allow, reasons)
	}
	if resp := chat(tenantBKey, "b-model", "after publish", nil); resp.status != 403 {
		t.Errorf("tenant B chat after its deny policy: %d, want 403", resp.status)
	}
	// The `allow if { true }` in tenant B's module did not open anything, and
	// tenant A is exactly as before.
	if allow, reasons := opaDecide(demoTenant, "chat.completions", "model"); !allow || reasons != "[]" {
		t.Errorf("tenant A affected by tenant B policy: allow=%v reasons=%s", allow, reasons)
	}
	if allow, _ := opaDecide(demoTenant, "admin.everything", "model"); allow {
		t.Errorf("tenant B's allow rule leaked into the base policy")
	}
	expect(t, "tenant A chat after tenant B publish", chat(inferenceKey, "stub-small", "a after b publish", nil), 200)

	// Tenant B publishes a policy written to deny tenant A. It has no effect.
	attack := cp("POST", "/api/v1/policies", tenantBKey, policyBody("b-attack", fmt.Sprintf(`package suez.authz
# tenant %s
published_policy_results contains {"policyId": "__POLICY_ID__", "allow": false, "reason": "hijack"} if {
    input.identity.tenantId == "%s"
    input.action == "chat.completions"
}`, tenantBID, demoTenant)))
	want(t, "attack draft", attack, 201)
	attackID := attack.str("policy", "id")
	addPolicyTest(t, tenantBKey, attackID, true, []string{})
	want(t, "attack publish", cp("POST", "/api/v1/policies/"+attackID+"/publish", tenantBKey, nil), 200)
	if allow, reasons := opaDecide(demoTenant, "chat.completions", "model"); !allow || strings.Contains(reasons, "hijack") {
		t.Errorf("tenant B policy aimed at tenant A took effect: allow=%v reasons=%s", allow, reasons)
	}
	expect(t, "tenant A chat after attack", chat(inferenceKey, "stub-small", "a after attack", nil), 200)

	// Modules that try to leave their namespace are refused and never loaded.
	modulesNow := opaModules(t)
	for name, source := range map[string]string{
		"decoy package in a string": "package suez.authz # real\nx := `\npackage suez.authz\n`\nallow if { true }\npublished_policy_results contains {\"policyId\": \"__POLICY_ID__\", \"allow\": true} if { input.action == \"chat.completions\" }",
		"other tenant's namespace":  "package suez.generated.t_" + strings.ReplaceAll(demoTenant, "-", "_") + ".p_x\npublished_policy_results contains {\"policyId\": \"__POLICY_ID__\", \"allow\": false} if { input.action == \"chat.completions\" }",
		"base package directly":     "package suez\nauthz := {\"allow\": true}",
	} {
		draft := cp("POST", "/api/v1/policies", tenantBKey, policyBody("escape", source))
		if draft.status == 201 {
			id := draft.str("policy", "id")
			addPolicyTest(t, tenantBKey, id, true, []string{})
			if published := cp("POST", "/api/v1/policies/"+id+"/publish", tenantBKey, nil); published.status == 200 {
				t.Errorf("%s: an escaping module was published", name)
			}
		}
	}
	if after := opaModules(t); after != modulesNow {
		t.Errorf("rejected modules changed what is loaded in OPA:\n before %s\n after  %s", modulesNow, after)
	}
	if allow, _ := opaDecide(demoTenant, "chat.completions", "model"); !allow {
		t.Errorf("tenant A decision changed after escape attempts")
	}

	// Rollback restores tenant B's earlier behaviour and still leaves A alone.
	allowAgain := cp("POST", "/api/v1/policies", tenantBKey, policyBody("b-allow", fmt.Sprintf(`package suez.authz
published_policy_results contains {"policyId": "__POLICY_ID__", "allow": true, "reason": "b_ok"} if {
    input.identity.tenantId == "%s"
    input.action == "chat.completions"
}`, tenantBID)))
	want(t, "allow draft", allowAgain, 201)
	allowID := allowAgain.str("policy", "id")
	addPolicyTest(t, tenantBKey, allowID, true, []string{})
	want(t, "publish allow", cp("POST", "/api/v1/policies/"+allowID+"/publish", tenantBKey, nil), 200)
	expect(t, "tenant B allowed again", chat(tenantBKey, "b-model", "allowed again", nil), 200)
	want(t, "rollback to the deny version", cp("POST", "/api/v1/policies/"+denyID+"/rollback", tenantBKey, nil), 200)
	expect(t, "tenant B denied after rollback", chat(tenantBKey, "b-model", "after rollback", nil), 403)
	expect(t, "tenant A unaffected by rollback", chat(inferenceKey, "stub-small", "a after rollback", nil), 200)
	// Leave tenant B usable for later tests.
	final := cp("POST", "/api/v1/policies/"+allowID+"/rollback", tenantBKey, nil)
	want(t, "restore allow", final, 200)
	expect(t, "tenant B usable", chat(tenantBKey, "b-model", "restored", nil), 200)
}

func TestF_ToolSecrets(t *testing.T) {
	const secret = "tool-secret-value-9f8e7d6c"
	tool := cp("POST", "/api/v1/tools", adminKey, map[string]any{
		"name": "secret.echo", "description": "echo", "executionMode": "http", "enabled": true,
		"targetUrl": "http://e2e:9200/v1/tool-does-not-exist", "httpHeaders": map[string]string{"Authorization": "Bearer " + secret},
	})
	want(t, "create tool", tool, 201)
	for _, token := range []string{inferenceKey, userToken("alice@corp.example", "demo")} {
		list := cp("GET", "/api/v1/tools", token, nil)
		want(t, "list tools", list, 200)
		if strings.Contains(list.raw, secret) || !strings.Contains(list.raw, `"Authorization":"***"`) {
			t.Errorf("tool header visible to a non-admin: %s", list.raw)
		}
	}
	if admin := cp("GET", "/api/v1/tools", adminKey, nil); !strings.Contains(admin.raw, secret) {
		t.Errorf("admin cannot see the configured header")
	}
	// Running the tool (it fails upstream) leaves the secret out of responses,
	// audit records and stored executions.
	grant(t, adminKey, "inference-key", "tool", "secret.echo")
	result := call("POST", gateway+"/v1/tools/execute", inferenceKey, map[string]any{"name": "secret.echo", "input": map[string]any{}})
	if result.status < 400 || strings.Contains(result.raw, secret) {
		t.Errorf("tool error response: %d %s", result.status, result.raw)
	}
	audit := cp("GET", "/api/v1/audit-log?limit=200", adminKey, nil)
	if strings.Contains(audit.raw, secret) {
		t.Errorf("tool secret in the audit log")
	}
	if leaked := queryString(t, `select id::text from tool_executions where output_payload::text like '%'||$1||'%' or input_payload::text like '%'||$1||'%' limit 1`, secret); leaked != "" {
		t.Errorf("tool secret stored in tool_executions")
	}
	if leaked := queryString(t, `select id::text from audit_log where metadata::text like '%'||$1||'%' limit 1`, secret); leaked != "" {
		t.Errorf("tool secret stored in audit_log")
	}
}

func TestG_OptionalParametersCacheAndProviderErrors(t *testing.T) {
	createModel(t, adminKey, "prov-ok", "openai-compatible", "up-ok", nil)
	createModel(t, adminKey, "prov-500", "openai-compatible", "up-500", nil)
	createModel(t, adminKey, "prov-empty", "openai-compatible", "up-empty", nil)
	for _, model := range []string{"prov-ok", "prov-500", "prov-empty"} {
		grant(t, adminKey, "inference-key", "model", model)
	}

	before := provider.count()
	first := chat(inferenceKey, "prov-ok", "omitted params", nil)
	want(t, "chat without optional parameters", first, 200)
	sent := provider.last()
	for _, field := range []string{"temperature", "max_tokens", "top_p", "stream", "suez", "tools"} {
		if _, present := sent[field]; present {
			t.Errorf("%s was sent upstream although the caller omitted it: %v", field, sent[field])
		}
	}
	if sent["model"] != "up-ok" || first.str("model") != "prov-ok" {
		t.Errorf("model mapping wrong: upstream %v, response %v", sent["model"], first.str("model"))
	}
	if first.header.Get("X-Suez-Cache") != "MISS" {
		t.Errorf("first call cache header = %q", first.header.Get("X-Suez-Cache"))
	}
	provider.mu.Lock()
	sentAuth := provider.auth[len(provider.auth)-1]
	provider.mu.Unlock()
	if sentAuth != "Bearer e2e-provider-key-not-real" {
		t.Errorf("provider credential not taken from gateway configuration: %q", sentAuth)
	}

	// Explicit zero is forwarded as zero.
	want(t, "explicit zero temperature", chat(inferenceKey, "prov-ok", "explicit zero", map[string]any{"temperature": 0, "max_tokens": 7}), 200)
	sent = provider.last()
	if sent["temperature"] != float64(0) || sent["max_tokens"] != float64(7) {
		t.Errorf("explicit values not forwarded: %v", sent)
	}
	expect(t, "max_tokens 0 rejected", chat(inferenceKey, "prov-ok", "bad", map[string]any{"max_tokens": 0}), 400)
	expect(t, "temperature 3 rejected", chat(inferenceKey, "prov-ok", "bad", map[string]any{"temperature": 3}), 400)

	// Same request again: served from cache, provider not called.
	calls := provider.count()
	second := chat(inferenceKey, "prov-ok", "omitted params", nil)
	if second.header.Get("X-Suez-Cache") != "HIT" || provider.count() != calls {
		t.Errorf("identical request was not served from cache (header %q)", second.header.Get("X-Suez-Cache"))
	}
	// Any meaningful difference misses.
	for name, extra := range map[string]map[string]any{
		"temperature": {"temperature": 0.7}, "max_tokens": {"max_tokens": 50},
		"response_format": {"response_format": map[string]string{"type": "json_object"}},
		"tools":           {"tools": []map[string]any{{"type": "function", "function": map[string]any{"name": "f"}}}},
	} {
		if resp := chat(inferenceKey, "prov-ok", "omitted params", extra); resp.header.Get("X-Suez-Cache") != "MISS" {
			t.Errorf("request differing in %s was served from cache", name)
		}
	}
	withSystem := call("POST", gateway+"/v1/chat/completions", inferenceKey, map[string]any{"model": "prov-ok", "messages": []map[string]string{{"role": "system", "content": "be terse"}, {"role": "user", "content": "omitted params"}}})
	if withSystem.header.Get("X-Suez-Cache") != "MISS" {
		t.Errorf("request with a different system message was served from cache")
	}
	// Another tenant never shares a cache entry.
	createModel(t, tenantBKey, "prov-ok", "openai-compatible", "up-ok", nil)
	grant(t, tenantBKey, "Initial tenant admin key", "model", "prov-ok")
	if resp := chat(tenantBKey, "prov-ok", "omitted params", nil); resp.status != 200 || resp.header.Get("X-Suez-Cache") != "MISS" {
		t.Errorf("tenant B was served tenant A's cached answer: %d %q", resp.status, resp.header.Get("X-Suez-Cache"))
	}
	// Cache hits and misses are both audited, without prompt content.
	trace := second.header.Get("X-SUEZ-Trace-Id")
	hit := cp("GET", "/api/v1/audit-log/traces/"+trace, adminKey, nil)
	if !strings.Contains(hit.raw, `"cache":"hit"`) {
		t.Errorf("cache hit not audited: %s", hit.raw)
	}
	miss := cp("GET", "/api/v1/audit-log/traces/"+first.header.Get("X-SUEZ-Trace-Id"), adminKey, nil)
	if !strings.Contains(miss.raw, `"cache":"miss"`) {
		t.Errorf("cache miss not audited: %s", miss.raw)
	}
	if leaked := queryString(t, `select id::text from audit_log where metadata::text like '%omitted params%' limit 1`); leaked != "" {
		t.Errorf("prompt content stored in the audit log")
	}
	_ = before

	// Provider failure: normalized error, nothing from the provider body, not cached.
	failing := chat(inferenceKey, "prov-500", "this prompt is private 4242", nil)
	if failing.status != 502 || failing.str("code") != "upstream_unavailable" || failing.str("traceId") == "" {
		t.Errorf("provider 500: %d %s", failing.status, failing.raw)
	}
	for _, leak := range []string{"sk-live", "SECRET", "private 4242", "internal:"} {
		if strings.Contains(failing.raw, leak) {
			t.Errorf("provider error body leaked %q to the caller", leak)
		}
	}
	if leaked := queryString(t, `select id::text from audit_log where metadata::text like '%sk-live%' or metadata::text like '%private 4242%' limit 1`); leaked != "" {
		t.Errorf("provider error body or prompt stored in the audit log")
	}
	callsBefore := provider.count()
	chat(inferenceKey, "prov-500", "this prompt is private 4242", nil)
	if provider.count() == callsBefore {
		t.Errorf("a failed response was served from cache")
	}
	empty := chat(inferenceKey, "prov-empty", "empty choices", nil)
	if empty.status != 502 || empty.str("code") != "upstream_invalid_response" {
		t.Errorf("empty choices: %d %s", empty.status, empty.raw)
	}
	callsBefore = provider.count()
	chat(inferenceKey, "prov-empty", "empty choices", nil)
	if provider.count() == callsBefore {
		t.Errorf("an invalid response was cached")
	}
}

func TestH_QuotaAndStreaming(t *testing.T) {
	createModel(t, adminKey, "quota-model", "stub", "", map[string]any{"maxRpm": 2})
	grant(t, adminKey, "inference-key", "model", "quota-model")
	statuses := []int{}
	for i := 0; i < 4; i++ {
		statuses = append(statuses, chat(inferenceKey, "quota-model", fmt.Sprintf("quota %d", i), nil).status)
	}
	if fmt.Sprint(statuses) != "[200 200 429 429]" {
		t.Errorf("model request quota statuses = %v, want [200 200 429 429]", statuses)
	}
	createModel(t, adminKey, "tpm-model", "stub", "", map[string]any{"maxTpm": 60})
	grant(t, adminKey, "inference-key", "model", "tpm-model")
	expect(t, "within token quota", chat(inferenceKey, "tpm-model", "short", nil), 200)
	expect(t, "over token quota", chat(inferenceKey, "tpm-model", strings.Repeat("many tokens ", 40), nil), 429)

	createModel(t, adminKey, "prov-stream", "openai-compatible", "up-stream", nil)
	createModel(t, adminKey, "prov-break", "openai-compatible", "up-stream-break", nil)
	grant(t, adminKey, "inference-key", "model", "prov-stream")
	grant(t, adminKey, "inference-key", "model", "prov-break")

	status, header, text, events, done := readSSE(inferenceKey, "prov-stream", "stream please", "X-SUEZ-Session-Id", "e2e-stream-1")
	if status != 200 || !done || text != "streamed answer from mock" || !strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: status %d done %v text %q", status, done, text)
	}
	if strings.Contains(strings.Join(events, "\n"), "up-stream") {
		t.Errorf("upstream model name leaked into the stream")
	}
	trailer := ""
	for _, event := range events {
		if strings.HasPrefix(event, ": suez ") {
			trailer = event
		}
	}
	if !strings.Contains(trailer, `"status"`) || !strings.Contains(trailer, "traceId") {
		t.Errorf("stream trailer missing: %q", trailer)
	}
	trace := header.Get("X-SUEZ-Trace-Id")
	audit := cp("GET", "/api/v1/audit-log/traces/"+trace, adminKey, nil)
	if !strings.Contains(audit.raw, `"stream":true`) || !strings.Contains(audit.raw, `"decision":"allow"`) || !strings.Contains(audit.raw, `"actualTotal":20`) {
		t.Errorf("stream not audited with provider usage: %s", audit.raw)
	}
	usage := call("GET", gateway+"/v1/context/usage?session=e2e-stream-1", inferenceKey, nil)
	if usage.dig("measurement", "contextTokens") != float64(20) || usage.str("measurement", "source") != "provider_reported" {
		t.Errorf("stream usage not recorded: %s", usage.raw)
	}

	// Stub provider streams too, and authorization applies to streams.
	if status, _, text, _, done := readSSE(inferenceKey, "stub-small", "stub stream"); status != 200 || !done || !strings.Contains(text, "stub stream") {
		t.Errorf("stub stream: %d %v %q", status, done, text)
	}
	readOnly, _ := createKey(t, adminKey, "ro-stream", []string{"read-only"}, nil)
	if status, _, _, _, _ := readSSE(readOnly, "stub-small", "no"); status != 403 {
		t.Errorf("read-only key streaming: %d, want 403", status)
	}
	if status, _, _, _, _ := readSSE(inferenceKey, "no-such-model", "no"); status != 404 {
		t.Errorf("stream for unknown model: %d, want 404", status)
	}

	// Upstream dies mid-stream: the client gets an error event and no [DONE];
	// the partial exchange is audited and metered.
	status, header, text, events, done = readSSE(inferenceKey, "prov-break", "break please")
	joined := strings.Join(events, "\n")
	if status != 200 || done || text != "streamed answer " || !strings.Contains(joined, "event: error") || !strings.Contains(joined, "upstream_stream_interrupted") {
		t.Errorf("interrupted stream: status %d done %v text %q events %v", status, done, text, events)
	}
	partial := cp("GET", "/api/v1/audit-log/traces/"+header.Get("X-SUEZ-Trace-Id"), adminKey, nil)
	if !strings.Contains(partial.raw, `"partial":true`) || !strings.Contains(partial.raw, `"decision":"error"`) {
		t.Errorf("interrupted stream not audited as partial: %s", partial.raw)
	}
}

func TestI_ContextThresholdsCheckpointsAndContinuation(t *testing.T) {
	createModel(t, adminKey, "ctx-model", "openai-compatible", "up-ctx", map[string]any{"contextWindow": 1000})
	grant(t, adminKey, "inference-key", "model", "ctx-model")
	session := "e2e-ctx-1"
	send := func(content string) response {
		return chat(inferenceKey, "ctx-model", content, nil, "X-SUEZ-Session-Id", session)
	}

	// Capture is off by default: nothing is stored.
	want(t, "chat before capture is enabled", send("first message USAGE=100"), 200)
	if stored := queryString(t, `select count(*)::text from memory_sessions where external_session_id = $1`, session); stored != "0" {
		t.Fatalf("memory captured while capture was disabled")
	}
	want(t, "enable capture", cp("PUT", "/api/v1/memory/settings", adminKey, map[string]any{"captureEnabled": true, "retrievalEnabled": true, "injectionEnabled": false}), 200)

	steps := []struct {
		usage  int
		status string
		draft  bool
	}{{500, "ok", false}, {820, "notice", false}, {870, "checkpoint", true}, {960, "warning", true}}
	draftID := ""
	conversation := []map[string]string{}
	for _, step := range steps {
		content := fmt.Sprintf("Plan the migration step %d. My API key is sk-proj-abcdefghijklmnopqrstuvwxyz012345 USAGE=%d", step.usage, step.usage)
		conversation = append(conversation, map[string]string{"role": "user", "content": content})
		resp := call("POST", gateway+"/v1/chat/completions", inferenceKey, map[string]any{"model": "ctx-model", "messages": conversation}, "X-SUEZ-Session-Id", session)
		want(t, fmt.Sprintf("chat at %d tokens", step.usage), resp, 200)
		conversation = append(conversation, map[string]string{"role": "assistant", "content": resp.dig("choices").([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)})
		if got := resp.header.Get("X-SUEZ-Context-Status"); got != step.status {
			t.Errorf("at %d/1000 tokens: context status %q, want %q", step.usage, got, step.status)
		}
		if got := resp.header.Get("X-SUEZ-Context-Percent"); got != fmt.Sprintf("%.2f", float64(step.usage)/10) {
			t.Errorf("at %d tokens: percent header %q", step.usage, got)
		}
		if resp.header.Get("X-SUEZ-Context-Source") != "provider_reported" {
			t.Errorf("context source = %q, want provider_reported", resp.header.Get("X-SUEZ-Context-Source"))
		}
		gotDraft := resp.header.Get("X-SUEZ-Checkpoint-Draft")
		if step.draft && gotDraft == "" {
			t.Errorf("at %d tokens: no checkpoint draft was created", step.usage)
		}
		if !step.draft && gotDraft != "" {
			t.Errorf("at %d tokens: unexpected checkpoint draft", step.usage)
		}
		if step.draft {
			if draftID != "" && gotDraft != draftID {
				t.Errorf("a second threshold draft was created for the same session")
			}
			draftID = gotDraft
		}
	}
	usage := call("GET", gateway+"/v1/context/usage?session="+session, inferenceKey, nil)
	if usage.str("contextStatus") != "warning" || usage.dig("percent") != float64(96) || usage.dig("thresholds", "checkpointPercent") != float64(85) {
		t.Errorf("usage endpoint: %s", usage.raw)
	}

	// Stored content: secrets redacted, content encrypted at rest.
	time.Sleep(500 * time.Millisecond)
	if leaked := queryString(t, `select id::text from memory_items where preview like '%sk-proj-abcdef%' or encode(content_sealed, 'escape') like '%sk-proj-abcdef%' limit 1`); leaked != "" {
		t.Errorf("a credential was stored in memory")
	}
	if plaintext := queryString(t, `select id::text from memory_items where encode(content_sealed, 'escape') like '%Plan the migration%' limit 1`); plaintext != "" {
		t.Errorf("memory content is stored unencrypted")
	}
	if redactions := queryString(t, `select sum(redaction_count)::text from memory_items i join memory_sessions s on s.id = i.session_id where s.external_session_id = $1`, session); redactions == "" || redactions == "0" {
		t.Errorf("no redactions recorded although the prompts contained a key")
	}

	// Progressive retrieval: index, then timeline, then one item.
	index := cp("GET", "/api/v1/memory/sessions?q=migration", inferenceKey, nil)
	want(t, "session index", index, 200)
	sessions, _ := index.dig("sessions").([]any)
	if len(sessions) != 1 || strings.Contains(index.raw, `"content"`) {
		t.Fatalf("index search: %s", index.raw)
	}
	sessionID := sessions[0].(map[string]any)["id"].(string)
	timeline := cp("GET", "/api/v1/memory/sessions/"+sessionID+"/timeline", inferenceKey, nil)
	want(t, "timeline", timeline, 200)
	items, _ := timeline.dig("timeline").([]any)
	if len(items) < 8 || strings.Contains(timeline.raw, `"content":`) || !strings.Contains(timeline.raw, "[REDACTED:api-key]") {
		t.Errorf("timeline has %d items or exposes content/secret: %.300s", len(items), timeline.raw)
	}
	firstItem := items[0].(map[string]any)
	detail := cp("GET", "/api/v1/memory/items/"+firstItem["id"].(string), inferenceKey, nil)
	want(t, "item detail", detail, 200)
	if !strings.Contains(detail.str("item", "content"), "Plan the migration") || strings.Contains(detail.raw, "sk-proj-abcdef") {
		t.Errorf("item detail: %s", detail.raw)
	}
	if detail.dig("item", "generated") != false || detail.str("item", "origin") != "user" || !strings.Contains(detail.raw, `"traceId"`) {
		t.Errorf("evidence item lacks provenance or is marked generated: %s", detail.raw)
	}

	// The threshold draft: generated, labelled, with evidence references.
	draft := cp("GET", "/api/v1/memory/checkpoints/"+draftID, inferenceKey, nil)
	want(t, "read draft", draft, 200)
	if draft.str("checkpoint", "status") != "draft" || draft.dig("checkpoint", "generated") != true || draft.str("checkpoint", "trigger") != "threshold" ||
		!strings.Contains(draft.str("checkpoint", "notice"), "not verified fact") {
		t.Errorf("draft not labelled as generated/unverified: %s", draft.raw)
	}
	evidence, _ := draft.dig("checkpoint", "content", "evidenceRefs").([]any)
	if len(evidence) == 0 || draft.str("checkpoint", "content", "objective") == "" {
		t.Fatalf("draft has no evidence or objective: %s", draft.raw)
	}

	// A summary must cite stored items; unsupported or cross-session sources are refused.
	expect(t, "summary without sources", cp("POST", "/api/v1/memory/sessions/"+sessionID+"/summaries", inferenceKey, map[string]any{"content": "We agreed on everything.", "generated": true}), 400)
	expect(t, "summary citing a missing item", cp("POST", "/api/v1/memory/sessions/"+sessionID+"/summaries", inferenceKey, map[string]any{"content": "x", "sourceItemIds": []string{"00000000-0000-0000-0000-00000000dead"}}), 400)
	summary := cp("POST", "/api/v1/memory/sessions/"+sessionID+"/summaries", inferenceKey, map[string]any{"content": "The user asked for a migration plan in four steps.", "generated": true, "generator": "e2e", "sourceItemIds": []any{evidence[0]}})
	want(t, "summary with sources", summary, 201)
	if summary.dig("item", "generated") != true || summary.str("item", "origin") != "generated" || len(summary.dig("item", "sourceItemIds").([]any)) != 1 {
		t.Errorf("summary not marked generated with sources: %s", summary.raw)
	}
	summaryID := summary.str("item", "id")
	expect(t, "generated text posted as evidence", cp("POST", "/api/v1/memory/sessions/"+sessionID+"/items", inferenceKey, map[string]any{"kind": "observation", "origin": "generated", "content": "x"}), 400)

	// Review: an unapproved draft cannot be carried forward; a summary cannot be cited as evidence.
	expect(t, "continue from an unapproved draft", cp("POST", "/api/v1/memory/continuations", inferenceKey, map[string]any{"checkpointId": draftID, "externalSessionId": "e2e-ctx-2"}), 409)
	content := draft.dig("checkpoint", "content").(map[string]any)
	content["decisions"] = []string{"Migrate in four steps, database first."}
	content["risks"] = []string{"Downtime during step 3."}
	content["evidenceRefs"] = append(evidence, summaryID)
	expect(t, "checkpoint citing generated text as evidence", cp("PATCH", "/api/v1/memory/checkpoints/"+draftID, inferenceKey, map[string]any{"content": content}), 400)
	content["evidenceRefs"] = evidence
	edited := cp("PATCH", "/api/v1/memory/checkpoints/"+draftID, inferenceKey, map[string]any{"content": content})
	want(t, "edit draft", edited, 200)
	if edited.dig("checkpoint", "editedByReviewer") != true {
		t.Errorf("edit not recorded: %s", edited.raw)
	}
	// Only the owner reviews; another tenant cannot even see it.
	expect(t, "tenant B reading the checkpoint", cp("GET", "/api/v1/memory/checkpoints/"+draftID, tenantBKey, nil), 404)
	expect(t, "tenant B approving the checkpoint", cp("POST", "/api/v1/memory/checkpoints/"+draftID+"/approve", tenantBKey, nil), 404)
	expect(t, "tenant admin approving someone else's checkpoint", cp("POST", "/api/v1/memory/checkpoints/"+draftID+"/approve", adminKey, nil), 403)
	approved := cp("POST", "/api/v1/memory/checkpoints/"+draftID+"/approve", inferenceKey, nil)
	want(t, "approve", approved, 200)
	if approved.str("checkpoint", "status") != "approved" || approved.str("checkpoint", "reviewedBy") != "inference-key" {
		t.Errorf("approval: %s", approved.raw)
	}
	expect(t, "editing an approved checkpoint", cp("PATCH", "/api/v1/memory/checkpoints/"+draftID, inferenceKey, map[string]any{"content": content}), 409)

	// Hard block: with it on, a request that cannot fit is refused before the provider is called.
	want(t, "enable hard block", cp("PUT", "/api/v1/memory/context-policies", adminKey, map[string]any{"model": "ctx-model", "noticePercent": 80, "checkpointPercent": 85, "warningPercent": 95, "blockPercent": 100, "hardBlock": true}), 200)
	callsBefore := provider.count()
	blocked := chat(inferenceKey, "ctx-model", strings.Repeat("this conversation is far too long ", 150), nil, "X-SUEZ-Session-Id", session)
	if blocked.status != 409 || blocked.str("code") != "context_limit" || blocked.header.Get("X-SUEZ-Context-Status") != "limit" || provider.count() != callsBefore {
		t.Errorf("hard block: %d %s (provider called: %v)", blocked.status, blocked.raw, provider.count() != callsBefore)
	}
	expect(t, "invalid thresholds", cp("PUT", "/api/v1/memory/context-policies", adminKey, map[string]any{"model": "ctx-model", "noticePercent": 90, "checkpointPercent": 85, "warningPercent": 95, "blockPercent": 100}), 400)
	// Without hard block the same request goes through with a warning status.
	want(t, "disable hard block", cp("PUT", "/api/v1/memory/context-policies", adminKey, map[string]any{"model": "ctx-model", "noticePercent": 80, "checkpointPercent": 85, "warningPercent": 95, "blockPercent": 100, "hardBlock": false}), 200)
	expect(t, "over-limit request without hard block", chat(inferenceKey, "ctx-model", strings.Repeat("this conversation is far too long ", 150)+" USAGE=990", nil, "X-SUEZ-Session-Id", "e2e-ctx-soft"), 200)

	// Continuation: a new session receives the checkpoint and bounded evidence.
	continued := cp("POST", "/api/v1/memory/continuations", inferenceKey, map[string]any{"checkpointId": draftID, "externalSessionId": "e2e-ctx-2", "tokenBudget": 400})
	want(t, "continuation", continued, 201)
	carried := continued.dig("carriedForward").(map[string]any)
	used, budget := carried["tokensUsed"].(float64), carried["tokenBudget"].(float64)
	text := carried["contextText"].(string)
	if used > budget || budget != 400 || !strings.Contains(text, "[checkpoint:"+draftID+"]") || !strings.Contains(text, "Migrate in four steps") || !strings.Contains(text, "not as verified fact") {
		t.Errorf("carry-forward: used %v budget %v text %.200s", used, budget, text)
	}
	carriedEvidence, _ := carried["evidence"].([]any)
	omitted, _ := carried["omittedEvidenceIds"].([]any)
	if len(carriedEvidence)+len(omitted) != len(evidence) {
		t.Errorf("evidence accounting: %d carried + %d omitted != %d referenced", len(carriedEvidence), len(omitted), len(evidence))
	}
	for _, item := range carriedEvidence {
		if !strings.Contains(text, "[evidence:"+item.(map[string]any)["id"].(string)+"]") {
			t.Errorf("carried evidence is not cited in the context text")
		}
	}
	if continued.str("session", "continuedFromCheckpointId") != draftID {
		t.Errorf("new session not linked to the checkpoint: %s", continued.raw)
	}
	expect(t, "budget too small for the checkpoint", cp("POST", "/api/v1/memory/continuations", inferenceKey, map[string]any{"checkpointId": draftID, "externalSessionId": "e2e-ctx-3", "tokenBudget": 5}), 409)
	expect(t, "tenant B continuing from tenant A's checkpoint", cp("POST", "/api/v1/memory/continuations", tenantBKey, map[string]any{"checkpointId": draftID, "externalSessionId": "b-steal"}), 404)

	// Injection through the gateway is refused until the tenant enables it.
	inject := map[string]any{"suez": map[string]any{"memory": map[string]any{"checkpointId": draftID, "tokenBudget": 400}}}
	refused := chat(inferenceKey, "ctx-model", "continue the plan USAGE=200", inject, "X-SUEZ-Session-Id", "e2e-ctx-2")
	if refused.status != 403 || refused.str("code") != "memory_injection_refused" {
		t.Errorf("injection while disabled: %d %s", refused.status, refused.raw)
	}
	want(t, "enable injection", cp("PUT", "/api/v1/memory/settings", adminKey, map[string]any{"captureEnabled": true, "retrievalEnabled": true, "injectionEnabled": true}), 200)
	injected := chat(inferenceKey, "ctx-model", "continue the plan USAGE=210", inject, "X-SUEZ-Session-Id", "e2e-ctx-2")
	want(t, "chat with injection", injected, 200)
	if !strings.Contains(injected.header.Get("X-SUEZ-Memory-Injected"), "checkpoint="+draftID) {
		t.Errorf("injection not disclosed in the response: %q", injected.header.Get("X-SUEZ-Memory-Injected"))
	}
	upstream := provider.last()
	upstreamMessages := upstream["messages"].([]any)
	system := upstreamMessages[0].(map[string]any)
	if system["role"] != "system" || !strings.Contains(system["content"].(string), "[checkpoint:"+draftID+"]") || len(upstreamMessages) != 2 {
		t.Errorf("provider did not receive exactly the checkpoint context plus the user message: %v", upstreamMessages)
	}
	if _, present := upstream["suez"]; present {
		t.Errorf("gateway extension field was forwarded to the provider")
	}
	injectionAudit := cp("GET", "/api/v1/audit-log/traces/"+injected.header.Get("X-SUEZ-Trace-Id"), adminKey, nil)
	if !strings.Contains(injectionAudit.raw, `"action":"memory.inject"`) || !strings.Contains(injectionAudit.raw, `"action":"memory.injection"`) {
		t.Errorf("injection not audited: %s", injectionAudit.raw)
	}
	// No injection without an explicit request.
	plain := chat(inferenceKey, "ctx-model", "no memory please USAGE=50", nil, "X-SUEZ-Session-Id", "e2e-ctx-2")
	want(t, "plain chat", plain, 200)
	if plain.header.Get("X-SUEZ-Memory-Injected") != "" || len(provider.last()["messages"].([]any)) != 1 {
		t.Errorf("memory was injected without being requested")
	}
	// Tenant B has not enabled injection, so it is refused before the lookup;
	// with injection enabled the checkpoint simply does not exist for B.
	expect(t, "tenant B injecting tenant A's checkpoint (injection off)", chat(tenantBKey, "prov-ok", "steal", inject), 403)
	want(t, "tenant B enables injection", cp("PUT", "/api/v1/memory/settings", tenantBKey, map[string]any{"captureEnabled": true, "retrievalEnabled": true, "injectionEnabled": true}), 200)
	stolen := chat(tenantBKey, "prov-ok", "steal again", inject)
	if stolen.status != 404 || stolen.header.Get("X-SUEZ-Memory-Injected") != "" {
		t.Errorf("tenant B injecting tenant A's checkpoint (injection on): %d %s", stolen.status, stolen.raw)
	}

	// User opt-out stops both capture and retrieval for that user.
	want(t, "opt out", cp("PUT", "/api/v1/memory/preferences", inferenceKey, map[string]any{"captureOptOut": true, "retrievalOptOut": true}), 200)
	want(t, "chat while opted out", chat(inferenceKey, "ctx-model", "opted out message USAGE=60", nil, "X-SUEZ-Session-Id", "e2e-optout"), 200)
	time.Sleep(300 * time.Millisecond)
	if stored := queryString(t, `select count(*)::text from memory_sessions where external_session_id = 'e2e-optout'`); stored != "0" {
		t.Errorf("memory captured for an opted-out user")
	}
	expect(t, "retrieval while opted out", cp("GET", "/api/v1/memory/sessions", inferenceKey, nil), 403)
	expect(t, "injection while opted out", chat(inferenceKey, "ctx-model", "x USAGE=70", inject), 403)
	want(t, "opt back in", cp("PUT", "/api/v1/memory/preferences", inferenceKey, map[string]any{"captureOptOut": false, "retrievalOptOut": false}), 200)

	// Retrieval is recorded.
	retrievals := cp("GET", "/api/v1/memory/retrievals?limit=200", adminKey, nil)
	want(t, "retrieval records", retrievals, 200)
	for _, purpose := range []string{"search", "timeline", "detail", "continuation", "injection"} {
		if !strings.Contains(retrievals.raw, `"purpose":"`+purpose+`"`) {
			t.Errorf("no retrieval record for %s", purpose)
		}
	}
	expect(t, "retrieval records need memory.manage", cp("GET", "/api/v1/memory/retrievals", inferenceKey, nil), 403)

	// Export, then deletion and retention.
	export := cp("GET", "/api/v1/memory/sessions/"+sessionID+"/export", inferenceKey, nil)
	want(t, "export", export, 200)
	if !strings.Contains(export.raw, "Plan the migration") || !strings.Contains(export.raw, draftID) || strings.Contains(export.raw, "sk-proj-abcdef") {
		t.Errorf("export incomplete or contains a secret")
	}
	expect(t, "tenant B exporting tenant A's session", cp("GET", "/api/v1/memory/sessions/"+sessionID+"/export", tenantBKey, nil), 404)
	expect(t, "tenant B deleting tenant A's session", cp("DELETE", "/api/v1/memory/sessions/"+sessionID, tenantBKey, nil), 404)

	// Retention: an expired item is wiped by the retention job.
	softID := queryString(t, `select id::text from memory_sessions where external_session_id = 'e2e-ctx-soft'`)
	if _, err := db.Exec(`update memory_items set expires_at = now() - interval '1 minute' where session_id = $1`, softID); err != nil {
		t.Fatal(err)
	}
	want(t, "run retention", cp("POST", "/api/v1/memory/retention/run", adminKey, nil), 200)
	if remaining := queryString(t, `select count(*)::text from memory_items where session_id = $1 and (content_sealed is not null or deleted_at is null)`, softID); remaining != "0" {
		t.Errorf("%s expired items still hold content after retention", remaining)
	}
	if kept := queryString(t, `select count(*)::text from memory_items where session_id = $1 and content_sealed is not null`, sessionID); kept == "0" {
		t.Errorf("retention removed items that had not expired")
	}

	// Deletion by the owner wipes content and stops every later read.
	want(t, "delete session", cp("DELETE", "/api/v1/memory/sessions/"+sessionID, inferenceKey, nil), 200)
	if remaining := queryString(t, `select count(*)::text from memory_items where session_id = $1 and (content_sealed is not null or preview <> '')`, sessionID); remaining != "0" {
		t.Errorf("%s items still hold content after deletion", remaining)
	}
	if remaining := queryString(t, `select count(*)::text from memory_checkpoints where session_id = $1 and content_sealed is not null`, sessionID); remaining != "0" {
		t.Errorf("checkpoint content survived session deletion")
	}
	expect(t, "timeline after deletion", cp("GET", "/api/v1/memory/sessions/"+sessionID+"/timeline", inferenceKey, nil), 404)
	expect(t, "checkpoint after deletion", cp("GET", "/api/v1/memory/checkpoints/"+draftID, inferenceKey, nil), 404)
	expect(t, "injection after deletion", chat(inferenceKey, "ctx-model", "x USAGE=80", inject), 404)
	deletionAudit := cp("GET", "/api/v1/audit-log?action=memory.session.delete", adminKey, nil)
	if !strings.Contains(deletionAudit.raw, sessionID) {
		t.Errorf("deletion not audited")
	}
	// A deleted session id cannot be silently reused for new capture.
	want(t, "chat on the deleted session id", send("after deletion USAGE=90"), 200)
	time.Sleep(300 * time.Millisecond)
	if revived := queryString(t, `select count(*)::text from memory_items where session_id = $1 and deleted_at is null`, sessionID); revived != "0" {
		t.Errorf("capture resumed into a deleted session")
	}
}

func TestJ_NoSecretsInStoredRecords(t *testing.T) {
	for name, needle := range map[string]string{
		"demo admin key": adminKey, "tenant B key": tenantBKey, "inference key": inferenceKey,
		"provider key": "e2e-provider-key-not-real", "internal token": internalTok, "database password": "e2e-password",
	} {
		for _, table := range []string{"audit_log", "tool_executions", "memory_retrievals", "policy_test_runs"} {
			if hit := queryString(t, fmt.Sprintf(`select 1::text from %s t where to_jsonb(t)::text like '%%'||$1||'%%' limit 1`, table), needle); hit != "" {
				t.Errorf("%s found in table %s", name, table)
			}
		}
	}
	var events []map[string]any
	resp := cp("GET", "/api/v1/audit-log?limit=200", adminKey, nil)
	raw, _ := json.Marshal(resp.dig("auditEvents"))
	_ = json.Unmarshal(raw, &events)
	if len(events) == 0 {
		t.Fatal("audit log is empty")
	}
	if resp.status != http.StatusOK {
		t.Fatalf("audit log: %d", resp.status)
	}
}

// The desktop app reaches SUEZ Memory through the gateway with the caller's
// own credential; the gateway adds no privilege.
func TestK_MemoryThroughGateway(t *testing.T) {
	gw := func(method, path, auth string, body any) response { return call(method, gateway+path, auth, body) }
	want(t, "chat to create a session", chat(inferenceKey, "stub-small", "desktop flow message", nil, "X-SUEZ-Session-Id", "e2e-desktop-1"), 200)
	time.Sleep(400 * time.Millisecond)

	index := gw("GET", "/v1/memory/sessions", inferenceKey, nil)
	want(t, "session index through the gateway", index, 200)
	sessionID := ""
	for _, raw := range index.dig("sessions").([]any) {
		if session := raw.(map[string]any); session["externalSessionId"] == "e2e-desktop-1" {
			sessionID = session["id"].(string)
		}
	}
	if sessionID == "" {
		t.Fatalf("captured session not visible through the gateway: %s", index.raw)
	}
	// Upload a checkpoint the way the desktop app does, then review and continue.
	draft := gw("POST", "/v1/memory/sessions/"+sessionID+"/checkpoints", inferenceKey, map[string]any{"content": map[string]any{
		"objective": "Finish the desktop flow", "completedWork": []string{"Policy isolation (verified locally: e2e passed)"},
		"nextActions": []string{"Write the runbook"}, "evidenceRefs": []string{},
	}})
	want(t, "upload draft", draft, 201)
	checkpointID := draft.str("checkpoint", "id")
	if draft.str("checkpoint", "status") != "draft" || draft.dig("checkpoint", "generated") != false {
		t.Errorf("uploaded checkpoint: %s", draft.raw)
	}
	expect(t, "continue before approval", gw("POST", "/v1/memory/continuations", inferenceKey, map[string]any{"checkpointId": checkpointID, "externalSessionId": "e2e-desktop-2"}), 409)
	want(t, "approve through the gateway", gw("POST", "/v1/memory/checkpoints/"+checkpointID+"/approve", inferenceKey, nil), 200)
	continued := gw("POST", "/v1/memory/continuations", inferenceKey, map[string]any{"checkpointId": checkpointID, "externalSessionId": "e2e-desktop-2", "tokenBudget": 500})
	want(t, "continue", continued, 201)
	if !strings.Contains(continued.str("carriedForward", "contextText"), "Finish the desktop flow") {
		t.Errorf("carry-forward text: %s", continued.raw)
	}

	// Same rules as the control plane: scope, tenant and method.
	readOnly, _ := createKey(t, adminKey, "ro-memory", []string{"read-only"}, nil)
	expect(t, "read-only key reading memory through the gateway", gw("GET", "/v1/memory/sessions", readOnly, nil), 403)
	expect(t, "read-only key writing memory through the gateway", gw("POST", "/v1/memory/continuations", readOnly, map[string]any{}), 403)
	expect(t, "no credentials", gw("GET", "/v1/memory/sessions", "", nil), 401)
	expect(t, "tenant B reading tenant A's checkpoint through the gateway", gw("GET", "/v1/memory/checkpoints/"+checkpointID, tenantBKey, nil), 404)
	if other := gw("GET", "/v1/memory/sessions", tenantBKey, nil); strings.Contains(other.raw, "e2e-desktop-1") {
		t.Errorf("tenant B sees tenant A's sessions through the gateway")
	}
	expect(t, "inference key changing tenant settings through the gateway", gw("PUT", "/v1/memory/settings", inferenceKey, map[string]any{"captureEnabled": false}), 403)
	expect(t, "internal endpoint through the memory route", gw("POST", "/v1/memory/../../internal/memory/capture", inferenceKey, map[string]any{}), 404)
}
