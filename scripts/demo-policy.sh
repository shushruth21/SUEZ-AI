#!/usr/bin/env bash
# Demo script 4: Showcase natural language policy — generate Rego from a plain-English rule.
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ROOT_DIR/.env"
  set +a
fi

CP_URL="${CONTROL_PLANE_URL:-http://localhost:8081}"
ADMIN_KEY="${SUEZ_DEMO_ADMIN_KEY:-suez-demo-admin-key}"

# Natural language rule: plain English description of the policy
NATURAL_LANGUAGE_RULE="${1:-Only allow chat completions for models that are enabled and within token limits.}"

echo "Generating policy from natural language..."
echo "  Rule: $NATURAL_LANGUAGE_RULE"
echo ""

json=$(jq -n \
  --arg name "Demo NL Policy" \
  --arg scope "runtime" \
  --arg targetName "*" \
  --arg nl "$NATURAL_LANGUAGE_RULE" \
  --arg pkg "suez.authz" \
  '{name:$name, scope:$scope, targetName:$targetName, naturalLanguageRule:$nl, regoPackage:$pkg}')
resp=$(curl -s -X POST "$CP_URL/api/v1/policies/generate" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "$json")

if echo "$resp" | jq -e '.policy' >/dev/null 2>&1; then
  echo "Generated Rego:"
  echo "$resp" | jq -r '.policy.regoSource'
  echo ""
  echo "Policy ID: $(echo "$resp" | jq -r '.policy.id')"
  echo "Status: $(echo "$resp" | jq -r '.policy.status')"
else
  echo "Generate failed or policy-llm unavailable: $resp"
  exit 1
fi

echo ""
echo "Done. Use the control-plane UI to validate, add tests, and publish this policy."
