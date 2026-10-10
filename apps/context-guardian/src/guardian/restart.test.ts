import { describe, expect, it } from "vitest";

import { Checkpoint } from "../types";
import { createDraft, hasAutoCheckpointForSession } from "./checkpoint";
import { DEFAULT_GUARD_CONFIG, evaluate, guardStateForSession } from "./threshold";

function draft(id: string, sessionId: string, trigger: "auto_threshold" | "manual"): Checkpoint {
  return createDraft({
    id,
    now: new Date("2026-10-10T12:00:00.000Z"),
    projectName: "P",
    sessionId,
    sessionLabel: "S",
    trigger,
    thresholdPercent: trigger === "auto_threshold" ? 85 : null,
    usage: null
  });
}

describe("restart duplicate prevention", () => {
  it("detects an existing automatic checkpoint for the same session only", () => {
    const onDisk = [draft("a", "gw-1", "auto_threshold"), draft("b", "gw-2", "manual")];
    expect(hasAutoCheckpointForSession(onDisk, "gw-1")).toBe(true);
    expect(hasAutoCheckpointForSession(onDisk, "gw-2")).toBe(false); // manual does not count
    expect(hasAutoCheckpointForSession(onDisk, "gw-3")).toBe(false);
    expect(hasAutoCheckpointForSession(onDisk, "")).toBe(false);
  });

  it("a restart above the threshold with an existing draft emits no second checkpoint", () => {
    // Session 1: crosses 85% and creates the draft.
    let first = guardStateForSession(false);
    const crossed = evaluate(first, 86, DEFAULT_GUARD_CONFIG);
    expect(crossed.events.map((e) => e.type)).toEqual(["checkpoint"]);
    first = crossed.state;

    // App restarts: guard rebuilt from disk, same gateway session still at 90%.
    const onDisk = [draft("auto-1", "gw-1", "auto_threshold")];
    let restarted = guardStateForSession(hasAutoCheckpointForSession(onDisk, "gw-1"));
    for (const percent of [90, 91, 90.5]) {
      const result = evaluate(restarted, percent, DEFAULT_GUARD_CONFIG);
      expect(result.events.filter((e) => e.type === "checkpoint")).toEqual([]);
      restarted = result.state;
    }
    // The critical warning is a warning, not a file: it still fires after restart.
    expect(evaluate(restarted, 96, DEFAULT_GUARD_CONFIG).events.map((e) => e.type)).toEqual(["critical"]);
  });

  it("re-arms normally after a restart once usage drops below the re-arm level", () => {
    let state = guardStateForSession(true);
    state = evaluate(state, 70, DEFAULT_GUARD_CONFIG).state;
    expect(evaluate(state, 86, DEFAULT_GUARD_CONFIG).events.map((e) => e.type)).toEqual(["checkpoint"]);
  });

  it("without an existing draft a restarted session still checkpoints", () => {
    const state = guardStateForSession(hasAutoCheckpointForSession([], "gw-9"));
    expect(evaluate(state, 88, DEFAULT_GUARD_CONFIG).events.map((e) => e.type)).toEqual(["checkpoint"]);
  });
});
