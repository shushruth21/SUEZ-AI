# Roadmap

Status as of the first GitHub publication. Items move between sections only
when the code (and its tests) actually change.

Legend: **Implemented** — in the code with automated tests · **In progress** —
partly implemented · **Planned** — not started · **Blocked** — waiting on a
decision or external dependency.

## Implemented

- Gateway stack: governed chat completions and tool execution (grants, OPA,
  quotas, cache, audit); control-plane APIs; policy lifecycle with local
  LLM drafting; portal; HTTPS edge; backup/export scripts.
- Per-session context-usage measurement in the gateway and
  `GET /v1/context/usage` (migration `0006`).
- Context Guardian: floating monitor, mock and gateway usage sources, 85 %
  draft / 95 % warning, checkpoint editor and history, continuation prompts,
  Keychain-stored API key.
- Persistent project memory and comprehensive checkpoints: schema v2 with
  backward-compatible reading and verified backups, evidence-aware work items
  and test provenance, reviewed memory updates with revisions and recovery,
  restart duplicate-draft fix.

Note that several implemented gateway and control-plane features have little
or no automated test coverage — see [TESTING.md](TESTING.md).

## In progress

- **Context-window management** — the `models.context_window` column exists;
  an API/portal field to set it does not.
- **Release engineering for Context Guardian** — local `.app`/DMG builds work;
  signing, notarization and published releases do not exist yet
  ([RELEASE.md](RELEASE.md)).

## Planned

- Retention policy and cleanup for `context_usage_measurements` and the audit log.
- Verified provider-reported token counts against live providers (needs an
  explicit decision to spend on paid calls).
- UI for checkpoint backups (list / restore / prune).
- Windows and Linux builds of Context Guardian, including credential-store testing.
- Automated tests for control-plane and core gateway enforcement; `gofmt`,
  `rustfmt` and `clippy` in CI.
- Hardening the gateway stack for shared environments: JWT signature
  verification, restricted CORS, non-default credentials, loopback-only ports,
  TLS between services.
- Enterprise SaaS on AWS (EKS, SSO, multi-tenancy) — see [`saas/`](saas/).
  These are design documents; the scripts in `scripts/aws/` are experimental.

## Blocked / waiting on a decision

- Applying migration `0006` to existing deployments and deploying the updated
  gateway — an operator action; nothing in this repository does it automatically.
- Context Guardian title-bar redesign — deferred by the maintainer.

## Not planned / unsupported

- Reading or measuring sessions in Claude.ai, Claude Code, ChatGPT/Codex,
  Cursor or other clients that do not route through the SUEZ gateway. No
  supported integration provides that data.
- Automatic summarisation of AI conversations by Context Guardian (the app
  cannot read conversations).
