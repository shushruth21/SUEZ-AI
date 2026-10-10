#!/usr/bin/env node
// Native end-to-end test of Context Guardian against the REAL SUEZ gateway,
// using the throwaway stack from scripts/gateway-test-stack.sh and the free
// stub model (no paid provider calls).
//
// The app runs in smoke phase "gateway" with a throwaway data dir and a
// throwaway Keychain service name. It signals each step ("need:50", ...); this
// driver answers by sending real chat requests with the app's session id, or
// by stopping/starting the test gateway container. Afterwards it checks that
// the API key never reached disk, that it was stored in the Keychain (then
// deletes that test item), and that the clipboard holds the prompt.
// A second launch ("gateway-restart") reuses the data dir and gateway session,
// whose latest measurement is still above 85%, and checks that the restart
// does not create a duplicate automatic draft.
//
// Usage: npm run smoke:gateway [-- --skip-build] [-- --keep] [-- --release]
// --release tests the existing release binary from `npm run tauri build`.
// Requires: scripts/gateway-test-stack.sh up

import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const appDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const args = new Set(process.argv.slice(2));
const profile = args.has("--release") ? "release" : "debug";
const binary = join(appDir, "src-tauri", "target", profile, "context-guardian");
const GATEWAY_URL = `http://127.0.0.1:${process.env.CG_TEST_GATEWAY_PORT ?? "18080"}`;
const API_KEY = "suez-demo-admin-key";
const WRONG_KEY = "wrong-key-for-smoke-test";
const CONTEXT_WINDOW = 1000;
const TIMEOUT_MS = 240_000;
const keychainService = `ai.acrosystems.suez.contextguardian.smoke-${process.pid}`;
const sessionId = `smoke-${Date.now().toString(36)}-${process.pid}`;

const log = (message) => console.log(`[smoke:gateway] ${message}`);

function sh(cmd, cmdArgs, options = {}) {
  return execFileSync(cmd, cmdArgs, { encoding: "utf8", stdio: ["pipe", "pipe", "pipe"], ...options });
}

// Mirrors the gateway's stub estimate (ceil(chars / 4) per string) so a
// request can be sized to a target context occupancy.
function stubTotal(content) {
  const est = (text) => Math.max(1, Math.ceil(text.trim().length / 4));
  return est("user") + est(content) + est(`Stub response from demo-model: ${content}`);
}

function contentFor(targetTokens, tag) {
  const prefix = `Context Guardian smoke ${sessionId} ${tag} `;
  let padding = "";
  while (stubTotal(prefix + padding) < targetTokens) padding += "x";
  return prefix + padding;
}

const sent = {};
async function chat(content) {
  const response = await fetch(`${GATEWAY_URL}/v1/chat/completions`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-API-Key": API_KEY, "X-SUEZ-Session-Id": sessionId },
    body: JSON.stringify({ model: "demo-model", messages: [{ role: "user", content }] })
  });
  const body = await response.json();
  if (!response.ok) throw new Error(`chat failed ${response.status}: ${JSON.stringify(body)}`);
  return `${response.headers.get("x-suez-cache")} total=${body.usage.total_tokens}`;
}

async function waitForGateway(up) {
  for (let i = 0; i < 60; i++) {
    const ok = await fetch(`${GATEWAY_URL}/`).then((r) => r.ok).catch(() => false);
    if (ok === up) return;
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`gateway did not become ${up ? "reachable" : "unreachable"}`);
}

const handlers = {
  "need:50": async () => chat((sent.a = contentFor(CONTEXT_WINDOW * 0.5, "a"))),
  "need:86": async () => chat((sent.b = contentFor(CONTEXT_WINDOW * 0.86, "b"))),
  "need:repeat": async () => chat(sent.b),
  "need:96": async () => chat(contentFor(CONTEXT_WINDOW * 0.96, "c")),
  "need:down": async () => {
    sh("docker", ["stop", "-t", "1", "cg-test-gateway"]);
    await waitForGateway(false);
    return "test gateway stopped";
  },
  "need:up": async () => {
    sh("docker", ["start", "cg-test-gateway"]);
    await waitForGateway(true);
    return "test gateway started";
  },
  done: async () => "app finished its steps"
};

function filesUnder(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? filesUnder(path) : [path];
  });
}

async function main() {
  if (!args.has("--skip-build") && profile === "debug") {
    log("building debug app…");
    execFileSync("npx", ["tauri", "build", "--debug", "--no-bundle"], { cwd: appDir, stdio: "inherit" });
  }
  if (!existsSync(binary)) throw new Error(`binary not found: ${binary}`);
  await waitForGateway(true).catch(() => {
    throw new Error(`test gateway not reachable at ${GATEWAY_URL}; run scripts/gateway-test-stack.sh up`);
  });

  sh("docker", ["exec", "cg-test-postgres", "psql", "-qAt", "-U", "suez", "-d", "suez", "-c",
    `update models set context_window = ${CONTEXT_WINDOW} where external_name = 'demo-model'`]);
  log(`test DB: demo-model context_window = ${CONTEXT_WINDOW} (test container only)`);

  const dataDir = mkdtempSync(join(tmpdir(), "context-guardian-gw-smoke-"));
  writeFileSync(join(dataDir, "settings.json"), JSON.stringify({
    projectName: "SUEZ", limitTokens: 200000, checkpointPercent: 85, criticalPercent: 95, rearmMargin: 5,
    usageSource: "gateway", gatewayUrl: GATEWAY_URL, gatewaySessionId: sessionId, pollIntervalSeconds: 1
  }));
  const savedClipboard = sh("pbpaste", []);
  log(`binary (${profile}): ${binary}`);
  log(`session ${sessionId}; data dir ${dataDir}; keychain service ${keychainService}`);

  const failures = [];
  let child = null;
  let finished = true;

  // Launches the app in one smoke phase and answers its signals until it exits.
  async function launch(phase) {
    const reportPath = join(dataDir, `smoke-report-${phase}.json`);
    const signalsPath = `${reportPath}.signals`;
    child = spawn(binary, [], {
      cwd: appDir,
      stdio: ["ignore", "inherit", "inherit"],
      env: {
        ...process.env,
        CONTEXT_GUARDIAN_DATA_DIR: dataDir,
        CONTEXT_GUARDIAN_KEYCHAIN_SERVICE: keychainService,
        CONTEXT_GUARDIAN_SMOKE_REPORT: reportPath,
        CONTEXT_GUARDIAN_SMOKE_PHASE: phase
      }
    });
    finished = false;
    child.on("exit", () => (finished = true));
    let handled = 0;
    const deadline = Date.now() + TIMEOUT_MS;
    while (!finished && Date.now() < deadline) {
      const lines = existsSync(signalsPath) ? readFileSync(signalsPath, "utf8").split("\n").filter(Boolean) : [];
      for (; handled < lines.length; handled++) {
        const signal = lines[handled];
        const handler = handlers[signal];
        if (!handler) throw new Error(`unknown signal ${signal}`);
        log(`  driver: ${signal} -> ${await handler()}`);
      }
      await new Promise((r) => setTimeout(r, 200));
    }
    if (!finished) {
      child.kill("SIGKILL");
      throw new Error(`app did not finish phase ${phase} in time`);
    }
    return JSON.parse(readFileSync(reportPath, "utf8"));
  }

  try {
    const report = await launch("gateway");
    for (const step of report.frontend.steps) {
      log(`  ${step.ok ? "PASS" : "FAIL"}  ${step.name}${step.detail ? `  — ${step.detail}` : ""}`);
    }
    if (!report.frontend.passed) failures.push("app steps failed");
    log(`  native window: ${JSON.stringify(report.native)}`);
    if (report.native.alwaysOnTop !== true) failures.push("window is not always-on-top");

    const leaks = filesUnder(dataDir).filter((file) => {
      const text = readFileSync(file, "utf8");
      return text.includes(API_KEY) || text.includes(WRONG_KEY);
    });
    log(`  ${leaks.length === 0 ? "PASS" : "FAIL"}  API key absent from every file in the data dir (${filesUnder(dataDir).length} files checked)`);
    if (leaks.length) failures.push(`API key found in ${leaks.join(", ")}`);

    let inKeychain = false;
    try {
      sh("security", ["find-generic-password", "-s", keychainService, "-a", "gateway-api-key"]);
      inKeychain = true;
    } catch {}
    log(`  ${inKeychain ? "PASS" : "FAIL"}  API key stored in the macOS Keychain (service ${keychainService})`);
    if (!inKeychain) failures.push("API key not found in the Keychain");

    const clipboard = sh("pbpaste", []);
    const matches = clipboard === report.frontend.copiedPrompt && clipboard.length > 0;
    log(`  ${matches ? "PASS" : "FAIL"}  clipboard holds the continuation prompt (${clipboard.length} chars)`);
    if (!matches) failures.push("clipboard mismatch");

    log("relaunch in the same gateway session (latest measurement still above 85%)");
    const restart = await launch("gateway-restart");
    for (const step of restart.frontend.steps) {
      log(`  ${step.ok ? "PASS" : "FAIL"}  ${step.name}${step.detail ? `  — ${step.detail}` : ""}`);
    }
    if (!restart.frontend.passed) failures.push("restart duplicate check failed");
  } finally {
    if (!finished && child) child.kill("SIGKILL");
    try {
      sh("security", ["delete-generic-password", "-s", keychainService, "-a", "gateway-api-key"]);
      log("removed the test Keychain item");
    } catch {}
    execFileSync("pbcopy", { input: savedClipboard });
    log("restored your previous clipboard text");
    try {
      sh("docker", ["start", "cg-test-gateway"]);
    } catch {}
    if (args.has("--keep")) log(`kept data dir ${dataDir}`);
    else rmSync(dataDir, { recursive: true, force: true });
  }

  if (failures.length) {
    log(`FAILED: ${failures.join("; ")}`);
    process.exit(1);
  }
  log("ALL CHECKS PASSED");
}

main().catch((error) => {
  console.error(`[smoke:gateway] ERROR: ${error.message}`);
  process.exit(1);
});
