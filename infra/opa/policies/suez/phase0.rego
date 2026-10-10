package suez.authz

default allow := false

supported_actions := {
    "chat.completions",
    "tools.execute",
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

is_admin if {
    some role in input.identity.roles
    lower(role) == "admin"
}

high_risk_tool if {
    input.resource.type == "tool"
    lower(object.get(input.resource, "riskTier", "")) == "high"
}

published_policy_results contains result if {
    false
    result := {"policyId": "", "allow": false, "reason": ""}
}

generated_matches contains result if {
    result := published_policy_results[_]
}

generated_policy_applies if {
    count([result | result := published_policy_results[_]]) > 0
}

generated_policy_denied if {
    result := published_policy_results[_]
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
    result := published_policy_results[_]
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
