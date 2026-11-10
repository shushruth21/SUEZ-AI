package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func logWarn(message, traceID string, err error) {
	slog.Warn(message, "traceId", traceID, "error", err.Error())
}

// chatCacheKey covers everything that can change the answer: tenant, the
// tenant's published policy set, model, every message (system, developer,
// user, assistant, tool) and every sampling or output option. Unset options
// hash differently from explicit zeros. The key is a SHA-256 digest, so no
// request content appears in Redis key names.
func chatCacheKey(tenantID, policyStamp, model string, req chatCompletionRequest) string {
	keyed := req
	keyed.Model, keyed.Stream, keyed.Suez = model, false, nil
	payload, _ := json.Marshal(struct {
		Policy  string                `json:"policy"`
		Request chatCompletionRequest `json:"request"`
	}{policyStamp, keyed})
	sum := sha256.Sum256(payload)
	return chatCacheTenantPrefix(tenantID) + hex.EncodeToString(sum[:])
}

func writeProviderError(w http.ResponseWriter, err error, traceID string) {
	var typed *providerError
	if !errors.As(err, &typed) {
		typed = &providerError{Code: "internal_error", Message: "internal server error", Status: http.StatusInternalServerError}
	}
	if typed.RetryAfter != "" {
		w.Header().Set("Retry-After", typed.RetryAfter)
	}
	w.Header().Set("X-SUEZ-Trace-Id", traceID)
	status := typed.Status
	if status == 499 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": typed.Message, "code": typed.Code, "traceId": traceID})
}

func providerErrorCode(err error) (string, int) {
	var typed *providerError
	if errors.As(err, &typed) {
		return typed.Code, typed.UpstreamStatus
	}
	return "internal_error", 0
}

func (a *gatewayApp) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity := mustIdentity(ctx)
	traceID := newTraceID()
	w.Header().Set("X-SUEZ-Trace-Id", traceID)

	// Optional: clients that send no session header behave exactly as before.
	sessionID, err := sessionIDFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := validateChatCompletionRequest(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	audit := func(decision string, metadata map[string]any) {
		if sessionID != "" {
			metadata["sessionId"] = sessionID
		}
		a.auditGatewayEvent(ctx, identity, "model.invoke", "model", strings.TrimSpace(req.Model), decision, traceID, metadata)
	}
	if !allowedByList(identity.AllowedModels, strings.TrimSpace(req.Model)) {
		audit("deny", map[string]any{"reason": "model is outside the api key's allowed models"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this api key is not allowed to use this model"})
		return
	}
	_ = a.redis.Incr(ctx, statsKey(identity.TenantID, "chat_requests")).Err()

	model, err := a.lookupModel(ctx, identity.TenantID, strings.TrimSpace(req.Model))
	if err != nil {
		audit("error", map[string]any{"reason": "model lookup failed"})
		writeGatewayLookupError(w, err, http.StatusNotFound, "model not found")
		return
	}
	if !model.Enabled {
		audit("deny", map[string]any{"reason": "model disabled"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "model is disabled"})
		return
	}
	if err := a.ensureGrant(ctx, identity, "model", model.ExternalName); err != nil {
		audit("deny", map[string]any{"reason": "no active grant"})
		writeGatewayLookupError(w, err, http.StatusForbidden, "no active model grant")
		return
	}

	// Carrying an earlier checkpoint into this request is never implicit: the
	// caller must name it, policy must allow it, and it is audited.
	var injection *memoryInjection
	if req.Suez != nil && req.Suez.Memory != nil {
		injected, status, message := a.injectMemory(r, identity, &req, sessionID, traceID)
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": message, "code": "memory_injection_refused", "traceId": traceID})
			return
		}
		injection = injected
		w.Header().Set("X-SUEZ-Memory-Injected", fmt.Sprintf("checkpoint=%s; evidence=%d; tokens=%d", injected.CheckpointID, len(injected.EvidenceIDs), injected.TokensUsed))
	}

	promptEstimate := estimateChatTokens(req.Messages, 0)
	estimatedTokens := promptEstimate + req.completionLimit()
	policy, err := a.evaluateOPA(ctx, "chat.completions", identity, map[string]any{
		"type": "model", "name": model.ExternalName, "provider": model.Provider, "enabled": model.Enabled,
		"maxRpm": model.MaxRPM, "maxTpm": model.MaxTPM, "riskTier": "standard", "upstream": model.UpstreamModel,
		"traceId": traceID, "estimated": estimatedTokens,
	}, req)
	if err != nil {
		logWarn("policy evaluation failed", traceID, err)
		audit("error", map[string]any{"reason": "policy evaluation failed"})
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "policy service unavailable", "traceId": traceID})
		return
	}
	if !policy.Allow {
		audit("deny", map[string]any{"policy": policy})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "policy denied model invocation", "policy": policy})
		return
	}

	// Context check before the provider is called, on a conservative estimate.
	thresholds := a.loadContextThresholds(ctx, identity.TenantID, model.ExternalName)
	window := a.contextWindow(ctx, identity.TenantID, model.ExternalName)
	pre := contextStateFor(estimatedTokens, window, measurementGatewayEstimate, thresholds)
	if pre.Status == contextLimit && thresholds.HardBlock {
		checkpointID := a.draftCheckpoint(ctx, identity, sessionID, traceID, pre.Percent)
		audit("deny", map[string]any{"reason": "context limit", "contextPercent": pre.Percent, "contextWindow": window, "checkpointId": checkpointID})
		pre.setHeaders(w)
		body := map[string]any{
			"error": "this request would exceed the model's context limit; continue in a new session from a checkpoint",
			"code":  "context_limit", "traceId": traceID, "context": pre,
		}
		if checkpointID != "" {
			w.Header().Set("X-SUEZ-Checkpoint-Draft", checkpointID)
			body["checkpointDraftId"] = checkpointID
		}
		writeJSON(w, http.StatusConflict, body)
		return
	}

	if err := a.enforceQuota(ctx, identity, model.ExternalName, model.MaxRPM, model.MaxTPM, estimatedTokens); err != nil {
		if errors.Is(err, errQuotaBackendUnavailable) {
			logWarn("quota backend unavailable", traceID, err)
			audit("error", map[string]any{"reason": "quota backend unavailable"})
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "quota service unavailable"})
			return
		}
		audit("deny", map[string]any{"reason": err.Error(), "estimatedTokens": estimatedTokens})
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}

	call := &chatCall{app: a, w: w, r: r, identity: identity, model: model, req: req, sessionID: sessionID, traceID: traceID,
		estimated: estimatedTokens, thresholds: thresholds, window: window, injection: injection, audit: audit}
	// A response is only cached or served from cache when the policy stamp is
	// known and nothing request-specific (memory injection) was added.
	if stamp, err := a.policyStamp(ctx, identity.TenantID); err == nil && injection == nil {
		call.cacheKey = chatCacheKey(identity.TenantID, stamp, model.ExternalName, req)
	}
	if call.cacheKey != "" {
		if cached, err := a.getCachedChatResponse(ctx, call.cacheKey); err == nil && cached != nil && len(cached.Choices) > 0 {
			_ = a.redis.Incr(ctx, statsKey(identity.TenantID, "cache_hits")).Err()
			call.finish(*cached, cacheStatusHit, true)
			return
		}
	}
	if req.Stream {
		call.stream()
		return
	}
	resp, err := a.executeChatProvider(ctx, model, req, traceID)
	if err != nil {
		code, upstreamStatus := providerErrorCode(err)
		audit("error", map[string]any{"provider": model.Provider, "code": code, "upstreamStatus": upstreamStatus})
		writeProviderError(w, err, traceID)
		return
	}
	if call.cacheKey != "" {
		_ = a.setCachedChatResponse(ctx, call.cacheKey, resp)
	}
	call.finish(resp, cacheStatusMiss, false)
}

// chatCall carries one authorized chat request through its response.
type chatCall struct {
	app        *gatewayApp
	w          http.ResponseWriter
	r          *http.Request
	identity   identityContext
	model      modelRuntime
	req        chatCompletionRequest
	sessionID  string
	traceID    string
	cacheKey   string
	estimated  int
	thresholds contextThresholds
	window     int
	injection  *memoryInjection
	audit      func(decision string, metadata map[string]any)
}

// account does everything that follows a model response, complete or
// partial: quota reconciliation, audit, usage measurement, memory capture
// and the context-threshold decision. It returns the context state and the
// id of a checkpoint draft when one now exists.
func (c *chatCall) account(usage usageSummary, usageKnown bool, reply, cacheStatus, decision string, extra map[string]any) (contextState, string) {
	ctx := c.r.Context()
	if !usageKnown {
		prompt := estimateChatTokens(c.req.Messages, 0)
		completion := conservativeTokens(reply)
		usage = usageSummary{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
	}
	if usage.TotalTokens > c.estimated {
		c.app.reconcileTokenUsage(ctx, c.identity, c.model.ExternalName, usage.TotalTokens-c.estimated)
	}
	metadata := map[string]any{
		"provider": c.model.Provider, "cache": cacheStatus, "estimatedTokens": c.estimated,
		"actualPrompt": usage.PromptTokens, "actualCompletion": usage.CompletionTokens, "actualTotal": usage.TotalTokens,
		"usageSource": map[bool]string{true: "provider", false: "gateway_estimate"}[usageKnown],
	}
	if c.injection != nil {
		metadata["memoryCheckpointId"] = c.injection.CheckpointID
	}
	for key, value := range extra {
		metadata[key] = value
	}

	measurement := measurementFor(c.identity, c.model, c.sessionID, c.traceID, cacheStatus, usage, time.Now())
	if !usageKnown && measurement.Source == measurementProviderReported {
		measurement.Source = measurementGatewayEstimate
	}
	c.app.recordContextUsage(ctx, measurement)
	state := contextStateFor(usage.TotalTokens, c.window, measurement.Source, c.thresholds)
	metadata["contextStatus"], metadata["contextPercent"] = state.Status, state.Percent

	checkpointID := ""
	if needsCheckpoint(state.Status) && c.sessionID != "" {
		// The draft is built from stored records, so capture must land first.
		c.app.captureToMemory(ctx, c.identity, c.model, c.sessionID, c.traceID, c.req.Messages, reply)
		checkpointID = c.app.draftCheckpoint(ctx, c.identity, c.sessionID, c.traceID, state.Percent)
		metadata["checkpointDraftId"] = checkpointID
	} else if c.sessionID != "" {
		go c.app.captureToMemory(ctx, c.identity, c.model, c.sessionID, c.traceID, c.req.Messages, reply)
	}
	c.audit(decision, metadata)
	return state, checkpointID
}

func (c *chatCall) finish(resp chatCompletionResponse, cacheStatus string, fromCache bool) {
	reply := ""
	if len(resp.Choices) > 0 {
		reply = resp.Choices[0].Message.Content
	}
	state, checkpointID := c.account(resp.Usage, resp.Usage.TotalTokens > 0 && !strings.EqualFold(c.model.Provider, "stub"), reply, cacheStatus, "allow", nil)
	state.setHeaders(c.w)
	if checkpointID != "" {
		c.w.Header().Set("X-SUEZ-Checkpoint-Draft", checkpointID)
	}
	c.w.Header().Set("X-Suez-Cache", map[bool]string{true: "HIT", false: "MISS"}[fromCache])
	if !c.req.Stream {
		writeJSON(c.w, http.StatusOK, resp)
		return
	}
	// A cached answer for a streaming request is sent as one chunk.
	emit, done := c.beginSSE()
	chunk, _ := json.Marshal(map[string]any{"id": resp.ID, "object": "chat.completion.chunk", "created": resp.Created, "model": resp.Model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}, "usage": resp.Usage})
	if emit(chunk) == nil {
		done(state, checkpointID)
	}
}

// beginSSE switches the response to an event stream. The write deadline is
// pushed forward on every chunk because the server's fixed WriteTimeout
// would otherwise cut long answers.
func (c *chatCall) beginSSE() (emit func([]byte) error, done func(contextState, string)) {
	controller := http.NewResponseController(c.w)
	header := c.w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	c.w.WriteHeader(http.StatusOK)
	write := func(payload string) error {
		_ = controller.SetWriteDeadline(time.Now().Add(streamIdleTimeout))
		if _, err := c.w.Write([]byte(payload)); err != nil {
			return err
		}
		return controller.Flush()
	}
	emit = func(data []byte) error { return write("data: " + string(data) + "\n\n") }
	done = func(state contextState, checkpointID string) {
		// SSE comment: ignored by standard clients, readable by SUEZ clients.
		trailer, _ := json.Marshal(map[string]any{"context": state, "checkpointDraftId": checkpointID, "traceId": c.traceID})
		_ = write(": suez " + string(trailer) + "\n\n")
		_ = write("data: [DONE]\n\n")
	}
	return emit, done
}

func (c *chatCall) stream() {
	// Headers are sent before the answer exists, so they carry the
	// pre-flight estimate; the final state follows in the stream trailer.
	contextStateFor(c.estimated, c.window, measurementGatewayEstimate, c.thresholds).setHeaders(c.w)
	c.w.Header().Set("X-Suez-Cache", "MISS")

	var emit func([]byte) error
	var done func(contextState, string)
	started := false
	relay := func(data []byte) error {
		if !started {
			emit, done = c.beginSSE()
			started = true
		}
		return emit(data)
	}
	result, err := c.app.streamChatProvider(c.r.Context(), c.model, c.req, c.traceID, relay)
	reply := result.content.String()
	if err == nil {
		state, checkpointID := c.account(result.usage, result.usageKnown && !strings.EqualFold(c.model.Provider, "stub"), reply, cacheStatusMiss, "allow", map[string]any{"stream": true})
		if !started {
			emit, done = c.beginSSE()
		}
		done(state, checkpointID)
		return
	}

	code, upstreamStatus := providerErrorCode(err)
	if !started {
		// Nothing was sent yet: answer with a normal error status.
		c.audit("error", map[string]any{"provider": c.model.Provider, "code": code, "upstreamStatus": upstreamStatus, "stream": true})
		writeProviderError(c.w, err, c.traceID)
		return
	}
	// Part of the answer already went out. Tokens were consumed, so they are
	// metered and the partial exchange is audited; the client is told the
	// stream is incomplete and gets no [DONE].
	decision := "error"
	if code == "client_cancelled" {
		decision = "cancelled"
	}
	c.account(result.usage, false, reply, cacheStatusMiss, decision, map[string]any{"stream": true, "partial": true, "code": code, "upstreamStatus": upstreamStatus})
	if code != "client_cancelled" {
		failure, _ := json.Marshal(map[string]string{"error": "stream interrupted; the response is incomplete", "code": code, "traceId": c.traceID})
		controller := http.NewResponseController(c.w)
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.w.Write([]byte("event: error\ndata: " + string(failure) + "\n\n"))
		_ = controller.Flush()
	}
}

// injectMemory resolves suez.memory on a request. It returns a non-zero
// status when the injection is refused; the chat call is then not made.
func (a *gatewayApp) injectMemory(r *http.Request, identity identityContext, req *chatCompletionRequest, sessionID, traceID string) (*memoryInjection, int, string) {
	ctx := r.Context()
	checkpointID := strings.TrimSpace(req.Suez.Memory.CheckpointID)
	deny := func(status int, reason, message string) (*memoryInjection, int, string) {
		a.auditGatewayEvent(ctx, identity, "memory.inject", "checkpoint", checkpointID, "deny", traceID, map[string]any{"reason": reason, "sessionId": sessionID})
		return nil, status, message
	}
	if !hasPermission(identity, "memory.read") {
		return deny(http.StatusForbidden, "missing memory.read", "permission denied")
	}
	decision, err := a.evaluateOPA(ctx, "memory.inject", identity, map[string]any{"type": "checkpoint", "name": checkpointID, "enabled": true, "traceId": traceID}, map[string]any{"tokenBudget": req.Suez.Memory.TokenBudget})
	if err != nil {
		return deny(http.StatusServiceUnavailable, "policy evaluation failed", "policy service unavailable")
	}
	if !decision.Allow {
		return deny(http.StatusForbidden, "policy denied", "policy denied memory injection")
	}
	injection, err := a.fetchInjection(ctx, identity, sessionID, traceID, checkpointID, req.Suez.Memory.TokenBudget)
	if err != nil {
		var typed *controlPlaneError
		if errors.As(err, &typed) && typed.status < 500 {
			return deny(typed.status, typed.message, typed.message)
		}
		logWarn("memory injection failed", traceID, err)
		return deny(http.StatusServiceUnavailable, "memory service unavailable", "memory service unavailable")
	}
	preface := "The following was carried forward from an earlier session at the user's request. " +
		"It is prior notes and records, not verified fact. When you rely on it, cite the [checkpoint:…] or [evidence:…] marker.\n\n"
	req.Messages = append([]chatMessage{{Role: "system", Content: preface + injection.ContextText}}, req.Messages...)
	a.auditGatewayEvent(ctx, identity, "memory.inject", "checkpoint", injection.CheckpointID, "allow", traceID, map[string]any{
		"sessionId": sessionID, "evidenceIds": injection.EvidenceIDs, "omittedEvidenceIds": injection.Omitted,
		"tokenBudget": injection.TokenBudget, "tokensUsed": injection.TokensUsed,
	})
	return &injection, 0, ""
}
