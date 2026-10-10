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

## Scope and known limitations

The gateway stack in this repository is a **local-development runtime** and is
known not to be production-hardened. The following are documented limitations,
not new findings (see [docs/SECURITY-AND-PRIVACY.md](docs/SECURITY-AND-PRIVACY.md)):

- JWT signatures are not verified by the control plane.
- A demo API key and demo identities are seeded into fresh databases.
- Default database credentials (`suez` / `suez`), `Access-Control-Allow-Origin: *`,
  self-signed TLS and ports published on all interfaces.

Reports that show these can be exploited in ways beyond what is documented, or
any issue in SUEZ Context Guardian's handling of credentials and local data,
are very welcome.
