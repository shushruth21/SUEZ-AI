#!/usr/bin/env bash
# Demo script 2: As admin, create two models (model-alpha, model-beta) for the demo tenant.
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

echo "Creating two models as admin..."

for name in model-alpha model-beta; do
  resp=$(curl -s -w "\n%{http_code}" -X POST "$CP_URL/api/v1/models" \
    -H "Authorization: ApiKey $ADMIN_KEY" \
    -H "Content-Type: application/json" \
    -d "{
      \"externalName\": \"$name\",
      \"provider\": \"stub\",
      \"modelName\": \"$name\",
      \"upstreamModel\": \"\",
      \"upstreamBaseUrl\": \"\",
      \"maxRpm\": 60,
      \"maxTpm\": 120000,
      \"enabled\": true
    }")
  code=$(echo "$resp" | tail -n1)
  body=$(echo "$resp" | sed '$d')
  if [ "$code" = "201" ]; then
    echo "  Created model: $name"
  else
    echo "  Failed to create $name (HTTP $code): $body"
    exit 1
  fi
done

# Grant admin access to model-alpha so demo scripts (e.g. cache) can use admin key
req=$(curl -s -X POST "$CP_URL/api/v1/access-requests" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"resourceType":"model","resourceName":"model-alpha","justification":"Admin grant for demo"}')
req_id=$(echo "$req" | jq -r '.accessRequest.id // empty')
if [ -n "$req_id" ]; then
  curl -s -X POST "$CP_URL/api/v1/access-requests/$req_id/approve" \
    -H "Authorization: ApiKey $ADMIN_KEY" \
    -H "Content-Type: application/json" \
    -d '{"decisionReason":"Admin self-approve for demo"}' >/dev/null
  echo "  Admin granted model-alpha for demo use."
fi

echo "Done. Two models (model-alpha, model-beta) are ready."
