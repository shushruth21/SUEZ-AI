# SUEZ HTTP API reference

This page covers both services. In production they are reached through the
edge:

- the gateway at `https://<host>/gateway`;
- the control plane at `https://<host>/control-plane`.

The edge returns 404 for `/internal*` and `/opa*`.

## Authentication

Send one of these headers:

```
Authorization: Bearer <api-key or JWT>
X-API-Key: <api-key>
```

- **API keys** (`sk_suez_…`) are shown once when created and stored only as
  hashes. Each has scopes, an optional expiry, optional model and tool
  allowlists, and an optional per-minute rate limit.
- **JWTs** are verified against the configured identity provider. The issuer,
  audience, signature (asymmetric algorithms only), `exp`, `nbf` and `iat` are
  all checked. The token must map to an active user. Roles come from the
  database, not from the token.

Errors return JSON `{"error": "...", "traceId": "..."}` where a trace exists:

| Status | Meaning |
| --- | --- |
| 401 | Missing, invalid, expired or revoked credential |
| 403 | Authenticated, but the permission, grant, allowlist or policy says no |
| 404 | Unknown route, or no such resource in the caller's tenant |
| 409 | State conflict (for example, publishing a disabled policy, or injecting an unapproved checkpoint) |
| 429 | Quota or rate limit reached |
| 503 | A dependency is unavailable. Quotas and policy checks fail closed |

## Scopes and permissions

A route requires exactly one permission. Scopes bundle permissions; a key can
only be given scopes whose permissions its creator already holds.

| Scope | Permissions (in addition to read-only) |
| --- | --- |
| `read-only` | `session.read`, `catalog.read`, `grants.read`, `tenants.read`, `usage.read` |
| `gateway-inference` | `gateway.invoke`, `gateway.tools`, `access.request`, `memory.read`, `memory.write` |
| `policy-admin` | `policy.read`, `policy.manage` |
| `audit-reader` | `audit.read` |
| `tenant-admin` | all of the above in its tenant, plus `catalog.manage`, `access.review`, `apikeys.manage`, `users.manage`, `memory.manage` |
| `platform-admin` | `session.read`, `tenants.read`, `tenants.manage` only (no tenant data) |

User roles map to scopes: `admin` → `tenant-admin`, `policy-admin`,
`auditor` → `audit-reader`, `user` → `gateway-inference`, `read-only`.

The source of truth is `apps/control-plane/cmd/server/authz.go`
(`routePermission`). A route missing from that table is refused.

## Gateway

| Method and path | Permission | Notes |
| --- | --- | --- |
| `GET /healthz`, `GET /readyz` | none | Liveness; readiness checks dependencies |
| `GET /v1/identity` | `session.read` | Tenant, actor, scopes and permissions of the caller |
| `POST /v1/chat/completions` | `gateway.invoke` | OpenAI-compatible; see below |
| `POST /v1/tools/execute` | `gateway.tools` | Calls a registered tool after grant and policy checks |
| `GET /v1/context/usage?session=<id>` | `usage.read` | Latest measurement for the caller's own session |
| `GET /v1/observability` | `usage.read` | Tenant usage summary |
| `/v1/memory/*` | as `/api/v1/memory/*` | Proxied to the control plane with the caller's identity |

### Chat completions

The request body is OpenAI-compatible. Optional fields are forwarded only when
you send them. Extra request headers:

| Header | Purpose |
| --- | --- |
| `X-SUEZ-Session-Id` | Groups requests into a session for context measurement and SUEZ Memory |

To carry an **approved** checkpoint into the request, add a gateway-only field.
It is never forwarded to the provider:

```json
{
  "model": "demo-model",
  "messages": [{"role": "user", "content": "Continue"}],
  "suez": {"memory": {"checkpointId": "<id>", "tokenBudget": 1500}}
}
```

Injection needs `memory.read`, an OPA `memory.inject` allow, tenant settings
that permit injection, and an approved checkpoint the caller can read. Every
attempt is audited.

Response headers:

| Header | Meaning |
| --- | --- |
| `X-SUEZ-Trace-Id` | Audit trace id |
| `X-SUEZ-Cache` | `HIT` or `MISS` |
| `X-SUEZ-Context-Status` | `ok`, `notice`, `checkpoint`, `warning`, `limit` or `unknown` |
| `X-SUEZ-Context-Percent`, `X-SUEZ-Context-Window`, `X-SUEZ-Context-Source` | Usage after this request, when the model's window is known |
| `X-SUEZ-Checkpoint-Draft` | Id of a checkpoint draft created by crossing the checkpoint threshold |
| `X-SUEZ-Memory-Injected` | Checkpoint, evidence count and tokens used when memory was injected |

`stream: true` returns server-sent events. The usage and context status are
recorded when the stream ends.

## Control plane (`/api/v1`)

| Path | Methods | Permission |
| --- | --- | --- |
| `auth/session` | GET | `session.read` |
| `tenants` | GET / POST | `tenants.read` / `tenants.manage` |
| `api-keys`, `api-keys/{id}` | GET, POST, DELETE | `apikeys.manage` |
| `users`, `users/{id}` | GET, POST, PATCH | `users.manage` |
| `access-requests` | GET / POST | `grants.read` / `access.request` |
| `access-requests/{id}/approve`, `…/deny` | POST | `access.review` |
| `access-grants` | GET | `grants.read` |
| `access-grants/{id}` | DELETE | `access.review` |
| `models`, `tools` | GET / POST | `catalog.read` / `catalog.manage` |
| `policies`, `policies/{id}`, `…/versions` | GET | `policy.read` |
| `policies`, `policies/generate`, `policies/{id}/validate`, `…/tests`, `…/tests/run`, `…/publish`, `…/rollback` | POST | `policy.manage` |
| `audit-log`, `audit-log/traces/{traceId}` | GET | `audit.read` |

### SUEZ Memory (`/api/v1/memory`)

| Path | Methods | Permission |
| --- | --- | --- |
| `settings`, `context-policies` | GET / PUT | `memory.read` / `memory.manage` |
| `preferences` | GET / PUT | `memory.read` / `memory.write` |
| `workspaces`, `workflows` | GET / POST | `memory.read` / `memory.write` |
| `sessions` | GET / POST | `memory.read` / `memory.write` |
| `sessions/{id}` | GET / DELETE | `memory.read` / owner or `memory.manage` |
| `sessions/{id}/timeline` | GET | `memory.read` (if retrieval is enabled) |
| `sessions/{id}/checkpoints` | GET / POST | `memory.read` / `memory.write` |
| `sessions/{id}/export` | GET | `memory.read` |
| `items/{id}` | GET / DELETE | `memory.read` / `memory.write` |
| `checkpoints/{id}` | GET / PATCH | `memory.read` / `memory.write` |
| `checkpoints/{id}/approve`, `…/reject` | POST | `memory.write` |
| `continuations` | POST | `memory.write` |
| `retrievals` | GET | `memory.manage` |
| `retention/run` | POST | `memory.manage` |

Sessions belong to their actor. Other users in the tenant can read them only
with `memory.manage`.

## Internal endpoints

`/internal/auth/resolve` and `/internal/memory/{capture,checkpoint-draft,injection}`
are for gateway → control-plane calls only. They need the
`X-SUEZ-Internal-Token` header and are blocked at the edge.
