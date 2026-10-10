INSERT INTO tenants (id, slug, name, environment)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'demo',
    'Demo Tenant',
    'dev'
)
ON CONFLICT (slug) DO NOTHING;

INSERT INTO api_keys (id, tenant_id, label, key_hash)
VALUES (
    '00000000-0000-0000-0000-000000000101',
    '00000000-0000-0000-0000-000000000001',
    'Bootstrap Admin Key',
    'sha256:d5f5b98a3843ee1f45db16acef1aea1ec084b29e1dbb08602fb5764b8e4dc7c3'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO models (
    id,
    tenant_id,
    provider,
    model_name,
    external_name,
    max_rpm,
    max_tpm,
    enabled,
    upstream_model,
    updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000201',
    '00000000-0000-0000-0000-000000000001',
    'openai-compatible',
    'demo-model',
    'demo-model',
    60,
    120000,
    TRUE,
    'demo-model',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO tools (id, tenant_id, name, description, input_schema)
VALUES (
    '00000000-0000-0000-0000-000000000301',
    '00000000-0000-0000-0000-000000000001',
    'weather.lookup',
    'Demo tool used to reserve the tool governance surface for later phases.',
    '{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}'::jsonb
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO policies (id, tenant_id, name, natural_language_rule, rego_package, rego_source, status, version)
VALUES (
    '00000000-0000-0000-0000-000000000401',
    '00000000-0000-0000-0000-000000000001',
    'Phase 0 Allow All',
    'Allow bootstrap requests in the local environment.',
    'suez.authz',
    'package suez.authz

default allow := true',
    'published',
    1
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO access_requests (id, tenant_id, subject, resource_type, resource_name, justification, status)
VALUES (
    '00000000-0000-0000-0000-000000000501',
    '00000000-0000-0000-0000-000000000001',
    'developer@example.com',
    'model',
    'demo-model',
    'Bootstrap request to validate the future approval flow.',
    'approved'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO audit_log (id, tenant_id, actor, action, resource_type, resource_name, decision, trace_id, metadata)
VALUES (
    '00000000-0000-0000-0000-000000000601',
    '00000000-0000-0000-0000-000000000001',
    'system',
    'bootstrap.seed',
    'stack',
    'phase-0',
    'allow',
    'phase0-seed-trace',
    '{"source":"infra/db/seeds/0001_seed.sql"}'::jsonb
)
ON CONFLICT (id) DO NOTHING;
