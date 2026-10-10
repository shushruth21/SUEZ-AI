#!/usr/bin/env node
// Native end-to-end smoke test for SUEZ Context Guardian.
//
// 1. Builds the debug app with the production frontend bundle (no dev server).
// 2. Phase 1: launches the real native window against a throwaway data dir;
//    the frontend drives the mock usage through 85% / 95%, edits, finalizes,
//    copies the prompt and reports back over IPC, then the app exits.
// 3. Verifies native window flags and (macOS) the clipboard contents.
// 4. Phase 2: relaunches against the same data dir to prove persistence.
//
// Usage: npm run smoke [-- --skip-build] [-- --keep] [-- --release]
// --release tests the existing release binary from `npm run tauri build` (no rebuild).
// Your clipboard text is saved before the run and restored afterwards (macOS).

import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const appDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const args = new Set(process.argv.slice(2));
const profile = args.has("--release") ? "release" : "debug";
const binary = join(appDir, "src-tauri", "target", profile, process.platform === "win32" ? "context-guardian.exe" : "context-guardian");
const PHASE_TIMEOUT_MS = 90_000;
const isMac = process.platform === "darwin";

function log(message) {
  console.log(`[smoke] ${message}`);
}

function readClipboard() {
  return isMac ? execFileSync("pbpaste", { encoding: "utf8" }) : null;
}

function writeClipboard(text) {
  if (isMac && text !== null) execFileSync("pbcopy", { input: text });
}

function runPhase(phase, dataDir) {
  const reportPath = join(dataDir, `smoke-report-${phase}.json`);
  return new Promise((resolve, reject) => {
    const child = spawn(binary, [], {
      cwd: appDir,
      stdio: ["ignore", "inherit", "inherit"],
      env: {
        ...process.env,
        CONTEXT_GUARDIAN_DATA_DIR: dataDir,
        // Never read or write the user's real Keychain item during a smoke run.
        CONTEXT_GUARDIAN_KEYCHAIN_SERVICE: `ai.acrosystems.suez.contextguardian.smoke-${process.pid}`,
        CONTEXT_GUARDIAN_SMOKE_REPORT: reportPath,
        CONTEXT_GUARDIAN_SMOKE_PHASE: phase
      }
    });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`phase ${phase} timed out after ${PHASE_TIMEOUT_MS / 1000}s`));
    }, PHASE_TIMEOUT_MS);
    child.on("error", reject);
    child.on("exit", (code) => {
      clearTimeout(timer);
      if (!existsSync(reportPath)) {
        reject(new Error(`phase ${phase} exited with code ${code} and wrote no report`));
        return;
      }
      resolve({ code, report: JSON.parse(readFileSync(reportPath, "utf8")) });
    });
  });
}

function printSteps(report) {
  for (const step of report.frontend.steps) {
    log(`  ${step.ok ? "PASS" : "FAIL"}  ${step.name}${step.detail ? `  — ${step.detail}` : ""}`);
  }
}

async function main() {
  if (!args.has("--skip-build") && profile === "debug") {
    log("building debug app (tauri build --debug --no-bundle)…");
    execFileSync("npx", ["tauri", "build", "--debug", "--no-bundle"], { cwd: appDir, stdio: "inherit" });
  }
  if (!existsSync(binary)) throw new Error(`binary not found: ${binary}`);

  const dataDir = mkdtempSync(join(tmpdir(), "context-guardian-smoke-"));
  log(`binary (${profile}): ${binary}`);
  log(`throwaway data dir: ${dataDir}`);
  const savedClipboard = readClipboard();
  const failures = [];

  try {
    log("phase 1: workflow in the native window");
    const one = await runPhase("1", dataDir);
    printSteps(one.report);
    const native = one.report.native;
    log(`  native window: ${JSON.stringify(native)}`);
    if (!one.report.frontend.passed) failures.push("phase 1 frontend steps failed");
    if (native.alwaysOnTop !== true) failures.push("window is not always-on-top");
    if (native.decorated !== false) failures.push("window is decorated (expected frameless widget)");
    if (native.visible !== true) failures.push("window was not visible");

    if (isMac) {
      const clipboard = readClipboard();
      const matches = clipboard === one.report.frontend.copiedPrompt && clipboard.length > 0;
      log(`  ${matches ? "PASS" : "FAIL"}  system clipboard holds the continuation prompt (${clipboard.length} chars)`);
      if (!matches) failures.push("clipboard does not match the generated prompt");
    } else {
      log("  SKIP  clipboard check (macOS only)");
    }

    log("phase 2: relaunch and verify persistence");
    const two = await runPhase("2", dataDir);
    printSteps(two.report);
    if (!two.report.frontend.passed) failures.push("phase 2 frontend steps failed");
  } finally {
    writeClipboard(savedClipboard);
    if (isMac) log("restored your previous clipboard text");
    if (args.has("--keep")) log(`kept data dir ${dataDir}`);
    else rmSync(dataDir, { recursive: true, force: true });
  }

  if (failures.length > 0) {
    log(`FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  log("ALL CHECKS PASSED");
}

main().catch((error) => {
  console.error(`[smoke] ERROR: ${error.message}`);
  process.exit(1);
});
