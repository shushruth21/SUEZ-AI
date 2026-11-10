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
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestChatCacheKeyCoversRequestParameters(t *testing.T) {
	temperature := 0.2
	maxTokens := 100
	base := chatCompletionRequest{Model: "m", Messages: []chatMessage{{Role: "user", Content: "hi"}}, Temperature: &temperature, MaxTokens: &maxTokens}
	key := chatCacheKey("t1", "m", base)

	variants := map[string]chatCompletionRequest{}
	v := base
	otherTemperature := 0.9
	v.Temperature = &otherTemperature
	variants["temperature"] = v
	v = base
	otherMaxTokens := 5
	v.MaxTokens = &otherMaxTokens
	variants["max_tokens"] = v
	v = base
	v.Messages = []chatMessage{{Role: "user", Content: "hello"}}
	variants["messages"] = v
	for name, req := range variants {
		if chatCacheKey("t1", "m", req) == key {
			t.Errorf("changing %s did not change the cache key", name)
		}
	}
	if chatCacheKey("t2", "m", base) == key || chatCacheKey("t1", "other", base) == key {
		t.Error("tenant and model must be part of the cache key")
	}
	if chatCacheKey("t1", "m", base) != key {
		t.Error("cache key is not deterministic")
	}
	unset := base
	unset.Temperature = nil
	unset.MaxTokens = nil
	zeroTemperature, zeroTokens := 0.0, 0
	zero := unset
	zero.Temperature, zero.MaxTokens = &zeroTemperature, &zeroTokens
	if chatCacheKey("t1", "m", unset) == chatCacheKey("t1", "m", zero) {
		t.Error("unset sampling fields must not share a cache key with explicit zero values")
	}
	if !strings.HasPrefix(key, chatCacheTenantPrefix("t1")) {
		t.Errorf("key %q lacks the tenant prefix used for per-tenant SCAN", key)
	}
}

func TestOptionalSamplingFieldsAreOmittedUpstream(t *testing.T) {
	var received []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatal(err)
		}
		received = append(received, body)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	t.Setenv("OPENAI_COMPAT_API_KEY", "test")
	app := &gatewayApp{httpClient: upstream.Client()}
	model := modelRuntime{Provider: "openai-compatible", ExternalName: "test", ModelName: "upstream", UpstreamBaseURL: upstream.URL}
	request := chatCompletionRequest{Model: "test", Messages: []chatMessage{{Role: "user", Content: "hi"}}}
	if _, err := app.executeOpenAICompatibleChat(context.Background(), model, request); err != nil {
		t.Fatal(err)
	}
	if _, present := received[0]["temperature"]; present {
		t.Fatal("unset temperature was sent upstream")
	}
	if _, present := received[0]["max_tokens"]; present {
		t.Fatal("unset max_tokens was sent upstream")
	}
	temperature, maxTokens := 0.0, 12
	request.Temperature, request.MaxTokens = &temperature, &maxTokens
	if _, err := app.executeOpenAICompatibleChat(context.Background(), model, request); err != nil {
		t.Fatal(err)
	}
	if received[1]["temperature"] != 0.0 || received[1]["max_tokens"] != float64(12) {
		t.Fatalf("explicit zero/values were not preserved: %#v", received[1])
	}
}

func TestValidateChatCompletionRequest(t *testing.T) {
	valid := chatCompletionRequest{Model: "m", Messages: []chatMessage{{Role: "tool", Content: "x"}}}
	if err := validateChatCompletionRequest(valid); err != nil {
		t.Fatal(err)
	}
	invalidTemperature := 2.1
	if err := validateChatCompletionRequest(chatCompletionRequest{Model: "m", Messages: valid.Messages, Temperature: &invalidTemperature}); err == nil {
		t.Fatal("invalid temperature accepted")
	}
	zero := 0
	if err := validateChatCompletionRequest(chatCompletionRequest{Model: "m", Messages: valid.Messages, MaxTokens: &zero}); err == nil {
		t.Fatal("zero max_tokens accepted")
	}
	if err := validateChatCompletionRequest(chatCompletionRequest{Model: "m", Messages: []chatMessage{{Role: "other"}}}); err == nil {
		t.Fatal("unknown message role accepted")
	}
}

func TestDecodeUpstreamChatResponseRejectsNoChoices(t *testing.T) {
	if _, err := decodeUpstreamChatResponse(strings.NewReader(`{"usage":{"total_tokens":1}}`)); err == nil {
		t.Fatal("response without choices accepted")
	}
}

func TestQuotaRedisFailureIsClassifiedAsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond})
	defer client.Close()
	app := &gatewayApp{redis: client}
	err := app.enforceQuota(context.Background(), identityContext{TenantID: "t", Actor: "a"}, "m", 10, 100, 1)
	if !errors.Is(err, errQuotaBackendUnavailable) {
		t.Fatalf("quota error = %v, want unavailable sentinel", err)
	}
}

func TestIdentityResolver503(t *testing.T) {
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
	}))
	defer resolver.Close()
	previous := authResolverClient
	authResolverClient = resolver.Client()
	defer func() { authResolverClient = previous }()

	handler := withIdentityResolver(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/identity", nil)
	request.Header.Set("X-API-Key", "key")
	t.Setenv("AUTH_RESOLVER_URL", resolver.URL)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "identity service unavailable") {
		t.Fatalf("resolver outage = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicProviderErrorHidesUpstreamBody(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &upstreamError{label: "groq upstream", status: "401 Unauthorized", body: `{"error":"invalid key sk-live-123"}`})
	got := publicProviderError(err)
	if got != "groq upstream returned 401 Unauthorized" {
		t.Fatalf("public error = %q", got)
	}
	if !strings.Contains(err.Error(), "sk-live-123") {
		t.Fatal("full error (for logs and audit) lost the upstream body")
	}
	if publicProviderError(context.DeadlineExceeded) != "upstream request timed out" {
		t.Fatal("timeouts should be summarised")
	}
	configErr := errors.New("Groq API key is not configured (set GROQ_API_KEY or GROQ_API_KEY_FILE)")
	if publicProviderError(configErr) != configErr.Error() {
		t.Fatal("gateway configuration errors should pass through")
	}
}

func TestStatsKeysArePerTenant(t *testing.T) {
	if statsKey("a", "cache_hits") == statsKey("b", "cache_hits") {
		t.Fatal("stats keys must differ per tenant")
	}
}
