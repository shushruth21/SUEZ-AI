package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeUsageStore struct {
	measurements []contextMeasurement
	windows      map[string]int
	err          error
}

func (f *fakeUsageStore) RecordMeasurement(_ context.Context, m contextMeasurement) error {
	if f.err != nil {
		return f.err
	}
	f.measurements = append(f.measurements, m)
	return nil
}

func (f *fakeUsageStore) LatestMeasurement(_ context.Context, tenantID, actor, sessionID string) (*contextMeasurement, error) {
	if f.err != nil {
		return nil, f.err
	}
	var latest *contextMeasurement
	for i := range f.measurements {
		m := f.measurements[i]
		if m.TenantID != tenantID || m.Actor != actor || m.SessionID != sessionID {
			continue
		}
		if latest == nil || m.MeasuredAt.After(latest.MeasuredAt) {
			latest = &m
		}
	}
	return latest, nil
}

func (f *fakeUsageStore) ContextWindow(_ context.Context, _ string, model string) (*int, error) {
	if f.err != nil {
		return nil, f.err
	}
	if window, ok := f.windows[model]; ok {
		return &window, nil
	}
	return nil, nil
}

var (
	aliceIdentity = identityContext{TenantID: "tenant-1", Actor: "alice", Roles: []string{"user"}}
	bobIdentity   = identityContext{TenantID: "tenant-1", Actor: "bob", Roles: []string{"user"}}
	stubModel     = modelRuntime{ExternalName: "demo-model", Provider: "stub"}
	groqModel     = modelRuntime{ExternalName: "groq-llama", Provider: "groq"}
	t0            = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
)

func intPtr(v int) *int { return &v }

func usageRequest(t *testing.T, app *gatewayApp, identity identityContext, query string) (int, contextUsageResponse, map[string]string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/context/usage"+query, nil)
	req = req.WithContext(context.WithValue(req.Context(), identityContextKey, identity))
	rec := httptest.NewRecorder()
	app.handleProtected(rec, req)

	var body contextUsageResponse
	var failure map[string]string
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
		}
	} else {
		_ = json.Unmarshal(rec.Body.Bytes(), &failure)
	}
	return rec.Code, body, failure
}

func TestValidateSessionID(t *testing.T) {
	valid := []string{"a", "session-1", "6f1c0b9e-4c1a-4f5e-9b55-1f0e2d3c4b5a", "proj:SUEZ.run_2", strings.Repeat("x", 128), "  padded  "}
	for _, id := range valid {
		if _, err := validateSessionID(id); err != nil {
			t.Errorf("%q should be valid: %v", id, err)
		}
	}
	invalid := []string{"", "   ", "-leading-dash", "has space", "semi;colon", "slash/x", strings.Repeat("x", 129), "emoji🙂", "new\nline"}
	for _, id := range invalid {
		if _, err := validateSessionID(id); err == nil {
			t.Errorf("%q should be rejected", id)
		}
	}
}

func TestSessionIDFromRequestIsOptional(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if id, err := sessionIDFromRequest(req); err != nil || id != "" {
		t.Fatalf("absent header should be allowed, got %q %v", id, err)
	}
	req.Header.Set(sessionHeader, " abc-1 ")
	if id, err := sessionIDFromRequest(req); err != nil || id != "abc-1" {
		t.Fatalf("got %q %v", id, err)
	}
	req.Header.Add(sessionHeader, "second")
	if _, err := sessionIDFromRequest(req); err == nil {
		t.Fatal("repeated header should be rejected")
	}
}

func TestChatCompletionsRejectsInvalidSessionHeaderBeforeAnyWork(t *testing.T) {
	// The app has no DB/Redis: the handler must reject before touching them.
	app := &gatewayApp{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(sessionHeader, "bad session")
	req = req.WithContext(context.WithValue(req.Context(), identityContextKey, aliceIdentity))
	rec := httptest.NewRecorder()
	app.handleChatCompletions(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid session id") {
		t.Fatalf("expected 400 invalid session id, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestMeasurementSource(t *testing.T) {
	usage := usageSummary{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}

	stub := measurementFor(aliceIdentity, stubModel, "s1", "trace", cacheStatusMiss, usage, t0)
	if stub.Source != measurementGatewayEstimate {
		t.Errorf("stub provider must be an estimate, got %s", stub.Source)
	}

	real := measurementFor(aliceIdentity, groqModel, "s1", "trace", cacheStatusMiss, usage, t0)
	if real.Source != measurementProviderReported || *real.TotalTokens != 120 || *real.PromptTokens != 100 {
		t.Errorf("unexpected provider measurement %+v", real)
	}

	missing := measurementFor(aliceIdentity, groqModel, "s1", "trace", cacheStatusMiss, usageSummary{}, t0)
	if missing.Source != measurementUnavailable || missing.TotalTokens != nil {
		t.Errorf("zero usage must be unavailable without counts, got %+v", missing)
	}

	noTotal := measurementFor(aliceIdentity, groqModel, "s1", "trace", cacheStatusMiss, usageSummary{PromptTokens: 50, CompletionTokens: 5}, t0)
	if *noTotal.TotalTokens != 55 {
		t.Errorf("total should fall back to prompt+completion, got %d", *noTotal.TotalTokens)
	}

	hit := measurementFor(aliceIdentity, groqModel, "s1", "trace", cacheStatusHit, usage, t0)
	if hit.CacheStatus != cacheStatusHit || hit.Source != measurementProviderReported {
		t.Errorf("cache hit should keep the source and be flagged, got %+v", hit)
	}
}

func TestRecordContextUsageSkipsUntaggedRequestsAndNeverFails(t *testing.T) {
	store := &fakeUsageStore{}
	app := &gatewayApp{usageStore: store}
	usage := usageSummary{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}

	app.recordContextUsage(context.Background(), measurementFor(aliceIdentity, stubModel, "", "t", cacheStatusMiss, usage, t0))
	if len(store.measurements) != 0 {
		t.Fatal("requests without a session id must not be recorded")
	}

	store.err = errors.New("db down")
	app.recordContextUsage(context.Background(), measurementFor(aliceIdentity, stubModel, "s1", "t", cacheStatusMiss, usage, t0))
	// No panic and no return value: the chat response is unaffected.
}

func TestContextUsageEndpoint(t *testing.T) {
	store := &fakeUsageStore{windows: map[string]int{"groq-llama": 1000}}
	app := &gatewayApp{usageStore: store}
	record := func(identity identityContext, model modelRuntime, session string, usage usageSummary, cache string, at time.Time) {
		store.measurements = append(store.measurements, measurementFor(identity, model, session, "trace-"+at.Format("150405"), cache, usage, at))
	}

	t.Run("missing or invalid session parameter", func(t *testing.T) {
		if code, _, _ := usageRequest(t, app, aliceIdentity, ""); code != http.StatusBadRequest {
			t.Errorf("missing session: got %d", code)
		}
		if code, _, _ := usageRequest(t, app, aliceIdentity, "?session=bad%20id"); code != http.StatusBadRequest {
			t.Errorf("invalid session: got %d", code)
		}
	})

	t.Run("no measurement yet", func(t *testing.T) {
		code, body, _ := usageRequest(t, app, aliceIdentity, "?session=s1")
		if code != http.StatusOK || body.Status != usageStatusNoMeasurement || body.Measurement != nil || body.Percent != nil {
			t.Fatalf("got %d %+v", code, body)
		}
	})

	record(aliceIdentity, groqModel, "s1", usageSummary{PromptTokens: 300, CompletionTokens: 50, TotalTokens: 350}, cacheStatusMiss, t0)
	record(aliceIdentity, groqModel, "s1", usageSummary{PromptTokens: 800, CompletionTokens: 60, TotalTokens: 860}, cacheStatusMiss, t0.Add(time.Minute))

	t.Run("latest provider measurement with percent, never summed", func(t *testing.T) {
		code, body, _ := usageRequest(t, app, aliceIdentity, "?session=s1")
		if code != http.StatusOK || body.Status != usageStatusOK {
			t.Fatalf("got %d %+v", code, body)
		}
		m := body.Measurement
		if *m.ContextTokens != 860 || *m.PromptTokens != 800 || *m.CompletionTokens != 60 {
			t.Errorf("expected latest request (860), got %+v", m)
		}
		if *body.Percent != 86 || *body.ContextWindow != 1000 {
			t.Errorf("expected 86%% of 1000, got %v of %v", *body.Percent, *body.ContextWindow)
		}
		if m.Source != measurementProviderReported || m.CacheStatus != cacheStatusMiss || m.MeasuredAt != t0.Add(time.Minute).Format(time.RFC3339Nano) {
			t.Errorf("unexpected measurement metadata %+v", m)
		}
		if body.RetrievedAt == "" {
			t.Error("retrievedAt must be set")
		}
	})

	t.Run("another user cannot see the session", func(t *testing.T) {
		code, body, _ := usageRequest(t, app, bobIdentity, "?session=s1")
		if code != http.StatusOK || body.Status != usageStatusNoMeasurement || body.Measurement != nil {
			t.Fatalf("bob must not see alice's session, got %+v", body)
		}
		otherTenant := identityContext{TenantID: "tenant-2", Actor: "alice"}
		_, body, _ = usageRequest(t, app, otherTenant, "?session=s1")
		if body.Status != usageStatusNoMeasurement {
			t.Fatalf("same actor name in another tenant must not see the session, got %+v", body)
		}
	})

	t.Run("cached response is reported explicitly as a hit", func(t *testing.T) {
		record(aliceIdentity, groqModel, "s1", usageSummary{PromptTokens: 800, CompletionTokens: 60, TotalTokens: 860}, cacheStatusHit, t0.Add(2*time.Minute))
		_, body, _ := usageRequest(t, app, aliceIdentity, "?session=s1")
		if body.Measurement.CacheStatus != cacheStatusHit || body.Measurement.MeasuredAt != t0.Add(2*time.Minute).Format(time.RFC3339Nano) {
			t.Fatalf("expected latest cache hit, got %+v", body.Measurement)
		}
		if !strings.Contains(body.Message, "cache") {
			t.Errorf("message should explain the cache hit: %q", body.Message)
		}
	})

	t.Run("unavailable usage is not replaced by an older measurement", func(t *testing.T) {
		record(aliceIdentity, groqModel, "s1", usageSummary{}, cacheStatusMiss, t0.Add(3*time.Minute))
		_, body, _ := usageRequest(t, app, aliceIdentity, "?session=s1")
		if body.Status != usageStatusUnavailable || body.Percent != nil || body.Measurement.ContextTokens != nil {
			t.Fatalf("expected unavailable with no numbers, got %+v", body)
		}
	})

	t.Run("unknown context window gives tokens but no percent", func(t *testing.T) {
		record(aliceIdentity, stubModel, "s2", usageSummary{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50}, cacheStatusMiss, t0)
		_, body, _ := usageRequest(t, app, aliceIdentity, "?session=s2")
		if body.Status != usageStatusUnknownContextWindow || body.Percent != nil || body.ContextWindow != nil {
			t.Fatalf("expected no percent, got %+v", body)
		}
		if *body.Measurement.ContextTokens != 50 || body.Measurement.Source != measurementGatewayEstimate {
			t.Fatalf("expected estimated tokens, got %+v", body.Measurement)
		}
	})

	t.Run("estimate with known window is labelled as an estimate", func(t *testing.T) {
		store.windows["demo-model"] = 200
		_, body, _ := usageRequest(t, app, aliceIdentity, "?session=s2")
		if body.Status != usageStatusOK || *body.Percent != 25 || !strings.Contains(body.Message, "estimate") {
			t.Fatalf("got %+v", body)
		}
	})

	t.Run("missing migration returns 503 with a hint", func(t *testing.T) {
		broken := &gatewayApp{usageStore: &fakeUsageStore{err: errUsageStorageUnavailable}}
		code, _, failure := usageRequest(t, broken, aliceIdentity, "?session=s1")
		if code != http.StatusServiceUnavailable || !strings.Contains(failure["error"], "0006") {
			t.Fatalf("got %d %v", code, failure)
		}
	})

	t.Run("other store errors are hidden behind a generic 500", func(t *testing.T) {
		broken := &gatewayApp{usageStore: &fakeUsageStore{err: errors.New("connection refused to 10.0.0.5")}}
		code, _, failure := usageRequest(t, broken, aliceIdentity, "?session=s1")
		if code != http.StatusInternalServerError || strings.Contains(failure["error"], "10.0.0.5") {
			t.Fatalf("got %d %v", code, failure)
		}
	})

	t.Run("only GET is routed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/context/usage?session=s1", nil)
		req = req.WithContext(context.WithValue(req.Context(), identityContextKey, aliceIdentity))
		rec := httptest.NewRecorder()
		app.handleProtected(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("POST should not be routed, got %d", rec.Code)
		}
	})
}

func TestContextUsageRequiresAuthentication(t *testing.T) {
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-API-Key") {
		case "alice-key":
			writeJSON(w, http.StatusOK, map[string]any{"identityContext": aliceIdentity})
		case "bob-key":
			writeJSON(w, http.StatusOK, map[string]any{"identityContext": bobIdentity})
		default:
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
		}
	}))
	defer resolver.Close()
	t.Setenv("AUTH_RESOLVER_URL", resolver.URL)

	store := &fakeUsageStore{windows: map[string]int{"groq-llama": 1000}}
	store.measurements = append(store.measurements, measurementFor(aliceIdentity, groqModel, "s1", "t", cacheStatusMiss, usageSummary{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, t0))
	app := &gatewayApp{usageStore: store}
	handler := withIdentityResolver(http.HandlerFunc(app.handleProtected))

	call := func(key string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/context/usage?session=s1", nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, _ := call(""); code != http.StatusUnauthorized {
		t.Errorf("no credentials: expected 401, got %d", code)
	}
	if code, _ := call("wrong-key"); code != http.StatusUnauthorized {
		t.Errorf("bad credentials: expected 401, got %d", code)
	}
	if code, body := call("alice-key"); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("alice: expected ok, got %d %s", code, body)
	}
	if code, body := call("bob-key"); code != http.StatusOK || !strings.Contains(body, `"status":"no_measurement"`) {
		t.Errorf("bob: expected no_measurement, got %d %s", code, body)
	}
}
