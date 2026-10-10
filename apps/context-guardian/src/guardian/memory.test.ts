import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { Checkpoint, ProjectMemory } from "../types";
import { createDraft } from "./checkpoint";
import { diffMemory, importFromCheckpoint, parseLegacyLabel, proposeEdit, proposeMerge, removals } from "./memory";

// Synthetic schema-1 checkpoint that mirrors the labels of real hand-written
// checkpoints. Rust fills schema 2 fields with defaults when loading; mirror that.
const FIXTURE_PATH = new URL("../../src-tauri/src/testdata/checkpoint-v1-sample.json", import.meta.url);
const raw = readFileSync(FIXTURE_PATH, "utf8");
function legacy(): Checkpoint {
  return {
    currentTask: "",
    nextAction: "",
    workItems: [],
    testResults: [],
    commands: [],
    errors: [],
    decisionRecords: [],
    filesCreated: [],
    filesModified: [],
    ...JSON.parse(raw)
  };
}

const NOW = new Date("2026-10-11T09:00:00.000Z");

describe("parseLegacyLabel", () => {
  it.each([
    ["[VERIFIED 2026-10-10, rerun during this checkpoint] Vitest: 54/54", "historical_verified"],
    ["[REPORTED, previous session — not rerun] Go vet clean", "reported"],
    ["[DONE] Floating widget", "reported"],
    ["[PENDING — user action] Migration 0006 is NOT applied", "approval"],
    ["[PENDING] Set models.context_window", "pending"],
    ["[NOT TESTED] Provider-reported token counts", "pending"],
    ["[DEFERRED by user] Title-bar design", "pending"],
    ["[HOUSEKEEPING] Throwaway stack", "pending"],
    ["[LIMITATION] Apple Silicon only", "limitation"],
    ["[FAILED] smoke", "failed"],
    ["No label here", "none"]
  ])("%s -> %s", (line, expected) => {
    expect(parseLegacyLabel(line).labelClass).toBe(expected);
  });
});

describe("importFromCheckpoint (legacy checkpoint)", () => {
  const checkpoint = legacy();
  const memory = importFromCheckpoint(checkpoint, "proj-1", "SUEZ Context Guardian", NOW);

  it("does not modify the checkpoint", () => {
    importFromCheckpoint(checkpoint, "proj-1", "SUEZ Context Guardian", NOW);
    expect(JSON.parse(raw)).toEqual(JSON.parse(raw));
    expect(checkpoint.completed).toEqual(JSON.parse(raw).completed);
  });

  it("keeps every piece of information somewhere in memory", () => {
    expect(memory.objective).toBe(checkpoint.goal);
    expect(memory.currentStatus).toBe(checkpoint.currentState);
    expect(memory.notes).toBe(checkpoint.notes);
    expect(memory.decisions.map((d) => d.text)).toEqual(checkpoint.decisions);
    const texts = new Set([
      ...memory.workItems.map((w) => w.text),
      ...memory.knownIssues,
      ...memory.requiredApprovals.map((a) => a.item)
    ]);
    for (const line of [...checkpoint.completed, ...checkpoint.openTasks]) expect(texts.has(line)).toBe(true);
    expect(memory.nextAction?.text).toBe(checkpoint.nextSteps[0]);
    for (const step of checkpoint.nextSteps.slice(1)) expect(texts.has(`Next step: ${step}`)).toBe(true);
    expect(memory.sources).toEqual([{ checkpointId: checkpoint.id, appliedAt: NOW.toISOString(), kind: "import" }]);
  });

  it("never marks anything verified: historical results stay historical", () => {
    expect(memory.workItems.filter((w) => w.status === "verified")).toEqual([]);
    const vitest = memory.workItems.find((w) => w.text.includes("Vitest: 4 files, 54/54"))!;
    expect(vitest.status).toBe("reported");
    expect(vitest.evidence).toHaveLength(1);
    expect(vitest.evidence[0].kind).toBe("prior_report");
    expect(vitest.evidence[0].sessionId).toBe(checkpoint.sessionId);
    expect(vitest.evidence[0].observedAt).toBe(checkpoint.createdAt);
    expect(vitest.evidence[0].summary).toContain("NOT rerun since");
    const goVet = memory.workItems.find((w) => w.text.includes("Go vet clean"))!;
    expect(goVet.status).toBe("reported");
    expect(goVet.evidence[0].summary).toContain("REPORTED, previous session");
  });

  it("classifies open work by its label", () => {
    expect(memory.requiredApprovals.map((a) => a.status)).toEqual(["awaiting", "awaiting"]);
    expect(memory.requiredApprovals[0].item).toContain("Migration 0006");
    expect(memory.requiredApprovals[1].item).toContain("NOT deployed");
    expect(memory.knownIssues.length).toBe(4);
    const pending = memory.workItems.filter((w) => w.status === "pending");
    expect(pending.some((w) => w.text.includes("[NOT TESTED]"))).toBe(true);
    expect(pending.some((w) => w.text.includes("[DEFERRED by user]"))).toBe(true);
  });

  it("produces unique ids", () => {
    const ids = [...memory.workItems, ...memory.decisions, ...memory.requiredApprovals].map((x) => x.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) expect(id).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
  });
});

function v2Checkpoint(memory: ProjectMemory): Checkpoint {
  const draft = createDraft({
    id: "cp-2",
    now: NOW,
    projectName: memory.name,
    projectId: memory.projectId,
    sessionId: "sess-2",
    sessionLabel: "S2",
    trigger: "manual",
    thresholdPercent: null,
    usage: null,
    memory
  });
  return {
    ...draft,
    currentState: "Stage 3 done",
    nextAction: "Build the memory panel",
    workItems: [
      ...draft.workItems,
      {
        id: "w-new",
        text: "Memory store implemented",
        status: "verified",
        evidence: [{ kind: "test_run", summary: "cargo test: 42 passed", observedAt: NOW.toISOString(), sessionId: "sess-2" }]
      }
    ],
    testResults: [
      {
        id: "t1",
        suite: "Rust cargo test",
        command: "cargo test",
        covers: "storage + memory",
        outcome: "passed",
        passed: 42,
        failed: 0,
        observedAt: NOW.toISOString(),
        sessionId: "sess-2",
        provenance: "this_session"
      },
      {
        id: "t0",
        suite: "Go unit tests",
        command: "",
        covers: "",
        outcome: "passed",
        observedAt: "2026-10-09T00:00:00.000Z",
        sessionId: "old",
        provenance: "carried",
        carriedFrom: "cp-1"
      }
    ],
    errors: [{ id: "e1", error: "Keychain prompt after rebuild", resolution: "", status: "open" }]
  };
}

describe("proposeMerge", () => {
  const base = importFromCheckpoint(legacy(), "proj-1", "SUEZ Context Guardian", NOW);

  it("only adds or updates, records the source, and is idempotent", () => {
    const checkpoint = v2Checkpoint(base);
    const proposal = proposeMerge(base, checkpoint, NOW);
    expect(proposal.baseRevision).toBe(base.revision);
    expect(removals(proposal.changes)).toEqual([]);
    const areas = proposal.changes.map((c) => `${c.kind}:${c.area}`);
    expect(areas).toContain("update:Current status");
    expect(areas).toContain("update:Next action");
    expect(areas).toContain("add:Work items");
    expect(areas).toContain("add:Test results");
    expect(areas).toContain("add:Known issues");
    expect(areas).toContain("add:Sources");
    // Every original item is still there.
    for (const item of base.workItems) expect(proposal.next.workItems.some((w) => w.id === item.id)).toBe(true);
    // Carried test results do not enter memory as new results.
    expect(proposal.next.testResults.map((t) => t.suite)).toEqual(["Rust cargo test"]);

    const again = proposeMerge(proposal.next, checkpoint, NOW);
    expect(again.changes).toEqual([]);
    expect(again.next).toEqual(proposal.next);
  });

  it("updates an outstanding memory item by id when the checkpoint resolves it", () => {
    const checkpoint = v2Checkpoint(base);
    const outstanding = checkpoint.workItems.find((w) => w.text.includes("[NOT TESTED]"))!;
    const resolved = checkpoint.workItems.map((w) =>
      w.id === outstanding.id ? { ...w, status: "failed" as const } : w
    );
    const proposal = proposeMerge(base, { ...checkpoint, workItems: resolved }, NOW);
    expect(proposal.changes).toContainEqual(
      expect.objectContaining({ kind: "update", area: "Work items", summary: expect.stringContaining("pending → failed") })
    );
  });

  it("an empty checkpoint proposes nothing", () => {
    const empty = { ...v2Checkpoint(base), currentState: "", nextAction: "", workItems: [], testResults: [], errors: [] };
    expect(proposeMerge(base, empty, NOW).changes).toEqual([]);
  });
});

describe("diffMemory / proposeEdit", () => {
  const base = importFromCheckpoint(legacy(), "proj-1", "SUEZ Context Guardian", NOW);

  it("shows removals explicitly so nothing disappears silently", () => {
    const edited = { ...base, knownIssues: base.knownIssues.slice(1), constraints: ["No paid calls"] };
    const proposal = proposeEdit(base, edited, NOW);
    expect(removals(proposal.changes)).toEqual([
      expect.objectContaining({ area: "Known issues", kind: "remove" })
    ]);
    expect(proposal.changes).toContainEqual(expect.objectContaining({ area: "Constraints", kind: "add" }));
    expect(proposal.baseRevision).toBe(base.revision);
  });

  it("approval status changes are visible", () => {
    const edited = structuredClone(base);
    edited.requiredApprovals[0].status = "granted";
    expect(diffMemory(base, edited)).toEqual([
      expect.objectContaining({ area: "Required approvals", summary: expect.stringContaining("awaiting → granted") })
    ]);
  });

  it("no edit, no changes", () => {
    expect(diffMemory(base, structuredClone(base))).toEqual([]);
  });
});
