package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Context usage for SUEZ Context Guardian.
//
// A client may tag /v1/chat/completions calls with X-SUEZ-Session-Id. For each
// tagged request the gateway records one measurement: the token counts of that
// request (prompt + completion). Because chat clients resend the whole
// conversation on every call, the latest measurement is the current context
// occupancy; measurements are never summed across requests.

const sessionHeader = "X-SUEZ-Session-Id"

const (
	measurementProviderReported = "provider_reported"
	measurementGatewayEstimate  = "gateway_estimate"
	measurementUnavailable      = "unavailable"

	cacheStatusMiss = "miss"
	cacheStatusHit  = "hit"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var errUsageStorageUnavailable = errors.New("context usage storage is not available; apply migration 0006_context_guardian.sql")

type contextMeasurement struct {
	TenantID         string
	Actor            string
	SessionID        string
	Model            string
	Provider         string
	Source           string
	CacheStatus      string
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
	TraceID          string
	MeasuredAt       time.Time
}

type contextUsageStore interface {
	RecordMeasurement(ctx context.Context, m contextMeasurement) error
	// LatestMeasurement returns nil when the caller has no measurement for the session.
	LatestMeasurement(ctx context.Context, tenantID, actor, sessionID string) (*contextMeasurement, error)
	// ContextWindow returns nil when the model is unknown or has no context window configured.
	ContextWindow(ctx context.Context, tenantID, model string) (*int, error)
}

// sessionIDFromRequest returns ("", nil) when the header is absent so existing
// clients are unaffected; a present but malformed header is an error.
func sessionIDFromRequest(r *http.Request) (string, error) {
	values := r.Header.Values(sessionHeader)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > 1 {
		return "", errors.New("X-SUEZ-Session-Id must be sent once")
	}
	return validateSessionID(values[0])
}

func validateSessionID(raw string) (string, error) {
	sessionID := strings.TrimSpace(raw)
	if !sessionIDPattern.MatchString(sessionID) {
		return "", errors.New("invalid session id: use 1-128 characters from A-Z a-z 0-9 . _ : - starting with a letter or digit")
	}
	return sessionID, nil
}

// measurementFor classifies the usage in a chat response. Only real upstream
// providers can produce provider_reported counts; the stub provider's counts
// are the gateway's own character-based estimate.
func measurementFor(identity identityContext, model modelRuntime, sessionID, traceID, cacheStatus string, usage usageSummary, now time.Time) contextMeasurement {
	m := contextMeasurement{
		TenantID:    identity.TenantID,
		Actor:       identity.Actor,
		SessionID:   sessionID,
		Model:       model.ExternalName,
		Provider:    model.Provider,
		CacheStatus: cacheStatus,
		TraceID:     traceID,
		MeasuredAt:  now.UTC(),
	}
	if usage.TotalTokens <= 0 && usage.PromptTokens <= 0 {
		m.Source = measurementUnavailable
		return m
	}
	prompt, completion := usage.PromptTokens, usage.CompletionTokens
	total := usage.TotalTokens
	if total <= 0 {
		total = prompt + completion
	}
	m.PromptTokens, m.CompletionTokens, m.TotalTokens = &prompt, &completion, &total
	if strings.EqualFold(model.Provider, "stub") {
		m.Source = measurementGatewayEstimate
	} else {
		m.Source = measurementProviderReported
	}
	return m
}

// recordContextUsage never fails the chat request: usage tracking is best effort.
func (a *gatewayApp) recordContextUsage(ctx context.Context, m contextMeasurement) {
	if a.usageStore == nil || m.SessionID == "" {
		return
	}
	if err := a.usageStore.RecordMeasurement(ctx, m); err != nil {
		log.Printf("context usage not recorded for trace %s: %v", m.TraceID, err)
	}
}

type contextUsageMeasurementJSON struct {
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	Source           string `json:"source"`
	CacheStatus      string `json:"cacheStatus"`
	PromptTokens     *int   `json:"promptTokens"`
	CompletionTokens *int   `json:"completionTokens"`
	ContextTokens    *int   `json:"contextTokens"`
	TraceID          string `json:"traceId,omitempty"`
	MeasuredAt       string `json:"measuredAt"`
}

type contextUsageResponse struct {
	SessionID     string                       `json:"sessionId"`
	Status        string                       `json:"status"`
	Message       string                       `json:"message"`
	Measurement   *contextUsageMeasurementJSON `json:"measurement"`
	ContextWindow *int                         `json:"contextWindow"`
	Percent       *float64                     `json:"percent"`
	RetrievedAt   string                       `json:"retrievedAt"`
}

// Status values returned by GET /v1/context/usage.
const (
	usageStatusOK                   = "ok"
	usageStatusNoMeasurement        = "no_measurement"
	usageStatusUnavailable          = "unavailable"
	usageStatusUnknownContextWindow = "unknown_context_window"
)

func (a *gatewayApp) handleContextUsage(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	sessionID, err := validateSessionID(r.URL.Query().Get("session"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if a.usageStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errUsageStorageUnavailable.Error()})
		return
	}

	response := contextUsageResponse{
		SessionID:   sessionID,
		RetrievedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	latest, err := a.usageStore.LatestMeasurement(r.Context(), identity.TenantID, identity.Actor, sessionID)
	if err != nil {
		writeUsageStoreError(w, err)
		return
	}
	if latest == nil {
		response.Status = usageStatusNoMeasurement
		response.Message = "No chat requests with this session id have been recorded for the current caller."
		writeJSON(w, http.StatusOK, response)
		return
	}

	response.Measurement = &contextUsageMeasurementJSON{
		Model:            latest.Model,
		Provider:         latest.Provider,
		Source:           latest.Source,
		CacheStatus:      latest.CacheStatus,
		PromptTokens:     latest.PromptTokens,
		CompletionTokens: latest.CompletionTokens,
		ContextTokens:    latest.TotalTokens,
		TraceID:          latest.TraceID,
		MeasuredAt:       latest.MeasuredAt.UTC().Format(time.RFC3339Nano),
	}

	if latest.Source == measurementUnavailable || latest.TotalTokens == nil {
		response.Status = usageStatusUnavailable
		response.Message = "The latest request for this session did not report token usage, so the current context size is unknown."
		writeJSON(w, http.StatusOK, response)
		return
	}

	window, err := a.usageStore.ContextWindow(r.Context(), identity.TenantID, latest.Model)
	if err != nil {
		writeUsageStoreError(w, err)
		return
	}
	response.ContextWindow = window
	if window == nil || *window <= 0 {
		response.ContextWindow = nil
		response.Status = usageStatusUnknownContextWindow
		response.Message = "Token counts are known but the model has no context window configured, so no percentage is reported."
		writeJSON(w, http.StatusOK, response)
		return
	}

	percent := math.Round(float64(*latest.TotalTokens)/float64(*window)*100*100) / 100
	response.Percent = &percent
	response.Status = usageStatusOK
	switch {
	case latest.Source == measurementGatewayEstimate:
		response.Message = "Token counts are a gateway estimate (stub provider), not provider-reported."
	case latest.CacheStatus == cacheStatusHit:
		response.Message = "Latest request was served from cache; token counts are those reported for the identical cached request."
	default:
		response.Message = "Token counts were reported by the provider for the latest request."
	}
	writeJSON(w, http.StatusOK, response)
}

func writeUsageStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUsageStorageUnavailable) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("context usage lookup failed: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "context usage lookup failed"})
}

type postgresUsageStore struct {
	db *sql.DB
}

// mapUsageStoreError turns "relation/column does not exist" into a clear
// migration hint instead of a generic 500.
func mapUsageStoreError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703") {
		return errUsageStorageUnavailable
	}
	return err
}

func (s *postgresUsageStore) RecordMeasurement(ctx context.Context, m contextMeasurement) error {
	_, err := s.db.ExecContext(ctx, `
		insert into context_usage_measurements
			(tenant_id, actor, session_id, model_external_name, provider, measurement_source, cache_status,
			 prompt_tokens, completion_tokens, total_tokens, trace_id, measured_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), $12)
	`, m.TenantID, m.Actor, m.SessionID, m.Model, m.Provider, m.Source, m.CacheStatus,
		m.PromptTokens, m.CompletionTokens, m.TotalTokens, m.TraceID, m.MeasuredAt)
	return mapUsageStoreError(err)
}

func (s *postgresUsageStore) LatestMeasurement(ctx context.Context, tenantID, actor, sessionID string) (*contextMeasurement, error) {
	row := s.db.QueryRowContext(ctx, `
		select model_external_name, provider, measurement_source, cache_status,
		       prompt_tokens, completion_tokens, total_tokens, coalesce(trace_id, ''), measured_at
		from context_usage_measurements
		where tenant_id = $1 and actor = $2 and session_id = $3
		order by measured_at desc
		limit 1
	`, tenantID, actor, sessionID)
	m := contextMeasurement{TenantID: tenantID, Actor: actor, SessionID: sessionID}
	var prompt, completion, total sql.NullInt64
	err := row.Scan(&m.Model, &m.Provider, &m.Source, &m.CacheStatus, &prompt, &completion, &total, &m.TraceID, &m.MeasuredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapUsageStoreError(err)
	}
	m.PromptTokens, m.CompletionTokens, m.TotalTokens = nullIntPtr(prompt), nullIntPtr(completion), nullIntPtr(total)
	return &m, nil
}

func (s *postgresUsageStore) ContextWindow(ctx context.Context, tenantID, model string) (*int, error) {
	var window sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		select context_window from models where tenant_id = $1 and external_name = $2 limit 1
	`, tenantID, model).Scan(&window)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapUsageStoreError(err)
	}
	return nullIntPtr(window), nil
}

func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}
