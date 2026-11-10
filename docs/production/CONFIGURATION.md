# Configuration reference

Every variable may also be given as a file with the `_FILE` suffix (for
example `SUEZ_DATA_KEY_FILE=/run/secrets/data_key`). Prefer files for secrets.

`SUEZ_ENV` defaults to **production**. In production the services refuse to
start when a required value is missing or unsafe and print every problem.

## Shared (gateway and control plane)

| Variable | Default | Production requirement |
| --- | --- | --- |
| `SUEZ_ENV` | `production` | `production` |
| `SUEZ_INTERNAL_TOKEN` | development: a fixed public value | Required, ≥ 32 characters, same on both services, not the development value |
| `DATABASE_URL` | dev compose value | Required; password must not be empty or a common default. Use `sslmode=verify-full` for managed databases |
| `CORS_ALLOWED_ORIGINS` | none (no CORS headers) | Explicit `https://` origins; `*` is refused |
| `TRUSTED_PROXY_CIDRS` | none | CIDRs of the edge/load balancer; `X-Forwarded-For` is only trusted from these |
| `RATE_LIMIT_RPM` | control plane 600, gateway 0 (off) | Per client address, per instance |
| `MAX_REQUEST_BODY_BYTES` | gateway 8 MiB, control plane 1 MiB | — |
| `PORT` | 8080 / 8081 | — |

## Control plane

| Variable | Default | Notes |
| --- | --- | --- |
| `JWT_MODE` | `disabled` | Must be set explicitly in production: `jwks` or `disabled` (API keys only). `dev-hs256` is refused in production |
| `JWT_ISSUER` | — | Required for `jwks`; must be `https` in production. OIDC discovery is used unless `JWT_JWKS_URL` is set |
| `JWT_AUDIENCE` | — | Required for `jwks`; comma-separated list |
| `JWT_JWKS_URL` | discovered | Optional override |
| `JWT_ALLOWED_ALGS` | `RS256,ES256` | Asymmetric only (RS/PS/ES 256/384/512) |
| `JWT_CLOCK_SKEW_SECONDS` | 60 | 0–300 |
| `JWT_JWKS_CACHE_SECONDS` | 600 | 30–86400; unknown `kid` refreshes at most every 30 s |
| `JWT_TENANT_ID_CLAIM` / `JWT_TENANT_SLUG_CLAIM` | `tenant_id` / `tenant_slug` | At least one must be present in tokens |
| `JWT_DEV_SECRET` | — | Development only, ≥ 32 characters |
| `SUEZ_KEY_PROVIDER` | `local` | Only `local` ships in this build |
| `SUEZ_DATA_KEY` | development: public fallback key (logged warning) | Required in production: 32 random bytes, base64 or hex |
| `SUEZ_DATA_KEY_PREVIOUS` | — | Comma-separated old keys kept for decryption during rotation |
| `SUEZ_EXPOSE_DEMO_CREDENTIALS` | off | Refused in production |
| `OPA_BASE_URL`, `OPA_DECISION_URL`, `OPA_HEALTH_URL` | `http://opa:8181/...` | — |
| `POLICY_LLM_URL`, `POLICY_LLM_BASE_URL`, `POLICY_LLM_MODEL` | Ollama on `policy-llm` | Optional; without it drafts come from a template |

## Gateway

| Variable | Default | Notes |
| --- | --- | --- |
| `REDIS_URL` | `redis://redis:6379/0` | Required in production; include the password |
| `AUTH_RESOLVER_URL` | `http://control-plane:8081/internal/auth/resolve` | — |
| `CONTROL_PLANE_INTERNAL_URL` | `http://control-plane:8081` | Memory capture, checkpoint drafts, injection, `/v1/memory` |
| `CONTROL_PLANE_HEALTH_URL` | `http://control-plane:8081/healthz` | Readiness |
| `OPA_DECISION_URL`, `OPA_HEALTH_URL` | `http://opa:8181/...` | — |
| `OPENAI_COMPAT_BASE_URL`, `OPENAI_COMPAT_API_KEY` | — | Per-model `upstreamBaseUrl` overrides the base URL |
| `GROQ_BASE_URL`, `GROQ_API_KEY` | `https://api.groq.com/openai/v1` | — |

## Production compose (`.env.production`, see `.env.production.example`)

`EDGE_HOST`, `ACME_EMAIL`, `POSTGRES_USER`, `POSTGRES_PASSWORD`,
`REDIS_PASSWORD`, `SUEZ_INTERNAL_TOKEN`, `CORS_ALLOWED_ORIGINS`, `JWT_MODE`,
`SUEZ_DATA_KEY_PATH` are required (`docker compose config` stops without
them). Optional: `POSTGRES_HOST`, `POSTGRES_SSLMODE`, `REDIS_HOST`,
`OPENAI_COMPAT_API_KEY_PATH`, `GROQ_API_KEY_PATH`, `EDGE_BIND`,
`EDGE_HTTPS_PUBLISH`, `EDGE_HTTP_PUBLISH`.

## Per-tenant settings (API, not environment)

- `PUT /api/v1/memory/settings` — capture (default off), retrieval, injection (default off), memory retention days, injection token/item limits, audit and usage retention days.
- `PUT /api/v1/memory/context-policies` — notice/checkpoint/warning/block percentages (default 80/85/95/100) and hard block, per model or `*`.
- `PUT /api/v1/memory/preferences` — per-user capture and retrieval opt-out.
- `contextWindow` on each model (`POST /api/v1/models`) — without it no percentage or threshold applies.
