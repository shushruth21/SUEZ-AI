package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type modelRecord struct {
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
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type toolRecord struct {
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
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
}

type policyRecord struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenantId"`
	ParentPolicyID      string `json:"parentPolicyId,omitempty"`
	Name                string `json:"name"`
	Scope               string `json:"scope"`
	TargetName          string `json:"targetName,omitempty"`
	NaturalLanguageRule string `json:"naturalLanguageRule,omitempty"`
	GenerationPrompt    string `json:"generationPrompt,omitempty"`
	GeneratorModel      string `json:"generatorModel,omitempty"`
	RegoPackage         string `json:"regoPackage"`
	RegoSource          string `json:"regoSource"`
	Status              string `json:"status"`
	Version             int    `json:"version"`
	Enabled             bool   `json:"enabled"`
	ValidationErrors    []string `json:"validationErrors"`
	PublishedAt         string   `json:"publishedAt,omitempty"`
	PublishedBy         string   `json:"publishedBy,omitempty"`
	LastCompiledAt      string   `json:"lastCompiledAt,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
}

type auditEvent struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenantId,omitempty"`
	Actor        string          `json:"actor"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resourceType"`
	ResourceName string          `json:"resourceName"`
	Decision     string          `json:"decision,omitempty"`
	TraceID      string          `json:"traceId,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    string          `json:"createdAt"`
}

type auditFilters struct {
	Actor        string
	Action       string
	ResourceType string
	ResourceName string
	Decision     string
	TraceID      string
	Since        *time.Time
	Until        *time.Time
	Limit        int
}

type createModelRequest struct {
	ExternalName    string `json:"externalName"`
	Provider        string `json:"provider"`
	ModelName       string `json:"modelName"`
	UpstreamModel   string `json:"upstreamModel"`
	UpstreamBaseURL string `json:"upstreamBaseUrl"`
	MaxRPM          int    `json:"maxRpm"`
	MaxTPM          int    `json:"maxTpm"`
	Enabled         bool   `json:"enabled"`
}

type createToolRequest struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"inputSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema"`
	ExecutionMode string          `json:"executionMode"`
	TargetURL     string          `json:"targetUrl"`
	TimeoutMS     int             `json:"timeoutMs"`
	HTTPHeaders   json.RawMessage `json:"httpHeaders"`
	RiskTier      string          `json:"riskTier"`
	Enabled       bool            `json:"enabled"`
}

type createPolicyRequest struct {
	Name                string `json:"name"`
	Scope               string `json:"scope"`
	TargetName          string `json:"targetName"`
	NaturalLanguageRule string `json:"naturalLanguageRule"`
	RegoPackage         string `json:"regoPackage"`
	RegoSource          string `json:"regoSource"`
	Status              string `json:"status"`
	Enabled             bool   `json:"enabled"`
}

func (a *controlPlaneApp) handleModels(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		models, err := a.listModels(r.Context(), identity.TenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"models":          models,
		})
	case http.MethodPost:
		if !isAdmin(identity) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
			return
		}
		var req createModelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.ExternalName) == "" || strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.ModelName) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "externalName, provider, and modelName are required"})
			return
		}
		if req.MaxRPM <= 0 {
			req.MaxRPM = 60
		}
		if req.MaxTPM <= 0 {
			req.MaxTPM = 120000
		}

		model, err := a.createModel(r.Context(), identity.TenantID, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_ = a.insertAudit(r.Context(), identity, "model.create", "model", model.ExternalName, "allow", map[string]any{
			"modelId":  model.ID,
			"provider": model.Provider,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"model":           model,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleTools(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		tools, err := a.listTools(r.Context(), identity.TenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"tools":           tools,
		})
	case http.MethodPost:
		if !isAdmin(identity) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
			return
		}
		var req createToolRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Description) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and description are required"})
			return
		}
		if strings.TrimSpace(req.ExecutionMode) == "" {
			req.ExecutionMode = "stub"
		}
		if req.TimeoutMS <= 0 {
			req.TimeoutMS = 5000
		}
		if strings.TrimSpace(req.RiskTier) == "" {
			req.RiskTier = "standard"
		}
		if len(req.InputSchema) == 0 {
			req.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		if len(req.OutputSchema) == 0 {
			req.OutputSchema = json.RawMessage(`{"type":"object"}`)
		}
		if len(req.HTTPHeaders) == 0 {
			req.HTTPHeaders = json.RawMessage(`{}`)
		}

		tool, err := a.createTool(r.Context(), identity.TenantID, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_ = a.insertAudit(r.Context(), identity, "tool.create", "tool", tool.Name, "allow", map[string]any{
			"toolId":        tool.ID,
			"executionMode": tool.ExecutionMode,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"tool":            tool,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handlePolicies(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	switch r.Method {
	case http.MethodGet:
		policies, err := a.listPolicies(r.Context(), identity.TenantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"policies":        policies,
		})
	case http.MethodPost:
		if !isAdmin(identity) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
			return
		}
		var req createPolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Scope) == "" || strings.TrimSpace(req.RegoPackage) == "" || strings.TrimSpace(req.RegoSource) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, scope, regoPackage, and regoSource are required"})
			return
		}
		if strings.TrimSpace(req.Status) == "" {
			req.Status = "draft"
		}
		req.Status = "draft"

		policy, err := a.createPolicy(r.Context(), identity.TenantID, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_ = a.insertAudit(r.Context(), identity, "policy.create", "policy", policy.Name, "allow", map[string]any{
			"policyId": policy.ID,
			"scope":    policy.Scope,
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"policy":          policy,
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	identity := mustIdentity(r.Context())
	if !isAdmin(identity) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
		return
	}

	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	filters := auditFilters{
		Actor:        strings.TrimSpace(r.URL.Query().Get("actor")),
		Action:       strings.TrimSpace(r.URL.Query().Get("action")),
		ResourceType: strings.TrimSpace(r.URL.Query().Get("resourceType")),
		ResourceName: strings.TrimSpace(r.URL.Query().Get("resourceName")),
		Decision:     strings.TrimSpace(r.URL.Query().Get("decision")),
		TraceID:      strings.TrimSpace(r.URL.Query().Get("traceId")),
		Limit:        limit,
	}
	if since, err := parseAuditTime(strings.TrimSpace(r.URL.Query().Get("since"))); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since timestamp"})
		return
	} else {
		filters.Since = since
	}
	if until, err := parseAuditTime(strings.TrimSpace(r.URL.Query().Get("until"))); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid until timestamp"})
		return
	} else {
		filters.Until = until
	}
	events, err := a.listAuditEvents(r.Context(), identity.TenantID, filters)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": identity,
		"auditEvents":     events,
		"filters":         filters,
	})
}

func (a *controlPlaneApp) handleAuditTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	identity := mustIdentity(r.Context())
	if !isAdmin(identity) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role is required"})
		return
	}
	traceID := strings.TrimPrefix(strings.TrimSpace(r.URL.Path), "/api/v1/audit-log/traces/")
	if traceID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	events, err := a.listAuditEvents(r.Context(), identity.TenantID, auditFilters{
		TraceID: traceID,
		Limit:   200,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"identityContext": identity,
		"traceId":         traceID,
		"auditEvents":     events,
	})
}

func (a *controlPlaneApp) handleHTTPToolEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": payload["message"],
		"echoed":  true,
		"source":  "control-plane-http-tool",
	})
}

func (a *controlPlaneApp) listModels(ctx context.Context, tenantID string) ([]modelRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, external_name, provider, model_name, coalesce(upstream_model, ''), coalesce(upstream_base_url, ''), max_rpm, max_tpm, enabled, created_at, updated_at
		from models
		where tenant_id = $1
		order by created_at asc
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []modelRecord
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func (a *controlPlaneApp) createModel(ctx context.Context, tenantID string, req createModelRequest) (modelRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		insert into models (tenant_id, provider, model_name, external_name, max_rpm, max_tpm, enabled, upstream_base_url, upstream_model, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), nullif($9, ''), now())
		returning id, tenant_id, external_name, provider, model_name, coalesce(upstream_model, ''), coalesce(upstream_base_url, ''), max_rpm, max_tpm, enabled, created_at, updated_at
	`, tenantID, strings.TrimSpace(req.Provider), strings.TrimSpace(req.ModelName), strings.TrimSpace(req.ExternalName), req.MaxRPM, req.MaxTPM, req.Enabled, strings.TrimSpace(req.UpstreamBaseURL), strings.TrimSpace(req.UpstreamModel))
	return scanModel(row)
}

func (a *controlPlaneApp) listTools(ctx context.Context, tenantID string) ([]toolRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, name, description, input_schema, output_schema, execution_mode, coalesce(target_url, ''), timeout_ms, http_headers, risk_tier, enabled, created_at, updated_at
		from tools
		where tenant_id = $1
		order by created_at asc
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tools []toolRecord
	for rows.Next() {
		tool, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, rows.Err()
}

func (a *controlPlaneApp) createTool(ctx context.Context, tenantID string, req createToolRequest) (toolRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		insert into tools (tenant_id, name, description, input_schema, output_schema, execution_mode, target_url, timeout_ms, http_headers, risk_tier, enabled, updated_at)
		values ($1, $2, $3, $4::jsonb, $5::jsonb, $6, nullif($7, ''), $8, $9::jsonb, $10, $11, now())
		returning id, tenant_id, name, description, input_schema, output_schema, execution_mode, coalesce(target_url, ''), timeout_ms, http_headers, risk_tier, enabled, created_at, updated_at
	`, tenantID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), string(req.InputSchema), string(req.OutputSchema), strings.TrimSpace(req.ExecutionMode), strings.TrimSpace(req.TargetURL), req.TimeoutMS, string(req.HTTPHeaders), strings.TrimSpace(req.RiskTier), req.Enabled)
	return scanTool(row)
}

func (a *controlPlaneApp) listPolicies(ctx context.Context, tenantID string) ([]policyRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
		from policies
		where tenant_id = $1
		order by
			case status
				when 'published' then 0
				when 'draft' then 1
				else 2
			end,
			version desc,
			created_at desc
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []policyRecord
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (a *controlPlaneApp) createPolicy(ctx context.Context, tenantID string, req createPolicyRequest) (policyRecord, error) {
	policyID, err := a.newDraftPolicyID(ctx)
	if err != nil {
		return policyRecord{}, err
	}
	regoSource := strings.ReplaceAll(req.RegoSource, "__POLICY_ID__", policyID)
	version, parentPolicyID, err := a.nextPolicyVersion(ctx, tenantID, strings.TrimSpace(req.Scope), strings.TrimSpace(req.TargetName))
	if err != nil {
		return policyRecord{}, err
	}
	validationErrors, compiledAt, err := a.validatePolicyModule(ctx, policyValidationInput{
		PolicyID:     policyID,
		Name:         strings.TrimSpace(req.Name),
		Scope:        strings.TrimSpace(req.Scope),
		TargetName:   strings.TrimSpace(req.TargetName),
		RegoPackage:  strings.TrimSpace(req.RegoPackage),
		RegoSource:   regoSource,
		TenantID:     tenantID,
		TenantSlug:   "",
		TargetStatus: "draft",
	})
	if err != nil {
		return policyRecord{}, err
	}

	row := a.db.QueryRowContext(ctx, `
		insert into policies (id, tenant_id, parent_policy_id, name, natural_language_rule, generation_prompt, generator_model, rego_package, rego_source, status, version, scope, target_name, enabled, validation_errors, last_compiled_at, updated_at)
		values ($1, $2, nullif($3, '')::uuid, $4, nullif($5, ''), nullif($5, ''), 'manual', $6, $7, $8, $9, $10, nullif($11, ''), $12, $13::jsonb, $14, now())
		returning id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
	`, policyID, tenantID, parentPolicyID, strings.TrimSpace(req.Name), strings.TrimSpace(req.NaturalLanguageRule), strings.TrimSpace(req.RegoPackage), regoSource, strings.TrimSpace(req.Status), version, strings.TrimSpace(req.Scope), strings.TrimSpace(req.TargetName), req.Enabled, string(mustJSON(validationErrors)), compiledAt)
	return scanPolicy(row)
}

func (a *controlPlaneApp) listAuditEvents(ctx context.Context, tenantID string, filters auditFilters) ([]auditEvent, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}
	rows, err := a.db.QueryContext(ctx, `
		select id, coalesce(tenant_id::text, ''), actor, action, resource_type, resource_name, coalesce(decision, ''), coalesce(trace_id, ''), metadata, created_at
		from audit_log
		where tenant_id = $1
			and ($2 = '' or actor ilike '%' || $2 || '%')
			and ($3 = '' or action ilike '%' || $3 || '%')
			and ($4 = '' or resource_type ilike '%' || $4 || '%')
			and ($5 = '' or resource_name ilike '%' || $5 || '%')
			and ($6 = '' or decision ilike '%' || $6 || '%')
			and ($7 = '' or trace_id = $7)
			and ($8::timestamptz is null or created_at >= $8)
			and ($9::timestamptz is null or created_at <= $9)
		order by created_at desc
		limit $10
	`, tenantID, filters.Actor, filters.Action, filters.ResourceType, filters.ResourceName, filters.Decision, filters.TraceID, filters.Since, filters.Until, filters.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []auditEvent
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func parseAuditTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	utc := parsed.UTC()
	return &utc, nil
}

func scanModel(scanner interface {
	Scan(dest ...any) error
}) (modelRecord, error) {
	var (
		model                modelRecord
		createdAt, updatedAt time.Time
	)
	err := scanner.Scan(
		&model.ID,
		&model.TenantID,
		&model.ExternalName,
		&model.Provider,
		&model.ModelName,
		&model.UpstreamModel,
		&model.UpstreamBaseURL,
		&model.MaxRPM,
		&model.MaxTPM,
		&model.Enabled,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return modelRecord{}, err
	}
	model.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	model.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return model, nil
}

func scanTool(scanner interface {
	Scan(dest ...any) error
}) (toolRecord, error) {
	var (
		tool                 toolRecord
		inputSchema          []byte
		outputSchema         []byte
		httpHeaders          []byte
		createdAt, updatedAt time.Time
	)
	err := scanner.Scan(
		&tool.ID,
		&tool.TenantID,
		&tool.Name,
		&tool.Description,
		&inputSchema,
		&outputSchema,
		&tool.ExecutionMode,
		&tool.TargetURL,
		&tool.TimeoutMS,
		&httpHeaders,
		&tool.RiskTier,
		&tool.Enabled,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return toolRecord{}, err
	}
	tool.InputSchema = append([]byte(nil), inputSchema...)
	tool.OutputSchema = append([]byte(nil), outputSchema...)
	tool.HTTPHeaders = append([]byte(nil), httpHeaders...)
	tool.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	tool.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return tool, nil
}

func scanPolicy(scanner interface {
	Scan(dest ...any) error
}) (policyRecord, error) {
	var (
		policy               policyRecord
		parentPolicyID       string
		generationPrompt     string
		generatorModel       string
		validationErrors     []byte
		publishedAt          sql.NullTime
		publishedBy          string
		lastCompiledAt       sql.NullTime
		createdAt, updatedAt time.Time
	)
	err := scanner.Scan(
		&policy.ID,
		&policy.TenantID,
		&parentPolicyID,
		&policy.Name,
		&policy.Scope,
		&policy.TargetName,
		&policy.NaturalLanguageRule,
		&generationPrompt,
		&generatorModel,
		&policy.RegoPackage,
		&policy.RegoSource,
		&policy.Status,
		&policy.Version,
		&policy.Enabled,
		&validationErrors,
		&publishedAt,
		&publishedBy,
		&lastCompiledAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return policyRecord{}, err
	}
	policy.ParentPolicyID = parentPolicyID
	policy.GenerationPrompt = generationPrompt
	policy.GeneratorModel = generatorModel
	_ = json.Unmarshal(validationErrors, &policy.ValidationErrors)
	policy.PublishedBy = publishedBy
	policy.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	policy.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	if publishedAt.Valid {
		policy.PublishedAt = publishedAt.Time.UTC().Format(time.RFC3339)
	}
	if lastCompiledAt.Valid {
		policy.LastCompiledAt = lastCompiledAt.Time.UTC().Format(time.RFC3339)
	}
	return policy, nil
}

func scanAuditEvent(scanner interface {
	Scan(dest ...any) error
}) (auditEvent, error) {
	var (
		event     auditEvent
		metadata  []byte
		createdAt time.Time
	)
	err := scanner.Scan(
		&event.ID,
		&event.TenantID,
		&event.Actor,
		&event.Action,
		&event.ResourceType,
		&event.ResourceName,
		&event.Decision,
		&event.TraceID,
		&metadata,
		&createdAt,
	)
	if err != nil {
		return auditEvent{}, err
	}
	event.Metadata = append([]byte(nil), metadata...)
	event.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return event, nil
}
