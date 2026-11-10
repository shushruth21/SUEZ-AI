package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

var gatewayPermissions = []string{permSessionRead, permUsageRead, permGatewayInvoke, permGatewayTools}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

func baseChatRequest() chatCompletionRequest {
	return chatCompletionRequest{Model: "m", Messages: []chatMessage{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}},
		Temperature: ptrF(0.2), MaxTokens: ptrI(64)}
}

func TestChatCacheKeyCoversEveryMeaningfulDifference(t *testing.T) {
	base := baseChatRequest()
	key := chatCacheKey("t1", "p1", "m", base)
	if chatCacheKey("t1", "p1", "m", base) != key {
		t.Fatal("cache key is not deterministic")
	}
	variants := map[string]func(*chatCompletionRequest){
		"temperature":    func(r *chatCompletionRequest) { r.Temperature = ptrF(0.9) },
		"max_tokens":     func(r *chatCompletionRequest) { r.MaxTokens = ptrI(128) },
		"top_p":          func(r *chatCompletionRequest) { r.TopP = ptrF(0.5) },
		"seed":           func(r *chatCompletionRequest) { seed := int64(7); r.Seed = &seed },
		"user message":   func(r *chatCompletionRequest) { r.Messages[1].Content = "hello" },
		"system message": func(r *chatCompletionRequest) { r.Messages[0].Content = "be verbose" },
		"developer role": func(r *chatCompletionRequest) { r.Messages[0].Role = "developer" },
		"extra message": func(r *chatCompletionRequest) {
			r.Messages = append(r.Messages, chatMessage{Role: "assistant", Content: "x"})
		},
		"tools": func(r *chatCompletionRequest) {
			r.Tools = json.RawMessage(`[{"type":"function","function":{"name":"f"}}]`)
		},
		"tool_choice":      func(r *chatCompletionRequest) { r.ToolChoice = json.RawMessage(`"required"`) },
		"response_format":  func(r *chatCompletionRequest) { r.ResponseFormat = json.RawMessage(`{"type":"json_object"}`) },
		"stop":             func(r *chatCompletionRequest) { r.Stop = json.RawMessage(`["END"]`) },
		"presence_penalty": func(r *chatCompletionRequest) { r.PresencePenalty = ptrF(1) },
	}
	for name, change := range variants {
		req := baseChatRequest()
		change(&req)
		if chatCacheKey("t1", "p1", "m", req) == key {
			t.Errorf("%s does not change the cache key", name)
		}
	}
	if chatCacheKey("t2", "p1", "m", base) == key || chatCacheKey("t1", "p2", "m", base) == key || chatCacheKey("t1", "p1", "other", base) == key {
		t.Error("tenant, policy version or model does not change the cache key")
	}
	unset, zero := baseChatRequest(), baseChatRequest()
	unset.Temperature = nil
	zero.Temperature = ptrF(0)
	if chatCacheKey("t1", "p1", "m", unset) == chatCacheKey("t1", "p1", "m", zero) {
		t.Error("unset temperature and temperature 0 share a cache key")
	}
	// Streaming is a transport choice, not a different answer.
	streamed := baseChatRequest()
	streamed.Stream = true
	if chatCacheKey("t1", "p1", "m", streamed) != key {
		t.Error("stream flag changed the cache key")
	}
	if strings.Contains(key, "hi") || strings.Contains(key, "be brief") {
		t.Error("request content appears in the cache key")
	}
}

func upstreamModel(url string) modelRuntime {
	return modelRuntime{ExternalName: "ext", Provider: "openai-compatible", ModelName: "up", UpstreamBaseURL: url}
}

const goodUpstreamBody = `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

func TestOptionalFieldsAreForwardedOnlyWhenSent(t *testing.T) {
	var seen map[string]any
	var auth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		seen = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = io.WriteString(w, goodUpstreamBody)
	}))
	defer upstream.Close()
	t.Setenv("OPENAI_COMPAT_API_KEY", "test-provider-key")
	app := &gatewayApp{httpClient: upstream.Client()}

	bare := chatCompletionRequest{Model: "ext", Messages: []chatMessage{{Role: "user", Content: "hi"}}, Suez: &suezExtensions{}}
	resp, err := app.executeChatProvider(context.Background(), upstreamModel(upstream.URL), bare, "trace")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "ext" {
		t.Errorf("response model = %q, want the external name", resp.Model)
	}
	if auth != "Bearer test-provider-key" {
		t.Errorf("provider key not sent")
	}
	for _, field := range []string{"temperature", "max_tokens", "top_p", "seed", "tools", "stop", "stream", "suez", "response_format", "n"} {
		if _, present := seen[field]; present {
			t.Errorf("%s was forwarded although the caller did not send it: %v", field, seen[field])
		}
	}
	if seen["model"] != "up" {
		t.Errorf("upstream model = %v, want up", seen["model"])
	}

	full := bare
	full.Temperature, full.MaxTokens, full.TopP = ptrF(0), ptrI(1), ptrF(1)
	full.Tools = json.RawMessage(`[{"type":"function","function":{"name":"f"}}]`)
	if _, err := app.executeChatProvider(context.Background(), upstreamModel(upstream.URL), full, "trace"); err != nil {
		t.Fatal(err)
	}
	if seen["temperature"] != float64(0) || seen["max_tokens"] != float64(1) || seen["top_p"] != float64(1) || seen["tools"] == nil {
		t.Errorf("explicit values (including zero) were not forwarded: %v", seen)
	}
}

func TestValidateChatCompletionRequest(t *testing.T) {
	valid := baseChatRequest()
	if err := validateChatCompletionRequest(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	invalid := map[string]func(*chatCompletionRequest){
		"temperature high":   func(r *chatCompletionRequest) { r.Temperature = ptrF(2.1) },
		"temperature low":    func(r *chatCompletionRequest) { r.Temperature = ptrF(-0.1) },
		"top_p":              func(r *chatCompletionRequest) { r.TopP = ptrF(1.5) },
		"max_tokens zero":    func(r *chatCompletionRequest) { r.MaxTokens = ptrI(0) },
		"both limits":        func(r *chatCompletionRequest) { r.MaxCompletionTokens = ptrI(5) },
		"n":                  func(r *chatCompletionRequest) { r.N = ptrI(3) },
		"role":               func(r *chatCompletionRequest) { r.Messages[0].Role = "wizard" },
		"no messages":        func(r *chatCompletionRequest) { r.Messages = nil },
		"no model":           func(r *chatCompletionRequest) { r.Model = " " },
		"tools not an array": func(r *chatCompletionRequest) { r.Tools = json.RawMessage(`{"a":1}`) },
		"penalty":            func(r *chatCompletionRequest) { r.FrequencyPenalty = ptrF(3) },
		"memory without id": func(r *chatCompletionRequest) {
			r.Suez = &suezExtensions{}
			_ = json.Unmarshal([]byte(`{"memory":{"tokenBudget":10}}`), r.Suez)
		},
	}
	for name, change := range invalid {
		req := baseChatRequest()
		change(&req)
		if err := validateChatCompletionRequest(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestUpstreamResponsesAreValidated(t *testing.T) {
	bad := map[string]string{
		"no choices":    `{"usage":{"total_tokens":1}}`,
		"empty choices": `{"choices":[]}`,
		"empty message": `{"choices":[{"message":{"role":"assistant","content":""}}]}`,
		"no role":       `{"choices":[{"message":{"content":"x"}}]}`,
		"not json":      `<html>bad gateway</html>`,
	}
	for name, body := range bad {
		_, err := decodeUpstreamChatResponse("upstream", strings.NewReader(body))
		var typed *providerError
		if !errors.As(err, &typed) || typed.Code != "upstream_invalid_response" {
			t.Errorf("%s: err = %v, want upstream_invalid_response", name, err)
		}
	}
	if _, err := decodeUpstreamChatResponse("upstream", strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1"}]}}]}`)); err != nil {
		t.Errorf("tool-call response rejected: %v", err)
	}
}

func TestProviderErrorsAreNormalizedAndNeverEchoUpstreamBodies(t *testing.T) {
	secretBody := `{"error":{"message":"Incorrect API key provided: sk-live-abcdefghijklmnopqrstuvwxyz. Prompt was: my salary is 123"}}`
	cases := []struct {
		upstream   int
		wantCode   string
		wantStatus int
	}{
		{401, "upstream_auth_failed", 502},
		{403, "upstream_auth_failed", 502},
		{400, "upstream_rejected_request", 400},
		{404, "upstream_model_not_found", 502},
		{429, "upstream_rate_limited", 429},
		{500, "upstream_unavailable", 502},
	}
	t.Setenv("OPENAI_COMPAT_API_KEY", "k")
	for _, c := range cases {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(c.upstream)
			_, _ = io.WriteString(w, secretBody)
		}))
		app := &gatewayApp{httpClient: upstream.Client()}
		_, err := app.executeChatProvider(context.Background(), upstreamModel(upstream.URL), baseChatRequest(), "trace")
		upstream.Close()
		var typed *providerError
		if !errors.As(err, &typed) || typed.Code != c.wantCode || typed.Status != c.wantStatus {
			t.Errorf("upstream %d: err = %+v, want %s/%d", c.upstream, typed, c.wantCode, c.wantStatus)
			continue
		}
		rec := httptest.NewRecorder()
		writeProviderError(rec, err, "trace-1")
		body := rec.Body.String()
		if rec.Code != c.wantStatus || !strings.Contains(body, `"traceId":"trace-1"`) || !strings.Contains(body, c.wantCode) {
			t.Errorf("upstream %d: response %d %s", c.upstream, rec.Code, body)
		}
		for _, leak := range []string{"sk-live", "salary", "Incorrect API key"} {
			if strings.Contains(body, leak) || strings.Contains(err.Error(), leak) {
				t.Errorf("upstream %d: %q leaked to the caller or the error text", c.upstream, leak)
			}
		}
		if strings.Contains(typed.detail, "sk-live-abcdefghijklmnopqrstuvwxyz") {
			t.Errorf("upstream %d: provider key not redacted in the log detail", c.upstream)
		}
	}

	if got := normalizeTransportError("upstream", context.DeadlineExceeded); got.Code != "upstream_timeout" || got.Status != http.StatusGatewayTimeout {
		t.Errorf("timeout = %+v", got)
	}
	app := &gatewayApp{httpClient: http.DefaultClient}
	t.Setenv("OPENAI_COMPAT_API_KEY", "")
	_, err := app.executeChatProvider(context.Background(), upstreamModel("http://127.0.0.1:1"), baseChatRequest(), "trace")
	var typed *providerError
	if !errors.As(err, &typed) || typed.Code != "provider_not_configured" || typed.Status != http.StatusServiceUnavailable {
		t.Errorf("missing provider key = %+v", typed)
	}
}

func TestUpstreamRetriesOnlyWhereSafe(t *testing.T) {
	t.Setenv("OPENAI_COMPAT_API_KEY", "k")
	run := func(statuses ...int) (int32, error) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			index := int(calls.Add(1)) - 1
			status := statuses[len(statuses)-1]
			if index < len(statuses) {
				status = statuses[index]
			}
			if status == 200 {
				_, _ = io.WriteString(w, goodUpstreamBody)
				return
			}
			w.WriteHeader(status)
		}))
		defer upstream.Close()
		app := &gatewayApp{httpClient: upstream.Client()}
		_, err := app.executeChatProvider(context.Background(), upstreamModel(upstream.URL), baseChatRequest(), "trace")
		return calls.Load(), err
	}
	if calls, err := run(503, 200); err != nil || calls != 2 {
		t.Errorf("503 then 200: calls=%d err=%v, want one retry and success", calls, err)
	}
	if calls, err := run(503); err == nil || calls != upstreamMaxAttempts {
		t.Errorf("always 503: calls=%d err=%v, want %d attempts then failure", calls, err, upstreamMaxAttempts)
	}
	for _, status := range []int{400, 401, 404, 429, 500} {
		if calls, err := run(status, 200); err == nil || calls != 1 {
			t.Errorf("%d must not be retried: calls=%d err=%v", status, calls, err)
		}
	}
}

func TestStreamingRelaysChunksAndCollectsUsage(t *testing.T) {
	t.Setenv("OPENAI_COMPAT_API_KEY", "k")
	var sentBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sentBody)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"id":"c","model":"up","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
			`: keep-alive comment`,
			`data: {"id":"c","model":"up","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`data: {"id":"c","model":"up","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			`data: [DONE]`,
		} {
			_, _ = io.WriteString(w, line+"\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()
	app := &gatewayApp{httpClient: upstream.Client(), streamClient: upstream.Client()}
	var chunks []string
	result, err := app.streamChatProvider(context.Background(), upstreamModel(upstream.URL), baseChatRequest(), "trace", func(data []byte) error {
		chunks = append(chunks, string(data))
		return nil
	})
	if err != nil || !result.completed {
		t.Fatalf("stream failed: %v", err)
	}
	if result.content.String() != "Hello" || !result.usageKnown || result.usage.TotalTokens != 7 || result.finishReason != "stop" {
		t.Errorf("result = %q usage=%+v finish=%q", result.content.String(), result.usage, result.finishReason)
	}
	if len(chunks) != 3 || strings.Contains(strings.Join(chunks, ""), `"model":"up"`) || !strings.Contains(chunks[0], `"model":"ext"`) {
		t.Errorf("relayed chunks = %v", chunks)
	}
	if sentBody["stream"] != true || sentBody["stream_options"] == nil {
		t.Errorf("upstream was not asked to stream with usage: %v", sentBody)
	}
}

func TestStreamingReportsInterruptionAndKeepsPartialContent(t *testing.T) {
	t.Setenv("OPENAI_COMPAT_API_KEY", "k")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"partial answer"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// Connection closes without [DONE].
	}))
	defer upstream.Close()
	app := &gatewayApp{httpClient: upstream.Client(), streamClient: upstream.Client()}
	result, err := app.streamChatProvider(context.Background(), upstreamModel(upstream.URL), baseChatRequest(), "trace", func([]byte) error { return nil })
	var typed *providerError
	if !errors.As(err, &typed) || typed.Code != "upstream_stream_interrupted" {
		t.Fatalf("err = %v, want upstream_stream_interrupted", err)
	}
	if result.completed || result.content.String() != "partial answer" {
		t.Errorf("partial content lost: completed=%v content=%q", result.completed, result.content.String())
	}

	// A client that goes away stops the relay.
	stopped, err := app.streamChatProvider(context.Background(), modelRuntime{ExternalName: "demo", Provider: "stub"}, baseChatRequest(), "trace", func([]byte) error {
		return errors.New("broken pipe")
	})
	if !errors.As(err, &typed) || typed.Code != "client_cancelled" || stopped.completed {
		t.Errorf("client disconnect: err = %v", err)
	}
	// A cancelled request does not start streaming at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.streamChatProvider(ctx, modelRuntime{ExternalName: "demo", Provider: "stub"}, baseChatRequest(), "trace", func([]byte) error { return nil }); !errors.As(err, &typed) || typed.Code != "client_cancelled" {
		t.Errorf("cancelled context: err = %v", err)
	}
}

func TestStubStreamMatchesNonStreamAnswer(t *testing.T) {
	app := &gatewayApp{}
	model := modelRuntime{ExternalName: "demo", Provider: "stub"}
	whole := executeStubChat(model, baseChatRequest())
	result, err := app.streamChatProvider(context.Background(), model, baseChatRequest(), "trace", func([]byte) error { return nil })
	if err != nil || result.content.String() != whole.Choices[0].Message.Content || result.usage != whole.Usage {
		t.Fatalf("stream %q %+v vs %q %+v (%v)", result.content.String(), result.usage, whole.Choices[0].Message.Content, whole.Usage, err)
	}
}

func TestContextThresholdClassification(t *testing.T) {
	cases := map[float64]string{0: contextOK, 79.99: contextOK, 80: contextNotice, 84.9: contextNotice, 85: contextCheckpoint, 94.9: contextCheckpoint, 95: contextWarning, 99.9: contextWarning, 100: contextLimit, 140: contextLimit}
	for percent, want := range cases {
		if got := defaultContextThresholds.classify(percent); got != want {
			t.Errorf("classify(%v) = %s, want %s", percent, got, want)
		}
	}
	custom := contextThresholds{Notice: 50, Checkpoint: 60, Warning: 70, Block: 90}
	if custom.classify(65) != contextCheckpoint || custom.classify(90) != contextLimit {
		t.Error("custom thresholds are not applied")
	}
	state := contextStateFor(870, 1000, measurementProviderReported, defaultContextThresholds)
	if state.Percent != 87 || state.Status != contextCheckpoint || !needsCheckpoint(state.Status) {
		t.Errorf("state = %+v", state)
	}
	if unknown := contextStateFor(870, 0, measurementProviderReported, defaultContextThresholds); unknown.Status != contextUnknown || needsCheckpoint(unknown.Status) {
		t.Errorf("unknown window must not produce a percentage: %+v", unknown)
	}
	rec := httptest.NewRecorder()
	state.setHeaders(rec)
	if rec.Header().Get("X-SUEZ-Context-Status") != "checkpoint" || rec.Header().Get("X-SUEZ-Context-Percent") != "87.00" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestEstimateLeansHigh(t *testing.T) {
	text := strings.Repeat("word ", 400) // 2000 characters
	estimate := estimateChatTokens([]chatMessage{{Role: "user", Content: text}}, 0)
	if estimate < 2000/4 {
		t.Errorf("estimate %d is below the usual 4-characters-per-token figure", estimate)
	}
	if estimateChatTokens([]chatMessage{{Role: "user", Content: text}}, 100) != estimate+100 {
		t.Error("requested completion tokens are not added to the estimate")
	}
}

func deadRedis() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond, MaxRetries: -1})
}

func TestQuotaFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	client := deadRedis()
	defer client.Close()
	app := &gatewayApp{redis: client}
	if err := app.enforceQuota(context.Background(), identityContext{TenantID: "t", Actor: "a"}, "m", 10, 100, 1); !errors.Is(err, errQuotaBackendUnavailable) {
		t.Fatalf("model quota error = %v, want unavailable", err)
	}
	limited := identityContext{TenantID: "t", Actor: "a", APIKeyID: "k1", RateLimitRPM: 5}
	if err := app.enforceKeyRateLimit(context.Background(), limited); !errors.Is(err, errQuotaBackendUnavailable) {
		t.Fatalf("key rate limit error = %v, want unavailable", err)
	}
	// Keys without a limit, and JWT users, never touch Redis for this check.
	if err := app.enforceKeyRateLimit(context.Background(), identityContext{TenantID: "t", Actor: "a", APIKeyID: "k1"}); err != nil {
		t.Fatalf("unlimited key: %v", err)
	}
}

func TestGatewayRoutesRequireTheirPermission(t *testing.T) {
	app := &gatewayApp{usageStore: &fakeUsageStore{}}
	call := func(method, path string, permissions ...string) int {
		identity := identityContext{TenantID: "t", Actor: "a", Permissions: permissions}
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		app.handleProtected(rec, req.WithContext(context.WithValue(req.Context(), identityContextKey, identity)))
		return rec.Code
	}
	// A read-only key can read usage but can neither chat nor run tools.
	if got := call(http.MethodPost, "/v1/chat/completions", permSessionRead, permUsageRead); got != http.StatusForbidden {
		t.Errorf("chat without gateway.invoke = %d, want 403", got)
	}
	if got := call(http.MethodPost, "/v1/tools/execute", permSessionRead, permUsageRead, permGatewayInvoke); got != http.StatusForbidden {
		t.Errorf("tool without gateway.tools = %d, want 403", got)
	}
	if got := call(http.MethodGet, "/v1/context/usage?session=s1", permGatewayInvoke); got != http.StatusForbidden {
		t.Errorf("usage without usage.read = %d, want 403", got)
	}
	if got := call(http.MethodGet, "/v1/identity"); got != http.StatusForbidden {
		t.Errorf("identity with no permissions = %d, want 403", got)
	}
	if got := call(http.MethodGet, "/v1/context/usage?session=s1", permUsageRead); got != http.StatusOK {
		t.Errorf("usage with usage.read = %d, want 200", got)
	}
	if got := call(http.MethodGet, "/v1/chat/completions", gatewayPermissions...); got != http.StatusMethodNotAllowed {
		t.Errorf("GET chat = %d, want 405", got)
	}
	// An identity with no tenant is never authorized, whatever it claims.
	req := httptest.NewRequest(http.MethodGet, "/v1/identity", nil)
	rec := httptest.NewRecorder()
	app.handleProtected(rec, req.WithContext(context.WithValue(req.Context(), identityContextKey, identityContext{Actor: "a", Permissions: gatewayPermissions})))
	if rec.Code != http.StatusForbidden {
		t.Errorf("identity without tenant = %d, want 403", rec.Code)
	}
}

func TestAPIKeyModelAndToolRestrictions(t *testing.T) {
	if !allowedByList(nil, "any") || !allowedByList([]string{"a", "b"}, "b") || allowedByList([]string{"a"}, "b") {
		t.Fatal("allowedByList is wrong")
	}
	client := deadRedis()
	defer client.Close()
	app := &gatewayApp{redis: client}
	restricted := identityContext{TenantID: "t", Actor: "a", Permissions: gatewayPermissions, AllowedModels: []string{"small"}, AllowedTools: []string{"weather.lookup"}}
	for path, body := range map[string]string{
		"/v1/chat/completions": `{"model":"large","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/tools/execute":    `{"name":"finance.transfer","input":{}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		app.handleProtected(rec, req.WithContext(context.WithValue(req.Context(), identityContextKey, restricted)))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not allowed") {
			t.Errorf("%s outside the key's allowlist = %d %s, want 403", path, rec.Code, rec.Body.String())
		}
	}
}

func TestIdentityResolverFailsClosed(t *testing.T) {
	status := http.StatusServiceUnavailable
	body := `{"error":"pq: connection refused to 10.0.0.5"}`
	var token string
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-SUEZ-Internal-Token")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer resolver.Close()
	t.Setenv("AUTH_RESOLVER_URL", resolver.URL)
	app := &gatewayApp{cfg: runtimeConfig{InternalToken: "internal-token-value"}}
	reached := false
	handler := app.withIdentityResolver(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	call := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/identity", nil)
		req.Header.Set("X-API-Key", "key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, out := call(); code != http.StatusServiceUnavailable || strings.Contains(out, "10.0.0.5") {
		t.Errorf("resolver 503: %d %s", code, out)
	}
	if token != "internal-token-value" {
		t.Error("internal token was not sent to the resolver")
	}
	status, body = http.StatusUnauthorized, `{"error":"unknown api key"}`
	if code, out := call(); code != http.StatusUnauthorized || strings.Contains(out, "unknown api key") {
		t.Errorf("resolver 401: %d %s (the reason must not be passed on)", code, out)
	}
	// Anything that is not an explicit, complete identity is refused.
	for _, c := range []struct {
		status int
		body   string
	}{{200, `{}`}, {200, `{"identityContext":{"actor":"a"}}`}, {200, `not json`}, {404, `{}`}, {302, ``}, {500, `{}`}} {
		status, body = c.status, c.body
		if code, _ := call(); code != http.StatusServiceUnavailable {
			t.Errorf("resolver %d %q produced %d, want 503", c.status, c.body, code)
		}
	}
	if reached {
		t.Fatal("a request reached the protected handler without a resolved identity")
	}
	resolver.Close()
	if code, _ := call(); code != http.StatusServiceUnavailable {
		t.Errorf("resolver down: %d, want 503", code)
	}
}

func TestToolErrorsAreNormalized(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"error":"token %s was rejected"}`, r.Header.Get("X-Token"))
	}))
	defer endpoint.Close()
	app := &gatewayApp{httpClient: endpoint.Client()}
	tool := toolRuntime{Name: "t", ExecutionMode: "http", TargetURL: endpoint.URL, HTTPHeaders: json.RawMessage(`{"X-Token":"s3cret-tool-token"}`)}
	_, err := app.executeTool(context.Background(), tool, map[string]any{})
	var typed *providerError
	if !errors.As(err, &typed) || typed.Code != "upstream_unavailable" || strings.Contains(err.Error(), "s3cret") || strings.Contains(typed.Message, "s3cret") {
		t.Fatalf("tool error = %+v", typed)
	}
	// Redirects are not followed, so secret headers never reach another host.
	tool.TargetURL = endpoint.URL + "/redirect"
	if _, err := app.executeTool(context.Background(), tool, map[string]any{}); err == nil {
		t.Fatal("a redirecting tool endpoint was treated as success")
	}
}

func TestStatsKeysArePerTenant(t *testing.T) {
	if statsKey("a", "cache_hits") == statsKey("b", "cache_hits") {
		t.Fatal("stats keys must differ per tenant")
	}
}
