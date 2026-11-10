package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type chatMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
}

// suezExtensions are gateway-only request fields. They are never forwarded
// to a provider.
type suezExtensions struct {
	Memory *struct {
		CheckpointID string `json:"checkpointId"`
		TokenBudget  int    `json:"tokenBudget"`
	} `json:"memory,omitempty"`
}

// chatCompletionRequest keeps optional fields as pointers or raw JSON so that
// "not sent" stays distinguishable from zero and is forwarded as "not sent".
type chatCompletionRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	N                   *int            `json:"n,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	Suez                *suezExtensions `json:"suez,omitempty"`
}

func (r chatCompletionRequest) completionLimit() int {
	switch {
	case r.MaxCompletionTokens != nil:
		return *r.MaxCompletionTokens
	case r.MaxTokens != nil:
		return *r.MaxTokens
	}
	return 0
}

var chatRoles = map[string]bool{"system": true, "developer": true, "user": true, "assistant": true, "tool": true}

// validateChatCompletionRequest applies the ranges the supported providers
// (OpenAI-compatible APIs and Groq) share, so a bad value is a clear 400 here
// instead of an opaque upstream error.
func validateChatCompletionRequest(req chatCompletionRequest) error {
	if strings.TrimSpace(req.Model) == "" || len(req.Messages) == 0 {
		return errors.New("model and messages are required")
	}
	if len(req.Messages) > 4096 {
		return errors.New("too many messages")
	}
	inRange := func(name string, value *float64, low, high float64) error {
		if value != nil && (*value < low || *value > high) {
			return fmt.Errorf("%s must be between %g and %g", name, low, high)
		}
		return nil
	}
	for _, check := range []error{
		inRange("temperature", req.Temperature, 0, 2),
		inRange("top_p", req.TopP, 0, 1),
		inRange("presence_penalty", req.PresencePenalty, -2, 2),
		inRange("frequency_penalty", req.FrequencyPenalty, -2, 2),
	} {
		if check != nil {
			return check
		}
	}
	if req.MaxTokens != nil && *req.MaxTokens < 1 {
		return errors.New("max_tokens must be at least 1")
	}
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens < 1 {
		return errors.New("max_completion_tokens must be at least 1")
	}
	if req.MaxTokens != nil && req.MaxCompletionTokens != nil {
		return errors.New("send max_tokens or max_completion_tokens, not both")
	}
	if req.N != nil && *req.N != 1 {
		return errors.New("n must be 1")
	}
	for _, message := range req.Messages {
		if !chatRoles[strings.ToLower(strings.TrimSpace(message.Role))] {
			return errors.New("message role must be system, developer, user, assistant, or tool")
		}
	}
	for name, raw := range map[string]json.RawMessage{"tools": req.Tools, "stop": req.Stop, "tool_choice": req.ToolChoice, "response_format": req.ResponseFormat} {
		if len(raw) > 0 && !json.Valid(raw) {
			return fmt.Errorf("%s is not valid JSON", name)
		}
	}
	if len(req.Tools) > 0 {
		var tools []json.RawMessage
		if err := json.Unmarshal(req.Tools, &tools); err != nil || len(tools) > 128 {
			return errors.New("tools must be an array of at most 128 entries")
		}
	}
	if req.Suez != nil && req.Suez.Memory != nil {
		if strings.TrimSpace(req.Suez.Memory.CheckpointID) == "" || req.Suez.Memory.TokenBudget < 0 {
			return errors.New("suez.memory needs a checkpointId and a non-negative tokenBudget")
		}
	}
	return nil
}

// upstreamChatBody builds the provider request. Only fields the caller sent
// are included; gateway extensions are left out.
func upstreamChatBody(model string, req chatCompletionRequest, stream bool) map[string]any {
	body := map[string]any{"model": model, "messages": req.Messages}
	set := func(name string, present bool, value any) {
		if present {
			body[name] = value
		}
	}
	set("temperature", req.Temperature != nil, req.Temperature)
	set("top_p", req.TopP != nil, req.TopP)
	set("max_tokens", req.MaxTokens != nil, req.MaxTokens)
	set("max_completion_tokens", req.MaxCompletionTokens != nil, req.MaxCompletionTokens)
	set("presence_penalty", req.PresencePenalty != nil, req.PresencePenalty)
	set("frequency_penalty", req.FrequencyPenalty != nil, req.FrequencyPenalty)
	set("seed", req.Seed != nil, req.Seed)
	set("stop", len(req.Stop) > 0, req.Stop)
	set("tools", len(req.Tools) > 0, req.Tools)
	set("tool_choice", len(req.ToolChoice) > 0, req.ToolChoice)
	set("response_format", len(req.ResponseFormat) > 0, req.ResponseFormat)
	if stream {
		body["stream"] = true
		// Ask for token counts in the final chunk so usage is provider-reported.
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return body
}

// providerError is the normalized form of everything that can go wrong with
// an upstream call. Callers see code, message and the gateway status; the
// upstream body is only ever logged, redacted and truncated.
type providerError struct {
	Code           string
	Message        string
	Status         int
	UpstreamStatus int
	RetryAfter     string
	detail         string
}

func (e *providerError) Error() string {
	if e.UpstreamStatus != 0 {
		return fmt.Sprintf("%s (upstream status %d)", e.Code, e.UpstreamStatus)
	}
	return e.Code
}

func normalizeUpstreamStatus(label string, resp *http.Response, body []byte) *providerError {
	detail, _ := redactSecrets(truncate(strings.TrimSpace(string(body)), 600))
	out := &providerError{UpstreamStatus: resp.StatusCode, detail: detail}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		out.Code, out.Message, out.Status = "upstream_rate_limited", label+" is rate limiting requests", http.StatusTooManyRequests
		out.RetryAfter = resp.Header.Get("Retry-After")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		out.Code, out.Message, out.Status = "upstream_auth_failed", label+" rejected the gateway's credentials", http.StatusBadGateway
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusRequestEntityTooLarge:
		out.Code, out.Message, out.Status = "upstream_rejected_request", label+" rejected the request", http.StatusBadRequest
	case resp.StatusCode == http.StatusNotFound:
		out.Code, out.Message, out.Status = "upstream_model_not_found", label+" does not know the configured model", http.StatusBadGateway
	default:
		out.Code, out.Message, out.Status = "upstream_unavailable", label+" is unavailable", http.StatusBadGateway
	}
	return out
}

func normalizeTransportError(label string, err error) *providerError {
	var typed *providerError
	if errors.As(err, &typed) {
		return typed
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return &providerError{Code: "client_cancelled", Message: "request was cancelled", Status: 499}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &providerError{Code: "upstream_timeout", Message: label + " did not answer in time", Status: http.StatusGatewayTimeout}
	}
	return &providerError{Code: "upstream_unreachable", Message: label + " could not be reached", Status: http.StatusBadGateway, detail: "transport error"}
}

type providerTarget struct {
	label, baseURL, apiKey, model string
}

func (a *gatewayApp) providerTarget(model modelRuntime, req chatCompletionRequest) (providerTarget, error) {
	target := providerTarget{model: firstNonEmpty(model.UpstreamModel, model.ModelName, req.Model)}
	notConfigured := func(what string) error {
		return &providerError{Code: "provider_not_configured", Message: what + " is not configured on the gateway", Status: http.StatusServiceUnavailable}
	}
	switch strings.ToLower(model.Provider) {
	case "openai-compatible":
		target.label = "upstream provider"
		target.baseURL = strings.TrimRight(firstNonEmpty(model.UpstreamBaseURL, env("OPENAI_COMPAT_BASE_URL", "")), "/")
		target.apiKey = strings.TrimSpace(env("OPENAI_COMPAT_API_KEY", ""))
	case "groq":
		target.label = "Groq"
		target.baseURL = strings.TrimRight(firstNonEmpty(model.UpstreamBaseURL, env("GROQ_BASE_URL", "https://api.groq.com/openai/v1")), "/")
		target.apiKey = strings.TrimSpace(env("GROQ_API_KEY", ""))
	default:
		return target, &providerError{Code: "provider_unsupported", Message: "model provider is not supported", Status: http.StatusBadGateway}
	}
	if target.baseURL == "" {
		return target, notConfigured("the provider base URL")
	}
	if target.apiKey == "" {
		return target, notConfigured("the provider API key")
	}
	return target, nil
}

const (
	upstreamChatResponseBodyLimit int64 = 16 << 20
	upstreamMaxAttempts                 = 3
)

// retryable reports whether sending the same request again is safe: only
// when the provider did not process it (connection never established, or an
// explicit overload/unavailable status with no result).
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		var opErr *net.OpError
		return errors.As(err, &opErr) && opErr.Op == "dial"
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusTooManyRequests:
		seconds, convErr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		return convErr == nil && seconds <= 2
	}
	return false
}

// callUpstream posts the chat request, retrying only where retryable allows.
// On success the caller owns the response body.
func (a *gatewayApp) callUpstream(ctx context.Context, client *http.Client, target providerTarget, body []byte, traceID string) (*http.Response, error) {
	var lastErr *providerError
	for attempt := 1; attempt <= upstreamMaxAttempts; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, &providerError{Code: "provider_not_configured", Message: "the provider base URL is not valid", Status: http.StatusServiceUnavailable}
		}
		httpReq.Header.Set("Authorization", "Bearer "+target.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(httpReq)
		if err == nil && resp.StatusCode < 400 {
			return resp, nil
		}
		again := retryable(resp, err) && attempt < upstreamMaxAttempts && ctx.Err() == nil
		if err != nil {
			lastErr = normalizeTransportError(target.label, err)
		} else {
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = normalizeUpstreamStatus(target.label, resp, payload)
		}
		slog.Warn("upstream call failed", "traceId", traceID, "attempt", attempt, "code", lastErr.Code,
			"upstreamStatus", lastErr.UpstreamStatus, "willRetry", again, "detail", lastErr.detail)
		if !again {
			break
		}
		select {
		case <-ctx.Done():
			return nil, normalizeTransportError(target.label, ctx.Err())
		case <-time.After(time.Duration(attempt*attempt) * 250 * time.Millisecond):
		}
	}
	return nil, lastErr
}

func decodeUpstreamChatResponse(label string, body io.Reader) (chatCompletionResponse, error) {
	invalid := func(why string) error {
		return &providerError{Code: "upstream_invalid_response", Message: label + " returned a response the gateway could not use", Status: http.StatusBadGateway, detail: why}
	}
	payload, err := io.ReadAll(io.LimitReader(body, upstreamChatResponseBodyLimit+1))
	if err != nil {
		return chatCompletionResponse{}, normalizeTransportError(label, err)
	}
	if int64(len(payload)) > upstreamChatResponseBodyLimit {
		return chatCompletionResponse{}, invalid("response larger than the body limit")
	}
	var upstream chatCompletionResponse
	if err := json.Unmarshal(payload, &upstream); err != nil {
		return chatCompletionResponse{}, invalid("response is not valid JSON")
	}
	if len(upstream.Choices) == 0 {
		return chatCompletionResponse{}, invalid("response has no choices")
	}
	for _, choice := range upstream.Choices {
		if choice.Message.Role == "" || (strings.TrimSpace(choice.Message.Content) == "" && len(choice.Message.ToolCalls) == 0) {
			return chatCompletionResponse{}, invalid("a choice has no content and no tool calls")
		}
	}
	return upstream, nil
}

func (a *gatewayApp) executeChatProvider(ctx context.Context, model modelRuntime, req chatCompletionRequest, traceID string) (chatCompletionResponse, error) {
	if strings.EqualFold(model.Provider, "stub") {
		return executeStubChat(model, req), nil
	}
	target, err := a.providerTarget(model, req)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	body, err := json.Marshal(upstreamChatBody(target.model, req, false))
	if err != nil {
		return chatCompletionResponse{}, &providerError{Code: "internal_error", Message: "request could not be encoded", Status: http.StatusInternalServerError}
	}
	resp, err := a.callUpstream(ctx, a.httpClient, target, body, traceID)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	upstream, err := decodeUpstreamChatResponse(target.label, resp.Body)
	if err != nil {
		return chatCompletionResponse{}, err
	}
	upstream.Model = model.ExternalName
	return upstream, nil
}

// streamResult is what a finished (or interrupted) stream produced.
type streamResult struct {
	content      strings.Builder
	usage        usageSummary
	usageKnown   bool
	finishReason string
	completed    bool
}

const streamIdleTimeout = 60 * time.Second

// streamChatProvider relays an upstream SSE stream chunk by chunk through
// emit. It returns what was relayed even when the stream fails part-way, so
// the caller can account for and audit a partial response.
func (a *gatewayApp) streamChatProvider(ctx context.Context, model modelRuntime, req chatCompletionRequest, traceID string, emit func(data []byte) error) (*streamResult, error) {
	result := &streamResult{}
	if strings.EqualFold(model.Provider, "stub") {
		return result, streamStubChat(ctx, model, req, result, emit)
	}
	target, err := a.providerTarget(model, req)
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(upstreamChatBody(target.model, req, true))
	if err != nil {
		return result, &providerError{Code: "internal_error", Message: "request could not be encoded", Status: http.StatusInternalServerError}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A stream that goes quiet is cancelled; the timer is reset per chunk.
	idle := time.AfterFunc(streamIdleTimeout, cancel)
	defer idle.Stop()

	resp, err := a.callUpstream(ctx, a.streamClient, target, body, traceID)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	interrupted := func(why string) error {
		return &providerError{Code: "upstream_stream_interrupted", Message: target.label + " stream ended before it was complete", Status: http.StatusBadGateway, detail: why}
	}
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var relayed int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 1<<20 {
			return result, interrupted("event larger than 1 MiB")
		}
		if trimmed := bytes.TrimSpace(line); bytes.HasPrefix(trimmed, []byte("data:")) {
			idle.Reset(streamIdleTimeout)
			data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
			if string(data) == "[DONE]" {
				result.completed = true
				return result, nil
			}
			var chunk map[string]any
			if json.Unmarshal(data, &chunk) != nil {
				return result, interrupted("chunk is not valid JSON")
			}
			absorbChunk(result, chunk)
			chunk["model"] = model.ExternalName
			out, _ := json.Marshal(chunk)
			if relayed += int64(len(out)); relayed > upstreamChatResponseBodyLimit {
				return result, interrupted("stream larger than the body limit")
			}
			if err := emit(out); err != nil {
				return result, &providerError{Code: "client_cancelled", Message: "client went away", Status: 499}
			}
		}
		if err != nil {
			if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
				return result, normalizeTransportError(target.label, ctx.Err())
			}
			return result, interrupted("connection closed before [DONE]")
		}
	}
}

func absorbChunk(result *streamResult, chunk map[string]any) {
	if choices, ok := chunk["choices"].([]any); ok {
		for _, raw := range choices {
			choice, _ := raw.(map[string]any)
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text, ok := delta["content"].(string); ok {
					result.content.WriteString(text)
				}
			}
			if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
				result.finishReason = reason
			}
		}
	}
	if usage, ok := chunk["usage"].(map[string]any); ok {
		number := func(name string) int {
			value, _ := usage[name].(float64)
			return int(value)
		}
		result.usage = usageSummary{PromptTokens: number("prompt_tokens"), CompletionTokens: number("completion_tokens"), TotalTokens: number("total_tokens")}
		result.usageKnown = result.usage.TotalTokens > 0
	}
}

func streamStubChat(ctx context.Context, model modelRuntime, req chatCompletionRequest, result *streamResult, emit func([]byte) error) error {
	full := executeStubChat(model, req)
	id, created := full.ID, full.Created
	chunk := func(delta map[string]any, finish any, usage *usageSummary) []byte {
		payload := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model.ExternalName,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			payload["usage"] = usage
		}
		out, _ := json.Marshal(payload)
		return out
	}
	words := strings.SplitAfter(full.Choices[0].Message.Content, " ")
	for index, word := range words {
		if ctx.Err() != nil {
			return normalizeTransportError("stub", ctx.Err())
		}
		delta := map[string]any{"content": word}
		if index == 0 {
			delta["role"] = "assistant"
		}
		if err := emit(chunk(delta, nil, nil)); err != nil {
			return &providerError{Code: "client_cancelled", Message: "client went away", Status: 499}
		}
		result.content.WriteString(word)
	}
	result.usage, result.usageKnown, result.finishReason, result.completed = full.Usage, true, "stop", true
	return emit(chunk(map[string]any{}, "stop", &full.Usage))
}

// estimateChatTokens is the gateway's own count, used for quota reservation
// and for context checks before a provider has reported real usage. It leans
// high (about 3.5 characters per token plus per-message overhead) so that a
// request is never under-counted on the way in.
func estimateChatTokens(messages []chatMessage, requestedMax int) int {
	total := 0
	for _, message := range messages {
		total += 4 + conservativeTokens(message.Content) + conservativeTokens(string(message.ToolCalls))
	}
	if requestedMax > 0 {
		total += requestedMax
	}
	return total
}

func conservativeTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text)*2 + 6) / 7
}
