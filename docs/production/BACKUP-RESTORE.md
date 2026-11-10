# Backup and restore

## What to back up

| Item | Why | How |
| --- | --- | --- |
| PostgreSQL | All configuration, users, keys (hashes), policies, audit, memory (sealed) | `pg_dump -Fc` (or managed-database snapshots) |
| Data key file(s) | Without it, memory content in backups cannot be decrypted | Your secret manager, **stored apart from the database backups** |
| `.env.production` and secret files | Rebuilding the deployment | Secret manager |

Redis holds only the 60-second response cache and per-minute quota counters;
it does not need a backup. OPA is rebuilt from PostgreSQL by the control plane.

## Backup

```bash
docker compose --env-file .env.production -f infra/compose/docker-compose.prod.yml \
  exec -T postgres pg_dump -U "$POSTGRES_USER" -d suez -Fc > suez-$(date +%F).dump
```

Encrypt the dump at rest and keep it on separate storage. Test a restore at
least quarterly.

## Restore

```bash
# into a new, empty database
docker compose ... exec -T postgres createdb -U "$POSTGRES_USER" suez_restored
docker compose ... exec -T postgres pg_restore -U "$POSTGRES_USER" -d suez_restored --no-owner < suez-YYYY-MM-DD.dump
```

Point `DATABASE_URL` at the restored database, start the control plane with
the **same** data key (plus any previous keys), and run `migrate` (it skips
applied migrations). The control plane republishes tenant policies to OPA on
start.

## Verified restore drill (this release)

On the e2e stack: dumped a database containing encrypted memory items,
restored it into a new database, started a control plane against it with the
original data key and read an item's decrypted content through the API; all
5 sealed items were present. With a different data key the same read returned
a generic `500` and no content.

The development helpers `scripts/backup-postgres.sh` and
`scripts/restore-postgres.sh` operate on the development compose stack only.
