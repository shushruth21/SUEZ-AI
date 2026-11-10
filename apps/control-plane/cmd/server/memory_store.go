package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SUEZ Memory.
//
// Stored items are evidence (origin user, provider, tool, system) or
// generated text (origin generated). Summaries and checkpoints must name the
// stored items they are based on; the API never presents generated text as
// verified. Every read above the session index is written to
// memory_retrievals and the audit log.

var (
	errMemoryNotFound  = errors.New("not found")
	errMemoryDisabled  = errors.New("memory capture is disabled for this tenant or user")
	errRetrievalDenied = errors.New("memory retrieval is disabled for this tenant or user")
	errInjectionDenied = errors.New("memory injection is not enabled for this tenant")
	errNotApproved     = errors.New("checkpoint is not approved")
)

type memoryError struct {
	status  int
	message string
}

func (e *memoryError) Error() string { return e.message }

func badMemoryRequest(format string, args ...any) error {
	return &memoryError{status: 400, message: fmt.Sprintf(format, args...)}
}

type memorySettings struct {
	CaptureEnabled     bool `json:"captureEnabled"`
	RetrievalEnabled   bool `json:"retrievalEnabled"`
	InjectionEnabled   bool `json:"injectionEnabled"`
	RetentionDays      int  `json:"retentionDays"`
	MaxInjectionTokens int  `json:"maxInjectionTokens"`
	MaxInjectionItems  int  `json:"maxInjectionItems"`
	AuditRetentionDays int  `json:"auditRetentionDays"`
	UsageRetentionDays int  `json:"usageRetentionDays"`
}

// Capture and injection are off until a tenant admin turns them on.
var defaultMemorySettings = memorySettings{
	RetrievalEnabled: true, RetentionDays: 90, MaxInjectionTokens: 2000, MaxInjectionItems: 8,
	AuditRetentionDays: 365, UsageRetentionDays: 90,
}

type memoryPreferences struct {
	CaptureOptOut   bool `json:"captureOptOut"`
	RetrievalOptOut bool `json:"retrievalOptOut"`
}

type memorySession struct {
	ID                        string `json:"id"`
	TenantID                  string `json:"tenantId"`
	WorkspaceID               string `json:"workspaceId,omitempty"`
	WorkflowID                string `json:"workflowId,omitempty"`
	ExternalSessionID         string `json:"externalSessionId"`
	Actor                     string `json:"actor"`
	Title                     string `json:"title,omitempty"`
	Model                     string `json:"model,omitempty"`
	ContinuedFromCheckpointID string `json:"continuedFromCheckpointId,omitempty"`
	Status                    string `json:"status"`
	StartedAt                 string `json:"startedAt"`
	LastActivityAt            string `json:"lastActivityAt"`
	ItemCount                 int    `json:"itemCount"`
}

type memoryItem struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"sessionId"`
	Kind           string          `json:"kind"`
	Role           string          `json:"role,omitempty"`
	Actor          string          `json:"actor"`
	Origin         string          `json:"origin"`
	Generated      bool            `json:"generated"`
	Sensitivity    string          `json:"sensitivity"`
	Provenance     json.RawMessage `json:"provenance"`
	SourceItemIDs  []string        `json:"sourceItemIds,omitempty"`
	Preview        string          `json:"preview"`
	Content        *string         `json:"content,omitempty"`
	TokenEstimate  int             `json:"tokenEstimate"`
	RedactionCount int             `json:"redactionCount"`
	CreatedAt      string          `json:"createdAt"`
	ExpiresAt      string          `json:"expiresAt,omitempty"`
}

type artifactRef struct {
	Label     string `json:"label"`
	Reference string `json:"reference"`
}

// checkpointContent is the structured hand-off record. evidenceRefs are ids of
// stored memory items that support the statements above them.
type checkpointContent struct {
	Objective     string        `json:"objective"`
	CompletedWork []string      `json:"completedWork"`
	Decisions     []string      `json:"decisions"`
	CurrentState  string        `json:"currentState"`
	Assumptions   []string      `json:"assumptions"`
	OpenQuestions []string      `json:"openQuestions"`
	NextActions   []string      `json:"nextActions"`
	Artifacts     []artifactRef `json:"artifacts"`
	ToolOutputs   []string      `json:"toolOutputs"`
	Risks         []string      `json:"risks"`
	EvidenceRefs  []string      `json:"evidenceRefs"`
}

type memoryCheckpoint struct {
	ID               string             `json:"id"`
	SessionID        string             `json:"sessionId"`
	Status           string             `json:"status"`
	Origin           string             `json:"origin"`
	Generated        bool               `json:"generated"`
	Generator        string             `json:"generator,omitempty"`
	Trigger          string             `json:"trigger"`
	ContextPercent   *float64           `json:"contextPercent,omitempty"`
	Content          *checkpointContent `json:"content,omitempty"`
	EditedByReviewer bool               `json:"editedByReviewer"`
	CreatedBy        string             `json:"createdBy"`
	CreatedAt        string             `json:"createdAt"`
	ReviewedBy       string             `json:"reviewedBy,omitempty"`
	ReviewedAt       string             `json:"reviewedAt,omitempty"`
	Notice           string             `json:"notice"`
}

const (
	generatedCheckpointNotice = "This checkpoint was drafted automatically from stored session records. It is a draft, not verified fact: review and correct it before approving. A checkpoint reduces what is lost between sessions; it does not prevent model errors."
	manualCheckpointNotice    = "This checkpoint was written by a person. Statements are only as reliable as the evidence they reference."
)

func (a *controlPlaneApp) getMemorySettings(ctx context.Context, tenantID string) (memorySettings, error) {
	settings := defaultMemorySettings
	err := a.db.QueryRowContext(ctx, `
		select capture_enabled, retrieval_enabled, injection_enabled, retention_days, max_injection_tokens,
		       max_injection_items, audit_retention_days, usage_retention_days
		from memory_settings where tenant_id = $1`, tenantID).Scan(
		&settings.CaptureEnabled, &settings.RetrievalEnabled, &settings.InjectionEnabled, &settings.RetentionDays,
		&settings.MaxInjectionTokens, &settings.MaxInjectionItems, &settings.AuditRetentionDays, &settings.UsageRetentionDays)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultMemorySettings, nil
	}
	return settings, err
}

func (a *controlPlaneApp) getMemoryPreferences(ctx context.Context, tenantID, actor string) (memoryPreferences, error) {
	var prefs memoryPreferences
	err := a.db.QueryRowContext(ctx, `
		select capture_opt_out, retrieval_opt_out from memory_preferences where tenant_id = $1 and actor = $2`,
		tenantID, actor).Scan(&prefs.CaptureOptOut, &prefs.RetrievalOptOut)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryPreferences{}, nil
	}
	return prefs, err
}

func (a *controlPlaneApp) captureAllowed(ctx context.Context, tenantID, actor string) (memorySettings, error) {
	settings, err := a.getMemorySettings(ctx, tenantID)
	if err != nil {
		return settings, err
	}
	prefs, err := a.getMemoryPreferences(ctx, tenantID, actor)
	if err != nil {
		return settings, err
	}
	if !settings.CaptureEnabled || prefs.CaptureOptOut {
		return settings, errMemoryDisabled
	}
	return settings, nil
}

func (a *controlPlaneApp) retrievalAllowed(ctx context.Context, tenantID, actor string) (memorySettings, error) {
	settings, err := a.getMemorySettings(ctx, tenantID)
	if err != nil {
		return settings, err
	}
	prefs, err := a.getMemoryPreferences(ctx, tenantID, actor)
	if err != nil {
		return settings, err
	}
	if !settings.RetrievalEnabled || prefs.RetrievalOptOut {
		return settings, errRetrievalDenied
	}
	return settings, nil
}

const memorySessionColumns = `s.id, s.tenant_id, coalesce(s.workspace_id::text, ''), coalesce(s.workflow_id::text, ''),
	s.external_session_id, s.actor, coalesce(s.title, ''), coalesce(s.model, ''),
	coalesce(s.continued_from_checkpoint_id::text, ''), s.status, s.started_at, s.last_activity_at,
	(select count(*) from memory_items i where i.session_id = s.id and i.deleted_at is null)`

func scanMemorySession(scanner interface{ Scan(...any) error }) (memorySession, error) {
	var session memorySession
	var started, activity time.Time
	if err := scanner.Scan(&session.ID, &session.TenantID, &session.WorkspaceID, &session.WorkflowID, &session.ExternalSessionID,
		&session.Actor, &session.Title, &session.Model, &session.ContinuedFromCheckpointID, &session.Status, &started, &activity, &session.ItemCount); err != nil {
		return memorySession{}, err
	}
	session.StartedAt = started.UTC().Format(time.RFC3339)
	session.LastActivityAt = activity.UTC().Format(time.RFC3339)
	return session, nil
}

type sessionOptions struct {
	WorkspaceID, WorkflowID, Title, Model, ContinuedFrom string
}

// ensureMemorySession returns the caller's session for externalID, creating
// it if needed. A session belongs to one tenant and one actor.
func (a *controlPlaneApp) ensureMemorySession(ctx context.Context, tenantID, actor, externalID string, opts sessionOptions) (memorySession, error) {
	if !sessionIDPattern.MatchString(externalID) {
		return memorySession{}, badMemoryRequest("externalSessionId must be 1-128 characters from A-Z a-z 0-9 . _ : - starting with a letter or digit")
	}
	if opts.WorkspaceID != "" {
		if err := a.ownsRow(ctx, "memory_workspaces", tenantID, opts.WorkspaceID); err != nil {
			return memorySession{}, err
		}
	}
	if opts.WorkflowID != "" {
		if err := a.ownsRow(ctx, "memory_workflows", tenantID, opts.WorkflowID); err != nil {
			return memorySession{}, err
		}
	}
	row := a.db.QueryRowContext(ctx, `
		insert into memory_sessions as s (tenant_id, actor, external_session_id, workspace_id, workflow_id, title, model, continued_from_checkpoint_id)
		values ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, nullif($6, ''), nullif($7, ''), nullif($8, '')::uuid)
		on conflict (tenant_id, actor, external_session_id) do update
			set last_activity_at = now(),
			    model = coalesce(nullif(excluded.model, ''), s.model),
			    title = coalesce(s.title, excluded.title),
			    workspace_id = coalesce(s.workspace_id, excluded.workspace_id),
			    workflow_id = coalesce(s.workflow_id, excluded.workflow_id)
		returning `+memorySessionColumns,
		tenantID, actor, externalID, opts.WorkspaceID, opts.WorkflowID, previewOf(opts.Title, 200), opts.Model, opts.ContinuedFrom)
	session, err := scanMemorySession(row)
	if err != nil {
		return memorySession{}, err
	}
	var deleted sql.NullTime
	if err := a.db.QueryRowContext(ctx, `select deleted_at from memory_sessions where id = $1`, session.ID).Scan(&deleted); err != nil {
		return memorySession{}, err
	}
	if deleted.Valid {
		return memorySession{}, &memoryError{status: 409, message: "this session was deleted; use a new session id"}
	}
	return session, nil
}

// ownsRow checks that a workspace or workflow id belongs to the tenant. table
// is always a constant from this file.
func (a *controlPlaneApp) ownsRow(ctx context.Context, table, tenantID, id string) error {
	var found string
	err := a.db.QueryRowContext(ctx, `select id from `+table+` where id = $1 and tenant_id = $2 and deleted_at is null`, id, tenantID).Scan(&found)
	if err != nil {
		if dbStatus(err) == 404 {
			return errMemoryNotFound
		}
		return err
	}
	return nil
}

// readableSession loads a session the identity may read: its own, or any
// session in the tenant for holders of memory.manage.
func (a *controlPlaneApp) readableSession(ctx context.Context, identity identityContext, sessionID string) (memorySession, error) {
	session, err := scanMemorySession(a.db.QueryRowContext(ctx, `
		select `+memorySessionColumns+` from memory_sessions s
		where s.id = $1 and s.tenant_id = $2 and s.deleted_at is null`, sessionID, identity.TenantID))
	if err != nil {
		if dbStatus(err) == 404 {
			return memorySession{}, errMemoryNotFound
		}
		return memorySession{}, err
	}
	if session.Actor != identity.Actor && !hasPermission(identity, permMemoryManage) {
		return memorySession{}, errMemoryNotFound
	}
	return session, nil
}

func validSensitivity(value string) bool {
	return oneOf(value, "public", "internal", "confidential", "restricted")
}

type newMemoryItem struct {
	Kind, Role, Origin, Sensitivity, Content string
	Provenance                               map[string]any
	// Position distinguishes identical messages at different points of a
	// conversation; -1 means "not positional" and makes every item distinct.
	Position int
	Sources  []string
}

func itemAAD(tenantID, itemID string) string { return "memory_items:" + tenantID + ":" + itemID }

// addMemoryItem redacts likely secrets, seals the content and stores it. It
// returns (item, true) when a row was inserted and (zero, false) when the
// same positional item already exists.
func (a *controlPlaneApp) addMemoryItem(ctx context.Context, tx *sql.Tx, tenantID, actor string, session memorySession, settings memorySettings, in newMemoryItem) (memoryItem, bool, error) {
	if !oneOf(in.Kind, "message", "event", "tool_result", "observation", "artifact", "summary") {
		return memoryItem{}, false, badMemoryRequest("kind must be message, event, tool_result, observation, artifact or summary")
	}
	if !oneOf(in.Origin, "user", "provider", "tool", "system", "generated") {
		return memoryItem{}, false, badMemoryRequest("origin must be user, provider, tool, system or generated")
	}
	if in.Sensitivity == "" {
		in.Sensitivity = "internal"
	}
	if !validSensitivity(in.Sensitivity) {
		return memoryItem{}, false, badMemoryRequest("sensitivity must be public, internal, confidential or restricted")
	}
	if strings.TrimSpace(in.Content) == "" || len(in.Content) > 512*1024 {
		return memoryItem{}, false, badMemoryRequest("content must be between 1 byte and 512 KiB")
	}
	content, redactions := redactSecrets(in.Content)
	preview := ""
	if in.Sensitivity == "public" || in.Sensitivity == "internal" {
		preview = previewOf(content, 240)
	}
	if in.Provenance == nil {
		in.Provenance = map[string]any{}
	}
	provenance, _ := json.Marshal(in.Provenance)

	hashInput := fmt.Sprintf("%d\x00%s\x00%s\x00%s", in.Position, in.Kind, in.Role, content)
	if in.Position < 0 {
		hashInput = newRequestID() + hashInput
	}
	sum := sha256.Sum256([]byte(session.ID + "\x00" + hashInput))

	var itemID string
	err := tx.QueryRowContext(ctx, `
		insert into memory_items (tenant_id, session_id, kind, role, actor, origin, sensitivity, provenance, preview,
		                          content_hash, token_estimate, redaction_count, expires_at)
		values ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8::jsonb, $9, $10, $11, $12, now() + make_interval(days => $13))
		on conflict (session_id, content_hash) do nothing
		returning id`,
		tenantID, session.ID, in.Kind, in.Role, actor, in.Origin, in.Sensitivity, string(provenance), preview,
		hex.EncodeToString(sum[:]), estimateTokens(content), redactions, settings.RetentionDays).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryItem{}, false, nil
	}
	if err != nil {
		return memoryItem{}, false, err
	}
	sealed, err := a.sealer.seal([]byte(content), itemAAD(tenantID, itemID))
	if err != nil {
		return memoryItem{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `update memory_items set content_sealed = $1 where id = $2`, sealed, itemID); err != nil {
		return memoryItem{}, false, err
	}
	for _, source := range in.Sources {
		if _, err := tx.ExecContext(ctx, `
			insert into memory_sources (tenant_id, derived_kind, derived_id, source_item_id) values ($1, 'summary', $2, $3)
			on conflict do nothing`, tenantID, itemID, source); err != nil {
			return memoryItem{}, false, err
		}
	}
	item, err := a.loadMemoryItem(ctx, tx, tenantID, itemID, false)
	return item, true, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const memoryItemColumns = `i.id, i.session_id, i.kind, coalesce(i.role, ''), i.actor, i.origin, i.sensitivity, i.provenance,
	i.preview, i.token_estimate, i.redaction_count, i.created_at, i.expires_at,
	array(select source_item_id::text from memory_sources m where m.derived_kind = 'summary' and m.derived_id = i.id)`

func scanMemoryItem(scanner interface{ Scan(...any) error }, extra ...any) (memoryItem, error) {
	var (
		item       memoryItem
		provenance []byte
		created    time.Time
		expires    sql.NullTime
		sources    textArray
	)
	dest := append([]any{&item.ID, &item.SessionID, &item.Kind, &item.Role, &item.Actor, &item.Origin, &item.Sensitivity, &provenance,
		&item.Preview, &item.TokenEstimate, &item.RedactionCount, &created, &expires, &sources}, extra...)
	if err := scanner.Scan(dest...); err != nil {
		return memoryItem{}, err
	}
	item.Provenance = json.RawMessage(provenance)
	item.Generated = item.Origin == "generated"
	item.SourceItemIDs = []string(sources)
	item.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	if expires.Valid {
		item.ExpiresAt = expires.Time.UTC().Format(time.RFC3339)
	}
	if item.Preview == "" {
		item.Preview = "[" + item.Sensitivity + " " + item.Kind + " — open the item to read it]"
	}
	return item, nil
}

func (a *controlPlaneApp) loadMemoryItem(ctx context.Context, q queryer, tenantID, itemID string, withContent bool) (memoryItem, error) {
	var sealed []byte
	item, err := scanMemoryItem(q.QueryRowContext(ctx, `
		select `+memoryItemColumns+`, i.content_sealed from memory_items i
		where i.id = $1 and i.tenant_id = $2 and i.deleted_at is null`, itemID, tenantID), &sealed)
	if err != nil {
		if dbStatus(err) == 404 {
			return memoryItem{}, errMemoryNotFound
		}
		return memoryItem{}, err
	}
	if withContent {
		plaintext, err := a.sealer.open(sealed, itemAAD(tenantID, item.ID))
		if err != nil {
			return memoryItem{}, fmt.Errorf("stored content could not be decrypted: %w", err)
		}
		text := string(plaintext)
		item.Content = &text
	}
	return item, nil
}

func (a *controlPlaneApp) recordRetrieval(ctx context.Context, tenantID, actor, purpose, sessionID, checkpointID, query, traceID string, itemIDs []string, budget, used int) {
	if itemIDs == nil {
		itemIDs = []string{}
	}
	_, _ = a.db.ExecContext(ctx, `
		insert into memory_retrievals (tenant_id, actor, purpose, session_id, checkpoint_id, query, item_ids, token_budget, tokens_used, trace_id)
		values ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, nullif($6, ''), $7::uuid[], nullif($8, 0), nullif($9, 0), nullif($10, ''))`,
		tenantID, actor, purpose, sessionID, checkpointID, previewOf(query, 200), itemIDs, budget, used, traceID)
	_ = insertAuditRecord(ctx, a.db, tenantID, actor, "memory."+purpose, "memory", firstNonEmptyString(checkpointID, sessionID, "tenant"), "allow", map[string]any{
		"sessionId": sessionID, "checkpointId": checkpointID, "itemCount": len(itemIDs), "tokenBudget": budget, "tokensUsed": used, "traceId": traceID,
	})
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func checkpointAAD(tenantID, checkpointID string) string {
	return "memory_checkpoints:" + tenantID + ":" + checkpointID
}

func normalizeCheckpointContent(content *checkpointContent) error {
	clean := func(values []string, name string) ([]string, error) {
		out := []string{}
		for _, value := range values {
			value, _ = redactSecrets(strings.TrimSpace(value))
			if value == "" {
				continue
			}
			if len(value) > 4000 {
				return nil, badMemoryRequest("%s entries must be at most 4000 characters", name)
			}
			out = append(out, value)
		}
		if len(out) > 100 {
			return nil, badMemoryRequest("%s has too many entries", name)
		}
		return out, nil
	}
	var err error
	if content.Objective, _ = redactSecrets(strings.TrimSpace(content.Objective)); len(content.Objective) > 4000 {
		return badMemoryRequest("objective must be at most 4000 characters")
	}
	if content.CurrentState, _ = redactSecrets(strings.TrimSpace(content.CurrentState)); len(content.CurrentState) > 8000 {
		return badMemoryRequest("currentState must be at most 8000 characters")
	}
	for name, field := range map[string]*[]string{
		"completedWork": &content.CompletedWork, "decisions": &content.Decisions, "assumptions": &content.Assumptions,
		"openQuestions": &content.OpenQuestions, "nextActions": &content.NextActions, "toolOutputs": &content.ToolOutputs,
		"risks": &content.Risks, "evidenceRefs": &content.EvidenceRefs,
	} {
		if *field, err = clean(*field, name); err != nil {
			return err
		}
	}
	artifacts := []artifactRef{}
	for _, artifact := range content.Artifacts {
		artifact.Label, _ = redactSecrets(strings.TrimSpace(artifact.Label))
		artifact.Reference, _ = redactSecrets(strings.TrimSpace(artifact.Reference))
		if artifact.Label == "" && artifact.Reference == "" {
			continue
		}
		if len(artifact.Label) > 500 || len(artifact.Reference) > 2000 {
			return badMemoryRequest("artifact label/reference is too long")
		}
		artifacts = append(artifacts, artifact)
	}
	content.Artifacts = artifacts
	return nil
}

// verifyEvidence checks that every referenced item exists in this tenant and
// session and is not itself generated text.
func (a *controlPlaneApp) verifyEvidence(ctx context.Context, q queryer, tenantID, sessionID string, refs []string, allowGenerated bool) error {
	for _, ref := range refs {
		var origin string
		err := q.QueryRowContext(ctx, `
			select origin from memory_items where id = $1 and tenant_id = $2 and session_id = $3 and deleted_at is null`,
			ref, tenantID, sessionID).Scan(&origin)
		if err != nil {
			if dbStatus(err) == 404 {
				return badMemoryRequest("evidence reference %s is not a stored item of this session", ref)
			}
			return err
		}
		if origin == "generated" && !allowGenerated {
			return badMemoryRequest("evidence reference %s is generated text; reference the records it was generated from", ref)
		}
	}
	return nil
}

const checkpointColumns = `c.id, c.session_id, c.status, c.origin, coalesce(c.generator, ''), c.trigger, c.context_percent,
	c.edited_by_reviewer, c.created_by, c.created_at, coalesce(c.reviewed_by, ''), c.reviewed_at, c.content_sealed, c.tenant_id`

func (a *controlPlaneApp) scanCheckpoint(scanner interface{ Scan(...any) error }, withContent bool) (memoryCheckpoint, error) {
	var (
		cp       memoryCheckpoint
		percent  sql.NullFloat64
		created  time.Time
		reviewed sql.NullTime
		sealed   []byte
		tenantID string
	)
	if err := scanner.Scan(&cp.ID, &cp.SessionID, &cp.Status, &cp.Origin, &cp.Generator, &cp.Trigger, &percent,
		&cp.EditedByReviewer, &cp.CreatedBy, &created, &cp.ReviewedBy, &reviewed, &sealed, &tenantID); err != nil {
		return memoryCheckpoint{}, err
	}
	cp.Generated = cp.Origin == "generated"
	cp.Notice = manualCheckpointNotice
	if cp.Generated {
		cp.Notice = generatedCheckpointNotice
	}
	if percent.Valid {
		cp.ContextPercent = &percent.Float64
	}
	cp.CreatedAt = created.UTC().Format(time.RFC3339)
	if reviewed.Valid {
		cp.ReviewedAt = reviewed.Time.UTC().Format(time.RFC3339)
	}
	if withContent && sealed != nil {
		plaintext, err := a.sealer.open(sealed, checkpointAAD(tenantID, cp.ID))
		if err != nil {
			return memoryCheckpoint{}, fmt.Errorf("stored checkpoint could not be decrypted: %w", err)
		}
		var content checkpointContent
		if err := json.Unmarshal(plaintext, &content); err != nil {
			return memoryCheckpoint{}, err
		}
		cp.Content = &content
	}
	return cp, nil
}

func (a *controlPlaneApp) storeCheckpointContent(ctx context.Context, tx *sql.Tx, tenantID, checkpointID string, content checkpointContent) error {
	payload, err := json.Marshal(content)
	if err != nil {
		return err
	}
	sealed, err := a.sealer.seal(payload, checkpointAAD(tenantID, checkpointID))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update memory_checkpoints set content_sealed = $1 where id = $2`, sealed, checkpointID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from memory_sources where derived_kind = 'checkpoint' and derived_id = $1`, checkpointID); err != nil {
		return err
	}
	for _, ref := range content.EvidenceRefs {
		if _, err := tx.ExecContext(ctx, `
			insert into memory_sources (tenant_id, derived_kind, derived_id, source_item_id) values ($1, 'checkpoint', $2, $3)
			on conflict do nothing`, tenantID, checkpointID, ref); err != nil {
			return err
		}
	}
	return nil
}

// draftCheckpointFromSession builds an extractive draft: it copies text from
// stored records into the checkpoint fields and references those records. It
// writes no new claims, so anything in it can be checked against its
// evidence. Fields it cannot fill are left empty for the reviewer.
func (a *controlPlaneApp) draftCheckpointFromSession(ctx context.Context, tenantID string, session memorySession) (checkpointContent, error) {
	rows, err := a.db.QueryContext(ctx, `
		select i.id, i.kind, coalesce(i.role, ''), i.origin, i.content_sealed from memory_items i
		where i.session_id = $1 and i.tenant_id = $2 and i.deleted_at is null and i.origin <> 'generated'
		order by i.created_at asc limit 2000`, session.ID, tenantID)
	if err != nil {
		return checkpointContent{}, err
	}
	defer rows.Close()
	type record struct{ id, kind, role, origin, text string }
	var records []record
	for rows.Next() {
		var (
			r      record
			sealed []byte
		)
		if err := rows.Scan(&r.id, &r.kind, &r.role, &r.origin, &sealed); err != nil {
			return checkpointContent{}, err
		}
		plaintext, err := a.sealer.open(sealed, itemAAD(tenantID, r.id))
		if err != nil {
			continue
		}
		r.text = string(plaintext)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return checkpointContent{}, err
	}
	content := checkpointContent{}
	seen := map[string]bool{}
	cite := func(id string) {
		if !seen[id] {
			seen[id] = true
			content.EvidenceRefs = append(content.EvidenceRefs, id)
		}
	}
	for _, r := range records {
		if r.kind == "message" && r.role == "user" {
			content.Objective = previewOf(r.text, 1000)
			cite(r.id)
			break
		}
	}
	for i := len(records) - 1; i >= 0; i-- {
		if r := records[i]; r.kind == "message" && r.role == "assistant" {
			content.CurrentState = "Last assistant reply (verbatim excerpt): " + previewOf(r.text, 2000)
			cite(r.id)
			break
		}
	}
	for i := len(records) - 1; i >= 0; i-- {
		if r := records[i]; r.kind == "message" && r.role == "user" {
			content.NextActions = []string{"Last user request (verbatim excerpt): " + previewOf(r.text, 1000)}
			cite(r.id)
			break
		}
	}
	for i := len(records) - 1; i >= 0 && len(content.ToolOutputs) < 5; i-- {
		if r := records[i]; r.kind == "tool_result" {
			content.ToolOutputs = append(content.ToolOutputs, previewOf(r.text, 800))
			cite(r.id)
		}
	}
	for i := len(records) - 1; i >= 0 && len(content.Artifacts) < 10; i-- {
		if r := records[i]; r.kind == "artifact" {
			content.Artifacts = append(content.Artifacts, artifactRef{Label: "Stored artifact reference", Reference: previewOf(r.text, 500)})
			cite(r.id)
		}
	}
	content.OpenQuestions = []string{"Draft generated without interpretation: add decisions, assumptions, risks and open questions before approving."}
	return content, normalizeCheckpointContent(&content)
}

type checkpointRequest struct {
	Generate       bool               `json:"generate"`
	Content        *checkpointContent `json:"content"`
	ContextPercent *float64           `json:"contextPercent"`
}

func (a *controlPlaneApp) createCheckpoint(ctx context.Context, tenantID, actor string, session memorySession, req checkpointRequest, trigger string) (memoryCheckpoint, bool, error) {
	origin, generator := "manual", ""
	var content checkpointContent
	if req.Generate || req.Content == nil {
		origin, generator = "generated", "suez-extractive-v1"
		draft, err := a.draftCheckpointFromSession(ctx, tenantID, session)
		if err != nil {
			return memoryCheckpoint{}, false, err
		}
		content = draft
	} else {
		content = *req.Content
		if err := normalizeCheckpointContent(&content); err != nil {
			return memoryCheckpoint{}, false, err
		}
		if err := a.verifyEvidence(ctx, a.db, tenantID, session.ID, content.EvidenceRefs, false); err != nil {
			return memoryCheckpoint{}, false, err
		}
	}
	settings, err := a.getMemorySettings(ctx, tenantID)
	if err != nil {
		return memoryCheckpoint{}, false, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return memoryCheckpoint{}, false, err
	}
	defer tx.Rollback()
	var checkpointID string
	err = tx.QueryRowContext(ctx, `
		insert into memory_checkpoints (tenant_id, session_id, origin, generator, trigger, context_percent, created_by, expires_at)
		values ($1, $2, $3, nullif($4, ''), $5, $6, $7, now() + make_interval(days => $8))
		on conflict (session_id) where status = 'draft' and trigger = 'threshold' and deleted_at is null do nothing
		returning id`, tenantID, session.ID, origin, generator, trigger, req.ContextPercent, actor, settings.RetentionDays).Scan(&checkpointID)
	if errors.Is(err, sql.ErrNoRows) {
		existing, err := a.scanCheckpoint(a.db.QueryRowContext(ctx, `
			select `+checkpointColumns+` from memory_checkpoints c
			where c.session_id = $1 and c.status = 'draft' and c.trigger = 'threshold' and c.deleted_at is null`, session.ID), true)
		return existing, false, err
	}
	if err != nil {
		return memoryCheckpoint{}, false, err
	}
	if err := a.storeCheckpointContent(ctx, tx, tenantID, checkpointID, content); err != nil {
		return memoryCheckpoint{}, false, err
	}
	if err := insertAuditRecord(ctx, tx, tenantID, actor, "memory.checkpoint.create", "checkpoint", checkpointID, "allow", map[string]any{
		"sessionId": session.ID, "origin": origin, "trigger": trigger, "evidenceCount": len(content.EvidenceRefs),
	}); err != nil {
		return memoryCheckpoint{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return memoryCheckpoint{}, false, err
	}
	created, err := a.scanCheckpoint(a.db.QueryRowContext(ctx, `select `+checkpointColumns+` from memory_checkpoints c where c.id = $1`, checkpointID), true)
	return created, true, err
}

// carryForward is what a continuation or injection hands to the next
// session: the approved checkpoint plus as much of its evidence as fits.
type carryForward struct {
	Checkpoint      memoryCheckpoint `json:"checkpoint"`
	Evidence        []memoryItem     `json:"evidence"`
	OmittedEvidence []string         `json:"omittedEvidenceIds"`
	TokenBudget     int              `json:"tokenBudget"`
	TokensUsed      int              `json:"tokensUsed"`
	ContextText     string           `json:"contextText"`
	Notice          string           `json:"notice"`
}

func renderCheckpoint(cp memoryCheckpoint) string {
	var b strings.Builder
	c := cp.Content
	fmt.Fprintf(&b, "[checkpoint:%s] Approved hand-off checkpoint (approved by %s on %s", cp.ID, cp.ReviewedBy, cp.ReviewedAt)
	if cp.Generated {
		b.WriteString("; originally machine-drafted")
	}
	b.WriteString("). Treat it as the previous session's notes, not as verified fact.\n")
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString(title + ":\n")
		for _, line := range lines {
			b.WriteString("- " + line + "\n")
		}
	}
	if c.Objective != "" {
		b.WriteString("Objective: " + c.Objective + "\n")
	}
	section("Completed work", c.CompletedWork)
	section("Decisions", c.Decisions)
	if c.CurrentState != "" {
		b.WriteString("Current state: " + c.CurrentState + "\n")
	}
	section("Assumptions", c.Assumptions)
	section("Open questions", c.OpenQuestions)
	section("Next actions", c.NextActions)
	artifacts := make([]string, 0, len(c.Artifacts))
	for _, artifact := range c.Artifacts {
		artifacts = append(artifacts, strings.TrimSpace(artifact.Label+" "+artifact.Reference))
	}
	section("Artifacts", artifacts)
	section("Important tool outputs", c.ToolOutputs)
	section("Risks", c.Risks)
	return b.String()
}

// buildCarryForward selects the checkpoint and the minimum evidence within
// budget. The checkpoint must be approved and readable by the identity.
func (a *controlPlaneApp) buildCarryForward(ctx context.Context, identity identityContext, checkpointID string, requestedBudget int, settings memorySettings) (carryForward, memorySession, error) {
	cp, session, err := a.readableCheckpoint(ctx, identity, checkpointID, true)
	if err != nil {
		return carryForward{}, memorySession{}, err
	}
	if cp.Status != "approved" {
		return carryForward{}, memorySession{}, errNotApproved
	}
	budget := settings.MaxInjectionTokens
	if requestedBudget > 0 && requestedBudget < budget {
		budget = requestedBudget
	}
	out := carryForward{Checkpoint: cp, TokenBudget: budget, Evidence: []memoryItem{}, OmittedEvidence: []string{},
		Notice: "Carried forward: the approved checkpoint and the listed evidence only. Nothing else from earlier sessions was added."}
	text := renderCheckpoint(cp)
	used := estimateTokens(text)
	if used > budget {
		return carryForward{}, memorySession{}, &memoryError{status: 409, message: fmt.Sprintf("the checkpoint alone needs about %d tokens, more than the %d-token budget", used, budget)}
	}
	var b strings.Builder
	b.WriteString(text)
	for _, ref := range cp.Content.EvidenceRefs {
		item, err := a.loadMemoryItem(ctx, a.db, identity.TenantID, ref, true)
		if err != nil || item.SessionID != session.ID {
			out.OmittedEvidence = append(out.OmittedEvidence, ref)
			continue
		}
		block := fmt.Sprintf("[evidence:%s] %s %s recorded %s:\n%s\n", item.ID, item.Origin, item.Kind, item.CreatedAt, *item.Content)
		cost := estimateTokens(block)
		if len(out.Evidence) >= settings.MaxInjectionItems || used+cost > budget {
			out.OmittedEvidence = append(out.OmittedEvidence, ref)
			continue
		}
		used += cost
		b.WriteString(block)
		out.Evidence = append(out.Evidence, item)
	}
	out.TokensUsed = used
	out.ContextText = b.String()
	return out, session, nil
}

func (a *controlPlaneApp) readableCheckpoint(ctx context.Context, identity identityContext, checkpointID string, withContent bool) (memoryCheckpoint, memorySession, error) {
	cp, err := a.scanCheckpoint(a.db.QueryRowContext(ctx, `
		select `+checkpointColumns+` from memory_checkpoints c
		where c.id = $1 and c.tenant_id = $2 and c.deleted_at is null`, checkpointID, identity.TenantID), withContent)
	if err != nil {
		if dbStatus(err) == 404 {
			return memoryCheckpoint{}, memorySession{}, errMemoryNotFound
		}
		return memoryCheckpoint{}, memorySession{}, err
	}
	session, err := a.readableSession(ctx, identity, cp.SessionID)
	if err != nil {
		return memoryCheckpoint{}, memorySession{}, err
	}
	return cp, session, nil
}

// deleteMemorySession removes content immediately (sealed bytes and previews
// are wiped) and keeps tombstone rows so audit records still resolve.
func (a *controlPlaneApp) deleteMemorySession(ctx context.Context, tenantID, actor, sessionID string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		update memory_items set deleted_at = now(), deleted_by = $3, content_sealed = null, preview = '', provenance = '{}'::jsonb
		where session_id = $1 and tenant_id = $2 and deleted_at is null`, sessionID, tenantID, actor); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update memory_checkpoints set deleted_at = now(), deleted_by = $3, content_sealed = null
		where session_id = $1 and tenant_id = $2 and deleted_at is null`, sessionID, tenantID, actor); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update memory_sessions set deleted_at = now(), deleted_by = $3, title = null, status = 'closed'
		where id = $1 and tenant_id = $2`, sessionID, tenantID, actor); err != nil {
		return err
	}
	if err := insertAuditRecord(ctx, tx, tenantID, actor, "memory.session.delete", "memory", sessionID, "allow", map[string]any{"sessionId": sessionID}); err != nil {
		return err
	}
	return tx.Commit()
}

// runRetention applies each tenant's retention settings once an hour. Expired
// memory content is wiped, tombstones older than 30 days are removed, and
// audit and usage rows past their retention are deleted.
func (a *controlPlaneApp) runRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if err := a.applyRetention(runCtx); err != nil {
			slogWarn("retention run failed", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *controlPlaneApp) applyRetention(ctx context.Context) error {
	statements := []string{
		`update memory_items set deleted_at = now(), deleted_by = 'retention', content_sealed = null, preview = '', provenance = '{}'::jsonb
		 where deleted_at is null and expires_at is not null and expires_at < now()`,
		`update memory_checkpoints set deleted_at = now(), deleted_by = 'retention', content_sealed = null
		 where deleted_at is null and expires_at is not null and expires_at < now()`,
		`delete from memory_items where deleted_at < now() - interval '30 days'`,
		`delete from memory_checkpoints where deleted_at < now() - interval '30 days'`,
		`delete from memory_sessions s where s.deleted_at < now() - interval '30 days'`,
		`delete from memory_retrievals r using tenants t
		 where r.tenant_id = t.id and r.created_at < now() - make_interval(days => coalesce(
		   (select audit_retention_days from memory_settings m where m.tenant_id = t.id), 365))`,
		`delete from audit_log l using tenants t
		 where l.tenant_id = t.id and l.created_at < now() - make_interval(days => coalesce(
		   (select audit_retention_days from memory_settings m where m.tenant_id = t.id), 365))`,
		`delete from context_usage_measurements u using tenants t
		 where u.tenant_id = t.id and u.measured_at < now() - make_interval(days => coalesce(
		   (select usage_retention_days from memory_settings m where m.tenant_id = t.id), 90))`,
		`delete from tool_executions e using tenants t
		 where e.tenant_id = t.id and e.created_at < now() - make_interval(days => coalesce(
		   (select usage_retention_days from memory_settings m where m.tenant_id = t.id), 90))`,
	}
	for _, statement := range statements {
		if _, err := a.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
