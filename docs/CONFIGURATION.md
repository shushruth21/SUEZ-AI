# Configuration

All values below are names and defaults only — never commit real values.
`.env` (copied from [`.env.example`](../.env.example)) and `infra/secrets/*`
are git-ignored.

## Docker Compose stack (`.env`)

| Variable | Default | Required | Purpose |
| --- | --- | --- | --- |
| `COMPOSE_PROJECT_NAME` | `suez` | No | Container name prefix (`suez-gateway-1`, …) |
| `POSTGRES_DB` / `POSTGRES_USER` / `POSTGRES_PASSWORD` | `suez` / `suez` / `suez` | No | Database credentials. **Change the password for anything beyond a laptop.** |
| `POSTGRES_PORT` | `5432` | No | Host port for PostgreSQL |
| `REDIS_PORT` | `6379` | No | Host port for Redis |
| `OPA_PORT` | `8181` | No | Host port for OPA |
| `GATEWAY_PORT` | `8080` | No | Host port for the gateway |
| `CONTROL_PLANE_PORT` | `8081` | No | Host port for the control plane |
| `PORTAL_UI_PORT` | `5173` | No | Host port for the portal dev server |
| `POLICY_LLM_PORT` | `11434` | No | Host port for Ollama |
| `POLICY_LLM_MODEL` | `qwen2.5-coder:1.5b` | No | Model pulled by `policy-llm-init` and used for policy drafting |
| `EDGE_HOST` | `suez.acrosystemsinc.ai` | No | Hostname on the edge certificate and portal URLs |
| `EDGE_HTTPS_PORT` | `5443` | No | HTTPS edge port |
| `OPENAI_COMPAT_API_KEY` | empty | Only for `openai-compatible` models | Upstream key (paid calls) |
| `GROQ_API_KEY` | empty | Only for `groq` models | Upstream key (paid calls) |
| `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` | No | Groq endpoint |

Note: all published ports bind to every host interface. Restrict them (e.g.
`127.0.0.1:8080:8080`) or use a firewall if your machine is on an untrusted
network.

### File-backed provider keys

Instead of environment variables, place keys in (git-ignored):

| File | Read by the gateway as |
| --- | --- |
| `infra/secrets/openai_compat_api_key` | `OPENAI_COMPAT_API_KEY_FILE` |
| `infra/secrets/groq_api_key` | `GROQ_API_KEY_FILE` |

Environment variables take precedence over files.

## Service variables (set by Compose; relevant when running a service directly)

| Variable | Service | Default |
| --- | --- | --- |
| `PORT` | gateway / control plane | `8080` / `8081` |
| `DATABASE_URL` | both | `postgres://suez:suez@postgres:5432/suez?sslmode=disable` |
| `REDIS_URL` | gateway | `redis://redis:6379/0` |
| `REDIS_ADDR` | control plane (health check) | `redis:6379` |
| `AUTH_RESOLVER_URL` | gateway | `http://control-plane:8081/internal/auth/resolve` |
| `OPA_DECISION_URL` / `OPA_HEALTH_URL` | gateway | `http://opa:8181/v1/data/suez/authz/…` |
| `OPA_BASE_URL` | control plane | `http://opa:8181` |
| `CONTROL_PLANE_HEALTH_URL` | gateway | `http://control-plane:8081/healthz` |
| `OPENAI_COMPAT_BASE_URL` | gateway | empty (per-model `upstream_base_url` is used) |
| `POLICY_LLM_BASE_URL` / `POLICY_LLM_URL` / `POLICY_LLM_MODEL` | control plane | `http://policy-llm:11434` … |
| `CONTROL_PLANE_BASE_URL` / `GATEWAY_BASE_URL` / `EDGE_BASE_URL` / `POSTGRES_ADDR` | control plane (status/bootstrap info) | service URLs |
| `VITE_CONTROL_PLANE_URL` / `VITE_GATEWAY_URL` | portal (build time) | `/control-plane`, `/gateway` behind the edge |

## Model context windows

Context-usage percentages need `models.context_window` (added by migration
`0006`). There is no API or UI for it yet; set it with SQL, using the
provider's documented limit:

```sql
UPDATE models SET context_window = 8192 WHERE external_name = 'demo-model';
```

## Context Guardian

Settings are edited in the app (⚙) and stored in `settings.json` in the
app-data directory. The API key is **not** stored there.

| Setting | Default | Meaning |
| --- | --- | --- |
| Project name | `SUEZ` | Name used for unassigned (legacy) checkpoints |
| Token limit | `200000` | Used by the mock source only |
| Checkpoint threshold | `85` % | Auto-save a draft |
| Critical threshold | `95` % | Critical warning |
| Re-arm margin | `5` points | Usage must drop this far below a threshold before it fires again |
| Usage source | `mock` | `mock` or `gateway` |
| Gateway URL | `http://localhost:8080` | Base URL of the SUEZ gateway |
| Gateway session id | generated | Value clients must send as `X-SUEZ-Session-Id` |
| Poll interval | `5` s | Readings older than 3 × this are stale |
| Active project | none | Stable id of the selected project |

Environment variables (development and tests):

| Variable | Purpose |
| --- | --- |
| `CONTEXT_GUARDIAN_DATA_DIR` | Use this directory instead of the OS app-data directory |
| `CONTEXT_GUARDIAN_KEYCHAIN_SERVICE` | Use a different Keychain service name (tests use a throwaway one) |
| `CONTEXT_GUARDIAN_SMOKE_REPORT`, `CONTEXT_GUARDIAN_SMOKE_PHASE` | Run the built-in native smoke test (test harness only) |
| `CG_REAL_CHECKPOINT` | Path for the opt-in, read-only `real_file` Rust test |
| `CG_TEST_GATEWAY_PORT` | Port of the throwaway gateway test stack (default `18080`) |

API key storage: macOS Keychain, service `ai.acrosystems.suez.contextguardian`,
account `gateway-api-key`.

## AWS (experimental)

Templates: [`scripts/aws/env.example`](../scripts/aws/env.example) and
[`docs/saas/implementation-plan-aws.env.example`](saas/implementation-plan-aws.env.example).
They contain placeholders only. These scripts create billable cloud resources
and have not been verified as part of this repository's publication.
