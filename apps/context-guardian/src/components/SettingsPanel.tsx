import { FormEvent, useState } from "react";

import { GatewayIdentity } from "../api";
import { validateGuardConfig } from "../guardian/threshold";
import { GuardianSettings, UsageSourceKind } from "../types";
import { GatewayResult } from "../usage/gatewayReading";

type Props = {
  settings: GuardianSettings;
  storageLocation: string;
  hasApiKey: boolean;
  onSave: (settings: GuardianSettings) => Promise<void>;
  onSetApiKey: (apiKey: string) => Promise<void>;
  onClearApiKey: () => Promise<void>;
  onTestConnection: (baseUrl: string) => Promise<GatewayResult<GatewayIdentity>>;
  onBack: () => void;
};

type Draft = {
  projectName: string;
  limitTokens: string;
  checkpointPercent: string;
  criticalPercent: string;
  rearmMargin: string;
  usageSource: UsageSourceKind;
  gatewayUrl: string;
  pollIntervalSeconds: string;
};

export function SettingsPanel(props: Props) {
  const { settings } = props;
  const [draft, setDraft] = useState<Draft>({
    projectName: settings.projectName,
    limitTokens: String(settings.limitTokens),
    checkpointPercent: String(settings.checkpointPercent),
    criticalPercent: String(settings.criticalPercent),
    rearmMargin: String(settings.rearmMargin),
    usageSource: settings.usageSource,
    gatewayUrl: settings.gatewayUrl,
    pollIntervalSeconds: String(settings.pollIntervalSeconds)
  });
  const [apiKey, setApiKey] = useState("");
  const [keyStatus, setKeyStatus] = useState("");
  const [connection, setConnection] = useState("");
  const [errors, setErrors] = useState<string[]>([]);
  const [status, setStatus] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const next: GuardianSettings = {
      ...settings,
      projectName: draft.projectName.trim(),
      limitTokens: Number(draft.limitTokens),
      checkpointPercent: Number(draft.checkpointPercent),
      criticalPercent: Number(draft.criticalPercent),
      rearmMargin: Number(draft.rearmMargin),
      usageSource: draft.usageSource,
      gatewayUrl: draft.gatewayUrl.trim(),
      pollIntervalSeconds: Number(draft.pollIntervalSeconds)
    };
    const problems = validateGuardConfig(next);
    if (!next.projectName) problems.push("Project name is required.");
    if (!Number.isFinite(next.limitTokens) || next.limitTokens < 1) {
      problems.push("Simulated context limit must be at least 1 token.");
    }
    if (!Number.isFinite(next.pollIntervalSeconds) || next.pollIntervalSeconds < 1 || next.pollIntervalSeconds > 300) {
      problems.push("Poll interval must be between 1 and 300 seconds.");
    }
    if (next.usageSource === "gateway" && !/^https?:\/\/\S+$/.test(next.gatewayUrl)) {
      problems.push("Gateway URL must start with http:// or https://.");
    }
    setErrors(problems);
    if (problems.length > 0) return;
    try {
      await props.onSave(next);
      setStatus("Settings saved");
    } catch (error) {
      setErrors([String(error)]);
    }
  }

  async function saveKey() {
    try {
      await props.onSetApiKey(apiKey);
      setApiKey("");
      setKeyStatus("API key saved to the macOS Keychain.");
    } catch (error) {
      setKeyStatus(`Could not save the key: ${String(error)}`);
    }
  }

  async function clearKey() {
    try {
      await props.onClearApiKey();
      setKeyStatus("API key removed from the Keychain.");
    } catch (error) {
      setKeyStatus(`Could not remove the key: ${String(error)}`);
    }
  }

  async function testConnection() {
    setConnection("Testing…");
    const result = await props.onTestConnection(draft.gatewayUrl.trim()).catch(
      (error) => ({ kind: "network_error", message: String(error) }) as const
    );
    if (result.kind === "ok") {
      setConnection(`Connected as ${result.data.actor} (tenant ${result.data.tenantSlug}).`);
    } else if (result.kind === "gateway_error") {
      setConnection(`Gateway error ${result.status}: ${result.message}`);
    } else {
      setConnection(result.message);
    }
  }

  const input = (key: Exclude<keyof Draft, "usageSource">, label: string, type = "number") => (
    <label className="field inline">
      <span>{label}</span>
      <input
        type={type}
        value={draft[key]}
        data-testid={`setting-${key}`}
        onChange={(event) => setDraft((previous) => ({ ...previous, [key]: event.target.value }))}
      />
    </label>
  );

  return (
    <form className="settings" onSubmit={submit} data-testid="settings">
      <strong>Settings</strong>

      <fieldset className="source-choice">
        <legend>Usage source</legend>
        <label>
          <input
            type="radio"
            name="usageSource"
            checked={draft.usageSource === "mock"}
            onChange={() => setDraft((previous) => ({ ...previous, usageSource: "mock" }))}
            data-testid="setting-usageSource-mock"
          />{" "}
          Mock (simulated data)
        </label>
        <label>
          <input
            type="radio"
            name="usageSource"
            checked={draft.usageSource === "gateway"}
            onChange={() => setDraft((previous) => ({ ...previous, usageSource: "gateway" }))}
            data-testid="setting-usageSource-gateway"
          />{" "}
          SUEZ gateway (measured)
        </label>
      </fieldset>

      {input("projectName", "Project name", "text")}
      {input("checkpointPercent", "Checkpoint threshold (%)")}
      {input("criticalPercent", "Critical warning (%)")}
      {input("rearmMargin", "Re-arm margin (%)")}
      {input("limitTokens", "Simulated context limit (mock only, tokens)")}

      <strong className="section-title">SUEZ gateway</strong>
      {input("gatewayUrl", "Gateway URL", "text")}
      {input("pollIntervalSeconds", "Poll interval (seconds)")}

      <label className="field inline">
        <span>API key</span>
        <input
          type="password"
          autoComplete="off"
          value={apiKey}
          placeholder={props.hasApiKey ? "Stored — type to replace" : "Paste API key"}
          data-testid="setting-apiKey"
          onChange={(event) => setApiKey(event.target.value)}
        />
      </label>
      <div className="editor-actions">
        <button type="button" onClick={saveKey} disabled={!apiKey.trim()} data-testid="save-api-key">
          Save key
        </button>
        <button type="button" onClick={clearKey} disabled={!props.hasApiKey} data-testid="clear-api-key">
          Remove key
        </button>
        <button type="button" onClick={testConnection} data-testid="test-connection">
          Test connection
        </button>
      </div>
      <p className="muted small" data-testid="api-key-state">
        {props.hasApiKey ? "A key is stored in the macOS Keychain." : "No key stored."} {keyStatus}
      </p>
      {connection ? (
        <p className="small" data-testid="connection-result">
          {connection}
        </p>
      ) : null}

      {errors.length > 0 ? (
        <ul className="warning small" data-testid="settings-errors">
          {errors.map((error) => (
            <li key={error}>{error}</li>
          ))}
        </ul>
      ) : null}
      {status ? <p className="muted small">{status}</p> : null}
      <p className="muted small">
        Checkpoints are stored locally in:
        <br />
        <code>{props.storageLocation}</code>
      </p>
      <div className="editor-actions">
        <button type="submit" className="primary" data-testid="save-settings">
          Save settings
        </button>
        <button type="button" onClick={props.onBack}>
          Back
        </button>
      </div>
    </form>
  );
}
