import { describe, expect, it } from "vitest";

import {
  DEFAULT_GUARD_CONFIG,
  GuardConfig,
  GuardEvent,
  GuardState,
  evaluate,
  initialGuardState,
  levelFor,
  validateGuardConfig
} from "./threshold";

function run(percents: number[], config: GuardConfig = DEFAULT_GUARD_CONFIG) {
  let state: GuardState = initialGuardState();
  const fired: GuardEvent[][] = [];
  for (const percent of percents) {
    const result = evaluate(state, percent, config);
    state = result.state;
    fired.push(result.events);
  }
  return { state, fired, types: fired.map((events) => events.map((event) => event.type)) };
}

describe("evaluate", () => {
  it("fires nothing below the checkpoint threshold", () => {
    expect(run([0, 10, 50, 84.9]).types).toEqual([[], [], [], []]);
  });

  it("fires checkpoint exactly at 85%", () => {
    const { fired } = run([84.99, 85]);
    expect(fired[1]).toEqual([{ type: "checkpoint", percent: 85, threshold: 85 }]);
  });

  it("fires checkpoint only once while usage stays high", () => {
    expect(run([86, 87, 90, 86, 89]).types).toEqual([["checkpoint"], [], [], [], []]);
  });

  it("does not re-fire when usage jitters around the threshold inside the margin", () => {
    expect(run([85, 84, 81, 85.5]).types).toEqual([["checkpoint"], [], [], []]);
  });

  it("re-arms after usage drops below threshold minus margin", () => {
    expect(run([85, 79.9, 85]).types).toEqual([["checkpoint"], [], ["checkpoint"]]);
  });

  it("fires critical at 95% after checkpoint", () => {
    expect(run([86, 94.9, 95, 99]).types).toEqual([["checkpoint"], [], ["critical"], []]);
  });

  it("fires checkpoint then critical on a single jump past both", () => {
    expect(run([50, 97]).types).toEqual([[], ["checkpoint", "critical"]]);
  });

  it("fires on the very first reading if it is already above threshold", () => {
    expect(run([90]).types).toEqual([["checkpoint"]]);
  });

  it("re-arms critical independently of checkpoint", () => {
    // 89 is below 95-5 so critical re-arms, but still above 80 so checkpoint stays disarmed.
    expect(run([96, 89, 96]).types).toEqual([["checkpoint", "critical"], [], ["critical"]]);
  });

  it("ignores non-finite readings without changing state", () => {
    const start = initialGuardState();
    const result = evaluate(start, Number.NaN, DEFAULT_GUARD_CONFIG);
    expect(result.events).toEqual([]);
    expect(result.state).toBe(start);
  });

  it("honours a custom threshold", () => {
    const config = { checkpointPercent: 70, criticalPercent: 90, rearmMargin: 0 };
    expect(run([69, 70, 69.9, 70], config).types).toEqual([[], ["checkpoint"], [], ["checkpoint"]]);
  });

  it("starts fresh for a new session", () => {
    const first = run([90]);
    expect(first.state.checkpointArmed).toBe(false);
    expect(evaluate(initialGuardState(), 90, DEFAULT_GUARD_CONFIG).events).toHaveLength(1);
  });
});

describe("levelFor", () => {
  it("maps percentages to levels", () => {
    expect(levelFor(0, DEFAULT_GUARD_CONFIG)).toBe("normal");
    expect(levelFor(84.9, DEFAULT_GUARD_CONFIG)).toBe("normal");
    expect(levelFor(85, DEFAULT_GUARD_CONFIG)).toBe("checkpoint");
    expect(levelFor(95, DEFAULT_GUARD_CONFIG)).toBe("critical");
  });
});

describe("validateGuardConfig", () => {
  it("accepts the defaults", () => {
    expect(validateGuardConfig(DEFAULT_GUARD_CONFIG)).toEqual([]);
  });

  it("rejects out-of-range thresholds", () => {
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, checkpointPercent: 0 })).not.toEqual([]);
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, criticalPercent: 101 })).not.toEqual([]);
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, checkpointPercent: Number.NaN })).not.toEqual([]);
  });

  it("requires critical above checkpoint", () => {
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, criticalPercent: 85 })).toContain(
      "Critical threshold must be higher than the checkpoint threshold."
    );
  });

  it("rejects a negative or oversized re-arm margin", () => {
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, rearmMargin: -1 })).not.toEqual([]);
    expect(validateGuardConfig({ ...DEFAULT_GUARD_CONFIG, rearmMargin: 85 })).not.toEqual([]);
  });
});
