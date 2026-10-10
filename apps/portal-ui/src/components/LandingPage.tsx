import { FormEvent } from "react";

import { AuthMode, PortalTab, RequesterUser } from "../types";

type LandingPageProps = {
  selectedRole: PortalTab;
  selectedRequesterUser: RequesterUser;
  authMode: AuthMode;
  authToken: string;
  demoRequesterJwt: string;
  statusMessage: string;
  onRoleChange: (role: PortalTab) => void;
  onRequesterUserChange: (user: RequesterUser) => void;
  onAuthModeChange: (mode: AuthMode) => void;
  onAuthTokenChange: (value: string) => void;
  onApplyDemoCredential: (mode: AuthMode, token: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
};

export function LandingPage({
  selectedRole,
  selectedRequesterUser,
  authMode,
  authToken,
  demoRequesterJwt,
  statusMessage,
  onRoleChange,
  onRequesterUserChange,
  onAuthModeChange,
  onAuthTokenChange,
  onApplyDemoCredential,
  onSubmit
}: LandingPageProps) {
  return (
    <div className="landing">
      <div className="landing-card">
        <h1 className="landing-title">Suez</h1>
        <p className="landing-tagline">Powered by AcroSystemsInc.ai</p>
        <p className="landing-desc">
          Governed model and tool access, policy lifecycle, and audit observability.
        </p>

        <form className="landing-form" onSubmit={onSubmit}>
          <label className="landing-label">
            Sign in as
            <select
              value={selectedRole}
              onChange={(e) => onRoleChange(e.target.value as PortalTab)}
              className="landing-select"
              aria-label="Sign in as"
            >
              <option value="admin">Admin</option>
              <option value="requester">Requester</option>
            </select>
          </label>

          {selectedRole === "requester" ? (
            <label className="landing-label">
              User
              <select
                value={selectedRequesterUser}
                onChange={(e) => onRequesterUserChange(e.target.value as RequesterUser)}
                className="landing-select"
                aria-label="Requester user"
              >
                <option value="user1">user1</option>
                <option value="user2">user2</option>
              </select>
            </label>
          ) : null}

          <label className="landing-label">
            Auth
            <select
              value={authMode}
              onChange={(e) => onAuthModeChange(e.target.value as AuthMode)}
              className="landing-select"
              aria-label="Auth mode"
            >
              <option value="apiKey">API key</option>
              <option value="jwt">JWT</option>
            </select>
          </label>

          <label className="landing-label">
            Credential
            <input
              type="text"
              value={authToken}
              onChange={(e) => onAuthTokenChange(e.target.value)}
              placeholder={
                selectedRole === "admin"
                  ? "API key"
                  : `API key or JWT for ${selectedRequesterUser}`
              }
              className="landing-input"
              aria-label="Credential"
            />
          </label>

          <div className="landing-actions">
            <button
              type="button"
              className="landing-demo secondary"
              onClick={() =>
                selectedRole === "admin"
                  ? onApplyDemoCredential("apiKey", "suez-demo-admin-key")
                  : onApplyDemoCredential("jwt", demoRequesterJwt)
              }
            >
              Use demo {selectedRole === "admin" ? "admin key" : `${selectedRequesterUser} JWT`}
            </button>
            <button type="submit" className="landing-submit">
              Sign in
            </button>
          </div>
          {statusMessage ? (
            <p className="landing-status" role="alert">
              {statusMessage}
            </p>
          ) : null}
        </form>
      </div>
    </div>
  );
}
