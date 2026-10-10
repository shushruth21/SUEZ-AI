#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
OUTPUT_PATH=${1:-"$ROOT_DIR/tmp/audit-log.csv"}
OUTPUT_DIR=$(dirname "$OUTPUT_PATH")

mkdir -p "$OUTPUT_DIR"

if [ -f "$ROOT_DIR/.env" ]; then
  # shellcheck disable=SC1091
  . "$ROOT_DIR/.env"
fi

docker compose -f "$ROOT_DIR/infra/compose/docker-compose.yml" exec -T postgres \
  psql -U "${POSTGRES_USER:-suez}" -d "${POSTGRES_DB:-suez}" \
  -c "\copy (select created_at, actor, action, resource_type, resource_name, decision, trace_id, metadata from audit_log order by created_at desc) to stdout with csv header" \
  >"$OUTPUT_PATH"

printf 'Exported audit log to %s\n' "$OUTPUT_PATH"
