#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
OUTPUT_PATH=${1:-"$ROOT_DIR/tmp/suez-postgres-backup.sql"}
OUTPUT_DIR=$(dirname "$OUTPUT_PATH")

mkdir -p "$OUTPUT_DIR"

if [ -f "$ROOT_DIR/.env" ]; then
  # shellcheck disable=SC1091
  . "$ROOT_DIR/.env"
fi

docker compose -f "$ROOT_DIR/infra/compose/docker-compose.yml" exec -T postgres \
  pg_dump -U "${POSTGRES_USER:-suez}" "${POSTGRES_DB:-suez}" >"$OUTPUT_PATH"

printf 'Wrote backup to %s\n' "$OUTPUT_PATH"
