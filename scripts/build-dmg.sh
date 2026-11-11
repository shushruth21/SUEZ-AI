#!/bin/sh
# Builds the Context Guardian desktop app and copies the installer to the
# main folder as ContextGuardian.dmg (git-ignored). Needs Node 20 and Rust.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$ROOT/apps/context-guardian"
cd "$APP"
npm ci
npm run tauri build -- --bundles dmg
DMG=$(ls -t "$APP"/src-tauri/target/release/bundle/dmg/*.dmg | head -1)
cp "$DMG" "$ROOT/ContextGuardian.dmg"
echo "built $ROOT/ContextGuardian.dmg"
