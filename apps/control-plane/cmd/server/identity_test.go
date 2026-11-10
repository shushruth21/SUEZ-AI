package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type testIDP struct {
	server  *httptest.Server
	keys    atomic.Value // []map[string]any
	fetches atomic.Int32
	down    atomic.Bool
}

func rsaJWK(kid string, key *rsa.PublicKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	idp := &testIDP{}
	idp.keys.Store([]map[string]any{})
	idp.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if idp.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": idp.server.URL, "jwks_uri": idp.server.URL + "/jwks"})
		case "/jwks":
			idp.fetches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": idp.keys.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *testIDP) verifier(t *testing.T) *jwtVerifier {
	t.Helper()
	t.Setenv("JWT_MODE", "jwks")
	t.Setenv("JWT_ISSUER", idp.server.URL)
	t.Setenv("JWT_AUDIENCE", "suez-api")
	t.Setenv("JWT_ALLOWED_ALGS", "RS256,ES256")
	cfg, err := loadJWTConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	return newJWTVerifier(cfg, idp.server.Client())
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func goodClaims(issuer string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{"iss": issuer, "aud": "suez-api", "sub": "alice", "tenant_slug": "demo",
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func TestJWTVerificationRejectsForgeries(t *testing.T) {
	idp := newTestIDP(t)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	attacker, _ := rsa.GenerateKey(rand.Reader, 2048)
	idp.keys.Store([]map[string]any{rsaJWK("k1", &key.PublicKey)})
	verifier := idp.verifier(t)
	ctx := context.Background()

	if _, err := verifier.verify(ctx, signRS256(t, key, "k1", goodClaims(idp.server.URL))); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	with := func(change func(jwt.MapClaims)) jwt.MapClaims {
		claims := goodClaims(idp.server.URL)
		change(claims)
		return claims
	}
	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	unsigned := b64(map[string]string{"alg": "none", "typ": "JWT"}) + "." + b64(goodClaims(idp.server.URL)) + "."

	// HS256 signed with the RSA public key as the HMAC secret (key confusion).
	publicPEMish := key.PublicKey.N.Bytes()
	confusion := jwt.NewWithClaims(jwt.SigningMethodHS256, goodClaims(idp.server.URL))
	confusion.Header["kid"] = "k1"
	confused, _ := confusion.SignedString(publicPEMish)

	valid := signRS256(t, key, "k1", goodClaims(idp.server.URL))
	parts := strings.Split(valid, ".")
	tamperedPayload := parts[0] + "." + b64(with(func(c jwt.MapClaims) { c["sub"] = "admin" })) + "." + parts[2]

	forgeries := map[string]string{
		"alg none":               unsigned,
		"hmac key confusion":     confused,
		"signed by attacker key": signRS256(t, attacker, "k1", goodClaims(idp.server.URL)),
		"unknown kid":            signRS256(t, attacker, "attacker", goodClaims(idp.server.URL)),
		"payload changed":        tamperedPayload,
		"signature stripped":     parts[0] + "." + parts[1] + ".",
		"wrong issuer":           signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["iss"] = "https://evil.example" })),
		"wrong audience":         signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["aud"] = "other-api" })),
		"missing audience":       signRS256(t, key, "k1", with(func(c jwt.MapClaims) { delete(c, "aud") })),
		"expired":                signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() })),
		"no expiry":              signRS256(t, key, "k1", with(func(c jwt.MapClaims) { delete(c, "exp") })),
		"not yet valid":          signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(10 * time.Minute).Unix() })),
		"issued in the future":   signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["iat"] = time.Now().Add(10 * time.Minute).Unix() })),
		"not a token":            "abc.def.ghi",
		"empty":                  "",
	}
	for name, token := range forgeries {
		if _, err := verifier.verify(ctx, token); !errors.Is(err, errJWTInvalid) {
			t.Errorf("%s: err = %v, want errJWTInvalid", name, err)
		}
	}

	// Within clock skew (60s) an expiry just passed is still accepted.
	if _, err := verifier.verify(ctx, signRS256(t, key, "k1", with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-20 * time.Second).Unix() }))); err != nil {
		t.Errorf("token inside clock skew rejected: %v", err)
	}
}

func TestJWKSRotationAndFailClosed(t *testing.T) {
	idp := newTestIDP(t)
	oldKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	idp.keys.Store([]map[string]any{rsaJWK("old", &oldKey.PublicKey)})
	verifier := idp.verifier(t)
	clock := time.Now()
	verifier.keys.now = func() time.Time { return clock }
	ctx := context.Background()

	if _, err := verifier.verify(ctx, signRS256(t, oldKey, "old", goodClaims(idp.server.URL))); err != nil {
		t.Fatal(err)
	}
	// The provider rotates: a token with the new kid triggers one refresh.
	idp.keys.Store([]map[string]any{rsaJWK("new", &newKey.PublicKey)})
	clock = clock.Add(jwksMinRefreshInterval + time.Second)
	if _, err := verifier.verify(ctx, signRS256(t, newKey, "new", goodClaims(idp.server.URL))); err != nil {
		t.Fatalf("token signed with rotated key rejected: %v", err)
	}
	if _, err := verifier.verify(ctx, signRS256(t, oldKey, "old", goodClaims(idp.server.URL))); !errors.Is(err, errJWTInvalid) {
		t.Fatalf("token signed with a removed key: err = %v, want invalid", err)
	}
	// Unknown kids cannot force a fetch per request.
	before := idp.fetches.Load()
	for i := 0; i < 20; i++ {
		_, _ = verifier.verify(ctx, signRS256(t, oldKey, "nope", goodClaims(idp.server.URL)))
	}
	if got := idp.fetches.Load() - before; got > 1 {
		t.Fatalf("unknown kid caused %d JWKS fetches, want at most 1", got)
	}

	// Provider down and cache expired: refuse, and report it as unavailable.
	idp.down.Store(true)
	clock = clock.Add(verifier.cfg.JWKSCacheTTL + time.Minute)
	_, err := verifier.verify(ctx, signRS256(t, newKey, "new", goodClaims(idp.server.URL)))
	if !errors.Is(err, errAuthenticationServiceUnavailable) {
		t.Fatalf("expired cache with provider down: err = %v, want unavailable", err)
	}
}

func TestJWTECDSAAndAlgorithmAllowlist(t *testing.T) {
	idp := newTestIDP(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	size := (key.Curve.Params().BitSize + 7) / 8
	pad := func(n *big.Int) string { return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, size))) }
	idp.keys.Store([]map[string]any{{"kty": "EC", "kid": "ec1", "crv": "P-256", "x": pad(key.X), "y": pad(key.Y)}})
	verifier := idp.verifier(t)

	sign := func(method jwt.SigningMethod) string {
		token := jwt.NewWithClaims(method, goodClaims(idp.server.URL))
		token.Header["kid"] = "ec1"
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	if _, err := verifier.verify(context.Background(), sign(jwt.SigningMethodES256)); err != nil {
		t.Fatalf("ES256 rejected: %v", err)
	}
	t.Setenv("JWT_ALLOWED_ALGS", "RS256")
	cfg, _ := loadJWTConfig(false)
	strict := newJWTVerifier(cfg, idp.server.Client())
	if _, err := strict.verify(context.Background(), sign(jwt.SigningMethodES256)); !errors.Is(err, errJWTInvalid) {
		t.Fatalf("algorithm outside the allowlist: err = %v, want invalid", err)
	}
}

func TestJWTConfigValidation(t *testing.T) {
	set := func(values map[string]string) {
		for _, name := range []string{"JWT_MODE", "JWT_ISSUER", "JWT_AUDIENCE", "JWT_ALLOWED_ALGS", "JWT_DEV_SECRET", "JWT_JWKS_URL"} {
			t.Setenv(name, values[name])
		}
	}
	bad := []struct {
		name       string
		production bool
		values     map[string]string
	}{
		{"hmac in jwks mode", true, map[string]string{"JWT_MODE": "jwks", "JWT_ISSUER": "https://idp.example", "JWT_AUDIENCE": "a", "JWT_ALLOWED_ALGS": "HS256"}},
		{"none algorithm", true, map[string]string{"JWT_MODE": "jwks", "JWT_ISSUER": "https://idp.example", "JWT_AUDIENCE": "a", "JWT_ALLOWED_ALGS": "none"}},
		{"missing audience", true, map[string]string{"JWT_MODE": "jwks", "JWT_ISSUER": "https://idp.example"}},
		{"missing issuer", true, map[string]string{"JWT_MODE": "jwks", "JWT_AUDIENCE": "a"}},
		{"http issuer in production", true, map[string]string{"JWT_MODE": "jwks", "JWT_ISSUER": "http://idp.example", "JWT_AUDIENCE": "a"}},
		{"dev mode in production", true, map[string]string{"JWT_MODE": "dev-hs256", "JWT_DEV_SECRET": strings.Repeat("x", 40)}},
		{"short dev secret", false, map[string]string{"JWT_MODE": "dev-hs256", "JWT_DEV_SECRET": "short"}},
		{"unknown mode", false, map[string]string{"JWT_MODE": "trust-me"}},
	}
	for _, c := range bad {
		set(c.values)
		if _, err := loadJWTConfig(c.production); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	set(map[string]string{})
	cfg, err := loadJWTConfig(true)
	if err != nil || cfg.Mode != jwtModeDisabled {
		t.Fatalf("default should be disabled: %+v %v", cfg, err)
	}
	if _, err := newJWTVerifier(cfg, http.DefaultClient).verify(context.Background(), "a.b.c"); !errors.Is(err, errJWTInvalid) {
		t.Fatalf("disabled mode must reject every token: %v", err)
	}
}

func TestRuntimeConfigRefusesUnsafeProduction(t *testing.T) {
	names := []string{"SUEZ_ENV", "CORS_ALLOWED_ORIGINS", "SUEZ_INTERNAL_TOKEN", "SUEZ_EXPOSE_DEMO_CREDENTIALS", "DATABASE_URL", "JWT_MODE", "SUEZ_DATA_KEY", "TRUSTED_PROXY_CIDRS", "RATE_LIMIT_RPM"}
	good := map[string]string{
		"CORS_ALLOWED_ORIGINS": "https://portal.example.com", "SUEZ_INTERNAL_TOKEN": strings.Repeat("t", 40),
		"DATABASE_URL": "postgres://suez:S0me-long-passw0rd@db:5432/suez", "JWT_MODE": "disabled",
		"SUEZ_DATA_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	load := func(overrides map[string]string) error {
		for _, name := range names {
			value, changed := overrides[name]
			if !changed {
				value = good[name]
			}
			t.Setenv(name, value)
		}
		_, err := loadRuntimeConfig()
		return err
	}
	if err := load(nil); err != nil {
		t.Fatalf("a complete production configuration was refused: %v", err)
	}
	for name, overrides := range map[string]map[string]string{
		"wildcard cors":         {"CORS_ALLOWED_ORIGINS": "*"},
		"http cors origin":      {"CORS_ALLOWED_ORIGINS": "http://portal.example.com"},
		"no internal token":     {"SUEZ_INTERNAL_TOKEN": ""},
		"dev internal token":    {"SUEZ_INTERNAL_TOKEN": developmentInternalToken},
		"demo credentials":      {"SUEZ_EXPOSE_DEMO_CREDENTIALS": "true"},
		"default db password":   {"DATABASE_URL": "postgres://suez:suez@db:5432/suez"},
		"no database url":       {"DATABASE_URL": ""},
		"jwt mode not explicit": {"JWT_MODE": ""},
		"no data key":           {"SUEZ_DATA_KEY": ""},
		"bad env name":          {"SUEZ_ENV": "staging-ish"},
	} {
		if err := load(overrides); err == nil {
			t.Errorf("%s: production startup was allowed", name)
		}
	}
	// The same omissions are fine when development is asked for explicitly.
	if err := load(map[string]string{"SUEZ_ENV": "development", "CORS_ALLOWED_ORIGINS": "*", "SUEZ_INTERNAL_TOKEN": "", "DATABASE_URL": "", "JWT_MODE": "", "SUEZ_DATA_KEY": ""}); err != nil {
		t.Fatalf("development configuration refused: %v", err)
	}
}

func TestRoutePermissionTable(t *testing.T) {
	cases := []struct {
		method, path, want string
		known              bool
	}{
		{"GET", "/api/v1/auth/session", permSessionRead, true},
		{"POST", "/api/v1/tenants", permTenantsManage, true},
		{"GET", "/api/v1/api-keys", permAPIKeysManage, true},
		{"DELETE", "/api/v1/api-keys/abc", permAPIKeysManage, true},
		{"POST", "/api/v1/access-requests", permAccessRequest, true},
		{"POST", "/api/v1/access-requests/abc/approve", permAccessReview, true},
		{"DELETE", "/api/v1/access-grants/abc", permAccessReview, true},
		{"POST", "/api/v1/models", permCatalogManage, true},
		{"GET", "/api/v1/tools", permCatalogRead, true},
		{"GET", "/api/v1/policies/abc/versions", permPolicyRead, true},
		{"POST", "/api/v1/policies/abc/publish", permPolicyManage, true},
		{"GET", "/api/v1/audit-log/traces/abc", permAuditRead, true},
		{"PUT", "/api/v1/memory/settings", permMemoryManage, true},
		{"GET", "/api/v1/memory/retrievals", permMemoryManage, true},
		{"POST", "/api/v1/memory/continuations", permMemoryWrite, true},
		{"GET", "/api/v1/memory/sessions", permMemoryRead, true},
		{"DELETE", "/api/v1/audit-log", "", false},
		{"PUT", "/api/v1/models", "", false},
		{"GET", "/api/v1/unknown", "", false},
	}
	for _, c := range cases {
		got, known := routePermission(c.method, c.path)
		if known != c.known || (known && got != c.want) {
			t.Errorf("%s %s = %q,%v; want %q,%v", c.method, c.path, got, known, c.want, c.known)
		}
	}
}

func TestScopesBoundPermissions(t *testing.T) {
	has := func(scope, permission string) bool {
		return hasPermission(identityContext{Permissions: permissionsForScopes([]string{scope})}, permission)
	}
	denied := map[string][]string{
		scopeReadOnly:         {permGatewayInvoke, permCatalogManage, permPolicyManage, permAuditRead, permAPIKeysManage, permMemoryRead, permAccessReview},
		scopeGatewayInference: {permCatalogManage, permPolicyManage, permAuditRead, permAPIKeysManage, permUsersManage, permAccessReview, permMemoryManage},
		scopePolicyAdmin:      {permGatewayInvoke, permAuditRead, permAPIKeysManage, permCatalogManage, permUsersManage},
		scopeAuditReader:      {permGatewayInvoke, permPolicyManage, permAPIKeysManage, permCatalogManage, permMemoryRead},
		scopeTenantAdmin:      {permTenantsManage},
	}
	for scope, permissions := range denied {
		for _, permission := range permissions {
			if has(scope, permission) {
				t.Errorf("scope %s must not carry %s", scope, permission)
			}
		}
	}
	if !has(scopeGatewayInference, permGatewayInvoke) || !has(scopeAuditReader, permAuditRead) || !has(scopePolicyAdmin, permPolicyManage) || !has(scopeTenantAdmin, permAPIKeysManage) {
		t.Error("a scope is missing its defining permission")
	}
	gateway := identityContext{Permissions: permissionsForScopes([]string{scopeGatewayInference})}
	if canGrantScopes(gateway, []string{scopeTenantAdmin}) || canGrantScopes(gateway, []string{scopeAuditReader}) {
		t.Error("a gateway-inference identity could grant broader scopes")
	}
}

func TestInternalRoutesNeedToken(t *testing.T) {
	cfg := runtimeConfig{InternalToken: strings.Repeat("s", 40)}
	called := false
	handler := cfg.requireInternalToken(func(w http.ResponseWriter, r *http.Request) { called = true })
	for _, token := range []string{"", "wrong", strings.Repeat("s", 39)} {
		req := httptest.NewRequest(http.MethodGet, "/internal/auth/resolve", nil)
		req.Header.Set("X-SUEZ-Internal-Token", token)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusNotFound || called {
			t.Fatalf("token %q reached the internal handler (status %d)", token, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/internal/auth/resolve", nil)
	req.Header.Set("X-SUEZ-Internal-Token", cfg.InternalToken)
	handler(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("the correct token was refused")
	}
}

func TestCORSAllowlist(t *testing.T) {
	cfg := runtimeConfig{CORSOrigins: []string{"https://portal.example.com"}}
	handler := cfg.withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for origin, want := range map[string]string{"https://portal.example.com": "https://portal.example.com", "https://evil.example": "", "": ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %q: Access-Control-Allow-Origin = %q, want %q", origin, got, want)
		}
	}
}

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeers(t *testing.T) {
	_, proxy, _ := netParse("10.0.0.0/8")
	cfg := runtimeConfig{TrustedProxies: proxy}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.RemoteAddr = "203.0.113.9:5555"
	if got := cfg.clientIP(req); got != "203.0.113.9" {
		t.Fatalf("untrusted peer spoofed its address: %s", got)
	}
	req.RemoteAddr = "10.1.1.1:5555"
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 198.51.100.7, 10.2.2.2")
	if got := cfg.clientIP(req); got != "198.51.100.7" {
		t.Fatalf("client behind trusted proxy = %s, want 198.51.100.7", got)
	}
}

func TestRedactSecrets(t *testing.T) {
	secrets := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz012345", "sk_suez_0123456789abcdef0123456789abcdef0123", "gsk_abcdefghijklmnopqrstuvwxyz0123",
		"AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "password = hunter2hunter2",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz", "postgres://app:s3cretpass@db.internal:5432/prod",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlMTIz",
	}
	for _, secret := range secrets {
		out, count := redactSecrets("before " + secret + " after")
		if count == 0 || strings.Contains(out, secret) {
			t.Errorf("not redacted: %q -> %q", secret, out)
		}
		if !strings.HasPrefix(out, "before ") || !strings.HasSuffix(out, " after") {
			t.Errorf("surrounding text damaged: %q", out)
		}
	}
	plain := "The deployment uses three replicas and a 30 second timeout."
	if out, count := redactSecrets(plain); count != 0 || out != plain {
		t.Errorf("ordinary text changed: %q", out)
	}
}

func TestContentSealer(t *testing.T) {
	keyA := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	keyB := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	t.Setenv("SUEZ_DATA_KEY", keyA)
	t.Setenv("SUEZ_DATA_KEY_PREVIOUS", "")
	sealer, err := newContentSealer(true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.seal([]byte("the plan"), "memory_items:tenant-a:item-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "the plan") {
		t.Fatal("plaintext visible in sealed content")
	}
	if plain, err := sealer.open(sealed, "memory_items:tenant-a:item-1"); err != nil || string(plain) != "the plan" {
		t.Fatalf("round trip failed: %q %v", plain, err)
	}
	// Moved to another tenant's row: must not decrypt.
	if _, err := sealer.open(sealed, "memory_items:tenant-b:item-1"); err == nil {
		t.Fatal("ciphertext decrypted under another tenant's binding")
	}
	// Key rotation: new key current, old key listed as previous.
	t.Setenv("SUEZ_DATA_KEY", keyB)
	rotatedWithout, _ := newContentSealer(true)
	if _, err := rotatedWithout.open(sealed, "memory_items:tenant-a:item-1"); err == nil {
		t.Fatal("old content opened without the previous key")
	}
	t.Setenv("SUEZ_DATA_KEY_PREVIOUS", keyA)
	rotated, err := newContentSealer(true)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := rotated.open(sealed, "memory_items:tenant-a:item-1"); err != nil || string(plain) != "the plan" {
		t.Fatalf("old content unreadable after rotation: %v", err)
	}
	// Production refuses to run without a key; a wrong-size key is refused.
	t.Setenv("SUEZ_DATA_KEY", "")
	if _, err := newContentSealer(true); err == nil {
		t.Fatal("production accepted a missing data key")
	}
	t.Setenv("SUEZ_DATA_KEY", "dG9vLXNob3J0")
	if _, err := newContentSealer(false); err == nil {
		t.Fatal("a short data key was accepted")
	}
}

func netParse(cidr string) (any, []*net.IPNet, error) {
	_, network, err := net.ParseCIDR(cidr)
	return nil, []*net.IPNet{network}, err
}

func TestTextArrayScan(t *testing.T) {
	cases := map[string][]string{
		`{}`:                          {},
		`{tenant-admin}`:              {"tenant-admin"},
		`{a,b,c}`:                     {"a", "b", "c"},
		`{"two words",plain}`:         {"two words", "plain"},
		`{"quote \" and \\ slash",x}`: {`quote " and \ slash`, "x"},
		`{"a,b",c}`:                   {"a,b", "c"},
	}
	for literal, want := range cases {
		var got textArray
		if err := got.Scan(literal); err != nil || strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("Scan(%s) = %q, %v; want %q", literal, []string(got), err, want)
		}
		if err := got.Scan([]byte(literal)); err != nil {
			t.Errorf("Scan bytes %s: %v", literal, err)
		}
	}
	var got textArray
	for _, bad := range []any{"not an array", `{"unterminated`, 42} {
		if err := got.Scan(bad); err == nil {
			t.Errorf("Scan(%v) accepted", bad)
		}
	}
}
