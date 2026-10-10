# Local Development

## Prerequisites

- Docker Desktop or compatible Docker runtime
- GNU Make

## Environment

The stack runs with Compose defaults, but you can create `.env` to override ports or credentials.

Optional file-backed secrets can be placed under `infra/secrets/`:

- `infra/secrets/openai_compat_api_key`

The gateway reads `OPENAI_COMPAT_API_KEY` first, then falls back to `OPENAI_COMPAT_API_KEY_FILE`.

## Commands

```sh
make build
make up
make ps
make logs
make down
make clean
```

## HTTPS Edge

The local stack now includes an HTTPS reverse proxy on `https://suez.acrosystemsinc.ai:5443`.

- `./scripts/bootstrap-local-tls.sh` generates a local CA-signed cert for `suez.acrosystemsinc.ai` in `infra/tls/generated/`, then trusts that CA in the macOS login keychain.
- Add `127.0.0.1 suez.acrosystemsinc.ai` to `/etc/hosts` if the bootstrap step has not already done so on your machine.
- `tls-init` remains as a fallback generator for bare container boots, but the host bootstrap script is the preferred browser workflow.
- Direct HTTP ports remain available for low-friction local debugging.

## Backup And Export

The repo now includes helper scripts for local operations:

```sh
./scripts/backup-postgres.sh
./scripts/restore-postgres.sh tmp/suez-postgres-backup.sql
./scripts/export-audit-log.sh
```

## First Boot

On the first `make up`, PostgreSQL runs the SQL scripts mounted from `infra/db/migrations/` and `infra/db/seeds/`.

If you need a clean bootstrap:

```sh
make clean
make up
```

## Current Local Trade-Offs

- Compose is used for local development even though the target deployment runtime is Kubernetes.
- The HTTPS edge uses a self-signed certificate to stay laptop-friendly; direct HTTP ports remain available.
- Secrets can be file-backed locally, but this is still a developer-oriented workflow rather than a production secret manager.
