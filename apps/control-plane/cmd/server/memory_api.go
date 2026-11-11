package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func slogWarn(message string, err error) { slog.Warn(message, "error", err.Error()) }

func memoryRoutePermission(method, routePath string) (string, bool) {
	rest := strings.TrimPrefix(routePath, "/api/v1/memory/")
	switch method {
	case http.MethodGet:
		if rest == "retrievals" {
			return permMemoryManage, true
		}
		return permMemoryRead, true
	case http.MethodPut:
		if rest == "settings" || rest == "context-policies" {
			return permMemoryManage, true
		}
		return permMemoryWrite, rest == "preferences"
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
		return permMemoryWrite, true
	}
	return "", false
}

func (a *controlPlaneApp) writeMemoryError(w http.ResponseWriter, err error) {
	var typed *memoryError
	switch {
	case errors.As(err, &typed):
		writeJSON(w, typed.status, map[string]string{"error": typed.message})
	case errors.Is(err, errMemoryNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, errMemoryDisabled), errors.Is(err, errRetrievalDenied), errors.Is(err, errInjectionDenied):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, errNotApproved):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeAppError(w, err)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}

func pageLimit(r *http.Request, fallback, maximum int) int {
	if parsed, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && parsed > 0 {
		if parsed > maximum {
			return maximum
		}
		return parsed
	}
	return fallback
}

// handleMemory routes /api/v1/memory/*. withIdentity has already applied
// memoryRoutePermission; ownership checks happen per resource below.
func (a *controlPlaneApp) handleMemory(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/memory"), "/"), "/")
	resource, id, action := parts[0], "", ""
	if len(parts) > 1 {
		id = parts[1]
	}
	if len(parts) > 2 {
		action = parts[2]
	}
	if len(parts) > 3 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	switch {
	case resource == "settings" && id == "":
		a.handleMemorySettings(w, r, identity)
	case resource == "preferences" && id == "":
		a.handleMemoryPreferences(w, r, identity)
	case resource == "context-policies" && id == "":
		a.handleContextPolicies(w, r, identity)
	case (resource == "workspaces" || resource == "workflows") && id == "":
		a.handleMemoryContainers(w, r, identity, resource)
	case resource == "sessions" && id == "":
		a.handleMemorySessions(w, r, identity)
	case resource == "sessions":
		a.handleMemorySession(w, r, identity, id, action)
	case resource == "items" && id != "" && action == "":
		a.handleMemoryItem(w, r, identity, id)
	case resource == "checkpoints" && id != "":
		a.handleCheckpoint(w, r, identity, id, action)
	case resource == "continuations" && id == "" && r.Method == http.MethodPost:
		a.handleContinuation(w, r, identity)
	case resource == "retrievals" && id == "" && r.Method == http.MethodGet:
		a.handleRetrievals(w, r, identity)
	case resource == "retention" && id == "run" && action == "" && r.Method == http.MethodPost:
		// Runs the hourly retention job now. It only ever removes data that
		// is already past its retention period.
		if !hasPermission(identity, permMemoryManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied", "requiredPermission": permMemoryManage})
			return
		}
		if err := a.applyRetention(r.Context()); err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "retention.run", "memory", "retention", "allow", map[string]any{})
		writeJSON(w, http.StatusOK, map[string]any{"ran": true})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *controlPlaneApp) handleMemorySettings(w http.ResponseWriter, r *http.Request, identity identityContext) {
	switch r.Method {
	case http.MethodGet:
		settings, err := a.getMemorySettings(r.Context(), identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
	case http.MethodPut:
		settings, err := a.getMemorySettings(r.Context(), identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		if !decodeBody(w, r, &settings) {
			return
		}
		if settings.RetentionDays < 1 || settings.RetentionDays > 3650 || settings.MaxInjectionTokens < 0 || settings.MaxInjectionTokens > 100000 ||
			settings.MaxInjectionItems < 0 || settings.MaxInjectionItems > 100 || settings.AuditRetentionDays < 30 || settings.AuditRetentionDays > 3650 ||
			settings.UsageRetentionDays < 1 || settings.UsageRetentionDays > 3650 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "retentionDays 1-3650, maxInjectionTokens 0-100000, maxInjectionItems 0-100, auditRetentionDays 30-3650, usageRetentionDays 1-3650"})
			return
		}
		_, err = a.db.ExecContext(r.Context(), `
			insert into memory_settings (tenant_id, capture_enabled, retrieval_enabled, injection_enabled, retention_days,
			                             max_injection_tokens, max_injection_items, audit_retention_days, usage_retention_days, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			on conflict (tenant_id) do update set capture_enabled = excluded.capture_enabled, retrieval_enabled = excluded.retrieval_enabled,
				injection_enabled = excluded.injection_enabled, retention_days = excluded.retention_days,
				max_injection_tokens = excluded.max_injection_tokens, max_injection_items = excluded.max_injection_items,
				audit_retention_days = excluded.audit_retention_days, usage_retention_days = excluded.usage_retention_days,
				updated_by = excluded.updated_by, updated_at = now()`,
			identity.TenantID, settings.CaptureEnabled, settings.RetrievalEnabled, settings.InjectionEnabled, settings.RetentionDays,
			settings.MaxInjectionTokens, settings.MaxInjectionItems, settings.AuditRetentionDays, settings.UsageRetentionDays, identity.Actor)
		if err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "memory.settings.update", "memory", "settings", "allow", map[string]any{"settings": settings})
		writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleMemoryPreferences(w http.ResponseWriter, r *http.Request, identity identityContext) {
	switch r.Method {
	case http.MethodGet:
		prefs, err := a.getMemoryPreferences(r.Context(), identity.TenantID, identity.Actor)
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preferences": prefs})
	case http.MethodPut:
		var prefs memoryPreferences
		if !decodeBody(w, r, &prefs) {
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `
			insert into memory_preferences (tenant_id, actor, capture_opt_out, retrieval_opt_out) values ($1, $2, $3, $4)
			on conflict (tenant_id, actor) do update set capture_opt_out = excluded.capture_opt_out,
				retrieval_opt_out = excluded.retrieval_opt_out, updated_at = now()`,
			identity.TenantID, identity.Actor, prefs.CaptureOptOut, prefs.RetrievalOptOut); err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "memory.preferences.update", "memory", "preferences", "allow", map[string]any{"preferences": prefs})
		writeJSON(w, http.StatusOK, map[string]any{"preferences": prefs})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

type contextPolicy struct {
	Model             string  `json:"model"`
	NoticePercent     float64 `json:"noticePercent"`
	CheckpointPercent float64 `json:"checkpointPercent"`
	WarningPercent    float64 `json:"warningPercent"`
	HardBlock         bool    `json:"hardBlock"`
	BlockPercent      float64 `json:"blockPercent"`
}

var defaultContextPolicy = contextPolicy{Model: "*", NoticePercent: 80, CheckpointPercent: 85, WarningPercent: 95, BlockPercent: 100}

func (a *controlPlaneApp) handleContextPolicies(w http.ResponseWriter, r *http.Request, identity identityContext) {
	switch r.Method {
	case http.MethodGet:
		rows, err := a.db.QueryContext(r.Context(), `
			select model_external_name, notice_percent, checkpoint_percent, warning_percent, hard_block, block_percent
			from context_policies where tenant_id = $1 order by model_external_name`, identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		policies := []contextPolicy{}
		for rows.Next() {
			var policy contextPolicy
			if err := rows.Scan(&policy.Model, &policy.NoticePercent, &policy.CheckpointPercent, &policy.WarningPercent, &policy.HardBlock, &policy.BlockPercent); err != nil {
				writeAppError(w, err)
				return
			}
			policies = append(policies, policy)
		}
		writeJSON(w, http.StatusOK, map[string]any{"contextPolicies": policies, "default": defaultContextPolicy})
	case http.MethodPut:
		policy := defaultContextPolicy
		if !decodeBody(w, r, &policy) {
			return
		}
		policy.Model = strings.TrimSpace(policy.Model)
		if policy.Model == "" {
			policy.Model = "*"
		}
		if len(policy.Model) > 256 || !(policy.NoticePercent > 0 && policy.NoticePercent <= policy.CheckpointPercent &&
			policy.CheckpointPercent <= policy.WarningPercent && policy.WarningPercent <= policy.BlockPercent && policy.BlockPercent <= 100) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "thresholds must satisfy 0 < notice <= checkpoint <= warning <= block <= 100"})
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `
			insert into context_policies (tenant_id, model_external_name, notice_percent, checkpoint_percent, warning_percent, hard_block, block_percent, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, model_external_name) do update set notice_percent = excluded.notice_percent,
				checkpoint_percent = excluded.checkpoint_percent, warning_percent = excluded.warning_percent,
				hard_block = excluded.hard_block, block_percent = excluded.block_percent, updated_by = excluded.updated_by, updated_at = now()`,
			identity.TenantID, policy.Model, policy.NoticePercent, policy.CheckpointPercent, policy.WarningPercent, policy.HardBlock, policy.BlockPercent, identity.Actor); err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "context_policy.update", "context_policy", policy.Model, "allow", map[string]any{"policy": policy})
		writeJSON(w, http.StatusOK, map[string]any{"contextPolicy": policy})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleMemoryContainers serves workspaces and workflows, which only group
// sessions and hold no content.
func (a *controlPlaneApp) handleMemoryContainers(w http.ResponseWriter, r *http.Request, identity identityContext, resource string) {
	type container struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspaceId,omitempty"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status,omitempty"`
		CreatedBy   string `json:"createdBy"`
		CreatedAt   string `json:"createdAt"`
	}
	workflows := resource == "workflows"
	switch r.Method {
	case http.MethodGet:
		query := `select id, '', name, coalesce(description, ''), '', created_by, created_at from memory_workspaces where tenant_id = $1 and deleted_at is null order by created_at desc limit 500`
		if workflows {
			query = `select id, workspace_id::text, name, coalesce(objective, ''), status, created_by, created_at from memory_workflows where tenant_id = $1 and deleted_at is null order by created_at desc limit 500`
		}
		rows, err := a.db.QueryContext(r.Context(), query, identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		items := []container{}
		for rows.Next() {
			var item container
			var created time.Time
			if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Name, &item.Description, &item.Status, &item.CreatedBy, &created); err != nil {
				writeAppError(w, err)
				return
			}
			item.CreatedAt = created.UTC().Format(time.RFC3339)
			items = append(items, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{resource: items})
	case http.MethodPost:
		var req struct {
			WorkspaceID string `json:"workspaceId"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Objective   string `json:"objective"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if !validLength(req.Name, 1, 200) || len(req.Description) > 2000 || len(req.Objective) > 2000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 1-200 characters; description/objective at most 2000"})
			return
		}
		var id string
		var err error
		if workflows {
			if err = a.ownsRow(r.Context(), "memory_workspaces", identity.TenantID, req.WorkspaceID); err == nil {
				err = a.db.QueryRowContext(r.Context(), `
					insert into memory_workflows (tenant_id, workspace_id, name, objective, created_by) values ($1, $2, $3, nullif($4, ''), $5) returning id`,
					identity.TenantID, req.WorkspaceID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Objective), identity.Actor).Scan(&id)
			}
		} else {
			err = a.db.QueryRowContext(r.Context(), `
				insert into memory_workspaces (tenant_id, name, description, created_by) values ($1, $2, nullif($3, ''), $4) returning id`,
				identity.TenantID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), identity.Actor).Scan(&id)
		}
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "memory."+strings.TrimSuffix(resource, "s")+".create", "memory", id, "allow", map[string]any{"name": req.Name})
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": strings.TrimSpace(req.Name)})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleMemorySessions is retrieval level 1: a compact index of sessions,
// optionally filtered by a search over titles and item previews.
func (a *controlPlaneApp) handleMemorySessions(w http.ResponseWriter, r *http.Request, identity identityContext) {
	switch r.Method {
	case http.MethodGet:
		if _, err := a.retrievalAllowed(r.Context(), identity.TenantID, identity.Actor); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(query) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q must be at most 200 characters"})
			return
		}
		rows, err := a.db.QueryContext(r.Context(), `
			select `+memorySessionColumns+` from memory_sessions s
			where s.tenant_id = $1 and s.deleted_at is null
			  and (s.actor = $2 or $3)
			  and ($4 = '' or s.workspace_id::text = $4)
			  and ($5 = '' or s.workflow_id::text = $5)
			  and ($6 = '' or s.title ilike '%' || $6 || '%' or s.external_session_id = $6 or exists (
			        select 1 from memory_items i where i.session_id = s.id and i.deleted_at is null
			          and to_tsvector('simple', i.preview) @@ plainto_tsquery('simple', $6)))
			order by s.last_activity_at desc limit $7`,
			identity.TenantID, identity.Actor, hasPermission(identity, permMemoryManage),
			strings.TrimSpace(r.URL.Query().Get("workspaceId")), strings.TrimSpace(r.URL.Query().Get("workflowId")),
			strings.NewReplacer("%", "", "_", " ").Replace(query), pageLimit(r, 50, 200))
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		sessions := []memorySession{}
		for rows.Next() {
			session, err := scanMemorySession(rows)
			if err != nil {
				writeAppError(w, err)
				return
			}
			sessions = append(sessions, session)
		}
		if query != "" {
			a.recordRetrieval(r.Context(), identity.TenantID, identity.Actor, "search", "", "", query, requestID(r.Context()), nil, 0, 0)
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "level": "index"})
	case http.MethodPost:
		var req struct {
			ExternalSessionID string `json:"externalSessionId"`
			WorkspaceID       string `json:"workspaceId"`
			WorkflowID        string `json:"workflowId"`
			Title             string `json:"title"`
			Model             string `json:"model"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if _, err := a.captureAllowed(r.Context(), identity.TenantID, identity.Actor); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		session, err := a.ensureMemorySession(r.Context(), identity.TenantID, identity.Actor, strings.TrimSpace(req.ExternalSessionID),
			sessionOptions{WorkspaceID: req.WorkspaceID, WorkflowID: req.WorkflowID, Title: req.Title, Model: req.Model})
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session": session})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleMemorySession(w http.ResponseWriter, r *http.Request, identity identityContext, sessionID, action string) {
	session, err := a.readableSession(r.Context(), identity, sessionID)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	owner := session.Actor == identity.Actor
	switch {
	case action == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"session": session})
	case action == "" && r.Method == http.MethodDelete:
		if !owner && !hasPermission(identity, permMemoryManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied"})
			return
		}
		if err := a.deleteMemorySession(r.Context(), identity.TenantID, identity.Actor, session.ID); err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "sessionId": session.ID})
	case action == "timeline" && r.Method == http.MethodGet:
		// Retrieval level 2: ordered previews, no content.
		if _, err := a.retrievalAllowed(r.Context(), identity.TenantID, identity.Actor); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		rows, err := a.db.QueryContext(r.Context(), `
			select `+memoryItemColumns+` from memory_items i
			where i.session_id = $1 and i.tenant_id = $2 and i.deleted_at is null
			  and (i.sensitivity <> 'restricted' or i.actor = $3)
			order by i.created_at asc limit $4`, session.ID, identity.TenantID, identity.Actor, pageLimit(r, 200, 1000))
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		items, ids := []memoryItem{}, []string{}
		for rows.Next() {
			item, err := scanMemoryItem(rows)
			if err != nil {
				writeAppError(w, err)
				return
			}
			items = append(items, item)
			ids = append(ids, item.ID)
		}
		a.recordRetrieval(r.Context(), identity.TenantID, identity.Actor, "timeline", session.ID, "", "", requestID(r.Context()), ids, 0, 0)
		writeJSON(w, http.StatusOK, map[string]any{"session": session, "timeline": items, "level": "timeline"})
	case (action == "items" || action == "summaries") && r.Method == http.MethodPost:
		if !owner {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the session owner can add records"})
			return
		}
		a.handleAddItem(w, r, identity, session, action == "summaries")
	case action == "checkpoints" && r.Method == http.MethodGet:
		rows, err := a.db.QueryContext(r.Context(), `
			select `+checkpointColumns+` from memory_checkpoints c
			where c.session_id = $1 and c.tenant_id = $2 and c.deleted_at is null order by c.created_at desc limit 100`, session.ID, identity.TenantID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer rows.Close()
		checkpoints := []memoryCheckpoint{}
		for rows.Next() {
			cp, err := a.scanCheckpoint(rows, false)
			if err != nil {
				writeAppError(w, err)
				return
			}
			checkpoints = append(checkpoints, cp)
		}
		writeJSON(w, http.StatusOK, map[string]any{"checkpoints": checkpoints})
	case action == "checkpoints" && r.Method == http.MethodPost:
		if !owner {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the session owner can create checkpoints"})
			return
		}
		var req checkpointRequest
		if !decodeBody(w, r, &req) {
			return
		}
		cp, _, err := a.createCheckpoint(r.Context(), identity.TenantID, identity.Actor, session, req, "manual")
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"checkpoint": cp})
	case action == "export" && r.Method == http.MethodGet:
		a.handleExport(w, r, identity, session)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleAddItem(w http.ResponseWriter, r *http.Request, identity identityContext, session memorySession, summary bool) {
	var req struct {
		Kind          string         `json:"kind"`
		Role          string         `json:"role"`
		Origin        string         `json:"origin"`
		Sensitivity   string         `json:"sensitivity"`
		Content       string         `json:"content"`
		Generated     bool           `json:"generated"`
		Generator     string         `json:"generator"`
		SourceItemIDs []string       `json:"sourceItemIds"`
		Provenance    map[string]any `json:"provenance"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	settings, err := a.captureAllowed(r.Context(), identity.TenantID, identity.Actor)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	in := newMemoryItem{Kind: req.Kind, Role: req.Role, Origin: req.Origin, Sensitivity: req.Sensitivity, Content: req.Content, Position: -1}
	in.Provenance = map[string]any{"recordedBy": identity.Actor, "authMethod": identity.AuthMethod, "requestId": requestID(r.Context())}
	if ref, ok := req.Provenance["reference"].(string); ok && len(ref) <= 2000 {
		in.Provenance["reference"], _ = redactSecrets(ref)
	}
	if summary {
		// A summary must say what it is based on; without sources it would be
		// an unsupported claim sitting next to evidence.
		if len(req.SourceItemIDs) == 0 || len(req.SourceItemIDs) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sourceItemIds is required for a summary (1-200 stored item ids)"})
			return
		}
		if err := a.verifyEvidence(r.Context(), a.db, identity.TenantID, session.ID, req.SourceItemIDs, false); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		in.Kind, in.Sources = "summary", req.SourceItemIDs
		in.Origin = "user"
		if req.Generated {
			in.Origin = "generated"
			in.Provenance["generator"] = previewOf(req.Generator, 200)
		}
	} else {
		if req.Kind == "summary" || req.Origin == "generated" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "summaries and generated text must be posted to /summaries with sourceItemIds"})
			return
		}
		if in.Origin == "" {
			in.Origin = "user"
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAppError(w, err)
		return
	}
	defer tx.Rollback()
	item, _, err := a.addMemoryItem(r.Context(), tx, identity.TenantID, identity.Actor, session, settings, in)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	if err := insertAuditRecord(r.Context(), tx, identity.TenantID, identity.Actor, "memory.item.create", "memory", item.ID, "allow", map[string]any{
		"sessionId": session.ID, "kind": item.Kind, "origin": item.Origin, "sensitivity": item.Sensitivity, "redactions": item.RedactionCount,
	}); err != nil {
		writeAppError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

// handleMemoryItem is retrieval level 3: one item with its content.
func (a *controlPlaneApp) handleMemoryItem(w http.ResponseWriter, r *http.Request, identity identityContext, itemID string) {
	item, err := a.loadMemoryItem(r.Context(), a.db, identity.TenantID, itemID, false)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	session, err := a.readableSession(r.Context(), identity, item.SessionID)
	if err != nil || (item.Sensitivity == "restricted" && item.Actor != identity.Actor) {
		a.writeMemoryError(w, errMemoryNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := a.retrievalAllowed(r.Context(), identity.TenantID, identity.Actor); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		full, err := a.loadMemoryItem(r.Context(), a.db, identity.TenantID, itemID, true)
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		a.recordRetrieval(r.Context(), identity.TenantID, identity.Actor, "detail", session.ID, "", "", requestID(r.Context()), []string{item.ID}, 0, full.TokenEstimate)
		writeJSON(w, http.StatusOK, map[string]any{"item": full, "level": "detail"})
	case http.MethodDelete:
		if session.Actor != identity.Actor && !hasPermission(identity, permMemoryManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied"})
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `
			update memory_items set deleted_at = now(), deleted_by = $3, content_sealed = null, preview = '', provenance = '{}'::jsonb
			where id = $1 and tenant_id = $2`, item.ID, identity.TenantID, identity.Actor); err != nil {
			writeAppError(w, err)
			return
		}
		_ = a.insertAudit(r.Context(), identity, "memory.item.delete", "memory", item.ID, "allow", map[string]any{"sessionId": session.ID})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "itemId": item.ID})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *controlPlaneApp) handleCheckpoint(w http.ResponseWriter, r *http.Request, identity identityContext, checkpointID, action string) {
	cp, session, err := a.readableCheckpoint(r.Context(), identity, checkpointID, true)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	owner := session.Actor == identity.Actor
	switch {
	case action == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"checkpoint": cp})
	case action == "" && r.Method == http.MethodPatch:
		if !owner {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the session owner can edit a checkpoint"})
			return
		}
		if cp.Status != "draft" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "only draft checkpoints can be edited"})
			return
		}
		var req struct {
			Content *checkpointContent `json:"content"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if req.Content == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
			return
		}
		if err := normalizeCheckpointContent(req.Content); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		if err := a.verifyEvidence(r.Context(), a.db, identity.TenantID, session.ID, req.Content.EvidenceRefs, false); err != nil {
			a.writeMemoryError(w, err)
			return
		}
		tx, err := a.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer tx.Rollback()
		if err := a.storeCheckpointContent(r.Context(), tx, identity.TenantID, cp.ID, *req.Content); err != nil {
			writeAppError(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `update memory_checkpoints set edited_by_reviewer = true where id = $1`, cp.ID); err != nil {
			writeAppError(w, err)
			return
		}
		if err := insertAuditRecord(r.Context(), tx, identity.TenantID, identity.Actor, "memory.checkpoint.edit", "checkpoint", cp.ID, "allow", map[string]any{"sessionId": session.ID}); err != nil {
			writeAppError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeAppError(w, err)
			return
		}
		updated, _, err := a.readableCheckpoint(r.Context(), identity, cp.ID, true)
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checkpoint": updated})
	case (action == "approve" || action == "reject") && r.Method == http.MethodPost:
		if !owner {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the session owner can review a checkpoint"})
			return
		}
		if cp.Status != "draft" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "only draft checkpoints can be reviewed"})
			return
		}
		status := "approved"
		if action == "reject" {
			status = "rejected"
		}
		tx, err := a.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeAppError(w, err)
			return
		}
		defer tx.Rollback()
		if status == "approved" {
			if _, err := tx.ExecContext(r.Context(), `
				update memory_checkpoints set status = 'superseded' where session_id = $1 and status = 'approved'`, session.ID); err != nil {
				writeAppError(w, err)
				return
			}
		}
		result, err := tx.ExecContext(r.Context(), `
			update memory_checkpoints set status = $1, reviewed_by = $2, reviewed_at = now() where id = $3 and status = 'draft'`, status, identity.Actor, cp.ID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "checkpoint was reviewed by another request"})
			return
		}
		if err := insertAuditRecord(r.Context(), tx, identity.TenantID, identity.Actor, "memory.checkpoint."+action, "checkpoint", cp.ID, "allow", map[string]any{
			"sessionId": session.ID, "origin": cp.Origin, "edited": cp.EditedByReviewer,
		}); err != nil {
			writeAppError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeAppError(w, err)
			return
		}
		updated, _, err := a.readableCheckpoint(r.Context(), identity, cp.ID, true)
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checkpoint": updated})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleContinuation starts a new session from an approved checkpoint and
// returns exactly what was carried forward.
func (a *controlPlaneApp) handleContinuation(w http.ResponseWriter, r *http.Request, identity identityContext) {
	var req struct {
		CheckpointID      string `json:"checkpointId"`
		ExternalSessionID string `json:"externalSessionId"`
		TokenBudget       int    `json:"tokenBudget"`
		Title             string `json:"title"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.TokenBudget < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tokenBudget must not be negative"})
		return
	}
	settings, err := a.retrievalAllowed(r.Context(), identity.TenantID, identity.Actor)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	carried, previous, err := a.buildCarryForward(r.Context(), identity, strings.TrimSpace(req.CheckpointID), req.TokenBudget, settings)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	next, err := a.ensureMemorySession(r.Context(), identity.TenantID, identity.Actor, strings.TrimSpace(req.ExternalSessionID), sessionOptions{
		WorkspaceID: previous.WorkspaceID, WorkflowID: previous.WorkflowID, Title: firstNonEmptyString(req.Title, previous.Title),
		Model: previous.Model, ContinuedFrom: carried.Checkpoint.ID,
	})
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	if next.ID == previous.ID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "externalSessionId must be a new session id"})
		return
	}
	ids := make([]string, 0, len(carried.Evidence))
	for _, item := range carried.Evidence {
		ids = append(ids, item.ID)
	}
	a.recordRetrieval(r.Context(), identity.TenantID, identity.Actor, "continuation", next.ID, carried.Checkpoint.ID, "", requestID(r.Context()), ids, carried.TokenBudget, carried.TokensUsed)
	writeJSON(w, http.StatusCreated, map[string]any{"session": next, "previousSessionId": previous.ID, "carriedForward": carried})
}

func (a *controlPlaneApp) handleExport(w http.ResponseWriter, r *http.Request, identity identityContext, session memorySession) {
	rows, err := a.db.QueryContext(r.Context(), `
		select i.id from memory_items i where i.session_id = $1 and i.tenant_id = $2 and i.deleted_at is null
		  and (i.sensitivity <> 'restricted' or i.actor = $3) order by i.created_at asc limit 5000`, session.ID, identity.TenantID, identity.Actor)
	if err != nil {
		writeAppError(w, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeAppError(w, err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	items := []memoryItem{}
	for _, id := range ids {
		item, err := a.loadMemoryItem(r.Context(), a.db, identity.TenantID, id, true)
		if err != nil {
			a.writeMemoryError(w, err)
			return
		}
		items = append(items, item)
	}
	checkpointRows, err := a.db.QueryContext(r.Context(), `
		select `+checkpointColumns+` from memory_checkpoints c where c.session_id = $1 and c.tenant_id = $2 and c.deleted_at is null order by c.created_at`, session.ID, identity.TenantID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	defer checkpointRows.Close()
	checkpoints := []memoryCheckpoint{}
	for checkpointRows.Next() {
		cp, err := a.scanCheckpoint(checkpointRows, true)
		if err != nil {
			writeAppError(w, err)
			return
		}
		checkpoints = append(checkpoints, cp)
	}
	a.recordRetrieval(r.Context(), identity.TenantID, identity.Actor, "export", session.ID, "", "", requestID(r.Context()), ids, 0, 0)
	w.Header().Set("Content-Disposition", `attachment; filename="suez-memory-session.json"`)
	writeJSON(w, http.StatusOK, map[string]any{
		"exportedAt": time.Now().UTC().Format(time.RFC3339), "session": session, "items": items, "checkpoints": checkpoints,
		"note": "Items with origin 'generated' are AI-written text, not evidence. Secrets detected at capture were redacted before storage.",
	})
}

func (a *controlPlaneApp) handleRetrievals(w http.ResponseWriter, r *http.Request, identity identityContext) {
	rows, err := a.db.QueryContext(r.Context(), `
		select id, actor, purpose, coalesce(session_id::text, ''), coalesce(checkpoint_id::text, ''), coalesce(query, ''),
		       cardinality(item_ids), coalesce(token_budget, 0), coalesce(tokens_used, 0), coalesce(trace_id, ''), created_at
		from memory_retrievals where tenant_id = $1 order by created_at desc limit $2`, identity.TenantID, pageLimit(r, 100, 500))
	if err != nil {
		writeAppError(w, err)
		return
	}
	defer rows.Close()
	type retrieval struct {
		ID, Actor, Purpose, SessionID, CheckpointID, Query string
		ItemCount, TokenBudget, TokensUsed                 int
		TraceID, CreatedAt                                 string
	}
	out := []map[string]any{}
	for rows.Next() {
		var item retrieval
		var created time.Time
		if err := rows.Scan(&item.ID, &item.Actor, &item.Purpose, &item.SessionID, &item.CheckpointID, &item.Query, &item.ItemCount, &item.TokenBudget, &item.TokensUsed, &item.TraceID, &created); err != nil {
			writeAppError(w, err)
			return
		}
		out = append(out, map[string]any{"id": item.ID, "actor": item.Actor, "purpose": item.Purpose, "sessionId": item.SessionID,
			"checkpointId": item.CheckpointID, "query": item.Query, "itemCount": item.ItemCount, "tokenBudget": item.TokenBudget,
			"tokensUsed": item.TokensUsed, "traceId": item.TraceID, "createdAt": created.UTC().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"retrievals": out})
}

// ---- Internal endpoints used by the gateway (X-SUEZ-Internal-Token) ----

type internalCaller struct {
	TenantID string   `json:"tenantId"`
	Actor    string   `json:"actor"`
	Scopes   []string `json:"scopes"`
}

func (c internalCaller) identity() identityContext {
	return identityContext{TenantID: c.TenantID, Actor: c.Actor, Scopes: c.Scopes, Permissions: permissionsForScopes(c.Scopes)}
}

func (a *controlPlaneApp) handleInternalMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	switch strings.TrimPrefix(path.Clean(r.URL.Path), "/internal/memory/") {
	case "capture":
		a.handleInternalCapture(w, r)
	case "checkpoint-draft":
		a.handleInternalCheckpointDraft(w, r)
	case "injection":
		a.handleInternalInjection(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

type capturedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// handleInternalCapture stores the conversation of one gateway chat call.
// Clients resend the whole conversation each time, so messages are keyed by
// position and content: only new ones are inserted.
func (a *controlPlaneApp) handleInternalCapture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		internalCaller
		ExternalSessionID string            `json:"externalSessionId"`
		Model             string            `json:"model"`
		Provider          string            `json:"provider"`
		TraceID           string            `json:"traceId"`
		Messages          []capturedMessage `json:"messages"`
		Response          *capturedMessage  `json:"response"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.TenantID == "" || req.Actor == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tenantId and actor are required"})
		return
	}
	settings, err := a.captureAllowed(r.Context(), req.TenantID, req.Actor)
	if errors.Is(err, errMemoryDisabled) {
		writeJSON(w, http.StatusOK, map[string]any{"captured": 0, "reason": "capture disabled"})
		return
	}
	if err != nil {
		writeAppError(w, err)
		return
	}
	title := ""
	for _, message := range req.Messages {
		if message.Role == "user" {
			redacted, _ := redactSecrets(message.Content)
			title = previewOf(redacted, 80)
			break
		}
	}
	session, err := a.ensureMemorySession(r.Context(), req.TenantID, req.Actor, req.ExternalSessionID, sessionOptions{Model: req.Model, Title: title})
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAppError(w, err)
		return
	}
	defer tx.Rollback()
	captured := 0
	add := func(position int, message capturedMessage, origin string) error {
		if strings.TrimSpace(message.Content) == "" {
			return nil
		}
		_, inserted, err := a.addMemoryItem(r.Context(), tx, req.TenantID, req.Actor, session, settings, newMemoryItem{
			Kind: "message", Role: message.Role, Origin: origin, Content: message.Content, Position: position,
			Provenance: map[string]any{"source": "gateway", "traceId": req.TraceID, "model": req.Model, "provider": req.Provider},
		})
		if inserted {
			captured++
		}
		return err
	}
	for position, message := range req.Messages {
		origin := "user"
		switch message.Role {
		case "assistant":
			origin = "provider"
		case "tool":
			origin = "tool"
		case "system":
			origin = "system"
		}
		if err := add(position, message, origin); err != nil {
			a.writeMemoryError(w, err)
			return
		}
	}
	if req.Response != nil {
		if err := add(len(req.Messages), *req.Response, "provider"); err != nil {
			a.writeMemoryError(w, err)
			return
		}
	}
	if captured > 0 {
		if err := insertAuditRecord(r.Context(), tx, req.TenantID, req.Actor, "memory.capture", "memory", session.ID, "allow", map[string]any{
			"sessionId": session.ID, "itemCount": captured, "traceId": req.TraceID,
		}); err != nil {
			writeAppError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"captured": captured, "sessionId": session.ID})
}

func (a *controlPlaneApp) handleInternalCheckpointDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		internalCaller
		ExternalSessionID string  `json:"externalSessionId"`
		ContextPercent    float64 `json:"contextPercent"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	var sessionID string
	err := a.db.QueryRowContext(r.Context(), `
		select id from memory_sessions where tenant_id = $1 and actor = $2 and external_session_id = $3 and deleted_at is null`,
		req.TenantID, req.Actor, req.ExternalSessionID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing was captured for this session, so there is nothing to draft from.
		writeJSON(w, http.StatusOK, map[string]any{"created": false, "reason": "no stored session"})
		return
	}
	if err != nil {
		writeAppError(w, err)
		return
	}
	identity := req.identity()
	identity.Permissions = append(identity.Permissions, permMemoryRead)
	session, err := a.readableSession(r.Context(), identity, sessionID)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	cp, created, err := a.createCheckpoint(r.Context(), req.TenantID, req.Actor, session, checkpointRequest{Generate: true, ContextPercent: &req.ContextPercent}, "threshold")
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "checkpointId": cp.ID, "status": cp.Status})
}

// handleInternalInjection returns the text the gateway may add to a chat
// request. It is refused unless the tenant enabled injection, the user has
// not opted out, and the checkpoint is approved and readable by the caller.
func (a *controlPlaneApp) handleInternalInjection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		internalCaller
		CheckpointID string `json:"checkpointId"`
		TokenBudget  int    `json:"tokenBudget"`
		TraceID      string `json:"traceId"`
		SessionID    string `json:"externalSessionId"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	settings, err := a.retrievalAllowed(r.Context(), req.TenantID, req.Actor)
	if err == nil && !settings.InjectionEnabled {
		err = errInjectionDenied
	}
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	carried, session, err := a.buildCarryForward(r.Context(), req.identity(), req.CheckpointID, req.TokenBudget, settings)
	if err != nil {
		a.writeMemoryError(w, err)
		return
	}
	ids := make([]string, 0, len(carried.Evidence))
	for _, item := range carried.Evidence {
		ids = append(ids, item.ID)
	}
	a.recordRetrieval(r.Context(), req.TenantID, req.Actor, "injection", session.ID, carried.Checkpoint.ID, "", req.TraceID, ids, carried.TokenBudget, carried.TokensUsed)
	writeJSON(w, http.StatusOK, map[string]any{
		"checkpointId": carried.Checkpoint.ID, "evidenceIds": ids, "omittedEvidenceIds": carried.OmittedEvidence,
		"tokenBudget": carried.TokenBudget, "tokensUsed": carried.TokensUsed, "contextText": carried.ContextText,
	})
}
