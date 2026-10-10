# Development

Day-to-day commands for each component. Installation:
[INSTALLATION.md](INSTALLATION.md). Tests: [TESTING.md](TESTING.md).

## Repository layout

| Path | What |
| --- | --- |
| `apps/context-guardian/src/` | React UI. `guardian/` (thresholds, drafts, memory proposals, prompts), `usage/` (usage sources), `components/` |
| `apps/context-guardian/src-tauri/src/` | Rust core: `lib.rs` (Tauri commands), `storage.rs` (checkpoints, settings, atomic writes), `memory.rs` (project memory), `gateway.rs` (Keychain + HTTP) |
| `apps/context-guardian/scripts/` | Native smoke-test drivers and the throwaway gateway test stack |
| `apps/gateway/cmd/server/` | Gateway (`main.go` routes, `runtime.go` enforcement and providers, `context_usage.go`) |
| `apps/control-plane/cmd/server/` | Control plane (auth, catalog, access, policies, audit) |
| `apps/portal-ui/src/` | Portal UI |
| `infra/` | Compose file, migrations/seeds, OPA policy, Ollama, TLS |
| `scripts/` | Demos, backup/restore, TLS bootstrap, experimental AWS scripts |

## Context Guardian

```sh
cd apps/context-guardian
npm ci
npm run tauri dev        # app with hot reload
npm run dev              # UI only in a browser (Tauri commands unavailable)
npm run typecheck
npm test                 # Vitest
npm run build            # type check + production frontend bundle (dist/)
npm run test:rust        # cargo test (needs dist/ to exist)
npx tauri build --debug --no-bundle   # debug binary used by the smoke tests
npm run tauri build      # release .app + DMG
```

Development data directory: by default the real app-data directory is used.
Point the app at a scratch directory to avoid touching your own checkpoints:

```sh
CONTEXT_GUARDIAN_DATA_DIR=$(mktemp -d) npm run tauri dev
```

Guidelines specific to this app:

- All file I/O, validation, evidence rules and secret checks live in Rust.
  Reuse `Store` and `write_atomic`; do not add a second storage layer.
- New checkpoint fields must be optional with `#[serde(default)]` so older
  files keep loading. Bump `SCHEMA_VERSION` only for incompatible changes and
  extend `SUPPORTED_SCHEMA_VERSIONS`.
- Never put the API key in UI state, logs, errors or files.
- Test fixtures must be **synthetic**. Real checkpoints may be used locally only
  in `src-tauri/src/testdata/local/` or `src/guardian/__fixtures__/local/`
  (git-ignored), e.g. with
  `CG_REAL_CHECKPOINT=<path> cargo test real_file -- --ignored`.

## Gateway and control plane

With Go installed:

```sh
cd apps/gateway        # or apps/control-plane
go vet ./...
go test ./...
go run ./cmd/server    # needs DATABASE_URL, REDIS_URL etc. (see CONFIGURATION.md)
```

Without Go, use Docker:

```sh
docker run --rm -v "$PWD/apps/gateway":/src -w /src golang:1.22-alpine \
  sh -c 'go vet ./... && go test ./...'
```

Rebuild and restart one service in the local stack:

```sh
docker compose -f infra/compose/docker-compose.yml up -d --build gateway
```

## Portal UI

```sh
cd apps/portal-ui
npm ci
npm run dev            # http://localhost:5173 (expects the stack's APIs)
npm run build
```

## Database changes

Add `infra/db/migrations/NNNN_description.sql` (and seeds if needed), mount it
in `infra/compose/docker-compose.yml` for fresh volumes, and document manual
application for existing databases in `infra/db/README.md`. Prefer additive,
idempotent SQL.

## Diagrams

Diagram sources are in `docs/architecture/*.mmd`. After editing one, copy its
content into the matching ```` ```mermaid ```` block in `ARCHITECTURE.md` so
GitHub renders it. Preview with the [Mermaid Live Editor](https://mermaid.live)
or any Mermaid-capable Markdown viewer.

## Demo scripts

See [scripts/README-demo.md](../scripts/README-demo.md).
`demo-groq-with-browser.sh` and `demo-requesters-openai-groq.sh` can call
**paid** providers if keys are configured. The AWS scripts in `scripts/aws/`
and `scripts/deploy-suez-*.sh` create cloud resources and are experimental.
