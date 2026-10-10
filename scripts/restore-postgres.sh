#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
INPUT_PATH=${1:-}

if [ -z "$INPUT_PATH" ]; then
  printf 'Usage: %s <backup.sql>\n' "$0" >&2
  exit 1
fi

if [ ! -f "$INPUT_PATH" ]; then
  printf 'Backup file not found: %s\n' "$INPUT_PATH" >&2
  exit 1
fi

if [ -f "$ROOT_DIR/.env" ]; then
  # shellcheck disable=SC1091
  . "$ROOT_DIR/.env"
fi

docker compose -f "$ROOT_DIR/infra/compose/docker-compose.yml" exec -T postgres \
  psql -U "${POSTGRES_USER:-suez}" -d "${POSTGRES_DB:-suez}" <"$INPUT_PATH"

printf 'Restored backup from %s\n' "$INPUT_PATH"
