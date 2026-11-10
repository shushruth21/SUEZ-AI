-- Identity: verified JWT subjects must map to a stored user with a tenant
-- membership and role. API keys become scoped, revocable and expirable.

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    email TEXT,
    display_name TEXT,
    role TEXT NOT NULL DEFAULT 'user'
        CHECK (role IN ('admin', 'policy-admin', 'auditor', 'user', 'read-only')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS users_tenant_subject_key ON users (tenant_id, subject);

ALTER TABLE api_keys
    -- Keys that existed before scopes were introduced keep full tenant access;
    -- new keys always state their scopes explicitly.
    ADD COLUMN IF NOT EXISTS scopes TEXT[] NOT NULL DEFAULT ARRAY['tenant-admin']::TEXT[],
    ADD COLUMN IF NOT EXISTS allowed_models TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN IF NOT EXISTS allowed_tools TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN IF NOT EXISTS rate_limit_rpm INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit_rpm >= 0),
    ADD COLUMN IF NOT EXISTS key_prefix TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_by TEXT,
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_by TEXT,
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS api_keys_key_hash_key ON api_keys (key_hash);

-- Distributed lock bookkeeping is done with pg advisory locks; nothing to
-- create here. Grants gain revocation attribution.
ALTER TABLE access_grants
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_by TEXT;
