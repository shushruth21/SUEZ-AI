import { UsageSnapshot } from "../types";
import { UsageReading, UsageSource } from "./source";

// Simulated context usage. Every snapshot is tagged source "mock" and
// simulated true; nothing here reflects real provider telemetry.
export class MockUsageSource implements UsageSource {
  readonly kind = "mock" as const;
  private usedTokens: number;
  private limitTokens: number;
  private listeners = new Set<(snapshot: UsageSnapshot) => void>();

  constructor(limitTokens: number, usedTokens = 0, private readonly now = () => new Date()) {
    this.limitTokens = sanitizeLimit(limitTokens);
    this.usedTokens = clamp(usedTokens, 0, this.limitTokens);
  }

  snapshot(): UsageSnapshot {
    return {
      usedTokens: this.usedTokens,
      limitTokens: this.limitTokens,
      percent: (this.usedTokens / this.limitTokens) * 100,
      source: "mock",
      simulated: true,
      measurementSource: "simulated",
      capturedAt: this.now().toISOString()
    };
  }

  setUsed(tokens: number) {
    this.usedTokens = clamp(Math.round(tokens), 0, this.limitTokens);
    this.emit();
  }

  setPercent(percent: number) {
    this.setUsed((clamp(percent, 0, 100) / 100) * this.limitTokens);
  }

  step(tokens: number) {
    this.setUsed(this.usedTokens + tokens);
  }

  setLimit(limitTokens: number) {
    this.limitTokens = sanitizeLimit(limitTokens);
    this.usedTokens = clamp(this.usedTokens, 0, this.limitTokens);
    this.emit();
  }

  reset() {
    this.setUsed(0);
  }

  start() {}

  stop() {}

  current(): UsageReading {
    return toReading(this.snapshot());
  }

  onReading(listener: (reading: UsageReading) => void): () => void {
    return this.subscribe((snapshot) => listener(toReading(snapshot)));
  }

  subscribe(listener: (snapshot: UsageSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  private emit() {
    const snapshot = this.snapshot();
    this.listeners.forEach((listener) => listener(snapshot));
  }
}

function toReading(snapshot: UsageSnapshot): UsageReading {
  return { source: "mock", guardPercent: snapshot.percent, display: snapshot, gateway: null };
}

function sanitizeLimit(limitTokens: number) {
  return Number.isFinite(limitTokens) && limitTokens >= 1 ? Math.round(limitTokens) : 1;
}

function clamp(value: number, min: number, max: number) {
  if (!Number.isFinite(value)) return min;
  return Math.min(max, Math.max(min, value));
}
