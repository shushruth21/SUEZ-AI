UPDATE policies
SET
    status = 'draft',
    enabled = TRUE,
    generation_prompt = natural_language_rule,
    generator_model = 'seed',
    validation_errors = '[]'::jsonb,
    published_at = NULL,
    published_by = NULL,
    last_compiled_at = NOW(),
    updated_at = NOW()
WHERE tenant_id = '00000000-0000-0000-0000-000000000001';

UPDATE policies
SET
    rego_package = 'suez.authz',
    rego_source = format(
        $rego$
package suez.authz

published_policy_results contains {
    "policyId": "%s",
    "allow": true,
    "reason": "seed_runtime_guard"
} if {
    input.identity.tenantId == "00000000-0000-0000-0000-000000000001"
    input.action == "tools.execute"
    input.resource.type == "tool"
}
$rego$,
        id
    ),
    updated_at = NOW()
WHERE id = '00000000-0000-0000-0000-000000000402';

INSERT INTO policies (
    id,
    tenant_id,
    parent_policy_id,
    name,
    natural_language_rule,
    generation_prompt,
    generator_model,
    rego_package,
    rego_source,
    status,
    version,
    scope,
    target_name,
    enabled,
    validation_errors,
    last_compiled_at,
    updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000403',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000402',
    'High Risk Tool Approval Draft',
    'Deny high-risk tool execution for non-admin users in the demo tenant.',
    'Deny high-risk tool execution for non-admin users in the demo tenant.',
    'seed',
    'suez.authz',
    $rego$
package suez.authz

admin_role_present if {
    some role in input.identity.roles
    lower(role) == "admin"
}

published_policy_results contains {
    "policyId": "00000000-0000-0000-0000-000000000403",
    "allow": false,
    "reason": "draft_high_risk_tool_block"
} if {
    input.identity.tenantId == "00000000-0000-0000-0000-000000000001"
    input.action == "tools.execute"
    input.resource.type == "tool"
    lower(object.get(input.resource, "riskTier", "")) == "high"
    not admin_role_present
}
$rego$,
    'draft',
    2,
    'tool',
    '*',
    TRUE,
    '[]'::jsonb,
    NOW(),
    NOW()
)
ON CONFLICT (id) DO NOTHING;
