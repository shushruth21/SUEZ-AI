UPDATE policies
SET
    status = 'published',
    published_at = NOW(),
    published_by = 'phase6-seed',
    validation_errors = '[]'::jsonb,
    last_compiled_at = NOW(),
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000402';

UPDATE policies
SET
    status = 'draft',
    published_at = NULL,
    published_by = NULL,
    validation_errors = '[]'::jsonb,
    last_compiled_at = NOW(),
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000403';

INSERT INTO policy_test_cases (
    id,
    policy_id,
    tenant_id,
    name,
    description,
    identity_payload,
    action,
    resource_payload,
    request_payload,
    expected_allow,
    expected_reasons
)
VALUES
(
    '00000000-0000-0000-0000-000000000801',
    '00000000-0000-0000-0000-000000000403',
    '00000000-0000-0000-0000-000000000001',
    'Requester blocked on high-risk tool',
    'The high-risk tool draft should deny a requester when the risk tier is high.',
    '{"tenantId":"00000000-0000-0000-0000-000000000001","tenantSlug":"demo","tenantName":"Demo Tenant","actor":"requester@example.com","authMethod":"jwt","roles":["user"]}'::jsonb,
    'tools.execute',
    '{"type":"tool","name":"finance.transfer","enabled":true,"mode":"http","riskTier":"high"}'::jsonb,
    '{"name":"finance.transfer","input":{"amount":100}}'::jsonb,
    FALSE,
    '["draft_high_risk_tool_block"]'::jsonb
),
(
    '00000000-0000-0000-0000-000000000802',
    '00000000-0000-0000-0000-000000000403',
    '00000000-0000-0000-0000-000000000001',
    'Admin allowed on high-risk tool',
    'The high-risk tool draft should still allow an admin request when base runtime checks succeed.',
    '{"tenantId":"00000000-0000-0000-0000-000000000001","tenantSlug":"demo","tenantName":"Demo Tenant","actor":"Bootstrap Admin Key","authMethod":"api_key","roles":["admin"]}'::jsonb,
    'tools.execute',
    '{"type":"tool","name":"finance.transfer","enabled":true,"mode":"http","riskTier":"high"}'::jsonb,
    '{"name":"finance.transfer","input":{"amount":100}}'::jsonb,
    TRUE,
    '[]'::jsonb
)
ON CONFLICT (id) DO NOTHING;
