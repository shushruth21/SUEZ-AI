import { describe, expect, it } from "vitest";

import { Checkpoint, ProjectMemory } from "../types";
import { createDraft, newestFor, newestForProject } from "./checkpoint";
import { newProjectMemory } from "./memory";

const NOW = new Date("2026-10-11T10:00:00.000Z");

function base(overrides: Partial<Checkpoint> = {}): Checkpoint {
  return {
    ...createDraft({
      id: "prev",
      now: new Date("2026-10-10T10:00:00.000Z"),
      projectName: "SUEZ Context Guardian",
      projectId: "proj-1",
      sessionId: "sess-1",
      sessionLabel: "S1",
      trigger: "manual",
      thresholdPercent: null,
      usage: null
    }),
    currentTask: "Stage 3",
    nextAction: "Write the panel",
    workItems: [
      {
        id: "w1",
        text: "Memory store",
        status: "verified",
        evidence: [{ kind: "test_run", summary: "cargo test 42 passed", observedAt: "2026-10-10T10:00:00.000Z", sessionId: "sess-1" }]
      },
      { id: "w2", text: "Smoke test", status: "failed", evidence: [] },
      { id: "w3", text: "Docs", status: "pending", evidence: [] }
    ],
    testResults: [
      {
        id: "t1",
        suite: "vitest",
        command: "npx vitest run",
        covers: "frontend units",
        outcome: "passed",
        passed: 80,
        observedAt: "2026-10-10T10:00:00.000Z",
        sessionId: "sess-1",
        provenance: "this_session"
      }
    ],
    commands: [{ id: "c1", command: "cargo test", purpose: "", at: "2026-10-10T10:00:00.000Z" }],
    errors: [
      { id: "e1", error: "open one", resolution: "", status: "open" },
      { id: "e2", error: "fixed one", resolution: "patched", status: "resolved" }
    ],
    filesCreated: ["a.ts"],
    ...overrides
  };
}

function next(previous: Checkpoint, memory: ProjectMemory | null = null, projectId = "proj-1"): Checkpoint {
  return createDraft({
    id: "new",
    now: NOW,
    projectName: "SUEZ Context Guardian",
    projectId,
    sessionId: "sess-2",
    sessionLabel: "S2",
    trigger: "auto_threshold",
    thresholdPercent: 85,
    usage: null,
    previous,
    memory
  });
}

describe("carry-forward", () => {
  it("downgrades verified claims to reported with provenance, keeps failed and pending", () => {
    const draft = next(base());
    const [w1, w2, w3] = draft.workItems;
    expect(w1.status).toBe("reported");
    expect(w1.carriedFrom).toBe("prev");
    expect(w1.evidence).toHaveLength(2);
    expect(w1.evidence[0]).toEqual(base().workItems[0].evidence[0]); // original evidence kept
    expect(w1.evidence[1]).toMatchObject({ kind: "prior_report", sessionId: "sess-1" });
    expect(w1.evidence[1].summary).toContain("not re-verified in this session");
    expect([w2.status, w3.status]).toEqual(["failed", "pending"]);
    expect(draft.workItems.filter((w) => w.status === "verified")).toEqual([]);
  });

  it("marks carried test results as carried, never as this session", () => {
    const draft = next(base());
    expect(draft.testResults).toEqual([{ ...base().testResults[0], provenance: "carried", carriedFrom: "prev" }]);
    // Carrying again keeps the original source.
    const third = createDraft({ ...{ id: "third", now: NOW, projectName: "x", projectId: "proj-1", sessionId: "sess-3", sessionLabel: "", trigger: "manual", thresholdPercent: null, usage: null }, previous: draft });
    expect(third.testResults[0].carriedFrom).toBe("prev");
  });

  it("starts session-specific fields empty and carries only open errors", () => {
    const draft = next(base());
    expect(draft.commands).toEqual([]);
    expect(draft.filesCreated).toEqual([]);
    expect(draft.currentState).toBe("");
    expect(draft.errors.map((e) => e.id)).toEqual(["e1"]);
    expect(draft.nextAction).toBe("Write the panel");
    expect(draft.projectId).toBe("proj-1");
    expect(draft.schemaVersion).toBe(2);
  });

  it("does not inherit across different project ids, even with the same name", () => {
    const draft = next(base(), null, "proj-2");
    expect(draft.workItems).toEqual([]);
    expect(draft.goal).toBe("");
  });

  it("seeds from project memory when there is no previous checkpoint", () => {
    const memory = newProjectMemory("proj-1", "SUEZ Context Guardian", NOW);
    memory.objective = "Remember everything";
    memory.nextAction = { text: "Apply nothing without approval", setAt: NOW.toISOString() };
    memory.workItems = [
      { id: "m1", text: "Outstanding", status: "pending", evidence: [] },
      { id: "m2", text: "Done", status: "reported", evidence: [] }
    ];
    const draft = createDraft({
      id: "n", now: NOW, projectName: "SUEZ Context Guardian", projectId: "proj-1", sessionId: "s", sessionLabel: "",
      trigger: "manual", thresholdPercent: null, usage: null, memory
    });
    expect(draft.goal).toBe("Remember everything");
    expect(draft.nextAction).toBe("Apply nothing without approval");
    expect(draft.workItems.map((w) => [w.id, w.carriedFrom])).toEqual([["m1", "memory-r1"]]);
  });

  it("legacy lookups ignore checkpoints that belong to a project", () => {
    const owned = base();
    const legacyOne = { ...base({ id: "legacy" }), projectId: undefined };
    expect(newestFor([owned, legacyOne], "SUEZ Context Guardian")?.id).toBe("legacy");
    expect(newestForProject([owned, legacyOne], "proj-1")?.id).toBe("prev");
    const legacyDraft = createDraft({
      id: "l", now: NOW, projectName: "SUEZ Context Guardian", sessionId: "s", sessionLabel: "",
      trigger: "manual", thresholdPercent: null, usage: null, previous: owned
    });
    expect(legacyDraft.workItems).toEqual([]);
  });
});
