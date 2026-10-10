UPDATE models
SET
    provider = 'stub',
    external_name = 'demo-model',
    enabled = TRUE,
    upstream_base_url = NULL,
    upstream_model = 'demo-model',
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000201';

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
    '00000000-0000-0000-0000-000000000202',
    '00000000-0000-0000-0000-000000000001',
    'openai-compatible',
    'gpt-4o-mini',
    'openai-gpt-4o-mini',
    30,
    90000,
    TRUE,
    'gpt-4o-mini',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

UPDATE tools
SET
    enabled = TRUE,
    execution_mode = 'stub',
    timeout_ms = 5000,
    output_schema = '{"type":"object","properties":{"city":{"type":"string"},"forecast":{"type":"string"},"source":{"type":"string"}},"required":["city","forecast","source"]}'::jsonb,
    risk_tier = 'standard',
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000301';

INSERT INTO tools (
    id,
    tenant_id,
    name,
    description,
    input_schema,
    enabled,
    execution_mode,
    target_url,
    timeout_ms,
    output_schema,
    risk_tier,
    updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000302',
    '00000000-0000-0000-0000-000000000001',
    'http.echo',
    'Demo HTTP tool that echoes structured payloads through the control-plane mock endpoint.',
    '{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}'::jsonb,
    TRUE,
    'http',
    'http://control-plane:8081/internal/tools/http-echo',
    5000,
    '{"type":"object","properties":{"message":{"type":"string"},"echoed":{"type":"boolean"},"source":{"type":"string"}},"required":["message","echoed","source"]}'::jsonb,
    'standard',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

UPDATE policies
SET
    name = 'Phase 3 Allow Runtime Decisions',
    natural_language_rule = 'Allow authenticated model and tool actions when runtime checks succeed.',
    scope = 'runtime',
    target_name = '*',
    enabled = TRUE,
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000401';

INSERT INTO policies (
    id,
    tenant_id,
    name,
    natural_language_rule,
    rego_package,
    rego_source,
    status,
    version,
    scope,
    target_name,
    enabled,
    updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000402',
    '00000000-0000-0000-0000-000000000001',
    'Tool Runtime Guard',
    'Allow standard-risk tool execution while keeping high-risk tools for admin review.',
    'suez.authz',
    'package suez.authz',
    'published',
    1,
    'tool',
    '*',
    TRUE,
    NOW()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO access_requests (
    id,
    tenant_id,
    subject,
    resource_type,
    resource_name,
    justification,
    status,
    updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000503',
    '00000000-0000-0000-0000-000000000001',
    'requester@example.com',
    'tool',
    'weather.lookup',
    'Need access to test governed tool execution in the requester portal.',
    'pending',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

-- Demo admin API key actor is the key label ("Bootstrap Admin Key").
-- Without these grants, first-run chat/tool calls with suez-demo-admin-key are denied.
INSERT INTO access_grants (
    id,
    request_id,
    tenant_id,
    subject,
    resource_type,
    resource_name,
    granted_by,
    status
)
VALUES
(
    '00000000-0000-0000-0000-000000000702',
    NULL,
    '00000000-0000-0000-0000-000000000001',
    'Bootstrap Admin Key',
    'model',
    'demo-model',
    'system',
    'active'
),
(
    '00000000-0000-0000-0000-000000000703',
    NULL,
    '00000000-0000-0000-0000-000000000001',
    'Bootstrap Admin Key',
    'tool',
    'weather.lookup',
    'system',
    'active'
),
(
    '00000000-0000-0000-0000-000000000704',
    NULL,
    '00000000-0000-0000-0000-000000000001',
    'Bootstrap Admin Key',
    'tool',
    'http.echo',
    'system',
    'active'
)
ON CONFLICT (tenant_id, subject, resource_type, resource_name) DO NOTHING;
