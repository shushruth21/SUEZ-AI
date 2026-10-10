#!/usr/bin/env bash
# Run the Playwright portal smoke test (landing screenshot). Register a Groq model in
# **Admin → Catalog** first, or use **demo-requesters-openai-groq.sh** for API-driven setup.
# The gateway needs GROQ_API_KEY or infra/secrets/groq_api_key to call Groq at runtime.
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ROOT_DIR/.env"
  set +a
fi

echo "Running browser smoke test (see scripts/README-demo.md for Groq + requester demos)."
echo ""
exec "$SCRIPT_DIR/run-browser-demo.sh"
