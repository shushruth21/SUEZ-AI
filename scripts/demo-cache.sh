#!/usr/bin/env bash
# Cache-hit demo: send the same chat request twice to the gateway.
# First request = cache MISS; second = cache HIT (X-Suez-Cache: HIT).
#
# Prerequisites: stack running, admin has grant for the model (run demo-admin.sh).
# Usage: ./scripts/demo-cache.sh [model]
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ROOT_DIR/.env"
  set +a
fi

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
ADMIN_KEY="${SUEZ_DEMO_ADMIN_KEY:-suez-demo-admin-key}"
MODEL="${1:-demo-model}"
PROMPT="${DEMO_PROMPT:-What is 2+2? Reply in one word.}"

echo "=== Cache hit demo ==="
echo "  Gateway: $GATEWAY_URL"
echo "  Model:   $MODEL"
echo "  Prompt:  $PROMPT"
echo ""

# Build JSON payload (escape prompt for JSON)
prompt_escaped=$(printf '%s' "$PROMPT" | sed 's/\\/\\\\/g; s/"/\\"/g')
payload="{\"model\": \"$MODEL\", \"messages\": [{\"role\": \"user\", \"content\": \"$prompt_escaped\"}], \"max_tokens\": 50}"

echo "Request 1 (expect cache MISS)..."
resp1=$(curl -s -i -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "$payload")
cache1=$(echo "$resp1" | grep -i "X-Suez-Cache:" | tr -d '\r' || echo "X-Suez-Cache: (none)")
echo "  $cache1"
body1=$(echo "$resp1" | sed '1,/^\r$/d')
content1=$(echo "$body1" | jq -r '.choices[0].message.content // .error // "?"' 2>/dev/null || echo "$body1" | head -c 80)
echo "  Response: $content1"
echo ""

echo "Request 2 (expect cache HIT)..."
resp2=$(curl -s -i -X POST "$GATEWAY_URL/v1/chat/completions" \
  -H "Authorization: ApiKey $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d "$payload")
cache2=$(echo "$resp2" | grep -i "X-Suez-Cache:" | tr -d '\r' || echo "X-Suez-Cache: (none)")
echo "  $cache2"
body2=$(echo "$resp2" | sed '1,/^\r$/d')
content2=$(echo "$body2" | jq -r '.choices[0].message.content // .error // "?"' 2>/dev/null || echo "$body2" | head -c 80)
echo "  Response: $content2"
echo ""

if echo "$cache2" | grep -qi "HIT"; then
  echo "Done. Second request was served from cache (X-Suez-Cache: HIT)."
else
  echo "Done. If both show MISS, ensure Redis is running, gateway has cache enabled, and model $MODEL is granted to the admin key (run demo-admin.sh)."
fi
