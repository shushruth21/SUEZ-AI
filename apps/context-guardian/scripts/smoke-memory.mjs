#!/usr/bin/env node
// Native end-to-end test of persistent project memory.
//
// Runs the real app three times against ONE throwaway data dir seeded with a
// copy of the synthetic schema-1 checkpoint
// src-tauri/src/testdata/checkpoint-v1-sample.json. The user's real data dir and
// Keychain item are never used: CONTEXT_GUARDIAN_DATA_DIR and a throwaway
// Keychain service are set.
//
//   memory1  import preview/approve, project drafts, evidence rules, merge,
//            stale/removal refusals, approvals, secret guard, prompt, assignment
//   memory2  restart: memory and history persisted; restore a revision
//   (driver) corrupts memory.json
//   memory3  restart: corrupt memory reported, recovered, file quarantined
//
// Usage: npm run smoke:memory [-- --skip-build] [-- --keep] [-- --release]

import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const appDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const args = new Set(process.argv.slice(2));
const profile = args.has("--release") ? "release" : "debug";
const binary = join(appDir, "src-tauri", "target", profile, "context-guardian");
const FIXTURE = join(appDir, "src-tauri", "src", "testdata", "checkpoint-v1-sample.json");
const LEGACY_ID = "example-cg-v1-legacy-sample";
const PHASE_TIMEOUT_MS = 120_000;
const keychainService = `ai.acrosystems.suez.contextguardian.smoke-memory-${process.pid}`;

const log = (message) => console.log(`[smoke:memory] ${message}`);
const sha = (path) => createHash("sha256").update(readFileSync(path)).digest("hex");

function runPhase(phase, dataDir) {
  const reportPath = join(dataDir, `smoke-report-${phase}.json`);
  return new Promise((resolve, reject) => {
    const child = spawn(binary, [], {
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
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`phase ${phase} timed out`));
    }, PHASE_TIMEOUT_MS);
    child.on("exit", () => {
      clearTimeout(timer);
      if (!existsSync(reportPath)) return reject(new Error(`phase ${phase} wrote no report`));
      resolve(JSON.parse(readFileSync(reportPath, "utf8")));
    });
  });
}

function check(failures, ok, label) {
  log(`  ${ok ? "PASS" : "FAIL"}  ${label}`);
  if (!ok) failures.push(label);
}

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

  const dataDir = mkdtempSync(join(tmpdir(), "context-guardian-memory-smoke-"));
  const legacyPath = join(dataDir, "checkpoints", `${LEGACY_ID}.json`);
  mkdirSync(dirname(legacyPath), { recursive: true });
  copyFileSync(FIXTURE, legacyPath);
  const fixtureHash = sha(FIXTURE);
  log(`binary (${profile}): ${binary}`);
  log(`throwaway data dir ${dataDir}; seeded copy sha256 ${fixtureHash.slice(0, 16)}…`);
  const savedClipboard = execFileSync("pbpaste", { encoding: "utf8" });
  const failures = [];

  try {
    for (const phase of ["memory1", "memory2", "memory3"]) {
      if (phase === "memory3") {
        const projectsDir = join(dataDir, "projects");
        const [projectId] = readdirSync(projectsDir);
        writeFileSync(join(projectsDir, projectId, "memory.json"), "{ truncated by smoke driver");
        log("driver: corrupted memory.json before memory3");
      }
      log(`phase ${phase}`);
      const report = await runPhase(phase, dataDir);
      for (const step of report.frontend.steps) {
        log(`  ${step.ok ? "PASS" : "FAIL"}  ${step.name}${step.detail ? `  — ${step.detail}` : ""}`);
      }
      if (!report.frontend.passed) failures.push(`${phase} steps failed`);
      if (phase === "memory1") {
        const clipboard = execFileSync("pbpaste", { encoding: "utf8" });
        check(failures, clipboard.length > 0 && clipboard === report.frontend.copiedPrompt, `clipboard holds the memory-aware prompt (${clipboard.length} chars)`);
        const backupDir = join(dataDir, "checkpoints", ".backups");
        const backups = existsSync(backupDir) ? readdirSync(backupDir) : [];
        check(failures, backups.length === 1 && sha(join(dataDir, "checkpoints", ".backups", backups[0])) === fixtureHash,
          `assignment kept a byte-identical backup of the original checkpoint (${backups[0]})`);
        const upgraded = JSON.parse(readFileSync(legacyPath, "utf8"));
        const original = JSON.parse(readFileSync(FIXTURE, "utf8"));
        const lost = Object.keys(original).filter((k) => k !== "schemaVersion" && k !== "updatedAt" && JSON.stringify(upgraded[k]) !== JSON.stringify(original[k]));
        check(failures, upgraded.schemaVersion === 2 && lost.length === 0, `upgraded file keeps every original field (changed: ${lost.join(", ") || "none"})`);
      }
    }

    const projectDir = join(dataDir, "projects", readdirSync(join(dataDir, "projects"))[0]);
    const quarantined = readdirSync(projectDir).filter((n) => n.startsWith("memory.corrupt-"));
    check(failures, quarantined.length === 1 && readFileSync(join(projectDir, quarantined[0]), "utf8") === "{ truncated by smoke driver",
      `unreadable memory kept as ${quarantined[0] ?? "(missing)"}`);
    const leaks = filesUnder(dataDir).filter((f) => readFileSync(f, "utf8").includes("cg-smoke-secret-"));
    check(failures, leaks.length === 0, `test API key absent from every file in the data dir (${filesUnder(dataDir).length} files)`);
    let keyLeft = true;
    try {
      execFileSync("security", ["find-generic-password", "-s", keychainService, "-a", "gateway-api-key"], { stdio: "ignore" });
    } catch {
      keyLeft = false;
    }
    check(failures, !keyLeft, "test Keychain item was cleared by the app");
    check(failures, sha(FIXTURE) === fixtureHash, "fixture unchanged");
  } finally {
    try {
      execFileSync("security", ["delete-generic-password", "-s", keychainService, "-a", "gateway-api-key"], { stdio: "ignore" });
    } catch {}
    execFileSync("pbcopy", { input: savedClipboard });
    log("restored your previous clipboard text");
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
  console.error(`[smoke:memory] ERROR: ${error.message}`);
  process.exit(1);
});
