ALTER TABLE policies
    ADD COLUMN IF NOT EXISTS rollback_source_policy_id UUID REFERENCES policies(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS policy_test_cases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    identity_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    action TEXT NOT NULL,
    resource_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    expected_allow BOOLEAN NOT NULL,
    expected_reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS policy_test_cases_policy_id_idx
    ON policy_test_cases (policy_id);

CREATE INDEX IF NOT EXISTS policy_test_cases_tenant_id_idx
    ON policy_test_cases (tenant_id);

CREATE TABLE IF NOT EXISTS policy_test_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    initiated_by TEXT NOT NULL,
    status TEXT NOT NULL,
    total_count INTEGER NOT NULL DEFAULT 0,
    passed_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    results JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS policy_test_runs_policy_id_idx
    ON policy_test_runs (policy_id, created_at DESC);

CREATE INDEX IF NOT EXISTS policy_test_runs_tenant_id_idx
    ON policy_test_runs (tenant_id, created_at DESC);
