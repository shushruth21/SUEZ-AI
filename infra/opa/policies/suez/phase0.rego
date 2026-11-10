package suez.authz

default allow := false

supported_actions := {
    "chat.completions",
    "tools.execute",
    "memory.inject",
}

supported_action if {
    supported_actions[input.action]
}

has_identity if {
    input.identity.tenantId != ""
    input.identity.actor != ""
}

resource_enabled if {
    input.resource.enabled == true
}

resource_shape_valid if {
    input.action == "chat.completions"
    input.resource.type == "model"
}

resource_shape_valid if {
    input.action == "tools.execute"
    input.resource.type == "tool"
}

# Carrying an approved checkpoint into a chat request. The memory service
# checks ownership, approval and tenant settings; a tenant policy can deny it
# here like any other action.
resource_shape_valid if {
    input.action == "memory.inject"
    input.resource.type == "checkpoint"
}

is_admin if {
    some role in input.identity.roles
    lower(role) == "admin"
}

high_risk_tool if {
    input.resource.type == "tool"
    lower(object.get(input.resource, "riskTier", "")) == "high"
}

tenant_package := sprintf("t_%s", [replace(input.identity.tenantId, "-", "_")])

sandbox_module := object.get(object.get(input, "sandbox", {}), "module", "")

sandbox_enabled if {
    sandbox_module != ""
}

# Live requests only read the caller's tenant package. A generated module
# cannot add rules to this base package, and therefore cannot affect another
# tenant or bypass the resource/identity checks below.
generated_matches contains result if {
    not sandbox_enabled
    tenant_pkg := tenant_package
    module := data.suez.generated[tenant_pkg][_]
    result := module.published_policy_results[_]
}

# Test requests use a private sandbox module. Published policies remain in
# effect except for the explicitly excluded policy package(s) that the draft
# replaces for its scope and target.
generated_matches contains result if {
    sandbox_enabled
    tenant_pkg := tenant_package
    some module_key
    module := data.suez.generated[tenant_pkg][module_key]
    not object.get(object.get(input.sandbox, "exclude", {}), module_key, false)
    result := module.published_policy_results[_]
}

generated_matches contains result if {
    sandbox_enabled
    module := data.suez.sandbox[sandbox_module]
    result := module.published_policy_results[_]
}

generated_policy_applies if {
    count([result | result := generated_matches[_]]) > 0
}

generated_policy_denied if {
    result := generated_matches[_]
    result.allow == false
}

generated_policy_allowed if {
    generated_policy_applies
    not generated_policy_denied
}

allow if {
    generated_policy_allowed
    supported_action
    has_identity
    resource_enabled
    resource_shape_valid
}

allow if {
    supported_action
    has_identity
    resource_enabled
    resource_shape_valid
    not high_risk_tool
    not generated_policy_applies
}

allow if {
    supported_action
    has_identity
    resource_enabled
    resource_shape_valid
    high_risk_tool
    is_admin
    not generated_policy_applies
}

reasons contains "unsupported_action" if {
    not supported_action
}

reasons contains "missing_identity" if {
    not has_identity
}

reasons contains "resource_disabled" if {
    not resource_enabled
}

reasons contains "resource_shape_invalid" if {
    not resource_shape_valid
}

reasons contains "admin_required_for_high_risk_tool" if {
    high_risk_tool
    not is_admin
}

reasons contains reason if {
    result := generated_matches[_]
    result.allow == false
    reason := object.get(result, "reason", "published_policy_denied")
}

decision := {
    "phase": data.runtime.phase,
    "policyVersion": data.runtime.policyVersion,
    "action": input.action,
    "allow": allow,
    "reasons": [reason | reason := reasons[_]],
}
