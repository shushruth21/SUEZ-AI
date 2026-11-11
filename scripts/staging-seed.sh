#!/bin/sh
# Seeds one tester tenant: two stub models, a stub tool, access grants for
# both tester keys, memory capture and context thresholds.
# Usage: ADMIN_KEY=... API=http://127.0.0.1:8090 staging-seed.sh ADMIN_LABEL DESKTOP_LABEL
set -eu
ADMIN_LABEL=$1; DESKTOP_LABEL=$2
: "${ADMIN_KEY:?}" "${API:?}"
CP="$API/control-plane/api/v1"

post() { # METHOD PATH JSON -> body, fails on HTTP >= 400
  out=$(curl -sS -w '\n%{http_code}' -X "$1" "$CP$2" -H "X-API-Key: $ADMIN_KEY" -H 'Content-Type: application/json' -d "$3")
  code=$(printf '%s' "$out" | tail -n1); body=$(printf '%s' "$out" | sed '$d')
  [ "$code" -lt 400 ] || { echo "seed: $1 $2 -> $code $body" >&2; exit 1; }
  printf '%s' "$body"
}

post POST /models '{"externalName":"demo-model","provider":"stub","modelName":"demo-model","enabled":true,"contextWindow":8000,"maxRpm":120,"maxTpm":200000}' >/dev/null
post POST /models '{"externalName":"demo-small","provider":"stub","modelName":"demo-small","enabled":true,"contextWindow":400,"maxRpm":120,"maxTpm":200000}' >/dev/null
post POST /tools '{"name":"weather.lookup","description":"Stub weather lookup (no external call)","executionMode":"stub","riskTier":"low","enabled":true,"inputSchema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"outputSchema":{"type":"object"}}' >/dev/null

for subject in "$ADMIN_LABEL" "$DESKTOP_LABEL"; do
  for resource in model:demo-model model:demo-small tool:weather.lookup; do
    type=${resource%%:*}; name=${resource#*:}
    id=$(post POST /access-requests "{\"subject\":\"$subject\",\"resourceType\":\"$type\",\"resourceName\":\"$name\",\"justification\":\"staging tester setup\"}" \
      | python3 -c 'import json,sys;print(json.load(sys.stdin)["accessRequest"]["id"])')
    post POST "/access-requests/$id/approve" '{"reason":"staging tester setup"}' >/dev/null
  done
done

post PUT /memory/settings '{"captureEnabled":true,"retrievalEnabled":true,"injectionEnabled":false,"retentionDays":30,"maxInjectionTokens":2000,"maxInjectionItems":8,"auditRetentionDays":90,"usageRetentionDays":30}' >/dev/null
post PUT /memory/context-policies '{"model":"*","noticePercent":80,"checkpointPercent":85,"warningPercent":95,"blockPercent":100,"hardBlock":false}' >/dev/null
