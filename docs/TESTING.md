# Testing

This page lists every test suite in the repository, what it covers, what it
needs, and what it deliberately does **not** cover. No test calls a paid AI
provider; gateway tests use only the in-process **stub** model.

## Summary

| Suite | Location | Command | Runs in CI |
| --- | --- | --- | --- |
| TS type check | `apps/context-guardian` | `npm run typecheck` | Yes |
| Vitest unit tests | `apps/context-guardian/src/**/*.test.ts` | `npm test` | Yes |
| Rust unit tests | `apps/context-guardian/src-tauri/src/*.rs` | `npm run build && npm run test:rust` | Yes (macOS) |
| Native smoke — mock | `scripts/smoke.mjs`, `src/smoke.ts` | `npm run smoke` | No (needs a desktop session) |
| Native smoke — project memory | `scripts/smoke-memory.mjs`, `src/smokeMemory.ts` | `npm run smoke:memory` | No |
| Native smoke — gateway | `scripts/smoke-gateway.mjs` | `npm run smoke:gateway` | No (needs Docker + desktop) |
| Go vet + unit tests | `apps/gateway`, `apps/control-plane` | `go vet ./... && go test ./...` | Yes |
| Gateway integration | `apps/gateway/cmd/server/context_usage_integration_test.go` | `go test -tags integration ./...` | No (needs PostgreSQL + Redis) |
| Portal build | `apps/portal-ui` | `npm ci && npm run build` | Yes |

## Context Guardian

### Unit tests

```sh
cd apps/context-guardian
npm ci
npm run typecheck
npm test
npm run build          # Rust tests embed dist/, so build the frontend first
npm run test:rust
```

- **Vitest** covers thresholds and re-arming, restart guard seeding, mock and
  gateway usage interpretation (stale, unavailable, auth, invalid), draft
  carry-forward and evidence downgrades, project-memory import / merge / diff /
  no-removal, and continuation-prompt output — including a byte-for-byte
  snapshot of the legacy prompt format
  (`src/guardian/__fixtures__/legacy-sample-prompt.txt`).
- **Rust** covers checkpoint schema v1/v2 loading, verified backups before an
  upgrade, refusal of unsupported or unreadable files, atomic writes,
  validation, evidence rules, the secret guard, Keychain abstraction (in-memory
  store), HTTP error mapping and key scrubbing, project-memory revisions,
  conflicts, retention of 50 revisions and corrupt-file recovery.

Fixtures are **synthetic** (`src-tauri/src/testdata/checkpoint-v1-sample.json`).

Ignored Rust tests (run explicitly, not in CI):

| Test | Command | Why ignored |
| --- | --- | --- |
| Real Keychain round trip | `cargo test --manifest-path src-tauri/Cargo.toml keychain -- --ignored` | Writes to the real OS credential store |
| Real checkpoint file, read-only | `CG_REAL_CHECKPOINT=<path> cargo test --manifest-path src-tauri/Cargo.toml real_file -- --ignored` | Needs one of your own checkpoint files; it is copied to a temp dir and checked to be unchanged |

### Native smoke tests (macOS)

These launch the real app binary with a throwaway data directory
(`CONTEXT_GUARDIAN_DATA_DIR`) and a throwaway Keychain service, drive the UI
from inside the WebView, and verify files on disk. Your own data directory and
Keychain item are never used. They temporarily use the clipboard and restore
it afterwards.

```sh
cd apps/context-guardian
npx tauri build --debug --no-bundle     # build the debug binary first
npm run smoke                           # mock source: thresholds, drafts, editor, history, restart
npm run smoke:memory                    # project memory: import, merge, approvals, revisions, recovery
npm run smoke:gateway                   # gateway source against the throwaway stack
```

Add `--release` (e.g. `node scripts/smoke.mjs --release`) to test a release build.

`smoke:gateway` starts a separate stack of containers named `cg-test-*` on
`127.0.0.1:18080` with a fresh database, uses the seeded demo key and the stub
model, and never touches a stack named `suez-*`. Remove it afterwards with
`npm run test-stack -- down --purge`.

**Not covered automatically:** dragging the window, the real-Keychain test
above, behaviour on Windows/Linux, provider-reported token counts from a live
paid provider.

## Gateway and control plane (Go)

```sh
cd apps/gateway && go vet ./... && go test -count=1 ./...
cd apps/control-plane && go vet ./... && go test -count=1 ./...
```

Without Go installed:

```sh
docker run --rm -v "$PWD/apps/gateway":/src -w /src golang:1.26-alpine \
  sh -c 'go vet ./... && go test -count=1 ./...'
```

- Gateway unit tests cover session-header validation, measurement-source
  classification, the `/v1/context/usage` handler (statuses, tenant/actor
  isolation, cache hits, unknown window, missing migration → 503) and
  authentication.
- Control-plane unit tests cover JWT verification (JWKS, algorithms, claims,
  rotation), scoped keys, the permission table, encryption, redaction and
  policy package isolation.
- Enforcement across services (grants, OPA, quotas, cache, tools, memory) is
  covered by the end-to-end suite below.

### End-to-end suite

`scripts/e2e.sh` starts a fresh, throwaway stack (PostgreSQL, Redis, OPA,
control plane, gateway, a mock identity provider and a mock
OpenAI-compatible provider), runs `tests/e2e`, and tears it down:

```sh
sh scripts/e2e.sh -v
```

| Group | Checks |
| --- | --- |
| A | Health probes and what is (not) exposed |
| B | JWT verification: signatures, issuer, audience, expiry, key rotation |
| C | Scoped API keys: scopes, expiry, revocation, allowlists, no escalation |
| D | Tenant isolation across every API |
| E | OPA policy isolation and sandboxed policy tests |
| F | Tool secrets never leak |
| G | Optional parameters, cache keys, provider errors |
| H | Fail-closed quotas and streaming |
| I | Context thresholds, checkpoint drafts and continuation |
| J | No secrets in stored records |
| K | SUEZ Memory through the gateway |

### OPA policies

```sh
docker run --rm -v "$PWD/infra/opa/policies:/p:ro" openpolicyagent/opa:1.4.2 test /p -v
```

### Integration test

Requires a **throwaway** PostgreSQL with all migrations and seeds (including
`0006`) and a Redis instance. Never point it at a database you care about.
With the Context Guardian test stack running (`npm run test-stack -- up` in
`apps/context-guardian`):

```sh
docker run --rm --network cg-test-net \
  -e DATABASE_URL="postgres://suez:cgtest@cg-test-postgres:5432/suez?sslmode=disable" \
  -e REDIS_URL="redis://cg-test-redis:6379/0" \
  -v "$PWD/apps/gateway":/src -w /src golang:1.26-alpine \
  go test -tags integration -count=1 ./...
```

Without `DATABASE_URL` and `REDIS_URL` the integration test skips itself.

## Portal UI

```sh
cd apps/portal-ui && npm ci && npm run build   # tsc --noEmit + vite build
```

There are no portal unit or end-to-end tests. `scripts/browser-demo/` contains
a Playwright demo, not a test suite.

## Known gaps

- `gofmt -l` reports four unformatted Go files
  (`gateway/cmd/server/{main,runtime}.go`,
  `control-plane/cmd/server/{policy_phase6,runtime_registry}.go`); CI does not
  enforce formatting yet.
- `rustfmt` / `clippy` are not part of CI.
- No coverage measurement is configured.
