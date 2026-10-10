import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DEFAULT_GUARD_CONFIG, GuardState, evaluate, initialGuardState } from "../guardian/threshold";
import {
  GatewayContextUsage,
  GatewayResult,
  initialGatewayReading,
  interpretGatewayResult,
  percentForGuard,
  withStaleness
} from "./gatewayReading";
import { GatewayUsageSource } from "./gatewaySource";
import { UsageReading } from "./source";

const SESSION = "sess-1";
const now = new Date("2026-10-09T12:00:00.000Z");

function usage(overrides: Partial<GatewayContextUsage> = {}, measurement: Partial<NonNullable<GatewayContextUsage["measurement"]>> = {}): GatewayContextUsage {
  return {
    sessionId: SESSION,
    status: "ok",
    message: "Token counts were reported by the provider for the latest request.",
    measurement: {
      model: "groq-llama",
      provider: "groq",
      source: "provider_reported",
      cacheStatus: "miss",
      promptTokens: 800,
      completionTokens: 60,
      contextTokens: 860,
      traceId: "t1",
      measuredAt: "2026-10-09T11:59:58Z",
      ...measurement
    },
    contextWindow: 1000,
    percent: 86,
    retrievedAt: "2026-10-09T12:00:00Z",
    ...overrides
  };
}

const ok = (data: GatewayContextUsage): GatewayResult<GatewayContextUsage> => ({ kind: "ok", data });
const interpret = (result: GatewayResult<GatewayContextUsage>, previous = initialGatewayReading) =>
  interpretGatewayResult(result, previous, now, SESSION);

describe("interpretGatewayResult", () => {
  it("turns a complete ok response into a gateway snapshot", () => {
    const reading = interpret(ok(usage()));
    expect(reading.status).toBe("ok");
    expect(reading.snapshot).toMatchObject({
      usedTokens: 860,
      limitTokens: 1000,
      percent: 86,
      source: "gateway",
      simulated: false,
      measurementSource: "provider_reported",
      cacheStatus: "miss",
      model: "groq-llama",
      gatewaySessionId: SESSION,
      measuredAt: "2026-10-09T11:59:58Z"
    });
    expect(reading.lastSuccessAt).toBe(now.toISOString());
    expect(percentForGuard(reading)).toBe(86);
  });

  it("keeps the estimate label for the stub provider", () => {
    const reading = interpret(ok(usage({}, { source: "gateway_estimate", provider: "stub" })));
    expect(reading.snapshot?.measurementSource).toBe("gateway_estimate");
  });

  it.each([
    ["no_measurement", usage({ status: "no_measurement", measurement: null, percent: null, contextWindow: null })],
    ["unavailable", usage({ status: "unavailable", percent: null }, { source: "unavailable", contextTokens: null })],
    ["unknown_context_window", usage({ status: "unknown_context_window", percent: null, contextWindow: null })]
  ] as const)("gives no percentage for status %s", (status, data) => {
    const reading = interpret(ok(data));
    expect(reading.status).toBe(status);
    expect(reading.snapshot).toBeNull();
    expect(reading.stale).toBe(false);
    expect(percentForGuard(reading)).toBeNull();
  });

  it("rejects an ok response with missing or inconsistent numbers", () => {
    for (const bad of [
      usage({ percent: null }),
      usage({ contextWindow: 0 }),
      usage({}, { contextTokens: null }),
      usage({}, { source: "unavailable" }),
      usage({ percent: Number.NaN })
    ]) {
      const reading = interpret(ok(bad));
      expect(reading.status).toBe("invalid_response");
      expect(percentForGuard(reading)).toBeNull();
    }
  });

  it("rejects a response for another session", () => {
    const reading = interpret(ok(usage({ sessionId: "someone-else" })));
    expect(reading.status).toBe("invalid_response");
    expect(percentForGuard(reading)).toBeNull();
  });

  it.each([
    [{ kind: "auth_failed", message: "The gateway rejected the API key (401): invalid api key" }, "auth_failed"],
    [{ kind: "network_error", message: "Could not connect to the gateway" }, "network_error"],
    [{ kind: "not_configured", message: "No gateway API key is stored." }, "not_configured"],
    [{ kind: "gateway_error", status: 503, message: "apply migration 0006" }, "gateway_error"],
    [{ kind: "invalid_response", message: "bad json" }, "invalid_response"]
  ] as const)("failure %s is stale, keeps the last good value for display only", (failure, status) => {
    const good = interpret(ok(usage()));
    const reading = interpret(failure as GatewayResult<GatewayContextUsage>, good);
    expect(reading.status).toBe(status);
    expect(reading.stale).toBe(true);
    expect(reading.snapshot).toBeNull();
    expect(reading.lastGoodSnapshot?.percent).toBe(86);
    expect(percentForGuard(reading)).toBeNull();
    if (failure.kind === "gateway_error") expect(reading.message).toBe("Gateway error 503: apply migration 0006");
  });
});

describe("withStaleness", () => {
  it("marks a reading stale when the last success is too old", () => {
    const reading = interpret(ok(usage()));
    expect(withStaleness(reading, new Date(now.getTime() + 10_000), 15_000).stale).toBe(false);
    const old = withStaleness(reading, new Date(now.getTime() + 20_000), 15_000);
    expect(old.stale).toBe(true);
    expect(percentForGuard(old)).toBeNull();
    expect(old.message).toContain("20 s");
  });
});

describe("GatewayUsageSource", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("polls immediately and then on the interval, never producing mock data", async () => {
    const fetcher = vi.fn(async () => ok(usage()));
    const source = new GatewayUsageSource(fetcher, { sessionId: SESSION, intervalMs: 1000 });
    const seen: UsageReading[] = [];
    source.onReading((reading) => seen.push(reading));
    source.start();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(2000);
    source.stop();
    await vi.advanceTimersByTimeAsync(5000);
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(fetcher).toHaveBeenCalledWith(SESSION);
    expect(seen.every((reading) => reading.source === "gateway" && reading.display?.simulated === false)).toBe(true);
  });

  it("turns a thrown fetcher error into a network_error reading", async () => {
    const source = new GatewayUsageSource(async () => Promise.reject(new Error("ipc down")), { sessionId: SESSION, intervalMs: 1000 });
    const reading = await source.poll();
    expect(reading.status).toBe("network_error");
    expect(reading.message).toContain("ipc down");
    expect(source.current().guardPercent).toBeNull();
  });

  it("shares one in-flight request between concurrent polls", async () => {
    let resolve: (value: GatewayResult<GatewayContextUsage>) => void = () => undefined;
    const fetcher = vi.fn(() => new Promise<GatewayResult<GatewayContextUsage>>((r) => (resolve = r)));
    const source = new GatewayUsageSource(fetcher, { sessionId: SESSION, intervalMs: 1000 });
    const a = source.poll();
    const b = source.poll();
    resolve(ok(usage()));
    expect(await a).toBe(await b);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("feeds the threshold engine once at 85%, ignores repeats, failures and missing data, then fires critical", async () => {
    const responses: GatewayResult<GatewayContextUsage>[] = [
      ok(usage({ status: "no_measurement", measurement: null, percent: null, contextWindow: null })),
      ok(usage({ percent: 50 }, { contextTokens: 500 })),
      ok(usage({ percent: 86 }, { contextTokens: 860 })),
      ok(usage({ percent: 86 }, { contextTokens: 860, cacheStatus: "hit" })),
      { kind: "network_error", message: "down" },
      ok(usage({ status: "unknown_context_window", percent: null, contextWindow: null })),
      ok(usage({ percent: 88 }, { contextTokens: 880 })),
      ok(usage({ percent: 96 }, { contextTokens: 960 })),
      ok(usage({ percent: 97 }, { contextTokens: 970 }))
    ];
    let index = 0;
    const source = new GatewayUsageSource(async () => responses[index++], { sessionId: SESSION, intervalMs: 1000 });
    let guard: GuardState = initialGuardState();
    const events: string[][] = [];
    source.onReading((reading) => {
      if (reading.guardPercent === null) {
        events.push([]);
        return;
      }
      const result = evaluate(guard, reading.guardPercent, DEFAULT_GUARD_CONFIG);
      guard = result.state;
      events.push(result.events.map((event) => event.type));
    });
    for (let i = 0; i < responses.length; i++) await source.poll();
    expect(events).toEqual([[], [], ["checkpoint"], [], [], [], [], ["critical"], []]);
  });
});
