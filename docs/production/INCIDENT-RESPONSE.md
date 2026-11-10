# Incident response notes

Every response carries `X-Request-Id`; chat and tool calls also carry
`X-SUEZ-Trace-Id`. Service logs are JSON with `requestId`; audit records
carry `trace_id`. Start every investigation from one of these ids:
`GET /api/v1/audit-log/traces/{traceId}` (permission `audit.read`).

## Suspected leaked SUEZ API key

1. Revoke: `DELETE /api/v1/api-keys/{id}` (effective on the next request at both services).
2. Find use: `GET /api/v1/audit-log?actor=<key label>&since=...`; `api_keys.last_used_at` shows the last use (minute precision).
3. Issue a replacement with the narrowest scope that works.

## Suspected leaked provider key

Rotate it at the provider, replace the secret file, restart the gateway
([KEY-ROTATION.md](KEY-ROTATION.md)). Provider keys never appear in SUEZ
responses or audit records; check provider-side usage for the exposure window.

## Identity provider compromise or outage

- Outage: token sign-in fails closed with 503 once cached keys expire (`JWT_JWKS_CACHE_SECONDS`); API keys keep working.
- Compromise: set `JWT_MODE=disabled` and restart the control plane to stop accepting all tokens; deactivate affected users (`PATCH /api/v1/users/{id} {"status":"inactive"}`).

## Tenant reports another tenant's data or wrong authorization

1. Capture the trace id. 2. Check the audit trace and the OPA module list (`GET http://opa:8181/v1/policies` from inside the data network): every generated module must be `suez/generated/<id>` with package `suez.generated.t_<tenant>...`. 3. Restart the control plane to force a sync. 4. Treat as a security incident until explained.

## Service degraded

| Symptom | Likely cause | Action |
| --- | --- | --- |
| 503 `identity service unavailable` | Control plane down, or internal token mismatch | Check control-plane readiness; compare `SUEZ_INTERNAL_TOKEN` on both |
| 503 `quota service unavailable` | Redis down | Restore Redis; requests are refused, not unlimited |
| 503 `policy service unavailable` | OPA down | Restart OPA; the reconciler reloads policies within 30 s |
| 502/504 with `code` `upstream_*` | Provider failing | See provider status; `Retry-After` is passed through for 429 |
| `memory capture failed` in gateway logs | Control plane unreachable | Chat continues; captured memory for that period is missing |

## Data exposure

Use the export endpoint to see exactly what is stored for a session, delete
with `DELETE /api/v1/memory/sessions/{id}`, and record the request id of the
deletion. See [DATA-RETENTION-DELETION.md](DATA-RETENTION-DELETION.md).
