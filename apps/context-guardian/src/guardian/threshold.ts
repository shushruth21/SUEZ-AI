// Threshold guard: turns a stream of usage percentages into one-shot events.
//
// - "checkpoint" fires the first time usage reaches checkpointPercent.
// - "critical" fires the first time usage reaches criticalPercent.
// - Each event re-arms only after usage drops below (threshold - rearmMargin),
//   so jitter around a threshold cannot spam drafts. A new session resets both.
// - A single jump past both thresholds emits checkpoint then critical.

export type GuardConfig = {
  checkpointPercent: number;
  criticalPercent: number;
  rearmMargin: number;
};

export type GuardState = {
  checkpointArmed: boolean;
  criticalArmed: boolean;
  lastPercent: number | null;
};

export type GuardEventType = "checkpoint" | "critical";

export type GuardEvent = {
  type: GuardEventType;
  percent: number;
  threshold: number;
};

export type GuardLevel = "normal" | "checkpoint" | "critical";

export const DEFAULT_GUARD_CONFIG: GuardConfig = {
  checkpointPercent: 85,
  criticalPercent: 95,
  rearmMargin: 5
};

export function initialGuardState(): GuardState {
  return { checkpointArmed: true, criticalArmed: true, lastPercent: null };
}

// Guard state for a session that may already have an automatic checkpoint on
// disk (for example after an app restart with the same gateway session). The
// checkpoint trigger starts disarmed so the restart cannot create a duplicate
// draft; it re-arms normally once usage drops below the re-arm level.
export function guardStateForSession(alreadyCheckpointed: boolean): GuardState {
  return { ...initialGuardState(), checkpointArmed: !alreadyCheckpointed };
}

export function validateGuardConfig(config: GuardConfig): string[] {
  const errors: string[] = [];
  const inRange = (value: number) => Number.isFinite(value) && value > 0 && value <= 100;
  if (!inRange(config.checkpointPercent)) {
    errors.push("Checkpoint threshold must be between 1 and 100.");
  }
  if (!inRange(config.criticalPercent)) {
    errors.push("Critical threshold must be between 1 and 100.");
  }
  if (
    inRange(config.checkpointPercent) &&
    inRange(config.criticalPercent) &&
    config.criticalPercent <= config.checkpointPercent
  ) {
    errors.push("Critical threshold must be higher than the checkpoint threshold.");
  }
  if (
    !Number.isFinite(config.rearmMargin) ||
    config.rearmMargin < 0 ||
    config.rearmMargin >= config.checkpointPercent
  ) {
    errors.push("Re-arm margin must be zero or more and below the checkpoint threshold.");
  }
  return errors;
}

export function levelFor(percent: number, config: GuardConfig): GuardLevel {
  if (percent >= config.criticalPercent) return "critical";
  if (percent >= config.checkpointPercent) return "checkpoint";
  return "normal";
}

export function evaluate(
  state: GuardState,
  percent: number,
  config: GuardConfig
): { state: GuardState; events: GuardEvent[] } {
  if (!Number.isFinite(percent)) {
    return { state, events: [] };
  }

  const events: GuardEvent[] = [];
  let { checkpointArmed, criticalArmed } = state;

  if (!checkpointArmed && percent < config.checkpointPercent - config.rearmMargin) {
    checkpointArmed = true;
  }
  if (!criticalArmed && percent < config.criticalPercent - config.rearmMargin) {
    criticalArmed = true;
  }

  if (checkpointArmed && percent >= config.checkpointPercent) {
    events.push({ type: "checkpoint", percent, threshold: config.checkpointPercent });
    checkpointArmed = false;
  }
  if (criticalArmed && percent >= config.criticalPercent) {
    events.push({ type: "critical", percent, threshold: config.criticalPercent });
    criticalArmed = false;
  }

  return { state: { checkpointArmed, criticalArmed, lastPercent: percent }, events };
}
