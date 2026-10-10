#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib.sh"

require_env AWS_PROFILE
require_env AWS_REGION
require_env BOOTSTRAP_STACK
require_env BOOTSTRAP_TEMPLATE
require_env APP_STACK
require_env APP_TEMPLATE
require_env VPC_ID
require_env PUBLIC_SUBNET_IDS
require_env PRIVATE_SUBNET_IDS
require_env CONTAINER_IMAGE
require_env DB_PASSWORD
require_env DOMAIN_NAME
require_env HOSTED_ZONE_ID

DESIRED_COUNT="${DESIRED_COUNT:-1}"
CPU="${CPU:-256}"
MEMORY="${MEMORY:-512}"
DB_USERNAME="${DB_USERNAME:-suezapp}"
DB_NAME="${DB_NAME:-suezdb}"

echo "Validating templates..."
aws_cli cloudformation validate-template --template-body "file://${BOOTSTRAP_TEMPLATE}" >/dev/null
aws_cli cloudformation validate-template --template-body "file://${APP_TEMPLATE}" >/dev/null

echo "Deploying bootstrap stack: ${BOOTSTRAP_STACK}"
aws_cli cloudformation deploy \
  --stack-name "${BOOTSTRAP_STACK}" \
  --template-file "${BOOTSTRAP_TEMPLATE}" \
  --capabilities CAPABILITY_NAMED_IAM

ROLE_ARN="$(stack_output "${BOOTSTRAP_STACK}" "CloudFormationExecutionRoleArn")"
if [[ -z "${ROLE_ARN}" || "${ROLE_ARN}" == "None" ]]; then
  echo "Missing CloudFormationExecutionRoleArn from ${BOOTSTRAP_STACK}" >&2
  exit 1
fi

PARAMS=(
  ResourceNamePrefix=suez
  DomainName="${DOMAIN_NAME}"
  HostedZoneId="${HOSTED_ZONE_ID}"
  CreateCertificate=true
  ExistingCertificateArn=""
  VpcId="${VPC_ID}"
  PublicSubnetIds="${PUBLIC_SUBNET_IDS}"
  PrivateSubnetIds="${PRIVATE_SUBNET_IDS}"
  ContainerImage="${CONTAINER_IMAGE}"
  DesiredCount="${DESIRED_COUNT}"
  Cpu="${CPU}"
  Memory="${MEMORY}"
  DBUsername="${DB_USERNAME}"
  DBPassword="${DB_PASSWORD}"
  DBName="${DB_NAME}"
)

echo "Deploying app stack: ${APP_STACK}"
aws_cli cloudformation deploy \
  --stack-name "${APP_STACK}" \
  --template-file "${APP_TEMPLATE}" \
  --role-arn "${ROLE_ARN}" \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides "${PARAMS[@]}"

echo ""
echo "Outputs:"
aws_cli cloudformation describe-stacks \
  --stack-name "${APP_STACK}" \
  --query "Stacks[0].Outputs[*].[OutputKey,OutputValue]" \
  --output table

echo ""
echo "Service URL: $(stack_output "${APP_STACK}" "ServiceUrl")"

echo ""
echo "Running tests..."
"${SCRIPT_DIR}/tests.sh"

echo "Done."

