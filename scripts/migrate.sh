#!/bin/sh
# Applies SQL migrations in infra/db/migrations in filename order, once each,
# recording them in schema_migrations. Safe to run repeatedly and from several
# hosts at once (a PostgreSQL advisory lock serialises runs).
#
# Usage: DATABASE_URL=postgres://... scripts/migrate.sh [migrations-dir] [seeds-dir]
# Seeds are demo data: pass a seeds directory only for development.
set -eu

MIGRATIONS_DIR="${1:-infra/db/migrations}"
SEEDS_DIR="${2:-}"
: "${DATABASE_URL:?DATABASE_URL is required}"

run() { psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q "$@"; }

run -c "create table if not exists schema_migrations (
  name text primary key, kind text not null default 'migration', applied_at timestamptz not null default now())"

apply() {
  kind="$1"; file="$2"; name="$kind/$(basename "$file")"
  # One transaction per file, under the lock; the file is skipped if another
  # runner applied it while this one waited.
  already=$(psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -qtA -c "select 1 from schema_migrations where name = '$name'")
  if [ "$already" = "1" ]; then
    return 0
  fi
  echo "applying $name"
  {
    echo "begin;"
    echo "select pg_advisory_xact_lock(735539001);"
    echo "do \$\$ begin if exists (select 1 from schema_migrations where name = '$name') then raise exception 'SUEZ_ALREADY_APPLIED'; end if; end \$\$;"
    cat "$file"
    echo ";"
    echo "insert into schema_migrations (name, kind) values ('$name', '$kind');"
    echo "commit;"
  } | psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -f - 2>/tmp/migrate.err || {
    if grep -q SUEZ_ALREADY_APPLIED /tmp/migrate.err; then return 0; fi
    cat /tmp/migrate.err >&2
    echo "migration $name failed; nothing from it was applied" >&2
    exit 1
  }
}

for file in $(ls "$MIGRATIONS_DIR"/*.sql | sort); do apply migration "$file"; done
if [ -n "$SEEDS_DIR" ]; then
  for file in $(ls "$SEEDS_DIR"/*.sql | sort); do apply seed "$file"; done
fi
echo "database schema is up to date"
