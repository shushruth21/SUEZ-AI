# Support

SUEZ is maintained on a best-effort basis. There is no commercial support.

## Getting help

1. Check the [README](README.md), [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)
   and the [Context Guardian README](apps/context-guardian/README.md).
2. Search [existing issues](https://github.com/shushruth21/SUEZ-AI/issues).
3. Open a new issue if your problem is not covered.

## Reporting a reproducible issue safely

Please include:

- What you did, what you expected and what happened.
- The component (Context Guardian, gateway, control plane, portal, Compose stack).
- Commit SHA, OS and version, and relevant tool versions (`node --version`,
  `rustc --version`, `go version`, `docker --version`).
- Minimal steps to reproduce, ideally with the stub model and demo data.
- Relevant log lines or screenshots.

**Never post** API keys, `.env` contents, files from `infra/secrets/`,
certificates, database dumps, Context Guardian checkpoints, project-memory
files or continuation prompts from real work. Redact identifiers you would not
want public. If you are unsure, describe the data instead of attaching it.

Security vulnerabilities must be reported privately — see [SECURITY.md](SECURITY.md).
