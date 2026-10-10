#!/usr/bin/env bash
set -euo pipefail

# Deploys the SUEZ bootstrap IAM stack.
# Phase 1 runs with the admin profile to create the execution role + group policy.
# Phase 2 prints the exact command the 'suez' user should use for future stack deploys.

STACK_NAME="${STACK_NAME:-suez-iam-bootstrap}"
REGION="${AWS_REGION:-us-east-1}"
ADMIN_PROFILE="${ADMIN_PROFILE:-admin}"
SUEZ_PROFILE="${SUEZ_PROFILE:-suez}"
GROUP_NAME="${GROUP_NAME:-suez-gateway}"
STACK_PREFIX="${STACK_PREFIX:-suez-}"
RESOURCE_PREFIX="${RESOURCE_PREFIX:-suez}"
ARTIFACT_BUCKET="${ARTIFACT_BUCKET:-}"
TEMPLATE_FILE="${TEMPLATE_FILE:-suez-iam-bootstrap.yaml}"

echo "==> Validating template with admin profile: ${ADMIN_PROFILE}"
aws --profile "${ADMIN_PROFILE}" --region "${REGION}" cloudformation validate-template \
  --template-body "file://${TEMPLATE_FILE}" >/dev/null

echo "==> Deploying bootstrap stack: ${STACK_NAME}"
aws --profile "${ADMIN_PROFILE}" --region "${REGION}" cloudformation deploy \
  --stack-name "${STACK_NAME}" \
  --template-file "${TEMPLATE_FILE}" \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides \
    GatewayGroupName="${GROUP_NAME}" \
    AllowedStackNamePrefix="${STACK_PREFIX}" \
    ResourceNamePrefix="${RESOURCE_PREFIX}" \
    ArtifactBucketName="${ARTIFACT_BUCKET}"

echo "==> Fetching execution role ARN"
EXEC_ROLE_ARN="$(aws --profile "${ADMIN_PROFILE}" --region "${REGION}" cloudformation describe-stacks \
  --stack-name "${STACK_NAME}" \
  --query "Stacks[0].Outputs[?OutputKey=='CloudFormationExecutionRoleArn'].OutputValue" \
  --output text)"

echo
echo "Bootstrap complete."
echo "Execution role ARN: ${EXEC_ROLE_ARN}"
echo
echo "Quick verification with the suez profile:"
aws --profile "${SUEZ_PROFILE}" sts get-caller-identity >/dev/null
echo "  [ok] suez credentials are valid"
echo
echo "For future SUEZ application stack deployments, use the suez profile like this:"
echo
echo "aws --profile ${SUEZ_PROFILE} --region ${REGION} cloudformation deploy \\"
echo "  --stack-name ${STACK_PREFIX}app \\"
echo "  --template-file app.yaml \\"
echo "  --role-arn ${EXEC_ROLE_ARN} \\"
echo "  --capabilities CAPABILITY_NAMED_IAM"
echo
echo "Notes:"
echo "  - The suez profile can manage only CloudFormation stacks prefixed with '${STACK_PREFIX}'."
echo "  - The suez profile cannot create resources directly; CloudFormation does that through the execution role."
echo "  - Tighten the execution role further once the exact MVP resources are finalized."
