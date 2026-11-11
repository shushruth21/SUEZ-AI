#!/usr/bin/env bash
# Staging SUEZ on this Mac, shared with testers through a Cloudflare quick tunnel.
#
#   scripts/staging.sh up                 start (first run: generate secrets, bootstrap)
#   scripts/staging.sh url                print the public URL
#   scripts/staging.sh add-tester NAME    create a tester tenant + keys; writes ~/.suez-staging/testers/NAME.txt
#   scripts/staging.sh list               testers and key status
#   scripts/staging.sh revoke NAME        revoke a tester's keys
#   scripts/staging.sh down               stop (data kept)
#   scripts/staging.sh destroy            stop and delete all staging data and secrets
#
# Secrets never go into the repository: .env.staging is git-ignored and the
# data key, root admin key and tester files live in ~/.suez-staging (mode 600).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STATE="${SUEZ_STAGING_HOME:-$HOME/.suez-staging}"
ENV_FILE="$ROOT/.env.staging"
COMPOSE_FILE="$ROOT/infra/compose/docker-compose.staging.yml"
compose() { docker compose -p suez-staging --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"; }

die() { echo "error: $*" >&2; exit 1; }
random() { openssl rand -hex 24; }
json() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)"; }

local_url() {
  local port; port=$(grep -E '^STAGING_LOCAL_PORT=' "$ENV_FILE" | cut -d= -f2)
  echo "http://127.0.0.1:${port:-8090}"
}

admin_key() { [ -s "$STATE/admin-key" ] || die "no root admin key; run: scripts/staging.sh up"; cat "$STATE/admin-key"; }

# api METHOD PATH KEY [JSON]  -> prints body; fails on HTTP >= 400
api() {
  local method=$1 path=$2 key=$3 body=${4:-}
  local out status
  out=$(mktemp)
  status=$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "$(local_url)$path" \
    -H "X-API-Key: $key" -H 'Content-Type: application/json' ${body:+-d "$body"}) || { rm -f "$out"; die "request to $path failed"; }
  if [ "$status" -ge 400 ]; then
    echo "error: $method $path -> HTTP $status: $(cat "$out")" >&2; rm -f "$out"; exit 1
  fi
  cat "$out"; rm -f "$out"
}

public_url() {
  local url
  for _ in $(seq 1 30); do
    url=$(compose logs tunnel 2>/dev/null | grep -Eo 'https://[a-z0-9-]+\.trycloudflare\.com' | tail -1 || true)
    [ -n "$url" ] && { echo "$url"; return 0; }
    sleep 2
  done
  return 1
}

cmd_up() {
  command -v docker >/dev/null || die "Docker is required"
  mkdir -p "$STATE/testers"; chmod 700 "$STATE"
  if [ ! -f "$ENV_FILE" ]; then
    [ -f "$STATE/data_key" ] || { openssl rand -base64 32 > "$STATE/data_key"; chmod 600 "$STATE/data_key"; }
    umask 077
    cat > "$ENV_FILE" <<ENV
COMPOSE_PROJECT_NAME=suez-staging
POSTGRES_PASSWORD=$(random)
REDIS_PASSWORD=$(random)
SUEZ_INTERNAL_TOKEN=$(random)
SUEZ_DATA_KEY_PATH=$STATE/data_key
STAGING_LOCAL_PORT=8090
ENV
    echo "created $ENV_FILE (git-ignored) and $STATE/data_key"
  fi
  echo "starting staging (first build takes a few minutes)..."
  compose up -d --build --wait edge >/dev/null
  compose up -d tunnel >/dev/null
  if [ ! -s "$STATE/admin-key" ]; then
    compose exec -T control-plane control-plane bootstrap \
      --tenant-slug staging-admin --tenant-name "Staging administration" --expires-in-days 365 \
      > "$STATE/admin-key"
    chmod 600 "$STATE/admin-key"
    echo "root admin key stored in $STATE/admin-key"
  fi
  [ -f "$ROOT/ContextGuardian.dmg" ] || echo "note: ContextGuardian.dmg is not built yet; run: make dmg"
  local url; url=$(public_url) || die "tunnel did not report a URL; check: docker compose -p suez-staging logs tunnel"
  echo "$url" > "$STATE/url"
  echo
  echo "Staging is up."
  echo "  Portal (testers):          $url"
  echo "  Gateway (Context Guardian): $url/gateway"
  echo "  Local check:               $(local_url)"
  echo "The URL changes every time the tunnel restarts. Add testers with: scripts/staging.sh add-tester NAME"
}

cmd_url() { public_url || die "tunnel is not running; run: scripts/staging.sh up"; }

cmd_add_tester() {
  local name=${1:-}
  [[ "$name" =~ ^[a-z0-9][a-z0-9-]{1,30}$ ]] || die "tester name must be 2-31 lowercase letters, digits or dashes"
  [ -e "$STATE/testers/$name.json" ] && die "tester $name already exists (revoke or pick another name)"
  local root url expires
  root=$(admin_key); url=$(public_url) || die "staging is not running"
  expires=$(date -u -v+14d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+14 days' +%Y-%m-%dT%H:%M:%SZ)

  # Tenant + an initial key we immediately replace with an expiring one.
  local tenant tenant_id initial_key
  tenant=$(api POST /control-plane/api/v1/tenants "$root" "{\"name\":\"Tester $name\",\"slug\":\"tester-$name\",\"environment\":\"staging\"}")
  tenant_id=$(echo "$tenant" | json 'd["tenant"]["id"]')
  initial_key=$(echo "$tenant" | json 'd["rawInitialApiKey"]')
  local initial_id; initial_id=$(echo "$tenant" | json 'd["initialAdminKey"]["id"]')

  local admin admin_key admin_id inference inference_key inference_id
  admin=$(api POST /control-plane/api/v1/api-keys "$initial_key" "{\"label\":\"$name-admin\",\"scopes\":[\"tenant-admin\"],\"expiresAt\":\"$expires\"}")
  admin_key=$(echo "$admin" | json 'd["rawApiKey"]'); admin_id=$(echo "$admin" | json 'd["apiKey"]["id"]')
  api DELETE "/control-plane/api/v1/api-keys/$initial_id" "$admin_key" >/dev/null
  inference=$(api POST /control-plane/api/v1/api-keys "$admin_key" "{\"label\":\"$name-desktop\",\"scopes\":[\"gateway-inference\"],\"rateLimitRpm\":120,\"expiresAt\":\"$expires\"}")
  inference_key=$(echo "$inference" | json 'd["rawApiKey"]'); inference_id=$(echo "$inference" | json 'd["apiKey"]["id"]')

  ADMIN_KEY="$admin_key" API="$(local_url)" sh "$ROOT/scripts/staging-seed.sh" "$name-admin" "$name-desktop"

  umask 077
  cat > "$STATE/testers/$name.json" <<JSON
{"name":"$name","tenantId":"$tenant_id","adminKeyId":"$admin_id","desktopKeyId":"$inference_id","expiresAt":"$expires"}
JSON
  cat > "$STATE/testers/$name.txt" <<TXT
SUEZ staging access for $name  (keys expire $expires)
=======================================================

Portal:  $url
  Admin view:      Sign in as "Admin", Auth "API key", credential:
                   $admin_key
  Requester view:  Sign in as "Requester", Auth "API key", credential:
                   $inference_key

Context Guardian (desktop app, ContextGuardian.dmg):
  1. Open the .dmg, drag Context Guardian to Applications.
  2. First launch: right-click the app > Open > Open (the app is not signed yet).
  3. Settings (gear): Usage source = gateway
                      Gateway URL  = $url/gateway
                      API key      = $inference_key
     Save, then "Test connection" should show tenant tester-$name.

Make the meter move (replace SESSION with the id from "Copy session header"):
  curl -s -o /dev/null -D - -X POST $url/gateway/v1/chat/completions \\
    -H 'X-API-Key: $inference_key' -H 'Content-Type: application/json' \\
    -H 'X-SUEZ-Session-Id: SESSION' \\
    -d '{"model":"demo-small","messages":[{"role":"user","content":"Plan the migration. $(printf 'x%.0s' $(seq 1 520))"}]}' | grep -i x-suez-context

Models: demo-model (8,000-token window), demo-small (400 tokens: thresholds show quickly).
Everything uses a free stub model; nothing is sent to a real AI provider.
Your data is visible only to your keys. The URL may change if staging restarts.
TXT
  echo "Tester $name created (tenant tester-$name). Send them:"
  echo "  $STATE/testers/$name.txt"
  echo "  $ROOT/ContextGuardian.dmg"
}

cmd_list() {
  local root; root=$(admin_key)
  shopt -s nullglob
  for file in "$STATE"/testers/*.json; do
    python3 - "$file" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print(f"{d['name']:<16} tenant tester-{d['name']:<14} keys expire {d['expiresAt']}  {'(revoked)' if d.get('revoked') else ''}")
PY
  done
}

cmd_revoke() {
  local name=${1:-}; local file="$STATE/testers/$name.json"
  [ -f "$file" ] || die "no tester named $name"
  local root tenant_id admin_id desktop_id
  root=$(admin_key)
  tenant_id=$(json 'd["tenantId"]' < "$file"); admin_id=$(json 'd["adminKeyId"]' < "$file"); desktop_id=$(json 'd["desktopKeyId"]' < "$file")
  # Revoke through the database: the root key belongs to another tenant, and
  # tenant isolation means it cannot revoke keys in the tester's tenant.
  compose exec -T postgres psql -U suez -d suez -qAt -c \
    "update api_keys set revoked_at = coalesce(revoked_at, now()), revoked_by = 'staging-admin' where tenant_id = '$tenant_id' and revoked_at is null" >/dev/null
  python3 - "$file" <<'PY'
import json, sys
d = json.load(open(sys.argv[1])); d["revoked"] = True; json.dump(d, open(sys.argv[1], "w"))
PY
  echo "revoked every key of tester $name (tenant tester-$name); data kept"
  : "$root" "$admin_id" "$desktop_id"
}

cmd_down() { compose down; }

cmd_destroy() {
  read -r -p "Delete ALL staging data, keys and secrets in $STATE? Type 'destroy': " answer
  [ "$answer" = "destroy" ] || die "cancelled"
  compose down -v --remove-orphans || true
  rm -rf "$STATE" "$ENV_FILE"
  echo "staging removed"
}

case "${1:-}" in
  up) cmd_up ;;
  url) cmd_url ;;
  add-tester) shift; cmd_add_tester "${1:-}" ;;
  list) cmd_list ;;
  revoke) shift; cmd_revoke "${1:-}" ;;
  down) cmd_down ;;
  destroy) cmd_destroy ;;
  *) sed -n '2,12p' "$0"; exit 1 ;;
esac
