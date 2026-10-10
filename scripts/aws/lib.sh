#!/usr/bin/env bash
set -euo pipefail

aws_cli() {
  aws --profile "${AWS_PROFILE:?Set AWS_PROFILE}" --region "${AWS_REGION:?Set AWS_REGION}" "$@"
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "Missing required env var: ${name}" >&2
    exit 2
  fi
}

wait_stack() {
  local stack="$1"
  local action="$2" # create|update|delete
  case "$action" in
    create) aws_cli cloudformation wait stack-create-complete --stack-name "$stack" ;;
    update) aws_cli cloudformation wait stack-update-complete --stack-name "$stack" ;;
    delete) aws_cli cloudformation wait stack-delete-complete --stack-name "$stack" ;;
    *) echo "Unknown wait_stack action: $action" >&2; exit 2 ;;
  esac
}

stack_exists() {
  local stack="$1"
  aws_cli cloudformation describe-stacks --stack-name "$stack" >/dev/null 2>&1
}

stack_status() {
  local stack="$1"
  aws_cli cloudformation describe-stacks \
    --stack-name "$stack" \
    --query 'Stacks[0].StackStatus' \
    --output text
}

stack_output() {
  local stack="$1"
  local key="$2"
  aws_cli cloudformation describe-stacks \
    --stack-name "$stack" \
    --query "Stacks[0].Outputs[?OutputKey=='${key}'].OutputValue | [0]" \
    --output text
}

