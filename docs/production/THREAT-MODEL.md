# Threat model

Scope: the gateway, control plane, OPA, PostgreSQL, Redis, the HTTPS edge,
the portal, and the Context Guardian desktop app as deployed with
`infra/compose/docker-compose.prod.yml` (or an equivalent).

## Assets

| Asset | Where | Why it matters |
| --- | --- | --- |
| Provider credentials (OpenAI-compatible, Groq) | Gateway secret files | Spend and data exposure at the provider |
| SUEZ API keys | Hash only (`api_keys.key_hash`, SHA-256) | Access to tenant resources |
| Data key (`SUEZ_DATA_KEY`) | Control-plane secret file | Decrypts stored memory content |
| Internal token (`SUEZ_INTERNAL_TOKEN`) | Gateway and control plane | Grants use of `/internal/*` (identity resolution, memory capture) |
| Prompts, responses, memory items, checkpoints | PostgreSQL (memory content sealed); Redis cache (60 s) | Customer data |
| Tenant policies (Rego) | PostgreSQL and OPA | Decide every request |
| Audit log | PostgreSQL | Accountability |
| Tool HTTP headers | PostgreSQL (`tools.http_headers`) | Credentials for downstream tools |

## Trust boundaries

1. **Internet → edge.** Only the edge publishes ports. TLS, HSTS, size limits, `/internal*`, `/opa*`, `/policy-llm*` return 404.
2. **Edge → gateway / control plane.** Every `/v1` and `/api/v1` request is authenticated, then authorized against one permission table per service.
3. **Gateway → control plane (`/internal/*`).** Requires `SUEZ_INTERNAL_TOKEN`; without it the endpoints answer 404.
4. **Services → OPA, PostgreSQL, Redis.** Private networks with no route in or out (`internal: true`).
5. **Gateway → providers and tool endpoints.** Outbound only; provider responses are untrusted input.
6. **Control plane → identity provider (JWKS).** Outbound only; keys are cached, unknown keys trigger at most one refresh per 30 s.
7. **Tenant A ↔ tenant B.** Every query is scoped by the authenticated tenant; OPA reads only the caller's tenant package.
8. **Desktop app → gateway.** API key held in the OS keychain, only sent by Rust, only to `https` or loopback `http`.

## Threats and mitigations (STRIDE)

| # | Threat | Mitigation | Verified by |
| --- | --- | --- | --- |
| S1 | Forged JWT (alg none, HMAC with public key, attacker key, swapped payload) | Signature verified against IdP JWKS; algorithm allowlist of asymmetric algorithms only; issuer, audience, exp (required), nbf, iat checked with ≤300 s skew | `TestJWTVerificationRejectsForgeries`, e2e `TestB_JWTVerification` (16 forgery cases, both services) |
| S2 | Self-declared role or tenant in a token | Roles come from the `users` table; tenant claim must name an existing tenant and the subject must be an active member | e2e `TestB` (role claim ignored, inactive user refused, cross-tenant member refused) |
| S3 | Stolen or leaked API key | Keys are scoped, optionally model/tool-restricted, rate-limited, expirable and revocable; revocation takes effect on the next request | e2e `TestC_ScopedAPIKeys` |
| S4 | IdP outage used to bypass auth | Fails closed: no stale keys after cache TTL, 503 to the caller | `TestJWKSRotationAndFailClosed` |
| T1 | Tenant policy changes another tenant's decisions or the base `allow` | Policies load into `suez.generated.t_<tenant>.p_<policy>`; base policy reads only the caller's tenant; package line checked lexically and against OPA's parsed AST | Rego tests, `TestOPAModuleSourceAndTestRunRoute`, e2e `TestE_PolicyIsolation` |
| T2 | Policy test or validation alters live decisions | Drafts load into private `suez.sandbox.*` modules; module set compared before/after in e2e | e2e `TestE` |
| T3 | Concurrent publish across replicas leaves OPA inconsistent | PostgreSQL advisory lock around publish/rollback/sync; put-then-delete sync; reconciler every 30 s | Code review; single-host e2e (multi-replica not load-tested, see LIMITATIONS) |
| T4 | Ciphertext copied between tenants' rows | AES-GCM with the row's tenant and id as additional data | `TestContentSealer` |
| R1 | Actions without attribution | Audit records for model/tool calls (hit, miss, error, partial stream), key create/revoke, grant revoke, policy lifecycle, memory capture/retrieval/injection/deletion | e2e `TestC`, `TestG`, `TestH`, `TestI` |
| I1 | Secrets in responses, logs, audit, cache keys | Tool headers hidden from non-admins; provider/tool error bodies never returned or audited; logs carry path and status only; cache keys are SHA-256 digests | e2e `TestF`, `TestG`, `TestJ`; production-stack log scan |
| I2 | Credentials pasted into prompts persisted in memory | Pattern-based redaction before storage; memory content encrypted at rest | e2e `TestI` (key in prompt never stored) |
| I3 | Cross-tenant cache hit | Key includes tenant, published-policy stamp, model and every request option | e2e `TestG`, unit `TestChatCacheKeyCoversEveryMeaningfulDifference` |
| I4 | Internals leaked by health or errors | Readiness returns names and states only; DB and upstream errors become generic messages with a request or trace id | e2e `TestA` |
| D1 | Quota bypass when Redis is down | Quota and per-key limits fail closed (503) | `TestQuotaFailsClosedWhenRedisIsUnavailable` |
| D2 | Oversized bodies, slow clients, runaway streams | Edge and service body limits, read/write/idle timeouts, upstream body cap 16 MiB, stream idle timeout 60 s | Code review, unit tests for body limit |
| D3 | Credential guessing | Per-IP rate limit on the control plane (`RATE_LIMIT_RPM`), generic 401s | Code review |
| E1 | Key minted with more rights than its creator | `canGrantScopes`; `platform-admin` cannot be granted by a tenant admin | `TestAPIKeyScopesCannotExceedCreator`, e2e `TestC` |
| E2 | Silent injection of old memory into a model | Only when the request names an approved checkpoint, the tenant enabled injection, policy allows `memory.inject`, the user has not opted out; budgeted, cited and audited | e2e `TestI` |
| E3 | SSRF via tool redirects carrying secret headers | Tool client does not follow redirects | `TestToolErrorsAreNormalized` |

## Residual risks

See [LIMITATIONS.md](LIMITATIONS.md). The largest: tool target URLs are chosen by tenant admins and can reach any address the gateway can reach; the bundled key provider is a local data key, not a KMS; redaction is pattern-based.
