import { useEffect, useState } from "react";

import { formatTokens } from "../guardian/prompt";
import { GuardLevel, levelFor } from "../guardian/threshold";
import { GuardianSettings } from "../types";
import { GatewayReading } from "../usage/gatewayReading";
import { UsageReading } from "../usage/source";

type DisplayLevel = GuardLevel | "unknown";

type Props = {
  reading: UsageReading;
  settings: GuardianSettings;
  sessionLabel: string;
  sessionId: string;
  simulating: boolean;
  onSetPercent: (percent: number) => void;
  onStep: (percentDelta: number) => void;
  onToggleSimulation: () => void;
  onNewSession: () => void;
  onManualCheckpoint: () => void;
  onRefresh: () => void;
  onCopySessionHeader: () => void;
};

const levelLabel: Record<DisplayLevel, string> = {
  normal: "OK",
  checkpoint: "Checkpoint zone",
  critical: "Critical",
  unknown: "No data"
};

function ago(iso: string | null | undefined, now: number): string {
  if (!iso) return "never";
  const seconds = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return new Date(iso).toLocaleString();
}

function gatewayOrigin(reading: GatewayReading): string {
  const m = reading.measurement;
  if (!m) return "SUEZ gateway";
  const origin =
    m.source === "provider_reported"
      ? "provider-reported"
      : m.source === "gateway_estimate"
        ? "gateway estimate (stub model)"
        : "usage not reported";
  return `SUEZ gateway · ${m.model} · ${origin}${m.cacheStatus === "hit" ? " · cached response" : ""}`;
}

export function Monitor(props: Props) {
  const { reading, settings } = props;
  const isGateway = reading.source === "gateway";
  const gateway = reading.gateway;
  const display = reading.display;
  const level: DisplayLevel = reading.guardPercent === null ? "unknown" : levelFor(reading.guardPercent, settings);
  const percent = Math.min(100, display?.percent ?? 0);
  const staleDisplay = isGateway && reading.guardPercent === null && display !== null;

  // Re-render every second so "x s ago" stays current.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!isGateway) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [isGateway]);

  let tokensLine = "";
  if (display) {
    tokensLine = `${formatTokens(display.usedTokens)} / ${formatTokens(display.limitTokens)} tokens`;
  } else if (gateway?.measurement?.contextTokens != null) {
    tokensLine = `${formatTokens(gateway.measurement.contextTokens)} tokens · context window unknown`;
  }

  return (
    <section className="monitor" data-testid="monitor">
      <div className="meter-row">
        <div className={`meter level-${level}${staleDisplay ? " stale" : ""}`} aria-label="Context usage">
          <div className="meter-fill" style={{ width: `${percent}%` }} />
          <div className="meter-mark" style={{ left: `${settings.checkpointPercent}%` }} title="Checkpoint threshold" />
          <div className="meter-mark critical" style={{ left: `${settings.criticalPercent}%` }} title="Critical threshold" />
        </div>
        <span className={`level-chip level-${level}`} data-testid="level">
          {levelLabel[level]}
        </span>
      </div>

      <div className="usage-line">
        <strong data-testid="percent">
          {reading.guardPercent !== null ? `${reading.guardPercent.toFixed(1)}%` : staleDisplay ? `${display!.percent.toFixed(1)}%` : "—"}
        </strong>
        {staleDisplay ? (
          <span className="stale-tag" data-testid="stale-tag">
            last known, not current
          </span>
        ) : null}
        <span data-testid="tokens">{tokensLine}</span>
      </div>

      {isGateway && gateway ? (
        <>
          <p className="gateway-note" data-testid="gateway-note">
            {gatewayOrigin(gateway)}
          </p>
          <p className={`gateway-status status-${gateway.status}${gateway.stale ? " is-stale" : ""}`} data-testid="gateway-status" data-status={gateway.status}>
            {gateway.message}
          </p>
          <p className="muted small" data-testid="gateway-times">
            Measured {ago(gateway.measurement?.measuredAt, now)} · checked {ago(gateway.checkedAt, now)}
          </p>
          <p className="muted small session-row">
            Session <code data-testid="gateway-session">{props.sessionId}</code>
          </p>
          <div className="controls">
            <button type="button" onClick={props.onCopySessionHeader} data-testid="copy-session-header" title="Copy the X-SUEZ-Session-Id header line for your client">
              Copy header
            </button>
            <button type="button" onClick={props.onRefresh} data-testid="refresh">
              Refresh
            </button>
            <button type="button" onClick={props.onManualCheckpoint} data-testid="manual-checkpoint">
              Checkpoint now
            </button>
            <button type="button" onClick={props.onNewSession} data-testid="new-session" title="Start watching a new session id">
              New session
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="sim-note" data-testid="sim-note">
            Simulated usage — not real provider telemetry · {props.sessionLabel}
          </p>
          <input
            className="slider"
            type="range"
            min={0}
            max={100}
            step={0.5}
            value={percent}
            onChange={(event) => props.onSetPercent(Number(event.target.value))}
            aria-label="Set simulated usage"
          />
          <div className="controls">
            <button type="button" onClick={props.onToggleSimulation} data-testid="simulate">
              {props.simulating ? "Pause" : "Simulate"}
            </button>
            <button type="button" onClick={() => props.onStep(5)} data-testid="step">
              +5%
            </button>
            <button type="button" onClick={props.onManualCheckpoint} data-testid="manual-checkpoint">
              Checkpoint now
            </button>
            <button type="button" onClick={props.onNewSession} data-testid="new-session" title="Reset usage and start a new session">
              New session
            </button>
          </div>
        </>
      )}
    </section>
  );
}
