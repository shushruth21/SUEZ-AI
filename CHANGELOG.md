# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project has **no
tagged releases yet**; component manifests carry version `0.1.0`.

Development before the first publication to GitHub was not tracked in Git, so
earlier history is summarised rather than itemised.

## [Unreleased]

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
