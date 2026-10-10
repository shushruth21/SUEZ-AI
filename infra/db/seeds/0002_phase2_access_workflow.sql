UPDATE access_requests
SET
    reviewed_by = 'security-admin@example.com',
    decision_reason = 'Bootstrap approval so the grant view has initial data.',
    reviewed_at = NOW(),
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000501';

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
    '00000000-0000-0000-0000-000000000502',
    '00000000-0000-0000-0000-000000000001',
    'requester@example.com',
    'model',
    'demo-model',
    'Need access for portal-based approval testing in Phase 2.',
    'pending',
    NOW()
)
ON CONFLICT (id) DO NOTHING;

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
VALUES (
    '00000000-0000-0000-0000-000000000701',
    '00000000-0000-0000-0000-000000000501',
    '00000000-0000-0000-0000-000000000001',
    'developer@example.com',
    'model',
    'demo-model',
    'security-admin@example.com',
    'active'
)
ON CONFLICT (tenant_id, subject, resource_type, resource_name) DO NOTHING;

INSERT INTO audit_log (id, tenant_id, actor, action, resource_type, resource_name, decision, metadata)
VALUES
(
    '00000000-0000-0000-0000-000000000602',
    '00000000-0000-0000-0000-000000000001',
    'developer@example.com',
    'access_request.create',
    'model',
    'demo-model',
    'pending',
    '{"requestId":"00000000-0000-0000-0000-000000000501"}'::jsonb
),
(
    '00000000-0000-0000-0000-000000000603',
    '00000000-0000-0000-0000-000000000001',
    'security-admin@example.com',
    'access_request.approve',
    'model',
    'demo-model',
    'allow',
    '{"requestId":"00000000-0000-0000-0000-000000000501","grantId":"00000000-0000-0000-0000-000000000701"}'::jsonb
)
ON CONFLICT (id) DO NOTHING;
