# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project has **no
tagged releases yet**; component manifests carry version `0.1.0`.

Development before the first publication to GitHub was not tracked in Git, so
earlier history is summarised rather than itemised.

## [Unreleased]

### Production readiness (branch `hardening/production-readiness`)

Security and identity
- JWT verification against an OIDC provider (JWKS discovery, issuer,
  audience, asymmetric algorithm allowlist, exp/nbf/iat with skew, key
  rotation, fail closed). Tokens bind to stored users; roles come from the
  database.
- Scoped, expirable, revocable, attributable API keys with model/tool
  allowlists and per-key rate limits; keys cannot grant more than their
  creator holds. `control-plane bootstrap` creates the first tenant.
- One permission table authorizes every control-plane route; the gateway
  checks a permission per route. Internal endpoints need a shared token.
- Secrets never appear in responses, logs, audit records or cache keys;
  provider and tool errors are normalized.

Operations
- `SUEZ_ENV` defaults to production and refuses unsafe settings; CORS
  allowlist, trusted proxies, rate limiting, security headers, request ids,
  structured logs, liveness and readiness probes.
- Separate development and production compose files; only the edge is
  public in production; migrations run through `scripts/migrate.sh`.
- Go 1.26 and patched dependencies (govulncheck clean); images run as non-root.

Policy
- Tenant policy isolation, sandboxed validation/testing, database advisory
  lock and reconciler for multi-replica publish/rollback/sync.

Gateway
- Streaming, safe retries, response validation, complete cache key with
  policy version, fail-closed quotas.
- Context-window thresholds per tenant/model with optional hard block and
  checkpoint drafts.

SUEZ Memory
- Tenant-scoped sessions, evidence, cited summaries, structured
  checkpoints with review, continuation within a token budget, explicit and
  audited injection, export, deletion, retention, envelope encryption and
  secret redaction. Context Guardian uses these APIs through the gateway.

Documentation and tests
- docs/production: threat model, architecture, configuration, runbooks,
  go-live checklist, verified limitations and verification record.
- Full-stack e2e suite (`scripts/e2e.sh`) and CI jobs for it, govulncheck,
  npm audit and compose validation.

### Added
- First publication of the SUEZ source tree to GitHub.
- Root documentation: README rewrite with feature-status table,
  `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`, this
  changelog.
- `docs/ARCHITECTURE.md` and editable Mermaid diagrams in `docs/architecture/`
  (system overview, context-monitoring flow, checkpoint lifecycle, deployment
  topology).
- `docs/` guides: `INSTALLATION`, `DEVELOPMENT`, `CONFIGURATION`, `TESTING`,
  `SECURITY-AND-PRIVACY`, `DATA-RETENTION`, `ROADMAP`, `RELEASE`.
- GitHub Actions CI: Context Guardian type check, Vitest and Rust tests; Go vet
  and unit tests; portal build. No secrets, deployments or migrations.
- `apps/context-guardian/.gitignore` and extended root `.gitignore`
  (build output, installers, local data, database dumps, local-only fixtures).

### Changed
- Context Guardian tests now use a **synthetic** schema-1 checkpoint fixture
  (`checkpoint-v1-sample.json`) and a matching legacy-prompt snapshot instead of
  a copy of a real checkpoint.
- `docs/architecture.md` renamed to `docs/gateway-architecture.md` and
  `docs/troubleshooting.md` to `docs/TROUBLESHOOTING.md` (avoids case-only name
  clashes); links updated. Added `GET /v1/context/usage` to the gateway API
  table and fixed an invalid Mermaid ER attribute.

### Security
- Control plane: only admins can create tenants or list/create API keys, and
  API keys can only be listed or created for the caller's own tenant;
  non-admins see only their own tenant. JWT tenants must exist in the database
  (unknown or mismatched `tenant_id`/`tenant_slug` is rejected).
- Generated Rego escapes tenant IDs and target names as string literals.
- `/api/v1/bootstrap/status` returns demo credentials only when
  `SUEZ_EXPOSE_DEMO_CREDENTIALS=true`.
- Caddy edge no longer routes `/opa/*` or `/policy-llm/*`; compose binds OPA
  and Ollama to `127.0.0.1` by default (`OPA_BIND`, `POLICY_LLM_BIND`).
- Gateway and control plane: request body limit (`MAX_REQUEST_BODY_BYTES`),
  read/write/idle timeouts, graceful shutdown, bounded identity lookups.
- Gateway: provider and tool error bodies go to logs/audit only; callers get
  a short message plus `traceId`.
- Context Guardian: plain `http://` gateway URLs are allowed only for
  loopback hosts.
- OPA: tenant-authored policies are loaded into their own package
  (`suez.generated.t_<tenant>.p_<policy>`) and the base policy reads only the
  caller's tenant. A published policy can no longer change the base `allow`
  rule or another tenant's decisions. The control plane accepts only modules
  whose first statement is `package suez.authz`, rewrites that line, and
  checks the package OPA actually parsed.
- Policy validation and test runs use a private `suez.sandbox.*` module and
  never replace the live policy set (previously a test run removed every
  tenant's published policies from OPA while it ran). Policy sync writes new
  modules before removing stale ones, under a lock.
- `GET /api/v1/tools` hides `httpHeaders` values from non-admins.

### Fixed
- Gateway chat cache key now includes `temperature` and `max_tokens`.
- Cache hits are written to the audit log.
- `/v1/observability` uses `SCAN` instead of `KEYS` and is per tenant.
- `POST /api/v1/policies/{id}/tests/run` always returned 404 (route order).
- Publishing a disabled policy now fails with 409 instead of superseding the
  live policy while enforcing nothing.
- Gateway no longer sends `temperature: 0` / `max_tokens: 0` upstream when the
  caller omits them; ranges and message roles are validated.
- Status codes: control-plane outage → 503 from the gateway (was 401 with an
  internal dial error); Redis outage → 503 (was 429); DB errors → generic 500
  (were 404/403 with SQL text); malformed IDs → 404; duplicates → 409.
- Input validation for models (provider, URL, limits), tools (mode, target
  URL, JSON Schemas, headers, risk tier, timeout), tenants (slug) and access
  requests (resource type, lengths).
- Quota counters set their TTL atomically; upstream chat bodies are capped and
  empty `choices` is treated as an upstream error.

### Added
- First control-plane tests (role and tenant checks, Rego escaping, body
  limit); gateway tests for cache key and error handling; CI `gofmt` check.
- OPA policy tests (`infra/opa/policies/suez/phase0_test.rego`) and a CI job
  that runs them.

## Pre-publication development (summary)

Work completed before Git history began, as found in the source tree:

- **Gateway stack** — gateway, control plane, portal, OPA policies,
  Ollama-based policy drafting, PostgreSQL migrations `0001`–`0005` with demo
  seeds, Redis quotas and cache, Caddy HTTPS edge, backup/export and demo
  scripts, AWS design documents.
- **Context Guardian MVP** — floating widget, mock usage, 85 % / 95 %
  thresholds, checkpoint editor, local history, continuation prompts.
- **Gateway integration** — `X-SUEZ-Session-Id`, per-session usage
  measurement, `GET /v1/context/usage`, migration `0006_context_guardian.sql`,
  gateway usage source with Keychain-stored API key.
- **Persistent project memory and comprehensive checkpoints** — checkpoint
  schema v2 with backward-compatible reading and verified backups, evidence
  model, project memory with reviewed updates and revisions, restart
  duplicate-draft fix, structured editor, memory-aware continuation prompts.
