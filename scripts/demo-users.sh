#!/usr/bin/env bash
# Demo script 3: Create two users; each user is allowed only one model (user-alpha -> model-alpha, user-beta -> model-beta).
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
DEMO_TENANT_ID="00000000-0000-0000-0000-000000000001"

echo "Creating two users (API keys) and granting each one model..."

# Create API key for user-alpha
resp_alpha=$(curl -s -X POST "$CP_URL/api/v1/api-keys" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"label\": \"demo-user-alpha\", \"tenantId\": \"$DEMO_TENANT_ID\"}")
raw_alpha=$(echo "$resp_alpha" | jq -r '.rawApiKey // empty')
if [ -z "$raw_alpha" ]; then
  echo "Failed to create API key for demo-user-alpha: $resp_alpha"
  exit 1
fi
echo "  Created user: demo-user-alpha"

# Create API key for user-beta
resp_beta=$(curl -s -X POST "$CP_URL/api/v1/api-keys" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"label\": \"demo-user-beta\", \"tenantId\": \"$DEMO_TENANT_ID\"}")
raw_beta=$(echo "$resp_beta" | jq -r '.rawApiKey // empty')
if [ -z "$raw_beta" ]; then
  echo "Failed to create API key for demo-user-beta: $resp_beta"
  exit 1
fi
echo "  Created user: demo-user-beta"

# User-alpha requests access to model-alpha
req_alpha=$(curl -s -X POST "$CP_URL/api/v1/access-requests" \
  -H "Authorization: ApiKey $raw_alpha" \
  -H "Content-Type: application/json" \
  -d '{"resourceType":"model","resourceName":"model-alpha","justification":"Demo access for user-alpha"}')
id_alpha=$(echo "$req_alpha" | jq -r '.accessRequest.id // empty')
if [ -z "$id_alpha" ]; then
  echo "Failed to create access request for user-alpha: $req_alpha"
  exit 1
fi
echo "  demo-user-alpha requested model-alpha"

# User-beta requests access to model-beta
req_beta=$(curl -s -X POST "$CP_URL/api/v1/access-requests" \
  -H "Authorization: ApiKey $raw_beta" \
  -H "Content-Type: application/json" \
  -d '{"resourceType":"model","resourceName":"model-beta","justification":"Demo access for user-beta"}')
id_beta=$(echo "$req_beta" | jq -r '.accessRequest.id // empty')
if [ -z "$id_beta" ]; then
  echo "Failed to create access request for user-beta: $req_beta"
  exit 1
fi
echo "  demo-user-beta requested model-beta"

# Admin approves both
curl -s -X POST "$CP_URL/api/v1/access-requests/$id_alpha/approve" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"decisionReason":"Approved for demo"}' >/dev/null
echo "  Approved model-alpha for demo-user-alpha"

curl -s -X POST "$CP_URL/api/v1/access-requests/$id_beta/approve" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"decisionReason":"Approved for demo"}' >/dev/null
echo "  Approved model-beta for demo-user-beta"

echo "Done. demo-user-alpha can use model-alpha only; demo-user-beta can use model-beta only."
echo "Store the API keys (rawApiKey) from this run if you need to call the gateway as these users."
