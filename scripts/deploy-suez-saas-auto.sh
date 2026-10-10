#!/usr/bin/env bash
set -euo pipefail

# Deploy the SUEZ SaaS MVP app stack and create the ALB, ACM certificate,
# Route53 alias record, ECS/Fargate service, and RDS automatically.
#
# Auto-certificate behavior:
# - If an issued ACM certificate for the domain already exists in this region, use it.
# - Otherwise request a new ACM certificate through CloudFormation.
# - Automatic DNS validation works only when the hosted zone exists in Route53
#   in the same AWS account and you provide HOSTED_ZONE_ID or allow auto-discovery.
#
# Required environment variables:
#   VPC_ID
#   PUBLIC_SUBNET_IDS   comma-separated subnet ids
#   PRIVATE_SUBNET_IDS  comma-separated subnet ids
#   CONTAINER_IMAGE
#   DB_PASSWORD
#
# Optional:
#   DOMAIN_NAME         default: suez.acrosystemsinc.ai
#   HOSTED_ZONE_ID      if omitted, script attempts Route53 lookup
#   STACK_NAME          default: suez-app
#   AWS_PROFILE         default: suez
#   AWS_REGION          default: us-east-1
#   BOOTSTRAP_STACK     default: suez-iam-bootstrap
#   DESIRED_COUNT       default: 1
#   CPU                 default: 256
#   MEMORY              default: 512
#   DB_USERNAME         default: suezapp
#   DB_NAME             default: suezdb

STACK_NAME="${STACK_NAME:-suez-app}"
AWS_PROFILE="${AWS_PROFILE:-suez}"
AWS_REGION="${AWS_REGION:-us-east-1}"
BOOTSTRAP_STACK="${BOOTSTRAP_STACK:-suez-iam-bootstrap}"
DOMAIN_NAME="${DOMAIN_NAME:-suez.acrosystemsinc.ai}"
DESIRED_COUNT="${DESIRED_COUNT:-1}"
CPU="${CPU:-256}"
MEMORY="${MEMORY:-512}"
DB_USERNAME="${DB_USERNAME:-suezapp}"
DB_NAME="${DB_NAME:-suezdb}"
TEMPLATE_FILE="${TEMPLATE_FILE:-suez-saas-app-auto-cert.yaml}"

: "${VPC_ID:?Set VPC_ID}"
: "${PUBLIC_SUBNET_IDS:?Set PUBLIC_SUBNET_IDS as comma-separated subnet ids}"
: "${PRIVATE_SUBNET_IDS:?Set PRIVATE_SUBNET_IDS as comma-separated subnet ids}"
: "${CONTAINER_IMAGE:?Set CONTAINER_IMAGE}"
: "${DB_PASSWORD:?Set DB_PASSWORD}"

aws_cli() {
  aws --profile "${AWS_PROFILE}" --region "${AWS_REGION}" "$@"
}

echo "Resolving CloudFormation execution role from bootstrap stack ${BOOTSTRAP_STACK}"
ROLE_ARN="$(aws_cli cloudformation describe-stacks \
  --stack-name "${BOOTSTRAP_STACK}" \
  --query "Stacks[0].Outputs[?OutputKey=='CloudFormationExecutionRoleArn'].OutputValue" \
  --output text)"

if [[ -z "${ROLE_ARN}" || "${ROLE_ARN}" == "None" ]]; then
  echo "Could not resolve CloudFormation execution role from stack ${BOOTSTRAP_STACK}" >&2
  exit 1
fi

find_hosted_zone() {
  local fqdn="${1%.}."
  aws_cli route53 list-hosted-zones-by-name \
    --dns-name "${fqdn}" \
    --query "HostedZones[?Name=='${fqdn}'].Id | [0]" \
    --output text 2>/dev/null | sed 's#/hostedzone/##'
}

parent_zone_from_domain() {
  local domain="$1"
  IFS='.' read -r -a parts <<< "${domain}"
  local count="${#parts[@]}"
  if (( count < 2 )); then
    echo "${domain}"
    return
  fi
  echo "${parts[count-2]}.${parts[count-1]}"
}

if [[ -z "${HOSTED_ZONE_ID:-}" ]]; then
  echo "HOSTED_ZONE_ID not provided, attempting auto-discovery"
  HOSTED_ZONE_ID="$(find_hosted_zone "${DOMAIN_NAME}")"
  if [[ -z "${HOSTED_ZONE_ID}" || "${HOSTED_ZONE_ID}" == "None" ]]; then
    PARENT_ZONE="$(parent_zone_from_domain "${DOMAIN_NAME}")"
    HOSTED_ZONE_ID="$(find_hosted_zone "${PARENT_ZONE}")"
  fi
fi

if [[ -z "${HOSTED_ZONE_ID:-}" || "${HOSTED_ZONE_ID}" == "None" ]]; then
  echo "Could not auto-discover a Route53 hosted zone for ${DOMAIN_NAME}" >&2
  echo "Set HOSTED_ZONE_ID explicitly." >&2
  exit 1
fi

echo "Using hosted zone: ${HOSTED_ZONE_ID}"

echo "Checking for an existing issued ACM certificate for ${DOMAIN_NAME}"
EXISTING_CERT_ARN="$(aws_cli acm list-certificates \
  --certificate-statuses ISSUED \
  --query "CertificateSummaryList[?DomainName=='${DOMAIN_NAME}'].CertificateArn | [0]" \
  --output text)"

CREATE_CERT="true"
if [[ -n "${EXISTING_CERT_ARN}" && "${EXISTING_CERT_ARN}" != "None" ]]; then
  echo "Found existing ACM certificate: ${EXISTING_CERT_ARN}"
  CREATE_CERT="false"
else
  echo "No existing issued ACM certificate found. CloudFormation will request one."
  EXISTING_CERT_ARN=""
fi

PARAMS=(
  ResourceNamePrefix=suez
  DomainName="${DOMAIN_NAME}"
  HostedZoneId="${HOSTED_ZONE_ID}"
  CreateCertificate="${CREATE_CERT}"
  ExistingCertificateArn="${EXISTING_CERT_ARN}"
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

echo "Validating template ${TEMPLATE_FILE}"
aws_cli cloudformation validate-template \
  --template-body "file://${TEMPLATE_FILE}" >/dev/null

echo "Deploying stack ${STACK_NAME} for ${DOMAIN_NAME}"
aws_cli cloudformation deploy \
  --stack-name "${STACK_NAME}" \
  --template-file "${TEMPLATE_FILE}" \
  --role-arn "${ROLE_ARN}" \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides "${PARAMS[@]}"

echo
echo "Deployment complete."
echo "App URL: https://${DOMAIN_NAME}"
echo
echo "Outputs:"
aws_cli cloudformation describe-stacks \
  --stack-name "${STACK_NAME}" \
  --query "Stacks[0].Outputs[*].[OutputKey,OutputValue]" \
  --output table
