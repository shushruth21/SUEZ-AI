import { describe, expect, it } from "vitest";

import { Checkpoint } from "../types";
import { createDraft, linesToList, newestFor } from "./checkpoint";
import { buildContinuationPrompt, describeUsage } from "./prompt";

const mockUsage = {
  usedTokens: 170_500,
  limitTokens: 200_000,
  percent: 85.25,
  source: "mock" as const,
  simulated: true,
  capturedAt: "2026-10-09T12:00:00.000Z"
};

function sample(overrides: Partial<Checkpoint> = {}): Checkpoint {
  return {
    ...createDraft({
      id: "cp-1",
      now: new Date("2026-10-09T12:00:00.000Z"),
      projectName: "SUEZ",
      sessionId: "s-1",
      sessionLabel: "Session 1",
      trigger: "auto_threshold",
      thresholdPercent: 85,
      usage: mockUsage
    }),
    goal: "Ship the Context Guardian MVP",
    currentState: "Threshold guard done; UI in progress.",
    decisions: ["Store checkpoints as JSON files"],
    completed: ["Rust toolchain installed"],
    openTasks: ["History view"],
    filesTouched: ["apps/context-guardian/src/App.tsx"],
    nextSteps: ["Build the editor"],
    notes: "Mock data only.",
    ...overrides
  };
}

describe("buildContinuationPrompt", () => {
  it("includes every structured section with its content", () => {
    const prompt = buildContinuationPrompt(sample());
    expect(prompt).toContain('project "SUEZ"');
    expect(prompt).toContain("- Checkpoint ID: cp-1");
    expect(prompt).toContain("## Goal\nShip the Context Guardian MVP");
    expect(prompt).toContain("## Current state\nThreshold guard done; UI in progress.");
    expect(prompt).toContain("## Decisions made\n- Store checkpoints as JSON files");
    expect(prompt).toContain("## Completed\n- Rust toolchain installed");
    expect(prompt).toContain("## Open tasks\n- History view");
    expect(prompt).toContain("## Files touched\n- apps/context-guardian/src/App.tsx");
    expect(prompt).toContain("## Next steps\n- Build the editor");
    expect(prompt).toContain("## Notes\nMock data only.");
    expect(prompt).toContain("automatic, at the 85% context threshold");
  });

  it("labels mock usage as simulated and never as real telemetry", () => {
    const prompt = buildContinuationPrompt(sample());
    expect(prompt).toContain(
      "170,500 / 200,000 tokens (85.3%) — SIMULATED (MOCK DATA). This figure is not real provider telemetry."
    );
  });

  it("marks drafts as unreviewed and finals as reviewed", () => {
    expect(buildContinuationPrompt(sample())).toContain("Status: draft (auto-saved; not yet reviewed by the user)");
    expect(buildContinuationPrompt(sample({ status: "final" }))).toContain("Status: final (reviewed by the user)");
  });

  it("writes a placeholder for empty sections and drops blank list items", () => {
    const prompt = buildContinuationPrompt(sample({ goal: "  ", decisions: ["", "  "], notes: "" }));
    expect(prompt).toContain("## Goal\n_None recorded._");
    expect(prompt).toContain("## Decisions made\n_None recorded._");
    expect(prompt).toContain("## Notes\n_None recorded._");
  });

  it("points at open tasks when there are no next steps", () => {
    expect(buildContinuationPrompt(sample({ nextSteps: [] }))).toContain('first item under "Open tasks"');
    expect(buildContinuationPrompt(sample())).toContain('first item under "Next steps"');
  });

  it("describes manual checkpoints", () => {
    expect(buildContinuationPrompt(sample({ trigger: "manual", thresholdPercent: null }))).toContain(
      "- Trigger: manual"
    );
  });

  it("is deterministic", () => {
    expect(buildContinuationPrompt(sample())).toBe(buildContinuationPrompt(sample()));
  });
});

describe("describeUsage", () => {
  const gatewayUsage = {
    usedTokens: 860,
    limitTokens: 1000,
    percent: 86,
    source: "gateway" as const,
    simulated: false,
    capturedAt: "2026-10-09T12:00:00.000Z",
    measurementSource: "provider_reported" as const,
    cacheStatus: "miss" as const,
    measuredAt: "2026-10-09T11:59:58Z",
    model: "groq-llama"
  };

  it("flags a snapshot as simulated even if only the flag is set", () => {
    expect(describeUsage({ ...mockUsage, simulated: true })).toContain("SIMULATED (MOCK DATA)");
  });

  it("describes provider-reported gateway usage without calling it simulated", () => {
    const text = describeUsage(gatewayUsage);
    expect(text).toBe(
      "860 / 1,000 tokens (86.0%) — measured by the SUEZ gateway from provider-reported token counts; model groq-llama; measured at 2026-10-09T11:59:58Z."
    );
    expect(text).not.toContain("SIMULATED");
  });

  it("labels gateway estimates and cache hits explicitly", () => {
    const text = describeUsage({ ...gatewayUsage, measurementSource: "gateway_estimate", cacheStatus: "hit" });
    expect(text).toContain("a gateway ESTIMATE (stub model), not provider-reported");
    expect(text).toContain("latest request served from cache");
  });

  it("says unavailable when the checkpoint has no measurement", () => {
    expect(describeUsage(null)).toContain("unavailable");
    expect(buildContinuationPrompt(sample({ usage: null }))).toContain(
      "Context usage at checkpoint: unavailable — no trustworthy context measurement"
    );
  });
});

describe("createDraft", () => {
  it("carries long-lived fields forward from the previous checkpoint of the same project", () => {
    const previous = sample({ status: "final" });
    const draft = createDraft({
      id: "cp-2",
      now: new Date("2026-10-09T13:00:00.000Z"),
      projectName: "SUEZ",
      sessionId: "s-2",
      sessionLabel: "Session 2",
      trigger: "auto_threshold",
      thresholdPercent: 85,
      usage: mockUsage,
      previous
    });
    expect(draft.status).toBe("draft");
    expect(draft.goal).toBe(previous.goal);
    expect(draft.decisions).toEqual(previous.decisions);
    expect(draft.decisions).not.toBe(previous.decisions);
    expect(draft.nextSteps).toEqual(previous.nextSteps);
    expect(draft.currentState).toBe("");
    expect(draft.notes).toBe("");
  });

  it("does not carry fields across projects", () => {
    const draft = createDraft({
      id: "cp-3",
      now: new Date(),
      projectName: "Other",
      sessionId: "s",
      sessionLabel: "",
      trigger: "manual",
      thresholdPercent: null,
      usage: mockUsage,
      previous: sample()
    });
    expect(draft.goal).toBe("");
    expect(draft.decisions).toEqual([]);
  });
});

describe("linesToList / newestFor", () => {
  it("strips bullets and numbering and drops blank lines", () => {
    expect(linesToList("- one\n* two\n\n3. three\n  • four  \n5) five")).toEqual([
      "one",
      "two",
      "three",
      "four",
      "five"
    ]);
  });

  it("picks the newest checkpoint for a project", () => {
    const older = sample({ id: "a", createdAt: "2026-10-01T00:00:00.000Z" });
    const newer = sample({ id: "b", createdAt: "2026-10-02T00:00:00.000Z" });
    const other = sample({ id: "c", projectName: "Other", createdAt: "2026-10-03T00:00:00.000Z" });
    expect(newestFor([older, other, newer], "SUEZ")?.id).toBe("b");
    expect(newestFor([], "SUEZ")).toBeUndefined();
  });
});
