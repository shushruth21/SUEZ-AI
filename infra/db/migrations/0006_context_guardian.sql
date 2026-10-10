-- SUEZ Context Guardian: context-window metadata and per-session usage measurements.
--
-- Additive and idempotent: it only adds a nullable column and a new table with
-- its index. No existing rows are modified or deleted, and it can be re-run.

-- Context window (in tokens) per model. NULL means unknown, in which case the
-- gateway reports token counts but no percentage.
ALTER TABLE models
    ADD COLUMN IF NOT EXISTS context_window INTEGER
        CHECK (context_window IS NULL OR context_window > 0);

-- One row per chat request that carried an X-SUEZ-Session-Id header. The
-- latest row for (tenant, actor, session) is the current context occupancy;
-- rows are never summed.
CREATE TABLE IF NOT EXISTS context_usage_measurements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    session_id TEXT NOT NULL,
    model_external_name TEXT NOT NULL,
    provider TEXT NOT NULL,
    measurement_source TEXT NOT NULL
        CHECK (measurement_source IN ('provider_reported', 'gateway_estimate', 'unavailable')),
    cache_status TEXT NOT NULL
        CHECK (cache_status IN ('miss', 'hit')),
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    trace_id TEXT,
    measured_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS context_usage_measurements_session_idx
    ON context_usage_measurements (tenant_id, actor, session_id, measured_at DESC);
