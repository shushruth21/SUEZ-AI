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

1. Create a branch from `main`, named by type: `feature/…`, `fix/…`,
   `docs/…`, `chore/…` or `hardening/…`.
2. Keep each change focused. Avoid unrelated reformatting or refactors.
3. Add or update tests for any behaviour you change. Bug fixes come with a
   test that fails without the fix.
4. Update the documentation when behaviour, configuration or commands change:
   - the README;
   - `docs/API.md` for API changes;
   - `docs/production/CONFIGURATION.md` for new settings;
   - `CHANGELOG.md` under *Unreleased*.
5. Record significant design decisions as an ADR in [docs/adr/](docs/adr/).
6. Open a pull request using the template. Say **what changed, why, and how
   you tested it**. List the tests you actually ran.

### Commit messages

Write the subject line in the imperative mood, at most about 72 characters,
for example "Gateway: fail closed when Redis is unavailable". Prefix it with
the component when that helps. Explain the *why* in the body. Keep each
commit atomic: it should build and pass tests on its own.

### Pull requests and review

- CI must be green before merging.
- Changes to authentication, authorization, tenant isolation, encryption,
  migrations or the production compose file need an owner's review (see
  `.github/CODEOWNERS`).
- Prefer squash merges for small changes. Keep a series of atomic commits
  when they tell a useful story.

## Checks to run

| Area | Command |
| --- | --- |
| Gateway / control plane | `go vet ./... && go test ./...` in `apps/gateway` and `apps/control-plane`. Without local Go, use the `golang:1.26` Docker image (see [docs/TESTING.md](docs/TESTING.md)) |
| Vulnerabilities | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` |
| OPA policies | `docker run --rm -v "$PWD/infra/opa/policies:/p:ro" openpolicyagent/opa:1.4.2 test /p -v` |
| End-to-end | `sh scripts/e2e.sh -v` (Docker; throwaway stack) |
| Context Guardian | `cd apps/context-guardian && npm run typecheck && npm test && npm run build && npm run test:rust` |
| Context Guardian (native, macOS) | `npx tauri build --debug --no-bundle && npm run smoke && npm run smoke:memory` |
| Portal UI | `cd apps/portal-ui && npm ci && npm run build` |

CI runs all of these except the native smoke tests on every pull request.

### Adding an endpoint

1. Add the route to `routePermission` in
   `apps/control-plane/cmd/server/authz.go`, or to the gateway's route table.
   An unlisted route is refused.
2. Scope every query to the caller's tenant.
3. Add an e2e case for cross-tenant access to `tests/e2e`.
4. Document it in `docs/API.md`.

### Database migrations

- Add a new numbered file in `infra/db/migrations/`.
- Never edit a migration that has been applied.
- Migrations run through `scripts/migrate.sh`, which records them in
  `schema_migrations`.
- Prefer additive, backward-compatible changes so a rollback of the services
  does not require a database rollback.

## Coding standards

- **Match the surrounding code**: naming, comment density and idioms.
- **TypeScript**: strict mode is on; keep `npm run typecheck` clean. Pure logic
  lives in `src/guardian/` and `src/usage/` with Vitest tests next to it.
- **Rust**: anything that protects data (validation, evidence rules, secret
  guard, file writes) belongs in Rust, not only in the UI. Use the existing
  `write_atomic` and `Store` APIs — do not add a parallel storage path. No new
  compiler warnings.
- **Go**: `gofmt` and `go vet` clean (CI enforces both). Return errors, don't panic. Never log request bodies, keys or tokens.
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
  addresses) in examples and tests. They only work on the development stack.
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
