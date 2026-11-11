# Deployment runbook

Reference deployment: `infra/compose/docker-compose.prod.yml` on one host. For
Kubernetes or a managed platform keep the same shape: only the edge is
public; PostgreSQL, Redis and OPA have no public route; secrets come from files.

## Prerequisites

- Docker Engine with Compose v2, or your orchestrator.
- A DNS name for the edge and a reachable ACME endpoint (or your own certificate in the Caddyfile).
- PostgreSQL 16 (managed recommended; then set `POSTGRES_HOST` and `POSTGRES_SSLMODE=verify-full` and remove the `postgres` service), Redis 7 with a password.
- If users sign in with tokens: an OIDC provider whose tokens carry `iss`, `aud`, `exp`, `sub` and a tenant claim (`tenant_id` or `tenant_slug`), signed with RS256/ES256.
- Provider API keys for the models you will register.

## First deployment

1. **Secrets.** Outside the repository:
   ```bash
   install -d -m 700 /etc/suez/secrets
   openssl rand -base64 32 > /etc/suez/secrets/data_key && chmod 600 /etc/suez/secrets/data_key
   ```
   Put provider keys in their own files. **Back up the data key separately from database backups** — memory content cannot be decrypted without it.
2. **Settings.** `cp .env.production.example .env.production`, fill every value (`openssl rand -base64 32` for tokens and passwords).
3. **Check.** `docker compose --env-file .env.production -f infra/compose/docker-compose.prod.yml config -q` must succeed.
4. **Start.** `docker compose --env-file .env.production -f infra/compose/docker-compose.prod.yml up -d --build --wait`.
   The `migrate` job applies `infra/db/migrations` once each (recorded in `schema_migrations`, under an advisory lock). Seeds are never applied.
5. **First tenant and key.**
   ```bash
   docker compose --env-file .env.production -f infra/compose/docker-compose.prod.yml \
     exec -T control-plane control-plane bootstrap --tenant-slug acme --tenant-name "Acme Inc"
   ```
   Prints one key (`tenant-admin` + `platform-admin`, expires in 30 days). It refuses to run if any key exists. Store it in your secret manager, create named admin users/keys, then revoke it.
6. **Users.** For each person: `POST /api/v1/users {subject, email, role}` (`admin`, `policy-admin`, `auditor`, `user`, `read-only`). `subject` must equal the token's `sub`.
7. **Catalog and access.** Register models (with `contextWindow`), tools, grants; set memory settings and context policies per tenant.
8. **Verify.** Run the [go-live checklist](GO-LIVE-CHECKLIST.md).

## Upgrades

1. Read the CHANGELOG for migrations and configuration changes.
2. Back up the database ([BACKUP-RESTORE.md](BACKUP-RESTORE.md)).
3. `docker compose ... up -d --build --wait` — migrations run first; services start only after they succeed.
4. Check `/control-plane/readyz` and `/gateway/readyz` through the edge, and the logs for `level=ERROR`.

Migrations in this repository are additive (`IF NOT EXISTS`); a release that
needs a destructive change will say so in the CHANGELOG.

## Running more than one control plane

Replicas may share the database: publish, rollback and OPA sync are
serialized by a PostgreSQL advisory lock and every replica reconciles OPA
every 30 s. If replicas use separate OPA instances, each reconciles its own.
The control plane's per-IP rate limit is per instance.
