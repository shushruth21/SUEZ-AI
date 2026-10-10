import { describe, expect, it } from "vitest";

import { MockUsageSource } from "./mockSource";

const fixedNow = () => new Date("2026-10-09T12:00:00.000Z");

describe("MockUsageSource", () => {
  it("always labels snapshots as simulated mock data", () => {
    const snapshot = new MockUsageSource(200_000, 1000, fixedNow).snapshot();
    expect(snapshot.source).toBe("mock");
    expect(snapshot.simulated).toBe(true);
    expect(snapshot.capturedAt).toBe("2026-10-09T12:00:00.000Z");
  });

  it("computes percent from used and limit", () => {
    const source = new MockUsageSource(200_000);
    source.setUsed(170_000);
    expect(source.snapshot().percent).toBe(85);
    source.setPercent(95);
    expect(source.snapshot().usedTokens).toBe(190_000);
  });

  it("clamps usage to the range 0..limit", () => {
    const source = new MockUsageSource(1000);
    source.step(5000);
    expect(source.snapshot().usedTokens).toBe(1000);
    source.setUsed(-10);
    expect(source.snapshot().usedTokens).toBe(0);
    source.setUsed(Number.NaN);
    expect(source.snapshot().usedTokens).toBe(0);
  });

  it("re-clamps when the limit shrinks and rejects invalid limits", () => {
    const source = new MockUsageSource(1000, 900);
    source.setLimit(500);
    expect(source.snapshot()).toMatchObject({ usedTokens: 500, limitTokens: 500, percent: 100 });
    source.setLimit(0);
    expect(source.snapshot().limitTokens).toBe(1);
  });

  it("notifies subscribers and supports unsubscribe", () => {
    const source = new MockUsageSource(100);
    const seen: number[] = [];
    const unsubscribe = source.subscribe((snapshot) => seen.push(snapshot.usedTokens));
    source.step(10);
    source.step(10);
    unsubscribe();
    source.step(10);
    expect(seen).toEqual([10, 20]);
  });
});
