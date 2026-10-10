//go:build integration

// Integration test for context usage against real PostgreSQL (all migrations
// and seeds, including 0006) and Redis, through the real chat handler and the
// free stub provider. OPA and the auth resolver are replaced by local stubs.
//
//	DATABASE_URL=... REDIS_URL=... go test -tags integration ./...

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const demoTenantID = "00000000-0000-0000-0000-000000000001"

func TestIntegrationContextUsage(t *testing.T) {
	databaseURL, redisURL := os.Getenv("DATABASE_URL"), os.Getenv("REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("DATABASE_URL and REDIS_URL are required")
	}
	ctx := context.Background()

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"allow": true}})
	}))
	defer opa.Close()
	t.Setenv("OPA_DECISION_URL", opa.URL)

	identities := map[string]identityContext{
		"admin-key": {TenantID: demoTenantID, TenantSlug: "demo", Actor: "Bootstrap Admin Key", Roles: []string{"admin"}},
		"other-key": {TenantID: demoTenantID, TenantSlug: "demo", Actor: "someone-else@example.com", Roles: []string{"user"}},
	}
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := identities[r.Header.Get("X-API-Key")]
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"identityContext": identity})
	}))
	defer resolver.Close()
	t.Setenv("AUTH_RESOLVER_URL", resolver.URL)

	app := &gatewayApp{db: db, redis: redisClient, httpClient: &http.Client{Timeout: 10 * time.Second}, usageStore: &postgresUsageStore{db: db}}
	handler := withIdentityResolver(http.HandlerFunc(app.handleProtected))

	setWindow := func(window any) {
		if _, err := db.ExecContext(ctx, `update models set context_window = $1 where tenant_id = $2 and external_name = 'demo-model'`, window, demoTenantID); err != nil {
			t.Fatal(err)
		}
	}
	setWindow(nil)
	defer setWindow(nil)

	run := newTraceID() // unique per run so Redis cache entries from earlier runs never match
	session := "it-" + run

	chat := func(key, sessionID string, messages []chatMessage) (*httptest.ResponseRecorder, chatCompletionResponse) {
		body, _ := json.Marshal(chatCompletionRequest{Model: "demo-model", Messages: messages})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("X-API-Key", key)
		if sessionID != "" {
			req.Header.Set(sessionHeader, sessionID)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var resp chatCompletionResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec, resp
	}
	usage := func(key, sessionID string) contextUsageResponse {
		req := httptest.NewRequest(http.MethodGet, "/v1/context/usage?session="+sessionID, nil)
		req.Header.Set("X-API-Key", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("usage: %d %s", rec.Code, rec.Body.String())
		}
		var out contextUsageResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rowsForTrace := func(traceID string) int {
		var n int
		if err := db.QueryRowContext(ctx, `select count(*) from context_usage_measurements where trace_id = $1`, traceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := []chatMessage{{Role: "user", Content: "Integration " + run + ": first question about the SUEZ gateway."}}

	// 1. Without the header nothing changes and nothing is recorded.
	rec, _ := chat("admin-key", "", []chatMessage{{Role: "user", Content: "untagged " + run}})
	if rec.Code != http.StatusOK {
		t.Fatalf("untagged chat failed: %d %s", rec.Code, rec.Body.String())
	}
	if n := rowsForTrace(rec.Header().Get("X-SUEZ-Trace-Id")); n != 0 {
		t.Fatalf("untagged request recorded %d measurement(s)", n)
	}

	// 2. Invalid header is rejected and not recorded.
	rec, _ = chat("admin-key", "bad session", first)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid session header: expected 400, got %d", rec.Code)
	}

	// 3. Tagged request: estimate recorded, but no percent while the window is unknown.
	rec, resp1 := chat("admin-key", session, first)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Suez-Cache") != "MISS" {
		t.Fatalf("tagged chat: %d cache=%s %s", rec.Code, rec.Header().Get("X-Suez-Cache"), rec.Body.String())
	}
	trace1 := rec.Header().Get("X-SUEZ-Trace-Id")
	u := usage("admin-key", session)
	if u.Status != usageStatusUnknownContextWindow || u.Percent != nil {
		t.Fatalf("expected unknown_context_window, got %+v", u)
	}
	if u.Measurement.Source != measurementGatewayEstimate || *u.Measurement.ContextTokens != resp1.Usage.TotalTokens || u.Measurement.TraceID != trace1 {
		t.Fatalf("unexpected measurement %+v (response usage %+v)", u.Measurement, resp1.Usage)
	}

	// 4. Session id is added to the existing audit metadata.
	var auditSession string
	if err := db.QueryRowContext(ctx, `select coalesce(metadata->>'sessionId', '') from audit_log where trace_id = $1`, trace1).Scan(&auditSession); err != nil || auditSession != session {
		t.Fatalf("audit metadata sessionId = %q (%v)", auditSession, err)
	}

	// 5. With a context window the percentage is reported.
	setWindow(1000)
	u = usage("admin-key", session)
	if u.Status != usageStatusOK || u.Percent == nil || *u.ContextWindow != 1000 {
		t.Fatalf("expected ok with percent, got %+v", u)
	}
	wantPercent := float64(resp1.Usage.TotalTokens) / 10
	if *u.Percent != wantPercent {
		t.Fatalf("percent %v, want %v", *u.Percent, wantPercent)
	}

	// 6. Identical request is a cache hit and is recorded explicitly as such.
	rec, _ = chat("admin-key", session, first)
	if rec.Header().Get("X-Suez-Cache") != "HIT" {
		t.Fatalf("expected cache HIT, got %s", rec.Header().Get("X-Suez-Cache"))
	}
	u = usage("admin-key", session)
	if u.Measurement.CacheStatus != cacheStatusHit || u.Measurement.TraceID != rec.Header().Get("X-SUEZ-Trace-Id") {
		t.Fatalf("expected the cache hit to be the latest measurement, got %+v", u.Measurement)
	}

	// 7. Longer conversation: the latest request is reported, not a running sum.
	longer := append(append([]chatMessage{}, first...),
		chatMessage{Role: "assistant", Content: resp1.Choices[0].Message.Content},
		chatMessage{Role: "user", Content: "Follow-up " + run + ": now explain how sessions are tracked in more detail please."})
	rec, resp2 := chat("admin-key", session, longer)
	if rec.Code != http.StatusOK {
		t.Fatalf("longer chat: %d", rec.Code)
	}
	u = usage("admin-key", session)
	if *u.Measurement.ContextTokens != resp2.Usage.TotalTokens {
		t.Fatalf("expected latest total %d, got %d", resp2.Usage.TotalTokens, *u.Measurement.ContextTokens)
	}
	var sum int
	if err := db.QueryRowContext(ctx, `select sum(total_tokens) from context_usage_measurements where session_id = $1`, session).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum == *u.Measurement.ContextTokens {
		t.Fatalf("reported value equals the running sum (%d); expected the latest request only", sum)
	}

	// 8. Another user in the same tenant cannot read this session.
	if other := usage("other-key", session); other.Status != usageStatusNoMeasurement || other.Measurement != nil {
		t.Fatalf("other user saw the session: %+v", other)
	}

	// 9. Unauthenticated requests are rejected by the existing resolver.
	req := httptest.NewRequest(http.MethodGet, "/v1/context/usage?session="+session, nil)
	recNoAuth := httptest.NewRecorder()
	handler.ServeHTTP(recNoAuth, req)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", recNoAuth.Code)
	}
}
