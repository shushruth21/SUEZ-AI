import { UsageSnapshot, UsageSourceKind } from "../types";
import { GatewayReading } from "./gatewayReading";

export type UsageReading = {
  source: UsageSourceKind;
  // The only value the threshold engine evaluates; null when there is no
  // trustworthy current percentage.
  guardPercent: number | null;
  // What the monitor shows (may be the last good gateway snapshot, flagged stale).
  display: UsageSnapshot | null;
  gateway: GatewayReading | null;
};

export interface UsageSource {
  readonly kind: UsageSourceKind;
  start(): void;
  stop(): void;
  current(): UsageReading;
  onReading(listener: (reading: UsageReading) => void): () => void;
}
