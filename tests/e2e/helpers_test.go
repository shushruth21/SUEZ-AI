//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	controlPlane = "http://control-plane:8081"
	gateway      = "http://gateway:8080"
	opaURL       = "http://opa:8181"
	issuer       = "http://e2e:9100"
	providerURL  = "http://e2e:9200/v1"
	adminKey     = "suez-demo-admin-key"
	demoTenant   = "00000000-0000-0000-0000-000000000001"
	internalTok  = "e2e-internal-token-0123456789abcdef0123"
)

var (
	idpKey, _      = rsa.GenerateKey(rand.Reader, 2048)
	attackerKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	db             *sql.DB
	provider       = &mockProvider{}
	client         = &http.Client{Timeout: 30 * time.Second}
)

type mockProvider struct {
	mu       sync.Mutex
	requests []map[string]any
	auth     []string
}

func (p *mockProvider) last() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return nil
	}
	return p.requests[len(p.requests)-1]
}

func (p *mockProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

var usagePattern = regexp.MustCompile(`USAGE=(\d+)`)

// The mock provider speaks the OpenAI chat-completions protocol. Behaviour is
// chosen by the upstream model name; "USAGE=n" in the last message sets the
// reported total token count.
func (p *mockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "tool-does-not-exist") {
		// A failing tool endpoint that echoes the credential it was sent.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"rejected credential `+r.Header.Get("Authorization")+`"}`)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	p.requests = append(p.requests, body)
	p.auth = append(p.auth, r.Header.Get("Authorization"))
	p.mu.Unlock()

	model, _ := body["model"].(string)
	messages, _ := body["messages"].([]any)
	lastContent := ""
	if len(messages) > 0 {
		lastContent, _ = messages[len(messages)-1].(map[string]any)["content"].(string)
	}
	total := 20
	if match := usagePattern.FindStringSubmatch(lastContent); match != nil {
		total, _ = strconv.Atoi(match[1])
	}
	usage := map[string]int{"prompt_tokens": total - 5, "completion_tokens": 5, "total_tokens": total}
	switch model {
	case "up-500":
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"internal: key sk-live-SECRETSECRETSECRETSECRET leaked, prompt was `+lastContent+`"}}`)
		return
	case "up-empty":
		_, _ = io.WriteString(w, `{"id":"x","choices":[]}`)
		return
	}
	if body["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		send := func(payload any) {
			raw, _ := json.Marshal(payload)
			_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
			flusher.Flush()
		}
		for _, word := range []string{"streamed ", "answer ", "from ", "mock"} {
			send(map[string]any{"id": "s1", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": word}}}})
			if model == "up-stream-break" && word == "answer " {
				return // connection closes with no [DONE]
			}
		}
		send(map[string]any{"id": "s1", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		send(map[string]any{"id": "s1", "model": model, "choices": []any{}, "usage": usage})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "cmpl-1", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "mock answer to: " + lastContent}, "finish_reason": "stop"}},
		"usage":   usage,
	})
}

func TestMain(m *testing.M) {
	if os.Getenv("SUEZ_E2E") == "" {
		fmt.Println("skipping: run through scripts/e2e.sh")
		os.Exit(0)
	}
	idp := http.NewServeMux()
	idp.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/jwks"})
	})
	idp.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "e2e-1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(idpKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idpKey.E)).Bytes()),
		}}})
	})
	go func() { _ = http.ListenAndServe(":9100", idp) }()
	go func() { _ = http.ListenAndServe(":9200", provider) }()

	var err error
	db, err = sql.Open("pgx", "postgres://suez:e2e-password@postgres:5432/suez?sslmode=disable")
	if err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func b64(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func signRS256(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	signingInput := b64(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func signHS256(secret []byte, claims map[string]any) string {
	signingInput := b64(map[string]string{"alg": "HS256", "typ": "JWT", "kid": "e2e-1"}) + "." + b64(claims)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func claimsFor(subject, tenantSlug string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": issuer, "aud": "suez-api", "sub": subject, "tenant_slug": tenantSlug,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func userToken(subject, tenantSlug string) string {
	return "Bearer " + signRS256(idpKey, "e2e-1", claimsFor(subject, tenantSlug))
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r response) str(path ...string) string {
	value, _ := r.dig(path...).(string)
	return value
}

func (r response) dig(path ...string) any {
	var current any = r.body
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func call(method, url, auth string, body any, headers ...string) response {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	if auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			req.Header.Set("Authorization", auth)
		} else {
			req.Header.Set("X-API-Key", auth)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		return response{status: -1, raw: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func cp(method, path, auth string, body any) response {
	return call(method, controlPlane+path, auth, body)
}

func chat(auth, model, content string, extra map[string]any, headers ...string) response {
	body := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": content}}}
	for key, value := range extra {
		body[key] = value
	}
	return call(http.MethodPost, gateway+"/v1/chat/completions", auth, body, headers...)
}

func want(t *testing.T, what string, got response, status int) {
	t.Helper()
	if got.status != status {
		t.Fatalf("%s: status %d, want %d: %s", what, got.status, status, got.raw)
	}
}

func expect(t *testing.T, what string, got response, status int) {
	t.Helper()
	if got.status != status {
		t.Errorf("%s: status %d, want %d: %s", what, got.status, status, got.raw)
	}
}

// createKey mints an API key with the given scopes using creatorAuth.
func createKey(t *testing.T, creatorAuth, label string, scopes []string, extra map[string]any) (raw, id string) {
	t.Helper()
	body := map[string]any{"label": label, "scopes": scopes}
	for key, value := range extra {
		body[key] = value
	}
	resp := cp("POST", "/api/v1/api-keys", creatorAuth, body)
	want(t, "create key "+label, resp, 201)
	return resp.str("rawApiKey"), resp.str("apiKey", "id")
}

// grant gives subject an active grant on a model or tool through the normal
// request-and-approve workflow.
func grant(t *testing.T, adminAuth, subject, resourceType, name string) string {
	t.Helper()
	req := cp("POST", "/api/v1/access-requests", adminAuth, map[string]any{"subject": subject, "resourceType": resourceType, "resourceName": name, "justification": "e2e"})
	want(t, "access request", req, 201)
	approved := cp("POST", "/api/v1/access-requests/"+req.str("accessRequest", "id")+"/approve", adminAuth, map[string]any{"reason": "e2e"})
	want(t, "approve", approved, 200)
	return approved.str("grant", "id")
}

func createModel(t *testing.T, adminAuth, name, providerName, upstream string, extra map[string]any) {
	t.Helper()
	body := map[string]any{"externalName": name, "provider": providerName, "modelName": name, "upstreamModel": upstream, "enabled": true, "maxRpm": 1000, "maxTpm": 10000000}
	if providerName == "openai-compatible" {
		body["upstreamBaseUrl"] = providerURL
	}
	for key, value := range extra {
		body[key] = value
	}
	want(t, "create model "+name, cp("POST", "/api/v1/models", adminAuth, body), 201)
}

func queryString(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out sql.NullString
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&out); err != nil && err != sql.ErrNoRows {
		t.Fatalf("query failed: %v", err)
	}
	return out.String
}

func opaModules(t *testing.T) string {
	t.Helper()
	resp := call("GET", opaURL+"/v1/policies", "", nil)
	var ids []string
	if result, ok := resp.body["result"].([]any); ok {
		for _, entry := range result {
			id, _ := entry.(map[string]any)["id"].(string)
			if strings.HasPrefix(id, "suez/") {
				ids = append(ids, id)
			}
		}
	}
	return strings.Join(sortStrings(ids), ",")
}

func sortStrings(values []string) []string {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
	return values
}

func opaDecide(tenantID, action, resourceType string) (bool, string) {
	resp := call("POST", opaURL+"/v1/data/suez/authz/decision", "", map[string]any{"input": map[string]any{
		"action": action, "identity": map[string]any{"tenantId": tenantID, "actor": "u", "roles": []string{"user"}},
		"resource": map[string]any{"type": resourceType, "name": "x", "enabled": true}}})
	allow, _ := resp.dig("result", "allow").(bool)
	reasons, _ := json.Marshal(resp.dig("result", "reasons"))
	return allow, string(reasons)
}

// readSSE collects a streamed chat response.
func readSSE(auth, model, content string, headers ...string) (status int, header http.Header, text string, events []string, done bool) {
	raw, _ := json.Marshal(map[string]any{"model": model, "stream": true, "messages": []map[string]string{{"role": "user", "content": content}}})
	req, _ := http.NewRequest("POST", gateway+"/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("X-API-Key", auth)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		return -1, nil, err.Error(), nil, false
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		events = append(events, line)
		if line == "data: [DONE]" {
			done = true
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) == nil {
				for _, choice := range chunk.Choices {
					text += choice.Delta.Content
				}
			}
		}
	}
	return resp.StatusCode, resp.Header, text, events, done
}
