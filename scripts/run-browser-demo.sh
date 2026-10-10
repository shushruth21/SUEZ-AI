#!/usr/bin/env bash
# Run the Playwright-based browser demo. Installs deps on first run.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
DEMO_DIR="$SCRIPT_DIR/browser-demo"
ROOT_DIR=$(cd "$SCRIPT_DIR/.." && pwd)

if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ROOT_DIR/.env"
  set +a
fi

export PORTAL_URL="${PORTAL_URL:-http://localhost:5173}"
# Show browser by default; set HEADLESS=1 to run without a window
export HEADLESS="${HEADLESS:-0}"

if [ ! -d "$DEMO_DIR/node_modules" ]; then
  echo "First run: installing browser-demo dependencies (Playwright)..."
  (cd "$DEMO_DIR" && npm install)
fi

# Use a local browser cache so Chromium is found regardless of environment
export PLAYWRIGHT_BROWSERS_PATH="$DEMO_DIR/.playwright-browsers"
if [ ! -d "$PLAYWRIGHT_BROWSERS_PATH" ]; then
  echo "Installing Playwright Chromium to $PLAYWRIGHT_BROWSERS_PATH..."
  (cd "$DEMO_DIR" && npx playwright install chromium)
fi

echo "Running browser demo at $PORTAL_URL (set PORTAL_URL to override)."
echo "Screenshots will be saved to $DEMO_DIR/screenshots/"
if [ "${HEADLESS}" = "0" ]; then
  echo "Browser window will open (set HEADLESS=1 to run headless)."
else
  echo "Running headless (set HEADLESS=0 to see the browser)."
fi
(cd "$DEMO_DIR" && npm run demo)
