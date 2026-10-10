import { FormEvent } from "react";

import { AuthMode, PortalTab } from "../types";

type LoginPanelProps = {
  activeTab: PortalTab;
  authMode: AuthMode;
  authToken: string;
  demoRequesterJwt: string;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onClear: () => void;
  onAuthModeChange: (mode: AuthMode) => void;
  onAuthTokenChange: (value: string) => void;
  onApplyDemoCredential: (mode: AuthMode, token: string) => void;
};

export function LoginPanel({
  activeTab,
  authMode,
  authToken,
  demoRequesterJwt,
  onSubmit,
  onClear,
  onAuthModeChange,
  onAuthTokenChange,
  onApplyDemoCredential
}: LoginPanelProps) {
  return (
    <section className="login-panel">
      <form className="login-form" onSubmit={onSubmit}>
        <div className="login-row">
          <button
            type="button"
            className="login-quick secondary"
            onClick={() =>
              activeTab === "admin"
                ? onApplyDemoCredential("apiKey", "suez-demo-admin-key")
                : onApplyDemoCredential("jwt", demoRequesterJwt)
            }
          >
            {activeTab === "admin" ? "Admin key" : "Requester JWT"}
          </button>
          <select
            value={authMode}
            onChange={(e) => onAuthModeChange(e.target.value as AuthMode)}
            className="login-select"
            aria-label="Auth mode"
          >
            <option value="apiKey">API key</option>
            <option value="jwt">JWT</option>
          </select>
          <input
            type="text"
            value={authToken}
            onChange={(e) => onAuthTokenChange(e.target.value)}
            placeholder="Credential"
            className="login-input"
            aria-label="Credential"
          />
          <button type="submit" className="login-submit">
            Sign in
          </button>
          <button type="button" className="secondary" onClick={onClear}>
            Clear
          </button>
        </div>
      </form>
    </section>
  );
}
