import {
  GatewayContextUsage,
  GatewayReading,
  GatewayResult,
  initialGatewayReading,
  interpretGatewayResult,
  percentForGuard,
  withStaleness
} from "./gatewayReading";
import { UsageReading, UsageSource } from "./source";

export type UsageFetcher = (sessionId: string) => Promise<GatewayResult<GatewayContextUsage>>;

export type GatewaySourceOptions = {
  sessionId: string;
  intervalMs: number;
  // Defaults to three poll intervals.
  staleAfterMs?: number;
  now?: () => Date;
};

// Polls the SUEZ gateway (through Rust) for the session's latest measurement.
// It never falls back to simulated data: failures surface as error readings.
export class GatewayUsageSource implements UsageSource {
  readonly kind = "gateway" as const;
  private reading: GatewayReading = initialGatewayReading;
  private listeners = new Set<(reading: UsageReading) => void>();
  private timer: ReturnType<typeof setInterval> | null = null;
  private inFlight: Promise<GatewayReading> | null = null;
  private readonly staleAfterMs: number;
  private readonly now: () => Date;

  constructor(
    private readonly fetcher: UsageFetcher,
    private readonly options: GatewaySourceOptions
  ) {
    this.staleAfterMs = options.staleAfterMs ?? options.intervalMs * 3;
    this.now = options.now ?? (() => new Date());
  }

  get sessionId() {
    return this.options.sessionId;
  }

  start() {
    if (this.timer !== null) return;
    void this.poll();
    this.timer = setInterval(() => void this.poll(), this.options.intervalMs);
  }

  stop() {
    if (this.timer !== null) clearInterval(this.timer);
    this.timer = null;
  }

  current(): UsageReading {
    return toReading(this.reading);
  }

  gatewayReading(): GatewayReading {
    return this.reading;
  }

  onReading(listener: (reading: UsageReading) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  /** One poll; concurrent calls share the in-flight request. */
  poll(): Promise<GatewayReading> {
    if (this.inFlight) return this.inFlight;
    this.inFlight = (async () => {
      let result: GatewayResult<GatewayContextUsage>;
      try {
        result = await this.fetcher(this.options.sessionId);
      } catch (error) {
        result = { kind: "network_error", message: `Gateway request failed: ${String(error)}` };
      }
      const next = withStaleness(
        interpretGatewayResult(result, this.reading, this.now(), this.options.sessionId),
        this.now(),
        this.staleAfterMs
      );
      this.reading = next;
      const reading = toReading(next);
      this.listeners.forEach((listener) => listener(reading));
      return next;
    })().finally(() => {
      this.inFlight = null;
    });
    return this.inFlight;
  }
}

function toReading(reading: GatewayReading): UsageReading {
  return {
    source: "gateway",
    guardPercent: percentForGuard(reading),
    display: reading.snapshot ?? reading.lastGoodSnapshot,
    gateway: reading
  };
}
