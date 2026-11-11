# Roadmap

Status as of the `hardening/production-readiness` branch (November 2026).
Items move between sections only when the code and its tests actually change.

Legend:
- **Implemented**: in the code, with automated tests.
- **In progress**: partly implemented.
- **Planned**: not started.
- **Blocked**: waiting on a decision or an external dependency.

## Implemented

- **Gateway:** governed chat completions (including streaming) and tool
  execution, with grants, OPA, fail-closed quotas, a complete cache key, safe
  retries, audit and secret redaction.
- **Identity:**
  - JWT verification against an OIDC provider;
  - scoped, expirable, revocable API keys;
  - one permission table for every route;
  - a first-tenant bootstrap command.
- **Tenant isolation:** across every API, plus per-tenant OPA packages and
  sandboxed policy tests.
- **Multi-replica policy changes:** an advisory lock and a reconciler for
  publishing, rolling back and syncing policies.
- **SUEZ Memory:** sessions, evidence, cited summaries, reviewed checkpoints,
  continuation, audited injection, export, deletion, retention, and envelope
  encryption.
- **Context thresholds in the gateway**, per tenant and model. The context
  window is set through the model catalog API.
- **Production configuration:**
  - safe defaults and refusal of unsafe settings;
  - separate dev, prod, staging and e2e compose files;
  - a migration runner;
  - non-root images.
- **Staging for testers** on one machine through a tunnel, and `make dmg`.
- **Context Guardian:**
  - floating monitor, mock and gateway sources, and the tenant's thresholds;
  - checkpoint editor, history and server checkpoints;
  - continuation prompts;
  - project memory;
  - Keychain-stored key.
- **CI:** Go vet and tests, gofmt, govulncheck, OPA tests, full-stack e2e,
  compose validation, Context Guardian TypeScript and Rust tests, portal
  build, and npm audit.

## In progress

- **Release engineering for Context Guardian.** Local `.app` and `.dmg`
  builds work. Signing, notarization and published releases need an Apple
  Developer ID.
- **First CI run on GitHub.** The workflow has been validated locally but has
  not yet run on GitHub-hosted runners.

## Planned

- A cloud KMS key provider (AWS KMS, GCP KMS or Vault) behind the existing
  `keyProvider` interface, and a re-wrap job for key rotation.
- Verification against a live paid provider. This needs a decision to spend on
  paid calls.
- A multi-replica load test of the gateway and control plane, and a shared
  (Redis) rate limit for the control plane.
- An egress allowlist for tool targets.
- A per-token JWT denylist, and a tenant deletion API.
- LLM-suggested content inside checkpoint drafts, keeping human approval.
- Windows and Linux builds of Context Guardian.
- Enterprise SaaS on AWS (EKS, SSO). See [`saas/`](saas/). These are design
  documents; the scripts in `scripts/aws/` are experimental.

## Blocked / waiting on a decision

- Context Guardian title-bar redesign, deferred by the maintainer.

## Not planned / unsupported

- Reading or measuring sessions in Claude.ai, Claude Code, ChatGPT/Codex,
  Cursor or other clients that do not route through the SUEZ gateway. No
  supported integration provides that data.
- Automatic summarisation of AI conversations by Context Guardian (the app
  cannot read conversations).
