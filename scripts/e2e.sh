#!/bin/sh
# Builds the services from this checkout, starts a fresh stack (PostgreSQL,
# Redis, OPA, control plane, gateway) and runs tests/e2e against it.
# Usage: scripts/e2e.sh [-run TestName]     KEEP_STACK=1 keeps it up afterwards.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
compose() { docker compose -p suez-e2e -f "$ROOT/infra/compose/docker-compose.e2e.yml" "$@"; }
cleanup() { [ "${KEEP_STACK:-0}" = "1" ] || compose down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

compose down -v --remove-orphans >/dev/null 2>&1 || true
compose up -d --build --wait gateway
status=0
docker run --rm --network suez-e2e_default --network-alias e2e \
  -v "$ROOT/tests/e2e":/src -w /src -v suez-gomod:/go/pkg/mod \
  -e SUEZ_E2E=1 golang:1.22 go test -count=1 -tags e2e -timeout 10m "$@" ./... || status=$?
if [ "$status" -ne 0 ]; then
  echo "---- control-plane log (tail) ----"; compose logs --no-color --tail 40 control-plane || true
  echo "---- gateway log (tail) ----"; compose logs --no-color --tail 40 gateway || true
fi
exit $status
