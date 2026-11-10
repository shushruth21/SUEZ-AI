package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xeipuuv/gojsonschema"
)

const chatCacheTTL = 60 * time.Second
const chatCacheKeyPrefix = "suez:chat:"

var (
	errModelNotFound           = errors.New("model not found")
	errToolNotFound            = errors.New("tool not found")
	errNoActiveGrant           = errors.New("no active grant")
	errQuotaBackendUnavailable = errors.New("quota backend unavailable")
)

type modelRuntime struct {
	ID              string `json:"id"`
	TenantID        string `json:"tenantId"`
	ExternalName    string `json:"externalName"`
	Provider        string `json:"provider"`
	ModelName       string `json:"modelName"`
	UpstreamModel   string `json:"upstreamModel,omitempty"`
	UpstreamBaseURL string `json:"upstreamBaseUrl,omitempty"`
	MaxRPM          int    `json:"maxRpm"`
	MaxTPM          int    `json:"maxTpm"`
	Enabled         bool   `json:"enabled"`
}

type toolRuntime struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenantId"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"inputSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema"`
	ExecutionMode string          `json:"executionMode"`
	TargetURL     string          `json:"targetUrl,omitempty"`
	TimeoutMS     int             `json:"timeoutMs"`
	HTTPHeaders   json.RawMessage `json:"httpHeaders"`
	RiskTier      string          `json:"riskTier"`
	Enabled       bool            `json:"enabled"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type usageSummary struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   usageSummary `json:"usage"`
}

type toolExecuteRequest struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type toolExecuteResponse struct {
	TraceID string      `json:"traceId"`
	Tool    string      `json:"tool"`
	Output  any         `json:"output"`
	Policy  opaDecision `json:"policy"`
}

type opaDecision struct {
	Allow         bool     `json:"allow"`
	Phase         string   `json:"phase,omitempty"`
	Action        string   `json:"action,omitempty"`
	PolicyVersion any      `json:"policyVersion,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
}

func (a *gatewayApp) handleObservability(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := mustIdentity(ctx).TenantID
	out := map[string]any{
		"cache_entries": int64(0),
		"cache_hits":    int64(0),
		"chat_requests": int64(0),
	}
	// SCAN, not KEYS: KEYS walks the whole keyspace in one blocking call.
	// Everything here is scoped to the caller's tenant.
	var entries int64
	iter := a.redis.Scan(ctx, 0, chatCacheTenantPrefix(tenantID)+"*", 500).Iterator()
	for iter.Next(ctx) {
		entries++
	}
	if iter.Err() == nil {
		out["cache_entries"] = entries
	}
	if n, err := a.redis.Get(ctx, statsKey(tenantID, "cache_hits")).Int64(); err == nil {
		out["cache_hits"] = n
	}
	if n, err := a.redis.Get(ctx, statsKey(tenantID, "chat_requests")).Int64(); err == nil {
		out["chat_requests"] = n
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *gatewayApp) handleToolExecute(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	traceID := newTraceID()

	var req toolExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tool name is required"})
		return
	}
	if req.Input == nil {
		req.Input = map[string]any{}
	}

	if !allowedByList(identity.AllowedTools, strings.TrimSpace(req.Name)) {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", strings.TrimSpace(req.Name), "deny", traceID, map[string]any{"reason": "tool is outside the api key's allowed tools"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this api key is not allowed to use this tool"})
		return
	}
	tool, err := a.lookupTool(r.Context(), identity.TenantID, strings.TrimSpace(req.Name))
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", strings.TrimSpace(req.Name), "error", traceID, map[string]any{"reason": "tool lookup failed"})
		writeGatewayLookupError(w, err, http.StatusNotFound, "tool not found")
		return
	}
	if !tool.Enabled {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"reason": "tool disabled"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "tool is disabled"})
		return
	}
	if err := a.ensureGrant(r.Context(), identity, "tool", tool.Name); err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"reason": "no active grant"})
		writeGatewayLookupError(w, err, http.StatusForbidden, "no active tool grant")
		return
	}
	if err := validateJSONSchema(tool.InputSchema, req.Input); err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"reason": err.Error()})
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	policy, err := a.evaluateOPA(r.Context(), "tools.execute", identity, map[string]any{
		"type":     "tool",
		"name":     tool.Name,
		"enabled":  tool.Enabled,
		"mode":     tool.ExecutionMode,
		"riskTier": tool.RiskTier,
		"traceId":  traceID,
	}, req)
	if err != nil {
		logWarn("policy evaluation failed", traceID, err)
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "error", traceID, map[string]any{"reason": "policy evaluation failed"})
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "policy service unavailable", "traceId": traceID})
		return
	}
	if !policy.Allow {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"policy": policy})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "policy denied tool execution", "policy": policy})
		return
	}

	output, err := a.executeTool(r.Context(), tool, req.Input)
	if err != nil {
		// The tool endpoint's own error text can echo inputs or credentials, so
		// only a normalized code is stored and returned.
		code, upstreamStatus := providerErrorCode(err)
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "error", traceID, map[string]any{"code": code, "upstreamStatus": upstreamStatus})
		_ = a.insertToolExecution(r.Context(), identity, tool, traceID, "error", req.Input, map[string]any{"error": code})
		writeProviderError(w, err, traceID)
		return
	}
	_ = a.insertToolExecution(r.Context(), identity, tool, traceID, "allow", req.Input, output)
	a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "allow", traceID, map[string]any{"mode": tool.ExecutionMode, "riskTier": tool.RiskTier})
	w.Header().Set("X-SUEZ-Trace-Id", traceID)
	writeJSON(w, http.StatusOK, toolExecuteResponse{
		TraceID: traceID,
		Tool:    tool.Name,
		Output:  output,
		Policy:  policy,
	})
}

func (a *gatewayApp) lookupModel(ctx context.Context, tenantID, externalName string) (modelRuntime, error) {
	row := a.db.QueryRowContext(ctx, `
		select id, tenant_id, external_name, provider, model_name, coalesce(upstream_model, ''), coalesce(upstream_base_url, ''), max_rpm, max_tpm, enabled
		from models
		where tenant_id = $1 and external_name = $2
		limit 1
	`, tenantID, externalName)
	var model modelRuntime
	if err := row.Scan(&model.ID, &model.TenantID, &model.ExternalName, &model.Provider, &model.ModelName, &model.UpstreamModel, &model.UpstreamBaseURL, &model.MaxRPM, &model.MaxTPM, &model.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return modelRuntime{}, errModelNotFound
		}
		return modelRuntime{}, err
	}
	return model, nil
}

func (a *gatewayApp) lookupTool(ctx context.Context, tenantID, name string) (toolRuntime, error) {
	row := a.db.QueryRowContext(ctx, `
		select id, tenant_id, name, description, input_schema, output_schema, execution_mode, coalesce(target_url, ''), timeout_ms, http_headers, risk_tier, enabled
		from tools
		where tenant_id = $1 and name = $2
		limit 1
	`, tenantID, name)
	var (
		tool         toolRuntime
		inputSchema  []byte
		outputSchema []byte
		httpHeaders  []byte
	)
	if err := row.Scan(&tool.ID, &tool.TenantID, &tool.Name, &tool.Description, &inputSchema, &outputSchema, &tool.ExecutionMode, &tool.TargetURL, &tool.TimeoutMS, &httpHeaders, &tool.RiskTier, &tool.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return toolRuntime{}, errToolNotFound
		}
		return toolRuntime{}, err
	}
	tool.InputSchema = append([]byte(nil), inputSchema...)
	tool.OutputSchema = append([]byte(nil), outputSchema...)
	tool.HTTPHeaders = append([]byte(nil), httpHeaders...)
	return tool, nil
}

func (a *gatewayApp) ensureGrant(ctx context.Context, identity identityContext, resourceType, resourceName string) error {
	var found string
	err := a.db.QueryRowContext(ctx, `
		select id
		from access_grants
		where tenant_id = $1 and subject = $2 and resource_type = $3 and resource_name = $4 and status = 'active'
		limit 1
	`, identity.TenantID, identity.Actor, resourceType, resourceName).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", errNoActiveGrant, resourceType)
	}
	return err
}

func (a *gatewayApp) evaluateOPA(ctx context.Context, action string, identity identityContext, resource map[string]any, payload any) (opaDecision, error) {
	requestBody, err := json.Marshal(map[string]any{
		"input": map[string]any{
			"action":   action,
			"identity": identity,
			"resource": resource,
			"request":  payload,
		},
	})
	if err != nil {
		return opaDecision{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env("OPA_DECISION_URL", "http://opa:8181/v1/data/suez/authz/decision"), bytes.NewReader(requestBody))
	if err != nil {
		return opaDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return opaDecision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return opaDecision{}, fmt.Errorf("opa returned %s", resp.Status)
	}

	var decoded struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return opaDecision{}, err
	}
	if len(decoded.Result) == 0 {
		return opaDecision{}, errors.New("opa returned empty result")
	}

	var decision opaDecision
	if err := json.Unmarshal(decoded.Result, &decision); err == nil {
		return decision, nil
	}
	var allowed bool
	if err := json.Unmarshal(decoded.Result, &allowed); err == nil {
		return opaDecision{Allow: allowed, Action: action}, nil
	}
	return opaDecision{}, errors.New("opa result format was not understood")
}

func (a *gatewayApp) enforceQuota(ctx context.Context, identity identityContext, resourceName string, maxRPM, maxTPM, estimatedTokens int) error {
	bucket := time.Now().UTC().Format("200601021504")
	baseKey := strings.Join([]string{"quota", identity.TenantID, identity.Actor, resourceName, bucket}, ":")
	rpmKey := baseKey + ":rpm"
	tpmKey := baseKey + ":tpm"

	var rpmCount, tpmCount *redis.IntCmd
	_, err := a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		rpmCount = pipe.Incr(ctx, rpmKey)
		pipe.Expire(ctx, rpmKey, time.Minute+10*time.Second)
		tpmCount = pipe.IncrBy(ctx, tpmKey, int64(estimatedTokens))
		pipe.Expire(ctx, tpmKey, time.Minute+10*time.Second)
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errQuotaBackendUnavailable, err)
	}
	rpmValue, err := rpmCount.Result()
	if err != nil {
		return fmt.Errorf("%w: %v", errQuotaBackendUnavailable, err)
	}
	tpmValue, err := tpmCount.Result()
	if err != nil {
		return fmt.Errorf("%w: %v", errQuotaBackendUnavailable, err)
	}
	if maxRPM > 0 && int(rpmValue) > maxRPM {
		return fmt.Errorf("request quota exceeded for %s", resourceName)
	}
	if maxTPM > 0 && int(tpmValue) > maxTPM {
		return fmt.Errorf("token quota exceeded for %s", resourceName)
	}
	return nil
}

func (a *gatewayApp) reconcileTokenUsage(ctx context.Context, identity identityContext, resourceName string, extraTokens int) {
	if extraTokens <= 0 {
		return
	}
	bucket := time.Now().UTC().Format("200601021504")
	tpmKey := strings.Join([]string{"quota", identity.TenantID, identity.Actor, resourceName, bucket, "tpm"}, ":")
	_, err := a.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.IncrBy(ctx, tpmKey, int64(extraTokens))
		pipe.Expire(ctx, tpmKey, time.Minute+10*time.Second)
		return nil
	})
	if err != nil {
		slog.Warn("quota reconciliation failed", "error", err.Error())
	}
}

func executeStubChat(model modelRuntime, req chatCompletionRequest) chatCompletionResponse {
	lastUserMessage := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if strings.EqualFold(req.Messages[i].Role, "user") {
			lastUserMessage = req.Messages[i].Content
			break
		}
	}
	if lastUserMessage == "" {
		lastUserMessage = "Hello from the SUEZ stub provider."
	}

	content := fmt.Sprintf("Stub response from %s: %s", model.ExternalName, lastUserMessage)
	promptTokens := estimateChatTokens(req.Messages, 0)
	completionTokens := estimateTokenString(content)
	return chatCompletionResponse{
		ID:      "chatcmpl-stub-" + newTraceID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model.ExternalName,
		Choices: []chatChoice{{
			Index: 0,
			Message: chatMessage{
				Role:    "assistant",
				Content: content,
			},
			FinishReason: "stop",
		}},
		Usage: usageSummary{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
}

func (a *gatewayApp) executeTool(ctx context.Context, tool toolRuntime, input map[string]any) (any, error) {
	switch strings.ToLower(tool.ExecutionMode) {
	case "stub":
		return executeStubTool(tool, input), nil
	case "http":
		return a.executeHTTPTool(ctx, tool, input)
	default:
		return nil, &providerError{Code: "tool_mode_unsupported", Message: "the tool's execution mode is not supported", Status: http.StatusBadGateway}
	}
}

func executeStubTool(tool toolRuntime, input map[string]any) map[string]any {
	switch tool.Name {
	case "weather.lookup":
		city, _ := input["city"].(string)
		if strings.TrimSpace(city) == "" {
			city = "unknown"
		}
		return map[string]any{
			"city":     city,
			"forecast": "clear skies",
			"source":   "stub-weather-tool",
		}
	default:
		return map[string]any{
			"tool":   tool.Name,
			"echoed": input,
			"source": "stub-tool-executor",
		}
	}
}

func (a *gatewayApp) executeHTTPTool(ctx context.Context, tool toolRuntime, input map[string]any) (any, error) {
	const label = "tool endpoint"
	invalid := func(code, message string) error {
		return &providerError{Code: code, Message: message, Status: http.StatusBadGateway}
	}
	if strings.TrimSpace(tool.TargetURL) == "" {
		return nil, &providerError{Code: "tool_not_configured", Message: "the tool has no target URL", Status: http.StatusServiceUnavailable}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, invalid("internal_error", "tool input could not be encoded")
	}

	timeout := time.Duration(tool.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, tool.TargetURL, bytes.NewReader(body))
	if err != nil {
		return nil, &providerError{Code: "tool_not_configured", Message: "the tool target URL is not valid", Status: http.StatusServiceUnavailable}
	}
	req.Header.Set("Content-Type", "application/json")
	var headers map[string]string
	if len(tool.HTTPHeaders) > 0 {
		_ = json.Unmarshal(tool.HTTPHeaders, &headers)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := a.toolClient().Do(req)
	if err != nil {
		return nil, normalizeTransportError(label, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, toolResponseBodyLimit+1))
	if err != nil {
		return nil, normalizeTransportError(label, err)
	}
	if int64(len(payload)) > toolResponseBodyLimit {
		return nil, invalid("tool_response_too_large", "the tool response is too large")
	}
	if resp.StatusCode >= 400 {
		return nil, normalizeUpstreamStatus(label, resp, payload)
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, invalid("tool_invalid_response", "the tool response is not valid JSON")
	}
	if len(tool.OutputSchema) > 0 && string(tool.OutputSchema) != "{}" {
		if err := validateJSONSchema(tool.OutputSchema, decoded); err != nil {
			return nil, invalid("tool_output_invalid", "the tool response does not match its output schema")
		}
	}
	return decoded, nil
}

// toolClient never follows redirects: a tool endpoint must not be able to
// bounce the request (with its configured secret headers) to another host.
func (a *gatewayApp) toolClient() *http.Client {
	client := *a.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (a *gatewayApp) insertToolExecution(ctx context.Context, identity identityContext, tool toolRuntime, traceID, status string, inputPayload, outputPayload any) error {
	inputJSON, err := json.Marshal(inputPayload)
	if err != nil {
		return err
	}
	outputJSON, err := json.Marshal(outputPayload)
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `
		insert into tool_executions (tenant_id, tool_id, tool_name, actor, status, trace_id, input_payload, output_payload)
		values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb)
	`, identity.TenantID, tool.ID, tool.Name, identity.Actor, status, traceID, string(inputJSON), string(outputJSON))
	return err
}

func (a *gatewayApp) auditGatewayEvent(ctx context.Context, identity identityContext, action, resourceType, resourceName, decision, traceID string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil || a.db == nil {
		return
	}
	// The audit row must be written even when the caller has gone away (a
	// cancelled stream is still an event), so the request's cancellation is
	// not inherited. A failed write is logged with the trace id.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := a.db.ExecContext(writeCtx, `
		insert into audit_log (tenant_id, actor, action, resource_type, resource_name, decision, trace_id, metadata)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
	`, identity.TenantID, identity.Actor, action, resourceType, resourceName, decision, traceID, string(payload)); err != nil {
		slog.Error("audit write failed", "traceId", traceID, "action", action, "error", err.Error())
	}
}

func validateJSONSchema(schema json.RawMessage, document any) error {
	if len(schema) == 0 {
		return nil
	}
	result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(schema), gojsonschema.NewGoLoader(document))
	if err != nil {
		return err
	}
	if result.Valid() {
		return nil
	}
	messages := make([]string, 0, len(result.Errors()))
	for _, issue := range result.Errors() {
		messages = append(messages, issue.String())
	}
	return errors.New(strings.Join(messages, "; "))
}

func writeGatewayLookupError(w http.ResponseWriter, err error, expectedStatus int, publicMessage string) {
	if expectedStatus == http.StatusNotFound && (errors.Is(err, errModelNotFound) || errors.Is(err, errToolNotFound)) {
		writeJSON(w, expectedStatus, map[string]string{"error": publicMessage})
		return
	}
	if expectedStatus == http.StatusForbidden && errors.Is(err, errNoActiveGrant) {
		writeJSON(w, expectedStatus, map[string]string{"error": publicMessage})
		return
	}
	slog.Error("gateway database lookup failed", "error", err.Error())
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func estimateTokenString(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 1
	}
	return int(math.Ceil(float64(len(trimmed)) / 4.0))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newTraceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// chatCacheTenantPrefix puts the tenant in clear in the key so per-tenant
// counts can be taken with a SCAN pattern.
func chatCacheTenantPrefix(tenantID string) string {
	return chatCacheKeyPrefix + tenantID + ":"
}

func statsKey(tenantID, name string) string {
	return "suez:stats:" + tenantID + ":" + name
}

const (
	toolResponseBodyLimit int64 = 4 << 20
)

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func (a *gatewayApp) getCachedChatResponse(ctx context.Context, key string) (*chatCompletionResponse, error) {
	val, err := a.redis.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var resp chatCompletionResponse
	if err := json.Unmarshal(val, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (a *gatewayApp) setCachedChatResponse(ctx context.Context, key string, resp chatCompletionResponse) error {
	val, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return a.redis.Set(ctx, key, val, chatCacheTTL).Err()
}
