#!/usr/bin/env bash
# Throwaway SUEZ stack for testing Context Guardian against the real gateway.
#
# Creates only containers named cg-test-* on the network cg-test-net, with a
# fresh database seeded from infra/db (including 0006). It never touches the
# suez-* containers, the suez_postgres-data volume or production-tagged images.
# Gateway and control plane are built from this worktree as :cg-test and the
# gateway is published on 127.0.0.1:${CG_TEST_GATEWAY_PORT:-18080}. Only the
# free stub model is used.
#
# Usage: scripts/gateway-test-stack.sh up | down | status
#        down --purge also removes the test image and Go cache volumes.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
NET=cg-test-net
PORT="${CG_TEST_GATEWAY_PORT:-18080}"
DB_URL="postgres://suez:cgtest@cg-test-postgres:5432/suez?sslmode=disable"
# Throwaway value for this local test stack only.
INTERNAL_TOKEN="cg-test-internal-token-0123456789abcdef"
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
    docker run -d --name cg-test-postgres --network "$NET" \
      -e POSTGRES_DB=suez -e POSTGRES_USER=suez -e POSTGRES_PASSWORD=cgtest \
      postgres:16-alpine >/dev/null
  elif ! running cg-test-postgres; then docker start cg-test-postgres >/dev/null; fi
  wait_for postgres 60 docker exec cg-test-postgres psql -U suez -d suez -c "select 1"
  # Same migration runner as every other environment, plus the demo seeds.
  docker run --rm --network "$NET" -e DATABASE_URL="$DB_URL" \
    -v "$REPO_ROOT/scripts/migrate.sh:/scripts/migrate.sh:ro" \
    -v "$REPO_ROOT/infra/db/migrations:/db/migrations:ro" -v "$REPO_ROOT/infra/db/seeds:/db/seeds:ro" \
    --entrypoint /bin/sh postgres:16-alpine /scripts/migrate.sh /db/migrations /db/seeds >/dev/null

  exists cg-test-redis || docker run -d --name cg-test-redis --network "$NET" redis:7-alpine >/dev/null
  running cg-test-redis || docker start cg-test-redis >/dev/null

  if ! exists cg-test-opa; then
    docker build -q -t suez-opa:cg-test "$REPO_ROOT/infra/opa" >/dev/null
    docker run -d --name cg-test-opa --network "$NET" suez-opa:cg-test >/dev/null
  fi
  running cg-test-opa || docker start cg-test-opa >/dev/null

  if ! exists cg-test-control-plane; then
    docker build -q -t suez-control-plane:cg-test "$REPO_ROOT/apps/control-plane" >/dev/null
    docker run -d --name cg-test-control-plane --network "$NET" \
      -e PORT=8081 -e DATABASE_URL="$DB_URL" \
      -e SUEZ_ENV=development -e SUEZ_INTERNAL_TOKEN="$INTERNAL_TOKEN" -e OPA_HEALTH_URL=http://cg-test-opa:8181/health \
      -e OPA_BASE_URL=http://cg-test-opa:8181 -e OPA_DECISION_URL=http://cg-test-opa:8181/v1/data/suez/authz/decision \
      suez-control-plane:cg-test >/dev/null
  fi
  running cg-test-control-plane || docker start cg-test-control-plane >/dev/null
  wait_for control-plane 60 docker exec cg-test-control-plane wget -qO- http://127.0.0.1:8081/healthz

  docker build -q -t suez-gateway:cg-test "$REPO_ROOT/apps/gateway" >/dev/null
  docker rm -f cg-test-gateway >/dev/null 2>&1 || true
  docker run -d --name cg-test-gateway --network "$NET" -p "127.0.0.1:$PORT:8080" \
    -e PORT=8080 -e DATABASE_URL="$DB_URL" -e REDIS_URL=redis://cg-test-redis:6379/0 \
    -e SUEZ_ENV=development -e SUEZ_INTERNAL_TOKEN="$INTERNAL_TOKEN" \
    -e CONTROL_PLANE_INTERNAL_URL=http://cg-test-control-plane:8081 -e CONTROL_PLANE_HEALTH_URL=http://cg-test-control-plane:8081/healthz \
    -e OPA_HEALTH_URL=http://cg-test-opa:8181/health \
    -e OPA_DECISION_URL=http://cg-test-opa:8181/v1/data/suez/authz/decision \
    -e AUTH_RESOLVER_URL=http://cg-test-control-plane:8081/internal/auth/resolve \
    suez-gateway:cg-test >/dev/null
  wait_for gateway 60 curl -fsS "http://127.0.0.1:$PORT/readyz"
  echo "test gateway ready on http://127.0.0.1:$PORT (stub model demo-model, key suez-demo-admin-key)"
}

down() {
  for c in "${CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  docker network rm "$NET" >/dev/null 2>&1 || true
  if [ "${1:-}" = "--purge" ]; then
    docker image rm suez-gateway:cg-test >/dev/null 2>&1 || true
    docker image rm suez-control-plane:cg-test suez-opa:cg-test >/dev/null 2>&1 || true
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
