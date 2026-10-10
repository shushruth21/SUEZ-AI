package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/xeipuuv/gojsonschema"
)

const chatCacheTTL = 60 * time.Second
const chatCacheKeyPrefix = "suez:chat:"

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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
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
	out := map[string]any{
		"cache_entries":  int64(0),
		"cache_hits":     int64(0),
		"chat_requests": int64(0),
	}
	keys, err := a.redis.Keys(ctx, chatCacheKeyPrefix+"*").Result()
	if err == nil {
		out["cache_entries"] = int64(len(keys))
	}
	if n, err := a.redis.Get(ctx, "suez:stats:cache_hits").Int64(); err == nil {
		out["cache_hits"] = n
	}
	if n, err := a.redis.Get(ctx, "suez:stats:chat_requests").Int64(); err == nil {
		out["chat_requests"] = n
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *gatewayApp) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	traceID := newTraceID()

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
	if strings.TrimSpace(req.Model) == "" || len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model and messages are required"})
		return
	}
	if req.Stream {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "streaming is not supported in the local runtime"})
		return
	}
	_ = a.redis.Incr(r.Context(), "suez:stats:chat_requests").Err()

	model, err := a.lookupModel(r.Context(), identity.TenantID, strings.TrimSpace(req.Model))
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", req.Model, "error", traceID, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if !model.Enabled {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "deny", traceID, map[string]any{"reason": "model disabled"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "model is disabled"})
		return
	}
	if err := a.ensureGrant(r.Context(), identity, "model", model.ExternalName); err != nil {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "deny", traceID, map[string]any{"reason": err.Error()})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	estimatedTokens := estimateChatTokens(req.Messages, req.MaxTokens)
	policy, err := a.evaluateOPA(r.Context(), "chat.completions", identity, map[string]any{
		"type":      "model",
		"name":      model.ExternalName,
		"provider":  model.Provider,
		"enabled":   model.Enabled,
		"maxRpm":    model.MaxRPM,
		"maxTpm":    model.MaxTPM,
		"riskTier":  "standard",
		"upstream":  model.UpstreamModel,
		"traceId":   traceID,
		"estimated": estimatedTokens,
	}, req)
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "error", traceID, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "policy evaluation failed"})
		return
	}
	if !policy.Allow {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "deny", traceID, map[string]any{"policy": policy})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "policy denied model invocation", "policy": policy})
		return
	}

	if err := a.enforceQuota(r.Context(), identity, model.ExternalName, model.MaxRPM, model.MaxTPM, estimatedTokens); err != nil {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "deny", traceID, map[string]any{"reason": err.Error(), "estimatedTokens": estimatedTokens})
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}

	cacheKey := chatCacheKey(identity.TenantID, model.ExternalName, req)
	if cached, err := a.getCachedChatResponse(r.Context(), cacheKey); err == nil && cached != nil {
		_ = a.redis.Incr(r.Context(), "suez:stats:cache_hits").Err()
		// Record hits too, otherwise the session's previous (smaller) measurement
		// would be reported as current. The cache key covers the full message list,
		// so the cached counts describe exactly this conversation.
		a.recordContextUsage(r.Context(), measurementFor(identity, model, sessionID, traceID, cacheStatusHit, cached.Usage, time.Now()))
		w.Header().Set("X-SUEZ-Trace-Id", traceID)
		w.Header().Set("X-Suez-Cache", "HIT")
		writeJSON(w, http.StatusOK, cached)
		return
	}

	resp, err := a.executeChatProvider(r.Context(), model, req)
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "error", traceID, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if resp.Usage.TotalTokens > estimatedTokens {
		a.reconcileTokenUsage(r.Context(), identity, model.ExternalName, resp.Usage.TotalTokens-estimatedTokens)
	}
	auditMetadata := map[string]any{
		"provider":         model.Provider,
		"estimatedTokens":  estimatedTokens,
		"actualPrompt":     resp.Usage.PromptTokens,
		"actualCompletion": resp.Usage.CompletionTokens,
		"actualTotal":      resp.Usage.TotalTokens,
	}
	if sessionID != "" {
		auditMetadata["sessionId"] = sessionID
	}
	a.auditGatewayEvent(r.Context(), identity, "model.invoke", "model", model.ExternalName, "allow", traceID, auditMetadata)
	a.recordContextUsage(r.Context(), measurementFor(identity, model, sessionID, traceID, cacheStatusMiss, resp.Usage, time.Now()))
	_ = a.setCachedChatResponse(r.Context(), cacheKey, resp)
	w.Header().Set("X-SUEZ-Trace-Id", traceID)
	w.Header().Set("X-Suez-Cache", "MISS")
	writeJSON(w, http.StatusOK, resp)
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

	tool, err := a.lookupTool(r.Context(), identity.TenantID, strings.TrimSpace(req.Name))
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", req.Name, "error", traceID, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if !tool.Enabled {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"reason": "tool disabled"})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "tool is disabled"})
		return
	}
	if err := a.ensureGrant(r.Context(), identity, "tool", tool.Name); err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"reason": err.Error()})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
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
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "error", traceID, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "policy evaluation failed"})
		return
	}
	if !policy.Allow {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "deny", traceID, map[string]any{"policy": policy})
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "policy denied tool execution", "policy": policy})
		return
	}

	output, err := a.executeTool(r.Context(), tool, req.Input)
	if err != nil {
		a.auditGatewayEvent(r.Context(), identity, "tool.execute", "tool", tool.Name, "error", traceID, map[string]any{"error": err.Error()})
		_ = a.insertToolExecution(r.Context(), identity, tool, traceID, "error", req.Input, map[string]any{"error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
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
			return modelRuntime{}, errors.New("model not found")
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
			return toolRuntime{}, errors.New("tool not found")
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
		return fmt.Errorf("no active %s grant for %s", resourceType, resourceName)
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

	rpmCount, err := a.redis.Incr(ctx, rpmKey).Result()
	if err != nil {
		return err
	}
	if rpmCount == 1 {
		_ = a.redis.Expire(ctx, rpmKey, time.Minute+10*time.Second).Err()
	}
	if maxRPM > 0 && int(rpmCount) > maxRPM {
		return fmt.Errorf("request quota exceeded for %s", resourceName)
	}

	tpmCount, err := a.redis.IncrBy(ctx, tpmKey, int64(estimatedTokens)).Result()
	if err != nil {
		return err
	}
	if tpmCount == int64(estimatedTokens) {
		_ = a.redis.Expire(ctx, tpmKey, time.Minute+10*time.Second).Err()
	}
	if maxTPM > 0 && int(tpmCount) > maxTPM {
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
	_ = a.redis.IncrBy(ctx, tpmKey, int64(extraTokens)).Err()
}

func (a *gatewayApp) executeChatProvider(ctx context.Context, model modelRuntime, req chatCompletionRequest) (chatCompletionResponse, error) {
	switch strings.ToLower(model.Provider) {
	case "stub":
		return executeStubChat(model, req), nil
	case "openai-compatible":
		return a.executeOpenAICompatibleChat(ctx, model, req)
	case "groq":
		return a.executeGroqChat(ctx, model, req)
	default:
		return chatCompletionResponse{}, fmt.Errorf("unsupported model provider %q", model.Provider)
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

func (a *gatewayApp) executeOpenAICompatibleChat(ctx context.Context, model modelRuntime, req chatCompletionRequest) (chatCompletionResponse, error) {
	baseURL := strings.TrimRight(firstNonEmpty(model.UpstreamBaseURL, env("OPENAI_COMPAT_BASE_URL", "")), "/")
	if baseURL == "" {
		return chatCompletionResponse{}, errors.New("openai-compatible base URL is not configured")
	}
	apiKey := strings.TrimSpace(env("OPENAI_COMPAT_API_KEY", ""))
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("openai-compatible API key is not configured")
	}

	upstreamModel := firstNonEmpty(model.UpstreamModel, model.ModelName, req.Model)
	body, err := json.Marshal(map[string]any{
		"model":       upstreamModel,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
	})
	if err != nil {
		return chatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatCompletionResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(resp.Body)
		return chatCompletionResponse{}, fmt.Errorf("upstream returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var upstream chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return chatCompletionResponse{}, err
	}
	upstream.Model = model.ExternalName
	return upstream, nil
}

func (a *gatewayApp) executeGroqChat(ctx context.Context, model modelRuntime, req chatCompletionRequest) (chatCompletionResponse, error) {
	baseURL := strings.TrimRight(firstNonEmpty(model.UpstreamBaseURL, env("GROQ_BASE_URL", "https://api.groq.com/openai/v1")), "/")
	if baseURL == "" {
		return chatCompletionResponse{}, errors.New("Groq base URL is not configured")
	}
	apiKey := strings.TrimSpace(env("GROQ_API_KEY", ""))
	if apiKey == "" {
		return chatCompletionResponse{}, errors.New("Groq API key is not configured (set GROQ_API_KEY or GROQ_API_KEY_FILE)")
	}

	upstreamModel := firstNonEmpty(model.UpstreamModel, model.ModelName, req.Model)
	body, err := json.Marshal(map[string]any{
		"model":       upstreamModel,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
	})
	if err != nil {
		return chatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return chatCompletionResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(resp.Body)
		return chatCompletionResponse{}, fmt.Errorf("groq upstream returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var upstream chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return chatCompletionResponse{}, err
	}
	upstream.Model = model.ExternalName
	return upstream, nil
}

func (a *gatewayApp) executeTool(ctx context.Context, tool toolRuntime, input map[string]any) (any, error) {
	switch strings.ToLower(tool.ExecutionMode) {
	case "stub":
		return executeStubTool(tool, input), nil
	case "http":
		return a.executeHTTPTool(ctx, tool, input)
	default:
		return nil, fmt.Errorf("unsupported tool execution mode %q", tool.ExecutionMode)
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
	if strings.TrimSpace(tool.TargetURL) == "" {
		return nil, errors.New("http tool target URL is not configured")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(tool.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, tool.TargetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var headers map[string]string
	if len(tool.HTTPHeaders) > 0 {
		_ = json.Unmarshal(tool.HTTPHeaders, &headers)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tool endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	if len(tool.OutputSchema) > 0 && string(tool.OutputSchema) != "{}" {
		if err := validateJSONSchema(tool.OutputSchema, decoded); err != nil {
			return nil, fmt.Errorf("tool output validation failed: %w", err)
		}
	}
	return decoded, nil
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
	if err != nil {
		return
	}
	_, _ = a.db.ExecContext(ctx, `
		insert into audit_log (tenant_id, actor, action, resource_type, resource_name, decision, trace_id, metadata)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
	`, identity.TenantID, identity.Actor, action, resourceType, resourceName, decision, traceID, string(payload))
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

func estimateChatTokens(messages []chatMessage, requestedMax int) int {
	total := 0
	for _, message := range messages {
		total += estimateTokenString(message.Role)
		total += estimateTokenString(message.Content)
	}
	if requestedMax > 0 {
		total += requestedMax
	}
	return total
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

func chatCacheKey(tenantID, model string, req chatCompletionRequest) string {
	h := sha256.New()
	h.Write([]byte(tenantID))
	h.Write([]byte(":"))
	h.Write([]byte(model))
	msgJSON, _ := json.Marshal(req.Messages)
	h.Write(msgJSON)
	return chatCacheKeyPrefix + hex.EncodeToString(h.Sum(nil))
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
