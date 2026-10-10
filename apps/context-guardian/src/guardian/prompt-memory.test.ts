import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { Checkpoint } from "../types";
import { createDraft } from "./checkpoint";
import { importFromCheckpoint } from "./memory";
import { PROMPT_LIST_LIMIT, buildContinuationPrompt, resolveNextAction } from "./prompt";

const raw = readFileSync(new URL("../../src-tauri/src/testdata/checkpoint-v1-sample.json", import.meta.url), "utf8");
// Expected legacy-format prompt for the same synthetic checkpoint. Checkpoints
// without structured content and without memory must keep producing exactly this.
const legacyPrompt = readFileSync(new URL("./__fixtures__/legacy-sample-prompt.txt", import.meta.url), "utf8");

function legacy(): Checkpoint {
  return {
    currentTask: "", nextAction: "", workItems: [], testResults: [], commands: [], errors: [],
    decisionRecords: [], filesCreated: [], filesModified: [], ...JSON.parse(raw)
  };
}

const NOW = new Date("2026-10-11T10:00:00.000Z");

describe("continuation prompt compatibility", () => {
  it("is byte-for-byte unchanged for a legacy checkpoint without memory", () => {
    expect(buildContinuationPrompt(legacy())).toBe(legacyPrompt);
  });
});

describe("continuation prompt with evidence and memory", () => {
  const memory = importFromCheckpoint(legacy(), "proj-1", "SUEZ Context Guardian", NOW);
  const draft = createDraft({
    id: "cp-2", now: NOW, projectName: memory.name, projectId: "proj-1", sessionId: "sess-2", sessionLabel: "S2",
    trigger: "manual", thresholdPercent: null, usage: null, memory
  });
  const checkpoint: Checkpoint = {
    ...draft,
    currentTask: "Stage 5 UI",
    nextAction: "Wire the memory panel into App.tsx",
    workItems: [
      ...draft.workItems,
      { id: "v1", text: "Memory store", status: "verified",
        evidence: [{ kind: "test_run", summary: "cargo test: 43 passed", observedAt: NOW.toISOString(), sessionId: "sess-2" }] },
      { id: "f1", text: "Smoke memory phase", status: "failed", evidence: [] }
    ],
    testResults: [
      { id: "t1", suite: "Rust cargo test", command: "cargo test", covers: "storage and memory modules", outcome: "passed",
        passed: 43, failed: 0, observedAt: NOW.toISOString(), sessionId: "sess-2", provenance: "this_session" },
      { id: "t2", suite: "Go unit tests", command: "", covers: "", outcome: "passed", observedAt: "2026-10-09T00:00:00.000Z",
        sessionId: "old", provenance: "carried", carriedFrom: "example-cg-v1-legacy-sample" }
    ]
  };
  const prompt = buildContinuationPrompt(checkpoint, memory);

  it("starts with the exact next action", () => {
    expect(prompt).toContain("## NEXT ACTION (start here)\nWire the memory panel into App.tsx");
    expect(prompt.indexOf("NEXT ACTION")).toBeLessThan(prompt.indexOf("## Goal"));
    expect(resolveNextAction({ ...checkpoint, nextAction: "" }, memory)).toBe(memory.nextAction!.text);
  });

  it("separates verified, reported, failed and pending work", () => {
    expect(prompt).toContain("### Verified in this checkpoint's session (evidence recorded)\n- Memory store — evidence: cargo test: 43 passed");
    expect(prompt).toContain("### Failed\n- Smoke memory phase");
    const reported = prompt.split("## Completed per memory — reported, NOT verified")[1].split("\n## ")[0];
    expect(reported).toContain("- [VERIFIED 2026-01-15, rerun during this checkpoint] Vitest");
    expect(prompt.split("## Completed per memory — verified when recorded")[1].split("\n## ")[0]).toContain("_None recorded._");
  });

  it("never presents carried test results as rerun and states what a test covers", () => {
    expect(prompt).toContain("Rust cargo test: PASSED (43 passed, 0 failed) — run in this session");
    expect(prompt).toContain("covers only: storage and memory modules");
    expect(prompt).toContain("Go unit tests: PASSED — CARRIED from checkpoint example-cg-v1-legacy-sample, observed 2026-10-09T00:00:00.000Z; NOT rerun");
  });

  it("lists awaiting approvals with a do-not-perform instruction", () => {
    expect(prompt).toContain("Required approvals — AWAITING (do not perform these without explicit user approval)");
    expect(prompt).toContain("Migration 0006 is NOT applied");
    expect(prompt).toContain("Never perform an item under \"Required approvals — AWAITING\"");
  });

  it("caps long lists instead of pasting the whole history", () => {
    const many = { ...checkpoint, filesModified: Array.from({ length: 40 }, (_, i) => `file-${i}.ts`) };
    const text = buildContinuationPrompt(many, memory);
    expect(text).toContain(`file-${PROMPT_LIST_LIMIT - 1}.ts`);
    expect(text).not.toContain(`file-${PROMPT_LIST_LIMIT}.ts`);
    expect(text).toContain(`…and ${40 - PROMPT_LIST_LIMIT} more`);
  });

  it("ignores memory of a different project", () => {
    const other = { ...memory, projectId: "proj-2" };
    expect(buildContinuationPrompt(checkpoint, other)).not.toContain("## Project memory");
  });
});
