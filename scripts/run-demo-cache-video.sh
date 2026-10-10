#!/usr/bin/env bash
# Runs the Playwright browser demo (portal smoke test + screenshot).
# For cache MISS/HIT behavior, use ./scripts/demo-cache.sh against the gateway.
#
# Optional: set PORTAL_URL, HEADLESS. First run installs npm deps under scripts/browser-demo/.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

export HEADLESS="${HEADLESS:-0}"

echo "Browser demo (Playwright)"
echo "  Portal: ${PORTAL_URL:-http://localhost:5173}"
echo ""

exec "$SCRIPT_DIR/run-browser-demo.sh"
