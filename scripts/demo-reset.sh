#!/usr/bin/env bash
# Demo script 1: Wipe all models and policies (and dependent data) for a clean demo slate.
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ROOT_DIR/.env"
  set +a
fi

COMPOSE_FILE="${ROOT_DIR}/infra/compose/docker-compose.yml"
export PATH="${HOME}/.docker/bin:${PATH:-}"

echo "Resetting models and policies..."

docker compose -f "$COMPOSE_FILE" exec -T postgres psql -U "${POSTGRES_USER:-suez}" -d "${POSTGRES_DB:-suez}" <<'SQL'
DELETE FROM policy_test_cases;
DELETE FROM policy_test_runs;
DELETE FROM policies;
DELETE FROM access_grants;
DELETE FROM access_requests;
DELETE FROM models;
SQL

echo "Done. Models, policies, access requests, and grants have been wiped."
