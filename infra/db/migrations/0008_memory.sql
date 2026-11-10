-- SUEZ Memory: tenant-scoped sessions, evidence, summaries, checkpoints and
-- retrieval records, plus per-tenant context-window thresholds.
--
-- Content is stored sealed (envelope encryption, see apps/control-plane
-- crypto.go). `preview` is a short, secret-redacted index text and is only
-- populated for items classified public or internal.

CREATE TABLE IF NOT EXISTS memory_settings (
    tenant_id UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    capture_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    retrieval_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    injection_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    retention_days INTEGER NOT NULL DEFAULT 90 CHECK (retention_days BETWEEN 1 AND 3650),
    max_injection_tokens INTEGER NOT NULL DEFAULT 2000 CHECK (max_injection_tokens BETWEEN 0 AND 100000),
    max_injection_items INTEGER NOT NULL DEFAULT 8 CHECK (max_injection_items BETWEEN 0 AND 100),
    audit_retention_days INTEGER NOT NULL DEFAULT 365 CHECK (audit_retention_days BETWEEN 30 AND 3650),
    usage_retention_days INTEGER NOT NULL DEFAULT 90 CHECK (usage_retention_days BETWEEN 1 AND 3650),
    updated_by TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS memory_preferences (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    capture_opt_out BOOLEAN NOT NULL DEFAULT FALSE,
    retrieval_opt_out BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, actor)
);

CREATE TABLE IF NOT EXISTS context_policies (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    model_external_name TEXT NOT NULL DEFAULT '*',
    notice_percent NUMERIC(5,2) NOT NULL DEFAULT 80,
    checkpoint_percent NUMERIC(5,2) NOT NULL DEFAULT 85,
    warning_percent NUMERIC(5,2) NOT NULL DEFAULT 95,
    hard_block BOOLEAN NOT NULL DEFAULT FALSE,
    block_percent NUMERIC(5,2) NOT NULL DEFAULT 100,
    updated_by TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, model_external_name),
    CHECK (notice_percent > 0 AND notice_percent <= checkpoint_percent
           AND checkpoint_percent <= warning_percent AND warning_percent <= block_percent
           AND block_percent <= 100)
);

CREATE TABLE IF NOT EXISTS memory_workspaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS memory_workspaces_tenant_name_key
    ON memory_workspaces (tenant_id, name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS memory_workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES memory_workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    objective TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'archived')),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS memory_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workspace_id UUID REFERENCES memory_workspaces(id) ON DELETE SET NULL,
    workflow_id UUID REFERENCES memory_workflows(id) ON DELETE SET NULL,
    external_session_id TEXT NOT NULL,
    actor TEXT NOT NULL,
    title TEXT,
    model TEXT,
    continued_from_checkpoint_id UUID,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    deleted_by TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS memory_sessions_owner_key
    ON memory_sessions (tenant_id, actor, external_session_id);
CREATE INDEX IF NOT EXISTS memory_sessions_tenant_activity_idx
    ON memory_sessions (tenant_id, last_activity_at DESC);

CREATE TABLE IF NOT EXISTS memory_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES memory_sessions(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('message', 'event', 'tool_result', 'observation', 'artifact', 'summary')),
    role TEXT,
    actor TEXT NOT NULL,
    -- origin separates evidence from generated text: user, provider and tool
    -- items are records of what happened; generated items are AI output about
    -- those records and are never treated as ground truth.
    origin TEXT NOT NULL CHECK (origin IN ('user', 'provider', 'tool', 'system', 'generated')),
    sensitivity TEXT NOT NULL DEFAULT 'internal'
        CHECK (sensitivity IN ('public', 'internal', 'confidential', 'restricted')),
    provenance JSONB NOT NULL DEFAULT '{}'::jsonb,
    preview TEXT NOT NULL DEFAULT '',
    content_sealed BYTEA,
    content_hash TEXT NOT NULL,
    token_estimate INTEGER NOT NULL DEFAULT 0,
    redaction_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    deleted_by TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS memory_items_dedupe_key ON memory_items (session_id, content_hash);
CREATE INDEX IF NOT EXISTS memory_items_session_time_idx ON memory_items (session_id, created_at);
CREATE INDEX IF NOT EXISTS memory_items_expiry_idx ON memory_items (expires_at) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS memory_items_preview_fts
    ON memory_items USING GIN (to_tsvector('simple', preview)) WHERE deleted_at IS NULL;

-- Which stored items a summary (or checkpoint) is based on.
CREATE TABLE IF NOT EXISTS memory_sources (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    derived_kind TEXT NOT NULL CHECK (derived_kind IN ('summary', 'checkpoint')),
    derived_id UUID NOT NULL,
    source_item_id UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    PRIMARY KEY (derived_kind, derived_id, source_item_id)
);

CREATE TABLE IF NOT EXISTS memory_checkpoints (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES memory_sessions(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved', 'rejected', 'superseded')),
    origin TEXT NOT NULL CHECK (origin IN ('manual', 'generated')),
    generator TEXT,
    trigger TEXT NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual', 'threshold')),
    context_percent NUMERIC(6,2),
    content_sealed BYTEA,
    edited_by_reviewer BOOLEAN NOT NULL DEFAULT FALSE,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_by TEXT,
    reviewed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    deleted_by TEXT
);
CREATE INDEX IF NOT EXISTS memory_checkpoints_session_idx ON memory_checkpoints (session_id, created_at DESC);
-- At most one open threshold draft per session, so repeated requests above
-- the threshold do not pile up drafts.
CREATE UNIQUE INDEX IF NOT EXISTS memory_checkpoints_one_threshold_draft
    ON memory_checkpoints (session_id) WHERE status = 'draft' AND trigger = 'threshold' AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS memory_retrievals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('index', 'search', 'timeline', 'detail', 'continuation', 'injection', 'export')),
    session_id UUID,
    checkpoint_id UUID,
    query TEXT,
    item_ids UUID[] NOT NULL DEFAULT ARRAY[]::UUID[],
    token_budget INTEGER,
    tokens_used INTEGER,
    trace_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS memory_retrievals_tenant_time_idx ON memory_retrievals (tenant_id, created_at DESC);
