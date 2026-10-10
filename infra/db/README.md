# Database Bootstrap

Phase 0 keeps the local data layer simple:

- `migrations/` contains schema bootstrap SQL.
- `seeds/` contains demo data used by the local runtime.

The PostgreSQL container mounts these files into `docker-entrypoint-initdb.d`, so they run automatically on the first startup of a fresh volume.

Migrations added after a volume was created must be applied by hand. Example for `0006_context_guardian.sql` (additive and idempotent), run from the repository root:

```sh
docker exec -i suez-postgres-1 sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  < infra/db/migrations/0006_context_guardian.sql
```
