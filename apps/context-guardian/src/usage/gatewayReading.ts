import { MeasurementSource, UsageSnapshot } from "../types";

// Shapes returned by the Rust gateway commands (see src-tauri/src/gateway.rs).
export type GatewayMeasurement = {
  model: string;
  provider: string;
  source: "provider_reported" | "gateway_estimate" | "unavailable";
  cacheStatus: "miss" | "hit";
  promptTokens: number | null;
  completionTokens: number | null;
  contextTokens: number | null;
  traceId?: string | null;
  measuredAt: string;
};

export type GatewayContextUsage = {
  sessionId: string;
  status: "ok" | "no_measurement" | "unavailable" | "unknown_context_window";
  message: string;
  measurement: GatewayMeasurement | null;
  contextWindow: number | null;
  percent: number | null;
  // Present on gateways with tenant context policies.
  contextStatus?: ServerContextStatus | null;
  thresholds?: ServerThresholds | null;
  retrievedAt: string;
};

export type ServerContextStatus = "ok" | "notice" | "checkpoint" | "warning" | "limit";

// The tenant's thresholds for the model, set by an administrator on the
// server. When present they replace the app's local threshold settings.
export type ServerThresholds = {
  noticePercent: number;
  checkpointPercent: number;
  warningPercent: number;
  blockPercent: number;
  hardBlock: boolean;
};

export type GatewayResult<T> =
  | { kind: "ok"; data: T }
  | { kind: "not_configured"; message: string }
  | { kind: "auth_failed"; message: string }
  | { kind: "network_error"; message: string }
  | { kind: "gateway_error"; status: number; message: string }
  | { kind: "invalid_response"; message: string };

export type GatewayStatus =
  | "waiting"
  | "ok"
  | "no_measurement"
  | "unknown_context_window"
  | "unavailable"
  | "not_configured"
  | "auth_failed"
  | "network_error"
  | "gateway_error"
  | "invalid_response";

export type GatewayReading = {
  status: GatewayStatus;
  message: string;
  // Present only for status "ok": a trustworthy percentage the threshold engine may use.
  snapshot: UsageSnapshot | null;
  // Most recent "ok" snapshot, kept for display (marked stale) when the latest poll fails.
  lastGoodSnapshot: UsageSnapshot | null;
  measurement: GatewayMeasurement | null;
  contextWindow: number | null;
  serverStatus: ServerContextStatus | null;
  serverThresholds: ServerThresholds | null;
  checkedAt: string | null;
  lastSuccessAt: string | null;
  // True when the gateway could not be reached or answered with an error, or
  // when no successful poll happened within the stale window.
  stale: boolean;
};

export const initialGatewayReading: GatewayReading = {
  status: "waiting",
  message: "Waiting for the first gateway reading…",
  snapshot: null,
  lastGoodSnapshot: null,
  measurement: null,
  contextWindow: null,
  serverStatus: null,
  serverThresholds: null,
  checkedAt: null,
  lastSuccessAt: null,
  stale: false
};

const FAILURE_KINDS = new Set(["not_configured", "auth_failed", "network_error", "gateway_error", "invalid_response"]);

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

/**
 * Turns one gateway poll result into a reading. Only a complete, consistent
 * "ok" response produces a snapshot; everything else leaves `snapshot` null so
 * the threshold engine never sees an invented percentage.
 */
export function interpretGatewayResult(
  result: GatewayResult<GatewayContextUsage>,
  previous: GatewayReading,
  now: Date,
  sessionId: string
): GatewayReading {
  const checkedAt = now.toISOString();

  if (result.kind !== "ok") {
    const status = FAILURE_KINDS.has(result.kind) ? (result.kind as GatewayStatus) : "invalid_response";
    const message =
      result.kind === "gateway_error" ? `Gateway error ${result.status}: ${result.message}` : result.message;
    return {
      ...previous,
      status,
      message,
      snapshot: null,
      checkedAt,
      stale: true
    };
  }

  const usage = result.data;
  const base: GatewayReading = {
    ...previous,
    message: usage.message,
    snapshot: null,
    measurement: usage.measurement,
    contextWindow: usage.contextWindow,
    serverStatus: usage.status === "ok" ? (usage.contextStatus ?? null) : null,
    serverThresholds: usage.status === "ok" ? validThresholds(usage.thresholds) : null,
    checkedAt,
    lastSuccessAt: checkedAt,
    stale: false,
    status: usage.status
  };

  if (usage.sessionId !== sessionId) {
    return { ...base, status: "invalid_response", message: "Gateway answered for a different session.", stale: true };
  }

  if (usage.status !== "ok") {
    return base;
  }

  const m = usage.measurement;
  const consistent =
    m !== null &&
    (m.source === "provider_reported" || m.source === "gateway_estimate") &&
    isFiniteNumber(m.contextTokens) &&
    m.contextTokens >= 0 &&
    isFiniteNumber(usage.contextWindow) &&
    usage.contextWindow > 0 &&
    isFiniteNumber(usage.percent) &&
    usage.percent >= 0;
  if (!consistent || m === null) {
    return { ...base, status: "invalid_response", message: "Gateway reported ok without a complete measurement." };
  }

  const snapshot: UsageSnapshot = {
    usedTokens: m.contextTokens as number,
    limitTokens: usage.contextWindow as number,
    percent: usage.percent as number,
    source: "gateway",
    simulated: false,
    capturedAt: checkedAt,
    measurementSource: m.source as MeasurementSource,
    cacheStatus: m.cacheStatus,
    measuredAt: m.measuredAt,
    model: m.model,
    gatewaySessionId: usage.sessionId
  };
  return { ...base, snapshot, lastGoodSnapshot: snapshot };
}

function validThresholds(value: ServerThresholds | null | undefined): ServerThresholds | null {
  if (!value) return null;
  const { noticePercent: n, checkpointPercent: c, warningPercent: w, blockPercent: b } = value;
  const ordered = [n, c, w, b].every(isFiniteNumber) && n > 0 && n <= c && c <= w && w <= b && b <= 100;
  return ordered ? { ...value, hardBlock: Boolean(value.hardBlock) } : null;
}

/**
 * The thresholds the guard should use: the tenant's (from the server) when a
 * fresh reading carries them, otherwise the app's own settings. The server's
 * warning level is the app's "critical" level.
 */
export function effectiveThresholds<T extends { checkpointPercent: number; criticalPercent: number; rearmMargin: number }>(
  local: T,
  reading: GatewayReading | null
): T {
  const server = reading && !reading.stale ? reading.serverThresholds : null;
  if (!server || server.warningPercent <= server.checkpointPercent) return local;
  return {
    ...local,
    checkpointPercent: server.checkpointPercent,
    criticalPercent: server.warningPercent,
    rearmMargin: Math.min(local.rearmMargin, Math.max(1, server.checkpointPercent - 1))
  };
}

/** Marks a reading stale when no successful poll happened within `staleAfterMs`. */
export function withStaleness(reading: GatewayReading, now: Date, staleAfterMs: number): GatewayReading {
  if (reading.stale || reading.lastSuccessAt === null) return reading;
  const age = now.getTime() - new Date(reading.lastSuccessAt).getTime();
  if (age <= staleAfterMs) return reading;
  return { ...reading, stale: true, snapshot: null, message: `No successful gateway reading for ${Math.round(age / 1000)} s.` };
}

/** The only value the threshold engine may evaluate: a fresh, trustworthy percentage. */
export function percentForGuard(reading: GatewayReading): number | null {
  return reading.status === "ok" && !reading.stale && reading.snapshot ? reading.snapshot.percent : null;
}
