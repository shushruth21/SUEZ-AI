# Architecture decision records

Each record explains one significant decision: the context, what was decided,
and the consequences. Records are never rewritten after they are accepted. If
a decision changes, add a new record that supersedes the old one, and mark the
old record "Superseded by NNNN".

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-production-safe-defaults.md) | Production-safe defaults that refuse unsafe settings | Accepted |
| [0002](0002-permission-table-and-scoped-keys.md) | One permission table, and scoped API keys | Accepted |
| [0003](0003-opa-tenant-policy-isolation.md) | Per-tenant OPA packages and sandboxed policy tests | Accepted |
| [0004](0004-envelope-encryption-for-memory.md) | Envelope encryption and redaction for SUEZ Memory | Accepted |
| [0005](0005-gateway-side-context-thresholds.md) | Context thresholds enforced in the gateway | Accepted |
| [0006](0006-reviewed-extractive-checkpoints.md) | Extractive checkpoints that need human approval | Accepted |

To add one, copy [template.md](template.md) to the next number and open a pull
request.
