package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Context-window protection.
//
// For every chat call the gateway knows the model's context window (models
// table) and the tenant's thresholds (context_policies, per model or "*").
// Before the provider is called it checks a conservative estimate; after the
// call it uses the provider-reported token count when there is one.

type contextThresholds struct {
	Notice     float64 `json:"noticePercent"`
	Checkpoint float64 `json:"checkpointPercent"`
	Warning    float64 `json:"warningPercent"`
	Block      float64 `json:"blockPercent"`
	HardBlock  bool    `json:"hardBlock"`
}

var defaultContextThresholds = contextThresholds{Notice: 80, Checkpoint: 85, Warning: 95, Block: 100}

const (
	contextOK         = "ok"
	contextNotice     = "notice"
	contextCheckpoint = "checkpoint"
	contextWarning    = "warning"
	contextLimit      = "limit"
	contextUnknown    = "unknown"
)

func (t contextThresholds) classify(percent float64) string {
	switch {
	case percent >= t.Block:
		return contextLimit
	case percent >= t.Warning:
		return contextWarning
	case percent >= t.Checkpoint:
		return contextCheckpoint
	case percent >= t.Notice:
		return contextNotice
	}
	return contextOK
}

// needsCheckpoint reports whether the status is at or past the point where a
// checkpoint draft should exist.
func needsCheckpoint(status string) bool {
	return status == contextCheckpoint || status == contextWarning || status == contextLimit
}

type contextState struct {
	Window     int               `json:"contextWindow"`
	Tokens     int               `json:"contextTokens"`
	Percent    float64           `json:"percent"`
	Status     string            `json:"status"`
	Source     string            `json:"source"`
	Thresholds contextThresholds `json:"thresholds"`
}

func contextStateFor(tokens, window int, source string, thresholds contextThresholds) contextState {
	state := contextState{Window: window, Tokens: tokens, Source: source, Thresholds: thresholds, Status: contextUnknown}
	if window > 0 {
		state.Percent = math.Round(float64(tokens)/float64(window)*10000) / 100
		state.Status = thresholds.classify(state.Percent)
	}
	return state
}

func (s contextState) setHeaders(w http.ResponseWriter) {
	w.Header().Set("X-SUEZ-Context-Status", s.Status)
	if s.Window > 0 {
		w.Header().Set("X-SUEZ-Context-Percent", fmt.Sprintf("%.2f", s.Percent))
		w.Header().Set("X-SUEZ-Context-Window", fmt.Sprint(s.Window))
		w.Header().Set("X-SUEZ-Context-Source", s.Source)
	}
}

// loadContextThresholds returns the tenant's thresholds for the model: the
// model-specific row, else the tenant default ("*"), else the built-in
// defaults. A missing table (migration 0008 not applied) also yields the
// defaults.
func (a *gatewayApp) loadContextThresholds(ctx context.Context, tenantID, model string) contextThresholds {
	if a.db == nil {
		return defaultContextThresholds
	}
	thresholds := defaultContextThresholds
	err := a.db.QueryRowContext(ctx, `
		select notice_percent, checkpoint_percent, warning_percent, block_percent, hard_block
		from context_policies
		where tenant_id = $1 and model_external_name in ($2, '*')
		order by (model_external_name = $2) desc limit 1`, tenantID, model).Scan(
		&thresholds.Notice, &thresholds.Checkpoint, &thresholds.Warning, &thresholds.Block, &thresholds.HardBlock)
	if err != nil {
		return defaultContextThresholds
	}
	return thresholds
}

func (a *gatewayApp) contextWindow(ctx context.Context, tenantID, model string) int {
	if a.usageStore == nil {
		return 0
	}
	window, err := a.usageStore.ContextWindow(ctx, tenantID, model)
	if err != nil || window == nil {
		return 0
	}
	return *window
}

// policyStamp changes whenever the tenant's published policy set changes. It
// is part of the chat cache key so a response cached under one policy set is
// never served under another.
func (a *gatewayApp) policyStamp(ctx context.Context, tenantID string) (string, error) {
	if a.db == nil {
		return "", errors.New("no database")
	}
	var stamp sql.NullString
	err := a.db.QueryRowContext(ctx, `
		select count(*)::text || ':' || coalesce(extract(epoch from max(updated_at))::text, '0')
		from policies where tenant_id = $1 and status = 'published' and enabled = true`, tenantID).Scan(&stamp)
	return stamp.String, err
}

// ---- control-plane internal API (memory) ----

type controlPlaneError struct {
	status  int
	message string
}

func (e *controlPlaneError) Error() string { return e.message }

func (a *gatewayApp) callControlPlane(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	base := strings.TrimRight(env("CONTROL_PLANE_INTERNAL_URL", "http://control-plane:8081"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SUEZ-Internal-Token", a.cfg.InternalToken)
	resp, err := authResolverClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		return &controlPlaneError{status: resp.StatusCode, message: firstNonEmpty(failure.Error, "memory service error")}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

type memoryCaller struct {
	TenantID string   `json:"tenantId"`
	Actor    string   `json:"actor"`
	Scopes   []string `json:"scopes"`
}

func callerOf(identity identityContext) memoryCaller {
	return memoryCaller{TenantID: identity.TenantID, Actor: identity.Actor, Scopes: identity.Scopes}
}

// captureToMemory hands the conversation to SUEZ Memory. The control plane
// decides whether anything is stored (tenant setting and user opt-out) and
// redacts likely secrets before it persists anything.
func (a *gatewayApp) captureToMemory(ctx context.Context, identity identityContext, model modelRuntime, sessionID, traceID string, messages []chatMessage, reply string) {
	if sessionID == "" || a.cfg.InternalToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payload := struct {
		memoryCaller
		ExternalSessionID string    `json:"externalSessionId"`
		Model             string    `json:"model"`
		Provider          string    `json:"provider"`
		TraceID           string    `json:"traceId"`
		Messages          []message `json:"messages"`
		Response          *message  `json:"response,omitempty"`
	}{memoryCaller: callerOf(identity), ExternalSessionID: sessionID, Model: model.ExternalName, Provider: model.Provider, TraceID: traceID}
	for _, m := range messages {
		payload.Messages = append(payload.Messages, message{Role: m.Role, Content: m.Content})
	}
	if reply != "" {
		payload.Response = &message{Role: "assistant", Content: reply}
	}
	if err := a.callControlPlane(ctx, "/internal/memory/capture", payload, nil); err != nil {
		logWarn("memory capture failed", traceID, err)
	}
}

// draftCheckpoint asks for a threshold checkpoint draft and returns its id,
// or "" when none exists (for example when capture is off for the tenant).
func (a *gatewayApp) draftCheckpoint(ctx context.Context, identity identityContext, sessionID, traceID string, percent float64) string {
	if sessionID == "" || a.cfg.InternalToken == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var out struct {
		CheckpointID string `json:"checkpointId"`
	}
	err := a.callControlPlane(ctx, "/internal/memory/checkpoint-draft", struct {
		memoryCaller
		ExternalSessionID string  `json:"externalSessionId"`
		ContextPercent    float64 `json:"contextPercent"`
	}{callerOf(identity), sessionID, percent}, &out)
	if err != nil {
		logWarn("checkpoint draft failed", traceID, err)
		return ""
	}
	return out.CheckpointID
}

type memoryInjection struct {
	CheckpointID string   `json:"checkpointId"`
	EvidenceIDs  []string `json:"evidenceIds"`
	Omitted      []string `json:"omittedEvidenceIds"`
	TokenBudget  int      `json:"tokenBudget"`
	TokensUsed   int      `json:"tokensUsed"`
	ContextText  string   `json:"contextText"`
}

func (a *gatewayApp) fetchInjection(ctx context.Context, identity identityContext, sessionID, traceID, checkpointID string, budget int) (memoryInjection, error) {
	var out memoryInjection
	err := a.callControlPlane(ctx, "/internal/memory/injection", struct {
		memoryCaller
		CheckpointID      string `json:"checkpointId"`
		TokenBudget       int    `json:"tokenBudget"`
		TraceID           string `json:"traceId"`
		ExternalSessionID string `json:"externalSessionId"`
	}{callerOf(identity), checkpointID, budget, traceID, sessionID}, &out)
	return out, err
}
