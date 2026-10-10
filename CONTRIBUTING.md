# Contributing to SUEZ

Thank you for considering a contribution. This guide covers setup, tests,
standards and — most importantly — how to keep secrets and private data out of
the repository.

## Before you start

- Read the [README](README.md) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- For anything larger than a small fix, open an issue first so the approach can
  be agreed.
- Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Setup

See [docs/INSTALLATION.md](docs/INSTALLATION.md) and
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md). In short:

```sh
# Gateway stack
make up

# Context Guardian
cd apps/context-guardian && npm ci && npm run tauri dev
```

## Making changes

1. Fork the repository and create a branch from `main`
   (`feature/…`, `fix/…`, `docs/…`).
2. Keep changes focused; avoid unrelated reformatting or refactors.
3. Add or update tests for behaviour you change.
4. Update documentation when behaviour, configuration or commands change —
   including the feature-status table in the README and
   [docs/ROADMAP.md](docs/ROADMAP.md).
5. Run the checks below and open a pull request describing **what changed, why,
   and how you tested it**. State which tests you ran; do not claim tests you
   did not run.

## Checks to run

| Area | Command |
| --- | --- |
| Context Guardian | `cd apps/context-guardian && npm run typecheck && npm test && npm run build && npm run test:rust` |
| Context Guardian (native, macOS) | `npx tauri build --debug --no-bundle && npm run smoke && npm run smoke:memory` |
| Gateway / control plane | `go vet ./... && go test ./...` in `apps/gateway` and `apps/control-plane` |
| Portal UI | `cd apps/portal-ui && npm ci && npm run build` |

CI runs the non-native checks on every pull request. Details and the tests that
need Docker or a desktop session: [docs/TESTING.md](docs/TESTING.md).

## Coding standards

- **Match the surrounding code**: naming, comment density and idioms.
- **TypeScript**: strict mode is on; keep `npm run typecheck` clean. Pure logic
  lives in `src/guardian/` and `src/usage/` with Vitest tests next to it.
- **Rust**: anything that protects data (validation, evidence rules, secret
  guard, file writes) belongs in Rust, not only in the UI. Use the existing
  `write_atomic` and `Store` APIs — do not add a parallel storage path. No new
  compiler warnings.
- **Go**: `go vet` clean; run `gofmt` on files you touch.
- **SQL migrations**: add a new numbered file; never edit an applied migration.
  Prefer additive, idempotent changes (`IF NOT EXISTS`) and document manual
  application for existing databases in `infra/db/README.md`.
- **Honesty in the product**: never present estimated, mock or stale numbers as
  measured; never mark something verified without evidence.

## Secrets and private data — rules

- **Never commit** `.env`, files in `infra/secrets/`, private keys,
  certificates, database dumps, logs with request bodies, or API keys of any
  kind. `.gitignore` covers the common cases, but it does not protect a file
  you force-add.
- **Never commit real Context Guardian data**: checkpoints, project memory,
  revisions or continuation prompts may contain private project details. Tests
  must use **synthetic fixtures** (see
  `apps/context-guardian/src-tauri/src/testdata/checkpoint-v1-sample.json`).
  Local-only fixtures go in the git-ignored `testdata/local/` and
  `__fixtures__/local/` folders.
- Use the documented demo values (e.g. `suez-demo-admin-key`, `example.com`
  addresses) in examples and tests.
- Tests must not call paid AI providers. Use the stub model.
- Before pushing, review `git diff --cached` and run a secret scanner if you
  have one (for example `gitleaks protect --staged`).
- If you accidentally commit a secret, **rotate it immediately** and tell the
  maintainer; removing it in a later commit does not remove it from history.

## Documentation

Documentation lives in `docs/` and in component READMEs. Diagrams are Mermaid
sources in `docs/architecture/*.mmd`; update the copy embedded in
`docs/ARCHITECTURE.md` when you change one.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE) of this repository.
