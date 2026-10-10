#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

require_env AWS_PROFILE
require_env AWS_REGION
require_env BOOTSTRAP_STACK
require_env APP_STACK

if stack_exists "${APP_STACK}"; then
  echo "Deleting app stack: ${APP_STACK}"
  aws_cli cloudformation delete-stack --stack-name "${APP_STACK}"
  wait_stack "${APP_STACK}" delete || true
else
  echo "App stack not found: ${APP_STACK} (skipping)"
fi

if stack_exists "${BOOTSTRAP_STACK}"; then
  echo "Deleting bootstrap stack: ${BOOTSTRAP_STACK}"
  aws_cli cloudformation delete-stack --stack-name "${BOOTSTRAP_STACK}"
  wait_stack "${BOOTSTRAP_STACK}" delete || true
else
  echo "Bootstrap stack not found: ${BOOTSTRAP_STACK} (skipping)"
fi

echo "Done."

