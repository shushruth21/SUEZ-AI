#!/usr/bin/env bash
# Throwaway SUEZ stack for testing Context Guardian against the real gateway.
#
# Creates only containers named cg-test-* on the network cg-test-net, with a
# fresh database seeded from infra/db (including 0006). It never touches the
# suez-* containers, the suez_postgres-data volume or suez-gateway:latest.
# The gateway is built from apps/gateway as suez-gateway:cg-test and published
# on 127.0.0.1:${CG_TEST_GATEWAY_PORT:-18080}. Only the free stub model is used.
#
# Usage: scripts/gateway-test-stack.sh up | down | status
#        down --purge also removes the test image and Go cache volumes.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
NET=cg-test-net
PORT="${CG_TEST_GATEWAY_PORT:-18080}"
DB_URL="postgres://suez:cgtest@cg-test-postgres:5432/suez?sslmode=disable"
CONTAINERS=(cg-test-gateway cg-test-control-plane cg-test-opa cg-test-redis cg-test-postgres)

exists() { docker container inspect "$1" >/dev/null 2>&1; }
running() { [ "$(docker container inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

wait_for() {
  local label="$1" tries="$2"; shift 2
  for _ in $(seq 1 "$tries"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "timed out waiting for $label" >&2
  return 1
}

up() {
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null

  if ! exists cg-test-postgres; then
    local M="$REPO_ROOT/infra/db/migrations" S="$REPO_ROOT/infra/db/seeds" I=/docker-entrypoint-initdb.d
    docker run -d --name cg-test-postgres --network "$NET" \
      -e POSTGRES_DB=suez -e POSTGRES_USER=suez -e POSTGRES_PASSWORD=cgtest \
      -v "$M/0001_init.sql:$I/001_init.sql:ro" \
      -v "$M/0002_phase2_access_workflow.sql:$I/002.sql:ro" \
      -v "$M/0003_phase3_runtime_governance.sql:$I/003.sql:ro" \
      -v "$M/0004_phase5_policy_lifecycle.sql:$I/004.sql:ro" \
      -v "$M/0005_phase6_policy_tests.sql:$I/005.sql:ro" \
      -v "$S/0001_seed.sql:$I/006.sql:ro" \
      -v "$S/0002_phase2_access_workflow.sql:$I/007.sql:ro" \
      -v "$S/0003_phase3_runtime_governance.sql:$I/008.sql:ro" \
      -v "$S/0004_phase5_policy_lifecycle.sql:$I/009.sql:ro" \
      -v "$S/0005_phase6_policy_tests.sql:$I/010.sql:ro" \
      -v "$M/0006_context_guardian.sql:$I/011_context_guardian.sql:ro" \
      postgres:16-alpine >/dev/null
  elif ! running cg-test-postgres; then docker start cg-test-postgres >/dev/null; fi
  wait_for postgres 60 docker exec cg-test-postgres psql -U suez -d suez -c "select 1 from context_usage_measurements limit 1"

  exists cg-test-redis || docker run -d --name cg-test-redis --network "$NET" redis:7-alpine >/dev/null
  running cg-test-redis || docker start cg-test-redis >/dev/null

  if ! exists cg-test-opa; then
    docker image inspect suez-opa:latest >/dev/null 2>&1 || docker build -q -t suez-opa:latest "$REPO_ROOT/infra/opa" >/dev/null
    docker run -d --name cg-test-opa --network "$NET" suez-opa:latest >/dev/null
  fi
  running cg-test-opa || docker start cg-test-opa >/dev/null

  if ! exists cg-test-control-plane; then
    docker image inspect suez-control-plane:latest >/dev/null 2>&1 || docker build -q -t suez-control-plane:latest "$REPO_ROOT/apps/control-plane" >/dev/null
    docker run -d --name cg-test-control-plane --network "$NET" \
      -e PORT=8081 -e DATABASE_URL="$DB_URL" \
      -e OPA_BASE_URL=http://cg-test-opa:8181 -e OPA_DECISION_URL=http://cg-test-opa:8181/v1/data/suez/authz/decision \
      suez-control-plane:latest >/dev/null
  fi
  running cg-test-control-plane || docker start cg-test-control-plane >/dev/null
  wait_for control-plane 60 docker exec cg-test-control-plane wget -qO- http://127.0.0.1:8081/

  docker build -q -t suez-gateway:cg-test "$REPO_ROOT/apps/gateway" >/dev/null
  docker rm -f cg-test-gateway >/dev/null 2>&1 || true
  docker run -d --name cg-test-gateway --network "$NET" -p "127.0.0.1:$PORT:8080" \
    -e PORT=8080 -e DATABASE_URL="$DB_URL" -e REDIS_URL=redis://cg-test-redis:6379/0 \
    -e OPA_DECISION_URL=http://cg-test-opa:8181/v1/data/suez/authz/decision \
    -e AUTH_RESOLVER_URL=http://cg-test-control-plane:8081/internal/auth/resolve \
    suez-gateway:cg-test >/dev/null
  wait_for gateway 60 curl -fsS "http://127.0.0.1:$PORT/"
  echo "test gateway ready on http://127.0.0.1:$PORT (stub model demo-model, key suez-demo-admin-key)"
}

down() {
  for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  docker network rm "$NET" >/dev/null 2>&1 || true
  if [ "${1:-}" = "--purge" ]; then
    docker image rm suez-gateway:cg-test >/dev/null 2>&1 || true
    docker volume rm cg-test-gomod cg-test-gocache >/dev/null 2>&1 || true
  fi
  echo "test stack removed"
}

status() {
  docker ps -a --filter name=cg-test- --format '{{.Names}}\t{{.Status}}'
}

case "${1:-}" in
  up) up ;;
  down) down "${2:-}" ;;
  status) status ;;
  *) echo "usage: $0 up | down [--purge] | status" >&2; exit 2 ;;
esac
