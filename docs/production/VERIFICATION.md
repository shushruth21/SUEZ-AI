# Verification record — release candidate on branch `hardening/production-readiness`

Run on 2026-11-10, macOS arm64 host, Docker Desktop. Go runs in
`golang:1.26`; OPA `openpolicyagent/opa:1.4.2` (amd64, emulated).

| Suite | Command | Result |
| --- | --- | --- |
| Control plane: format, vet, unit tests | `docker run --rm -v $PWD/apps/control-plane:/src -w /src golang:1.26 sh -c 'gofmt -l . && go vet ./... && go vet -tags integration ./... && go test -count=1 ./...'` | gofmt clean, vet clean, **24 tests passed, 0 failed** |
| Gateway: format, vet, unit tests | same, `apps/gateway` | gofmt clean, vet clean, **25 tests passed, 0 failed** |
| Go vulnerability scan | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` in each service | **No vulnerabilities found** (both). Before upgrading: 10+ stdlib findings fixed by Go 1.26, plus x/text, pgx and go-redis advisories |
| OPA policy tests | `docker run --rm -v $PWD/infra/opa/policies:/p:ro openpolicyagent/opa:1.4.2 test /p` | **PASS 8/8** |
| Context Guardian TypeScript | `cd apps/context-guardian && npm run typecheck && npx vitest run` | **106 passed** (10 files) |
| Context Guardian Rust | `cargo test --manifest-path src-tauri/Cargo.toml` | **45 passed**, 0 failed, 2 ignored (real keychain/file tests, run manually) |
| Portal | `cd apps/portal-ui && npm run build` | builds |
| npm audit (prod deps, ≥ high) | `npm audit --omit=dev --audit-level=high` in portal and desktop app | **0 vulnerabilities** (both) |
| Compose | `docker compose -f infra/compose/docker-compose.yml config -q`; same for `docker-compose.prod.yml` | dev renders; prod **refuses** without its required secrets |
| Production edge config | `caddy validate` on `infra/tls/Caddyfile.prod` | valid |
| **Full-stack e2e** | `sh scripts/e2e.sh -v` (fresh PostgreSQL, Redis, OPA, control plane, gateway; mock OIDC IdP; mock OpenAI-compatible provider) | **11/11 test groups passed** |

## Acceptance evidence (e2e test → requirement)

| Requirement | Evidence |
| --- | --- |
| Real JWT forgery attempts are rejected | `TestB`: 16 forgeries (alg none, HMAC with the RSA public key, attacker-signed, unknown kid, swapped payload, stripped signature, expired, no exp, nbf in future, wrong iss/aud, unknown/missing/mismatched tenant, no sub) → 401 at control plane **and** gateway; role claims in tokens ignored; inactive user refused |
| Scoped keys cannot exceed permissions or cross tenants | `TestC`: 14 operations × 4 scopes matrix; scope escalation refused; model allowlist; per-key rate limit `[200 200 200 429 429]`; expiry; revocation at both services; grant revocation; `TestD`: cross-tenant reads/creates refused |
| Tenant A's policy cannot affect tenant B | `TestE`: tenant B publishes `allow if { true }` and a policy written to deny tenant A — tenant A's decision unchanged, tenant B's own deny enforced; namespace-escape modules refused; rollback scoped to tenant B |
| Validation/testing never changes live policy | `TestE`: loaded OPA module set compared before/after validate + test run: identical |
| Unauthorized tool-header reads reveal nothing | `TestF`: non-admin sees `***`; secret absent from responses, audit log and `tool_executions` even when the tool endpoint echoes it |
| Optional parameters and cache | `TestG`: omitted fields absent upstream, explicit zero forwarded, invalid ranges 400, identical request served from cache without calling the provider, every meaningful difference misses, no cross-tenant hit, hits and misses audited without prompt text, failed/invalid responses never cached |
| Quotas, audit, usage | `TestH`: RPM `[200 200 429 429]`, TPM 429, stream audited with provider usage, interrupted stream audited as partial and metered |
| Context thresholds, checkpoints, continuation | `TestI`: 50/82/87/96 % → ok/notice/checkpoint/warning headers; one threshold draft; hard block 409 before provider call; progressive retrieval; review/approve; continuation within budget with citations; injection off by default, opt-in, audited, never implicit |
| Retention and deletion | `TestI`: retention job wipes expired items only; session deletion wipes content and blocks timeline, checkpoint and injection; deletion audited |
| Safe production exposure defaults | `TestA` + production-stack run below |
| No secrets in stored records | `TestJ`: admin/tenant/inference keys, provider key, internal token, DB password absent from audit, tool executions, retrievals, policy test runs |

## Additional manual verification (this release)

- **Production example stack** (`docker-compose.prod.yml`, real secrets generated locally, `JWT_MODE=disabled`): only the edge published (bound to 127.0.0.1 for the test); `data` and `backend` networks `internal=true`; demo key rejected; bootstrap printed one key, second bootstrap refused; `/opa*`, `/control-plane/internal*`, `/gateway/internal*` → 404 at the edge; HSTS, CSP, `X-Frame-Options`, `nosniff` present; CORS echoed only for the configured origin; HTTP → HTTPS redirect; unsigned JWT → 401; chat and streaming through the edge with context headers; **no secret value in any service log**.
- **Production refusal**: control plane and gateway with no configuration, and control plane with `JWT_MODE=dev-hs256`, exit listing every problem.
- **Portal click-through** (development stack, built-in browser): admin sign-in with demo key; Setup → generate scoped key (shown once) → revoke (status `revoked`); Governance → *Run tests* → `POST .../tests/run` 200 (previously always 404); Observability loads; sign out; requester sign-in with the control plane's signed dev token; chat refused with *no active model grant* (403) until an admin approved access, then 200. No console errors other than the expected 403.
- **Backup/restore drill** with encrypted memory — see [BACKUP-RESTORE.md](BACKUP-RESTORE.md).
- **Containers run as uid 10001**, not root.

## Not verified (see LIMITATIONS.md)

- A real OpenAI-compatible provider (no provider key was available). The gateway's provider path was exercised against a mock that speaks the same chat-completions and streaming protocol, with the provider key taken from gateway configuration.
- Multiple control-plane replicas under concurrent publishes (lock logic reviewed, not load-tested).
- The native Context Guardian smoke test (`npm run smoke:gateway`, needs a desktop session and a Tauri build) — not run in this pass; its server APIs are covered by e2e `TestK`.
- CI itself has not run on GitHub (nothing was pushed).
