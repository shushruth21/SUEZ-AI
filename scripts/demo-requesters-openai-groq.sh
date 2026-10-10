#!/usr/bin/env bash
# Create two requesters: one with access to an OpenAI model, one with access to a Groq model.
# Ensures the Groq model exists, then creates two API keys and grants each access to one model.
# Prerequisites: Control-plane and gateway running; OpenAI model (e.g. demo-model) in catalog.
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
DEMO_TENANT_ID="${DEMO_TENANT_ID:-00000000-0000-0000-0000-000000000001}"

# Model names: OpenAI-compatible (e.g. demo-model from seed) and Groq
OPENAI_MODEL="${OPENAI_MODEL:-demo-model}"
GROQ_MODEL="${GROQ_MODEL:-groq-llama-70b}"
GROQ_UPSTREAM_MODEL="${GROQ_UPSTREAM_MODEL:-llama-3.1-70b-versatile}"

echo "Creating two requesters: OpenAI-only and Groq-only..."
echo "  OpenAI model: $OPENAI_MODEL"
echo "  Groq model:   $GROQ_MODEL"
echo ""

# Ensure Groq model exists (create if missing)
echo "Ensuring Groq model exists..."
resp=$(curl -s -w "\n%{http_code}" -X POST "$CP_URL/api/v1/models" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "{
    \"externalName\": \"$GROQ_MODEL\",
    \"provider\": \"groq\",
    \"modelName\": \"$GROQ_UPSTREAM_MODEL\",
    \"upstreamModel\": \"$GROQ_UPSTREAM_MODEL\",
    \"upstreamBaseUrl\": \"\",
    \"maxRpm\": 60,
    \"maxTpm\": 120000,
    \"enabled\": true
  }")
code=$(echo "$resp" | tail -n1)
body=$(echo "$resp" | sed '$d')
if [ "$code" = "201" ]; then
  echo "  Created model: $GROQ_MODEL"
elif [ "$code" = "409" ] || echo "$body" | grep -qi "already exists\|duplicate"; then
  echo "  Groq model $GROQ_MODEL already exists."
else
  echo "  Note: Could not create Groq model (HTTP $code). Ensure $GROQ_MODEL exists for user2."
fi

# Create API key for user1
echo ""
echo "Creating requester: user1 (access to $OPENAI_MODEL only)..."
resp_openai=$(curl -s -X POST "$CP_URL/api/v1/api-keys" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"label\": \"user1\", \"tenantId\": \"$DEMO_TENANT_ID\"}")
raw_openai=$(echo "$resp_openai" | jq -r '.rawApiKey // empty')
if [ -z "$raw_openai" ]; then
  echo "  Failed to create API key for user1: $resp_openai"
  exit 1
fi
echo "  Created API key: user1"

# Create API key for user2
echo "Creating requester: user2 (access to $GROQ_MODEL only)..."
resp_groq=$(curl -s -X POST "$CP_URL/api/v1/api-keys" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"label\": \"user2\", \"tenantId\": \"$DEMO_TENANT_ID\"}")
raw_groq=$(echo "$resp_groq" | jq -r '.rawApiKey // empty')
if [ -z "$raw_groq" ]; then
  echo "  Failed to create API key for user2: $resp_groq"
  exit 1
fi
echo "  Created API key: user2"

# user1: request + approve access to OpenAI model
echo ""
echo "Granting user1 access to $OPENAI_MODEL..."
req_openai=$(curl -s -X POST "$CP_URL/api/v1/access-requests" \
  -H "Authorization: ApiKey $raw_openai" \
  -H "Content-Type: application/json" \
  -d "{\"resourceType\":\"model\",\"resourceName\":\"$OPENAI_MODEL\",\"justification\":\"OpenAI model access for user1\"}")
id_openai=$(echo "$req_openai" | jq -r '.accessRequest.id // empty')
if [ -z "$id_openai" ]; then
  echo "  Failed to create access request for user1: $req_openai"
  exit 1
fi
curl -s -X POST "$CP_URL/api/v1/access-requests/$id_openai/approve" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"decisionReason":"Approved: user1"}' >/dev/null
echo "  Approved $OPENAI_MODEL for user1"

# user2: request + approve access to Groq model
echo "Granting user2 access to $GROQ_MODEL..."
req_groq=$(curl -s -X POST "$CP_URL/api/v1/access-requests" \
  -H "Authorization: ApiKey $raw_groq" \
  -H "Content-Type: application/json" \
  -d "{\"resourceType\":\"model\",\"resourceName\":\"$GROQ_MODEL\",\"justification\":\"Groq model access for user2\"}")
id_groq=$(echo "$req_groq" | jq -r '.accessRequest.id // empty')
if [ -z "$id_groq" ]; then
  echo "  Failed to create access request for user2 (is $GROQ_MODEL in the catalog?): $req_groq"
  exit 1
fi
curl -s -X POST "$CP_URL/api/v1/access-requests/$id_groq/approve" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"decisionReason":"Approved: user2"}' >/dev/null
echo "  Approved $GROQ_MODEL for user2"

echo ""
echo "Done. Two requesters created:"
echo "  • user1 — API key below; can use model: $OPENAI_MODEL (OpenAI-compatible)"
echo "  • user2 — API key below; can use model: $GROQ_MODEL (Groq)"
echo ""
echo "user1 (save this key):"
echo "  $raw_openai"
echo ""
echo "user2 (save this key):"
echo "  $raw_groq"
echo ""
echo "Use in portal: select Requester, choose user1 or user2, paste API key. Or call the gateway:"
echo "  curl -X POST \$GATEWAY_URL/v1/chat/completions -H \"Authorization: ApiKey <KEY>\" -H \"Content-Type: application/json\" -d '{\"model\":\"$OPENAI_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"max_tokens\":50}'"
echo "  curl -X POST \$GATEWAY_URL/v1/chat/completions -H \"Authorization: ApiKey <KEY>\" -H \"Content-Type: application/json\" -d '{\"model\":\"$GROQ_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"max_tokens\":50}'"
