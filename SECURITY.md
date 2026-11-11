# Security Policy

## Supported versions

SUEZ has no published releases. Security fixes are made on the `main` branch
only.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately to the maintainer through GitHub:

- If the repository's **"Report a vulnerability"** button is available under the
  **Security** tab, use it (GitHub private vulnerability reporting).
- Otherwise, contact the repository owner,
  [@shushruth21](https://github.com/shushruth21), through GitHub and ask for a
  private channel before sharing details.

Include the affected component, version or commit, steps to reproduce and the
impact you observed. **Do not include real API keys, `.env` contents,
checkpoints or project-memory files** — redact them.

There is no formal response-time commitment; reports are handled on a
best-effort basis by the maintainer.

## Scope

In scope:

- The gateway, control plane, OPA policies, portal and Context Guardian in
  this repository, run with **production settings** (`SUEZ_ENV=production`,
  `infra/compose/docker-compose.prod.yml`).
- Anything that lets one tenant read or change another tenant's data,
  policies, memory or keys; lets an API key exceed its scopes; or exposes
  secrets in responses, logs, audit records or memory.

Out of scope (documented, intentional behaviour):

- The **development** stack (`infra/compose/docker-compose.yml`): demo API
  key, `dev-hs256` JWTs, permissive CORS and default database credentials are
  there on purpose and refused by production mode.
- Limitations already listed in
  [docs/production/LIMITATIONS.md](docs/production/LIMITATIONS.md), unless you
  show impact beyond what is described there.

The threat model is in
[docs/production/THREAT-MODEL.md](docs/production/THREAT-MODEL.md).
