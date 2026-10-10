ALTER TABLE policies
    ADD COLUMN IF NOT EXISTS parent_policy_id UUID REFERENCES policies(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS generation_prompt TEXT,
    ADD COLUMN IF NOT EXISTS generator_model TEXT,
    ADD COLUMN IF NOT EXISTS validation_errors JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS published_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS published_by TEXT,
    ADD COLUMN IF NOT EXISTS last_compiled_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS policies_parent_policy_id_idx
    ON policies (parent_policy_id);

CREATE UNIQUE INDEX IF NOT EXISTS policies_one_active_publish_per_target
    ON policies (tenant_id, scope, COALESCE(target_name, ''))
    WHERE status = 'published' AND enabled = TRUE;
