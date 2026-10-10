#!/usr/bin/env bash
set -euo pipefail

# Lightweight bash test harness for AWS deployments.
# Tests are written as functions named test__* and executed in order.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

fail() { echo "FAIL: $*" >&2; return 1; }
pass() { echo "PASS: $*"; }

assert_nonempty() {
  local name="$1"
  local val="$2"
  [[ -n "$val" && "$val" != "None" ]] || fail "${name} expected non-empty, got '${val}'"
}

assert_equals() {
  local name="$1"
  local a="$2"
  local b="$3"
  [[ "$a" == "$b" ]] || fail "${name} expected '${b}', got '${a}'"
}

test__bootstrap_stack_exists() {
  stack_exists "${BOOTSTRAP_STACK:?}" || fail "bootstrap stack not found: ${BOOTSTRAP_STACK}"
  pass "bootstrap stack exists"
}

test__bootstrap_role_output_present() {
  local arn
  arn="$(stack_output "${BOOTSTRAP_STACK}" "CloudFormationExecutionRoleArn")"
  assert_nonempty "CloudFormationExecutionRoleArn" "$arn"
  pass "bootstrap execution role output present"
}

test__app_stack_exists() {
  stack_exists "${APP_STACK:?}" || fail "app stack not found: ${APP_STACK}"
  pass "app stack exists"
}

test__app_stack_healthy_state() {
  local st
  st="$(stack_status "${APP_STACK}")"
  case "$st" in
    CREATE_COMPLETE|UPDATE_COMPLETE) pass "app stack status ${st}" ;;
    *) fail "unexpected app stack status: ${st}" ;;
  esac
}

test__app_outputs_present() {
  # These outputs are expected from the template; adjust if you rename OutputKeys.
  local alb url db
  alb="$(stack_output "${APP_STACK}" "LoadBalancerDnsName")"
  url="$(stack_output "${APP_STACK}" "ServiceUrl")"
  db="$(stack_output "${APP_STACK}" "DatabaseEndpoint")"
  assert_nonempty "LoadBalancerDnsName" "$alb"
  assert_nonempty "ServiceUrl" "$url"
  assert_nonempty "DatabaseEndpoint" "$db"
  pass "app outputs present"
}

run_all_tests() {
  local failed=0
  local t
  for t in $(declare -F | awk '{print $3}' | grep '^test__'); do
    echo "== $t =="
    if ! "$t"; then
      failed=1
    fi
    echo ""
  done
  return "$failed"
}

run_all_tests

