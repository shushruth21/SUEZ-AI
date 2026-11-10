-- Development demo data only. Never load seeds in production.
INSERT INTO users (tenant_id, subject, email, display_name, role, created_by)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin@example.com', 'admin@example.com', 'Demo Admin', 'admin', 'seed'),
    ('00000000-0000-0000-0000-000000000001', 'requester@example.com', 'requester@example.com', 'Demo Requester', 'user', 'seed'),
    ('00000000-0000-0000-0000-000000000001', 'auditor@example.com', 'auditor@example.com', 'Demo Auditor', 'auditor', 'seed'),
    ('00000000-0000-0000-0000-000000000001', 'developer@example.com', 'developer@example.com', 'Demo Developer', 'user', 'seed'),
    ('00000000-0000-0000-0000-000000000001', 'security-admin@example.com', 'security-admin@example.com', 'Demo Security Admin', 'policy-admin', 'seed')
ON CONFLICT (tenant_id, subject) DO NOTHING;

-- The demo bootstrap key may also create tenants in a local stack.
UPDATE api_keys
SET scopes = ARRAY['tenant-admin', 'platform-admin']::TEXT[], key_prefix = 'suez-demo', created_by = 'seed'
WHERE id = '00000000-0000-0000-0000-000000000101';
