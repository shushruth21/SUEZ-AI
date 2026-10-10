#!/usr/bin/env bash
# Runs the Playwright browser demo (portal smoke test + screenshot) with a visible window.
# The multi-step flow described in older demos (requesters, Catalog, Groq) is not automated
# in demo.mjs yet; extend scripts/browser-demo/demo.mjs if you need that coverage.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
export HEADLESS="${HEADLESS:-0}"
exec "$SCRIPT_DIR/run-browser-demo.sh"
