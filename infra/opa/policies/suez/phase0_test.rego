package suez.authz

test_tenant_a_generated_policy_does_not_affect_tenant_b if {
    input_a := {
        "action": "chat.completions",
        "identity": {"tenantId": "tenant-a", "actor": "a", "roles": ["user"]},
        "resource": {"type": "model", "enabled": true},
    }
    input_b := object.union(input_a, {"identity": {"tenantId": "tenant-b", "actor": "b", "roles": ["user"]}})
    generated := {
        "t_tenant_a": {
            "p_one": {"published_policy_results": [{"policyId": "one", "allow": false, "reason": "a_denied"}]},
        },
    }
    not allow with input as input_a with data.suez.generated as generated
    allow with input as input_b with data.suez.generated as generated
}

test_generated_allow_cannot_bypass_base_checks if {
    req := {
        "action": "chat.completions",
        "identity": {"tenantId": "tenant-a", "actor": "a", "roles": ["user"]},
        "resource": {"type": "model", "enabled": false},
    }
    generated := {
        "t_tenant_a": {
            "p_one": {"published_policy_results": [{"policyId": "one", "allow": true, "reason": "allow"}]},
        },
    }
    not allow with input as req with data.suez.generated as generated
}

test_sandbox_replaces_excluded_policy if {
    req := {
        "action": "chat.completions",
        "identity": {"tenantId": "tenant-a", "actor": "a", "roles": ["user"]},
        "resource": {"type": "model", "enabled": true},
        "sandbox": {"module": "r_one", "exclude": {"p_old": true}},
    }
    generated := {
        "t_tenant_a": {
            "p_old": {"published_policy_results": [{"policyId": "old", "allow": false, "reason": "old_deny"}]},
        },
    }
    sandbox := {"r_one": {"published_policy_results": [{"policyId": "new", "allow": true, "reason": "new_allow"}]}}
    allow with input as req with data.suez.generated as generated with data.suez.sandbox as sandbox
}

test_live_requests_ignore_sandbox_modules if {
    req := {
        "action": "chat.completions",
        "identity": {"tenantId": "tenant-a", "actor": "a", "roles": ["user"]},
        "resource": {"type": "model", "enabled": true},
    }
    sandbox := {"r_one": {"published_policy_results": [{"policyId": "new", "allow": false, "reason": "sandbox_deny"}]}}
    allow with input as req with data.suez.sandbox as sandbox
}
