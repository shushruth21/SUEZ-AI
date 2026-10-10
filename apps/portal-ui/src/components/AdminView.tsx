import { FormEvent, useEffect, useMemo, useState } from "react";

import {
  AccessGrant,
  AccessRequest,
  ApiKeySummary,
  AuditEvent,
  AuditFilters,
  FetchState,
  IdentityContext,
  ModelRecord,
  PolicyRecord,
  PolicyTestCase,
  PolicyTestRun,
  Tenant,
  ToolRecord
} from "../types";

type AdminViewProps = {
  states: FetchState[];
  identity: IdentityContext | null;
  isAdmin: boolean;
  pendingRequests: AccessRequest[];
  accessGrants: AccessGrant[];
  tenants: Tenant[];
  apiKeys: ApiKeySummary[];
  models: ModelRecord[];
  tools: ToolRecord[];
  policies: PolicyRecord[];
  policyVersions: PolicyRecord[];
  policyTestCases: PolicyTestCase[];
  policyTestRuns: PolicyTestRun[];
  auditEvents: AuditEvent[];
  traceEvents: AuditEvent[];
  selectedTraceId: string;
  auditFilters: AuditFilters;
  generatedKey: string;
  decisionReason: string;
  apiKeyLabel: string;
  tenantForm: { name: string; slug: string; environment: string };
  modelForm: {
    externalName: string;
    provider: string;
    modelName: string;
    upstreamModel: string;
    upstreamBaseUrl: string;
    maxRpm: string;
    maxTpm: string;
    enabled: boolean;
  };
  toolForm: {
    name: string;
    description: string;
    executionMode: string;
    targetUrl: string;
    timeoutMs: string;
    inputSchema: string;
    outputSchema: string;
    riskTier: string;
    enabled: boolean;
  };
  policyForm: {
    name: string;
    scope: string;
    targetName: string;
    naturalLanguageRule: string;
    regoPackage: string;
    regoSource: string;
    status: string;
    enabled: boolean;
  };
  policyTestForm: {
    name: string;
    description: string;
    action: string;
    identity: string;
    resource: string;
    request: string;
    expectedAllow: boolean;
    expectedReasons: string;
  };
  selectedPolicyId: string;
  gatewayBaseUrl: string;
  authHeaders: Record<string, string>;
  accessRequestsCount: number;
  onDecisionReasonChange: (value: string) => void;
  onApiKeyLabelChange: (value: string) => void;
  onTenantFieldChange: (field: "name" | "slug" | "environment", value: string) => void;
  onModelFieldChange: (field: string, value: string | boolean) => void;
  onToolFieldChange: (field: string, value: string | boolean) => void;
  onPolicyFieldChange: (field: string, value: string | boolean) => void;
  onPolicyTestFieldChange: (field: string, value: string | boolean) => void;
  onAuditFilterChange: (field: keyof AuditFilters, value: string) => void;
  onSelectPolicy: (policy: PolicyRecord) => void;
  onOpenTrace: (traceId: string) => void | Promise<void>;
  onClearTrace: () => void;
  onCreateTenant: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onCreateApiKey: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onCreateModel: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onCreateTool: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onCreatePolicy: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onCreatePolicyTest: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onApplyAuditFilters: (event: FormEvent<HTMLFormElement>) => void | Promise<void>;
  onResetAuditFilters: () => void | Promise<void>;
  onGeneratePolicy: () => void | Promise<void>;
  onValidatePolicy: (policyId: string) => void | Promise<void>;
  onRunPolicyTests: (policyId: string) => void | Promise<void>;
  onPublishPolicy: (policyId: string) => void | Promise<void>;
  onRollbackPolicy: (policyId: string) => void | Promise<void>;
  onDecision: (requestId: string, action: "approve" | "deny") => void | Promise<void>;
};

function prettyJson(value: unknown) {
  return JSON.stringify(value, null, 2);
}

export function AdminView(props: AdminViewProps) {
  const {
    states,
    identity,
    isAdmin,
    pendingRequests,
    accessGrants,
    tenants,
    apiKeys,
    models,
    tools,
    policies,
    policyVersions,
    policyTestCases,
    policyTestRuns,
    auditEvents,
    traceEvents,
    selectedTraceId,
    auditFilters,
    generatedKey,
    decisionReason,
    apiKeyLabel,
    tenantForm,
    modelForm,
    toolForm,
    policyForm,
    policyTestForm,
    selectedPolicyId,
    gatewayBaseUrl,
    authHeaders,
    accessRequestsCount,
    onDecisionReasonChange,
    onApiKeyLabelChange,
    onTenantFieldChange,
    onModelFieldChange,
    onToolFieldChange,
    onPolicyFieldChange,
    onPolicyTestFieldChange,
    onAuditFilterChange,
    onSelectPolicy,
    onOpenTrace,
    onClearTrace,
    onCreateTenant,
    onCreateApiKey,
    onCreateModel,
    onCreateTool,
    onCreatePolicy,
    onCreatePolicyTest,
    onApplyAuditFilters,
    onResetAuditFilters,
    onGeneratePolicy,
    onValidatePolicy,
    onRunPolicyTests,
    onPublishPolicy,
    onRollbackPolicy,
    onDecision
  } = props;

  const [activeTab, setActiveTab] = useState<
    "overview" | "access" | "catalog" | "governance" | "setup" | "observability"
  >("overview");

  const [observability, setObservability] = useState<{
    cache_entries: number;
    cache_hits: number;
    chat_requests: number;
  } | null>(null);

  useEffect(() => {
    if (activeTab !== "observability" || !gatewayBaseUrl) return;
    const url = `${gatewayBaseUrl.replace(/\/$/, "")}/v1/observability`;
    fetch(url, { headers: authHeaders })
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(res.statusText))))
      .then((data: { cache_entries?: number; cache_hits?: number; chat_requests?: number }) =>
        setObservability({
          cache_entries: Number(data.cache_entries) || 0,
          cache_hits: Number(data.cache_hits) || 0,
          chat_requests: Number(data.chat_requests) || 0
        })
      )
      .catch(() => setObservability(null));
  }, [activeTab, gatewayBaseUrl, authHeaders]);

  const selectedPolicy = useMemo(
    () => policies.find((policy) => policy.id === selectedPolicyId) ?? null,
    [policies, selectedPolicyId]
  );

  return (
    <section className="panel panel-modern">
      <div className="section-tabs" aria-label="Admin sections">
        <button
          type="button"
          className={activeTab === "overview" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("overview")}
        >
          Overview
        </button>
        <button
          type="button"
          className={activeTab === "access" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("access")}
        >
          Access
        </button>
        <button
          type="button"
          className={activeTab === "catalog" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("catalog")}
        >
          Catalog
        </button>
        <button
          type="button"
          className={activeTab === "governance" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("governance")}
        >
          Governance
        </button>
        <button
          type="button"
          className={activeTab === "setup" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("setup")}
        >
          Setup
        </button>
        <button
          type="button"
          className={activeTab === "observability" ? "section-tab active" : "section-tab"}
          onClick={() => setActiveTab("observability")}
        >
          Observability
        </button>
      </div>

      <div className="tab-content">
        {activeTab === "overview" ? (
          <>
            <section className="subpanel">
              <h2>Local Service Checks</h2>
              <div className="grid">
                {states.map((state) => (
                  <article key={state.label} className="card">
                    <div className="card-header">
                      <strong>{state.label}</strong>
                      <span className={state.ok ? "badge ok" : "badge error"}>
                        {state.ok ? "OK" : "ERROR"}
                      </span>
                    </div>
                    <code>{state.url}</code>
                    <p>{state.detail}</p>
                  </article>
                ))}
              </div>
            </section>

            <section className="subpanel">
              <h2>Identity Context</h2>
              {identity ? (
                <pre className="code-block">{JSON.stringify(identity, null, 2)}</pre>
              ) : null}
            </section>
          </>
        ) : null}

        {activeTab === "access" ? (
          <>
            <section className="subpanel">
              <h2>Approval Queue</h2>
              {isAdmin ? (
                <div className="split">
                  <form className="form" onSubmit={(event) => event.preventDefault()}>
                    <label>
                      Decision note
                      <textarea
                        value={decisionReason}
                        onChange={(event) => onDecisionReasonChange(event.target.value)}
                      />
                    </label>
                  </form>
                  <div className="stack">
                    {pendingRequests.length === 0 ? null : (
                      pendingRequests.map((request) => (
                        <article key={request.id} className="queue-card">
                          <div className="card-header">
                            <strong>{`${request.resourceType}:${request.resourceName}`}</strong>
                            <span className="badge">{request.status.toUpperCase()}</span>
                          </div>
                          <p>{request.justification}</p>
                          <div className="actions">
                            <button
                              type="button"
                              onClick={() => onDecision(request.id, "approve")}
                            >
                              Approve
                            </button>
                            <button
                              type="button"
                              className="secondary"
                              onClick={() => onDecision(request.id, "deny")}
                            >
                              Deny
                            </button>
                          </div>
                        </article>
                      ))
                    )}
                  </div>
                </div>
              ) : null}
            </section>

            <section className="subpanel">
              <h2>Grants View</h2>
              <div className="table-shell">
                <table>
                  <thead>
                    <tr>
                      <th>Subject</th>
                      <th>Resource</th>
                      <th>Granted by</th>
                      <th>Status</th>
                      <th>Created</th>
                    </tr>
                  </thead>
                  <tbody>
                    {accessGrants.map((grant) => (
                      <tr key={grant.id}>
                        <td>{grant.subject}</td>
                        <td>{`${grant.resourceType}:${grant.resourceName}`}</td>
                        <td>{grant.grantedBy}</td>
                        <td>{grant.status}</td>
                        <td>{new Date(grant.createdAt).toLocaleString()}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          </>
        ) : null}

        {activeTab === "catalog" ? (
          <>
            <section className="subpanel">
              <h2>Model Registry</h2>
              <div className="split">
                <form className="form" onSubmit={onCreateModel}>
                  <label>
                    External name
                    <input
                      value={modelForm.externalName}
                      onChange={(event) => onModelFieldChange("externalName", event.target.value)}
                    />
                  </label>
                  <label>
                    Provider
                    <select
                      value={modelForm.provider}
                      onChange={(event) => onModelFieldChange("provider", event.target.value)}
                    >
                      <option value="stub">stub</option>
                      <option value="openai-compatible">openai-compatible</option>
                      <option value="groq">groq</option>
                    </select>
                  </label>
                  <label>
                    Stored model name
                    <input
                      value={modelForm.modelName}
                      onChange={(event) => onModelFieldChange("modelName", event.target.value)}
                    />
                  </label>
                  <label>
                    Upstream model
                    <input
                      value={modelForm.upstreamModel}
                      onChange={(event) => onModelFieldChange("upstreamModel", event.target.value)}
                    />
                  </label>
                  <label>
                    Upstream base URL override
                    <input
                      value={modelForm.upstreamBaseUrl}
                      onChange={(event) =>
                        onModelFieldChange("upstreamBaseUrl", event.target.value)
                      }
                    />
                  </label>
                  <label>
                    Max RPM
                    <input
                      value={modelForm.maxRpm}
                      onChange={(event) => onModelFieldChange("maxRpm", event.target.value)}
                    />
                  </label>
                  <label>
                    Max TPM
                    <input
                      value={modelForm.maxTpm}
                      onChange={(event) => onModelFieldChange("maxTpm", event.target.value)}
                    />
                  </label>
                  <label className="checkbox-row">
                    <input
                      type="checkbox"
                      checked={modelForm.enabled}
                      onChange={(event) => onModelFieldChange("enabled", event.target.checked)}
                    />
                    Enabled
                  </label>
                  <button type="submit" disabled={!identity || !isAdmin}>
                    Create model
                  </button>
                </form>
                <div className="table-shell">
                  <table>
                    <thead>
                      <tr>
                        <th>External name</th>
                        <th>Provider</th>
                        <th>Model</th>
                        <th>Limits</th>
                        <th>Enabled</th>
                      </tr>
                    </thead>
                    <tbody>
                      {models.map((model) => (
                        <tr key={model.id}>
                          <td>{model.externalName}</td>
                          <td>{model.provider}</td>
                          <td>{model.modelName}</td>
                          <td>{`${model.maxRpm} rpm / ${model.maxTpm} tpm`}</td>
                          <td>{model.enabled ? "yes" : "no"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>

            <section className="subpanel">
              <h2>Tool Registry</h2>
              <div className="split">
                <form className="form" onSubmit={onCreateTool}>
                  <label>
                    Tool name
                    <input
                      value={toolForm.name}
                      onChange={(event) => onToolFieldChange("name", event.target.value)}
                    />
                  </label>
                  <label>
                    Execution mode
                    <select
                      value={toolForm.executionMode}
                      onChange={(event) => onToolFieldChange("executionMode", event.target.value)}
                    >
                      <option value="stub">stub</option>
                      <option value="http">http</option>
                    </select>
                  </label>
                  <label>
                    Description
                    <textarea
                      value={toolForm.description}
                      onChange={(event) => onToolFieldChange("description", event.target.value)}
                    />
                  </label>
                  <label>
                    Target URL
                    <input
                      value={toolForm.targetUrl}
                      onChange={(event) => onToolFieldChange("targetUrl", event.target.value)}
                    />
                  </label>
                  <label>
                    Timeout ms
                    <input
                      value={toolForm.timeoutMs}
                      onChange={(event) => onToolFieldChange("timeoutMs", event.target.value)}
                    />
                  </label>
                  <label>
                    Risk tier
                    <select
                      value={toolForm.riskTier}
                      onChange={(event) => onToolFieldChange("riskTier", event.target.value)}
                    >
                      <option value="standard">standard</option>
                      <option value="high">high</option>
                    </select>
                  </label>
                  <label>
                    Input schema
                    <textarea
                      value={toolForm.inputSchema}
                      onChange={(event) => onToolFieldChange("inputSchema", event.target.value)}
                    />
                  </label>
                  <label>
                    Output schema
                    <textarea
                      value={toolForm.outputSchema}
                      onChange={(event) => onToolFieldChange("outputSchema", event.target.value)}
                    />
                  </label>
                  <label className="checkbox-row">
                    <input
                      type="checkbox"
                      checked={toolForm.enabled}
                      onChange={(event) => onToolFieldChange("enabled", event.target.checked)}
                    />
                    Enabled
                  </label>
                  <button type="submit" disabled={!identity || !isAdmin}>
                    Create tool
                  </button>
                </form>
                <div className="table-shell">
                  <table>
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Mode</th>
                        <th>Risk</th>
                        <th>Enabled</th>
                      </tr>
                    </thead>
                    <tbody>
                      {tools.map((tool) => (
                        <tr key={tool.id}>
                          <td>{tool.name}</td>
                          <td>{tool.executionMode}</td>
                          <td>{tool.riskTier}</td>
                          <td>{tool.enabled ? "yes" : "no"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>
          </>
        ) : null}

        {activeTab === "governance" ? (
          <>
            <section className="subpanel">
              <h2>Policy Registry</h2>
              <div className="split">
                <form className="form" onSubmit={onCreatePolicy}>
                  <label>
                    Name
                    <input
                      value={policyForm.name}
                      onChange={(event) => onPolicyFieldChange("name", event.target.value)}
                    />
                  </label>
                  <label>
                    Scope
                    <select
                      value={policyForm.scope}
                      onChange={(event) => onPolicyFieldChange("scope", event.target.value)}
                    >
                      <option value="runtime">runtime</option>
                      <option value="model">model</option>
                      <option value="tool">tool</option>
                    </select>
                  </label>
                  <label>
                    Target name
                    <input
                      value={policyForm.targetName}
                      onChange={(event) => onPolicyFieldChange("targetName", event.target.value)}
                    />
                  </label>
                  <label>
                    Natural language rule
                    <textarea
                      value={policyForm.naturalLanguageRule}
                      onChange={(event) =>
                        onPolicyFieldChange("naturalLanguageRule", event.target.value)
                      }
                    />
                  </label>
                  <label>
                    Rego package
                    <input
                      value={policyForm.regoPackage}
                      onChange={(event) => onPolicyFieldChange("regoPackage", event.target.value)}
                    />
                  </label>
                  <label>
                    Rego source
                    <textarea
                      value={policyForm.regoSource}
                      onChange={(event) => onPolicyFieldChange("regoSource", event.target.value)}
                    />
                  </label>
                  <div className="generated">
                    <strong>Selected policy</strong>
                    <code>{selectedPolicyId || "New unsaved draft"}</code>
                    {selectedPolicy ? (
                      <>
                        <span>
                          Status: <strong>{selectedPolicy.status}</strong> | Version:{" "}
                          <strong>{selectedPolicy.version}</strong>
                        </span>
                        <span>
                          Validation:{" "}
                          {selectedPolicy.validationErrors.length > 0
                            ? selectedPolicy.validationErrors.join(" | ")
                            : "clean"}
                        </span>
                      </>
                    ) : null}
                  </div>
                  <label className="checkbox-row">
                    <input
                      type="checkbox"
                      checked={policyForm.enabled}
                      onChange={(event) => onPolicyFieldChange("enabled", event.target.checked)}
                    />
                    Enabled
                  </label>
                  <div className="actions">
                    <button type="submit" disabled={!identity || !isAdmin}>
                      Save draft
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      disabled={!identity || !isAdmin}
                      onClick={() => void onGeneratePolicy()}
                    >
                      Generate draft
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      disabled={!selectedPolicyId}
                      onClick={() => void onValidatePolicy(selectedPolicyId)}
                    >
                      Validate
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      disabled={!selectedPolicyId}
                      onClick={() => void onRunPolicyTests(selectedPolicyId)}
                    >
                      Run tests
                    </button>
                    <button
                      type="button"
                      disabled={!selectedPolicyId}
                      onClick={() => void onPublishPolicy(selectedPolicyId)}
                    >
                      Publish
                    </button>
                  </div>
                </form>
                <div className="table-shell">
                  <table>
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Scope</th>
                        <th>Status</th>
                        <th>Version</th>
                        <th>Generator</th>
                        <th>Published</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {policies.map((policy) => (
                        <tr key={policy.id}>
                          <td>{policy.name}</td>
                          <td>{policy.scope}</td>
                          <td>{policy.status}</td>
                          <td>{policy.version}</td>
                          <td>{policy.generatorModel || "-"}</td>
                          <td>
                            {policy.publishedAt
                              ? new Date(policy.publishedAt).toLocaleString()
                              : "-"}
                          </td>
                          <td>
                            <div className="actions">
                              <button type="button" onClick={() => onSelectPolicy(policy)}>
                                Edit
                              </button>
                              <button
                                type="button"
                                className="secondary"
                                onClick={() => void onValidatePolicy(policy.id)}
                              >
                                Validate
                              </button>
                              <button
                                type="button"
                                className="secondary"
                                onClick={() => void onRunPolicyTests(policy.id)}
                              >
                                Test
                              </button>
                              <button
                                type="button"
                                onClick={() => void onPublishPolicy(policy.id)}
                              >
                                Publish
                              </button>
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>

            <section className="subpanel">
              <h2>Policy Tests</h2>
              {selectedPolicy ? (
                <div className="split">
                  <form className="form" onSubmit={onCreatePolicyTest}>
                    <label>
                      Test name
                      <input
                        value={policyTestForm.name}
                        onChange={(event) =>
                          onPolicyTestFieldChange("name", event.target.value)
                        }
                      />
                    </label>
                    <label>
                      Description
                      <textarea
                        value={policyTestForm.description}
                        onChange={(event) =>
                          onPolicyTestFieldChange("description", event.target.value)
                        }
                      />
                    </label>
                    <label>
                      Action
                      <input
                        value={policyTestForm.action}
                        onChange={(event) =>
                          onPolicyTestFieldChange("action", event.target.value)
                        }
                      />
                    </label>
                    <label>
                      Identity payload
                      <textarea
                        value={policyTestForm.identity}
                        onChange={(event) =>
                          onPolicyTestFieldChange("identity", event.target.value)
                        }
                      />
                    </label>
                    <label>
                      Resource payload
                      <textarea
                        value={policyTestForm.resource}
                        onChange={(event) =>
                          onPolicyTestFieldChange("resource", event.target.value)
                        }
                      />
                    </label>
                    <label>
                      Request payload
                      <textarea
                        value={policyTestForm.request}
                        onChange={(event) =>
                          onPolicyTestFieldChange("request", event.target.value)
                        }
                      />
                    </label>
                    <label className="checkbox-row">
                      <input
                        type="checkbox"
                        checked={policyTestForm.expectedAllow}
                        onChange={(event) =>
                          onPolicyTestFieldChange("expectedAllow", event.target.checked)
                        }
                      />
                      Expected allow
                    </label>
                    <label>
                      Expected reasons
                      <input
                        value={policyTestForm.expectedReasons}
                        onChange={(event) =>
                          onPolicyTestFieldChange("expectedReasons", event.target.value)
                        }
                      />
                    </label>
                    <div className="actions">
                      <button type="submit" disabled={!selectedPolicyId}>
                        Add test case
                      </button>
                      <button
                        type="button"
                        className="secondary"
                        disabled={!selectedPolicyId}
                        onClick={() => void onRunPolicyTests(selectedPolicyId)}
                      >
                        Run selected tests
                      </button>
                    </div>
                  </form>
                  <div className="stack">
                    <div className="table-shell">
                      <table>
                        <thead>
                          <tr>
                            <th>Name</th>
                            <th>Action</th>
                            <th>Expected</th>
                            <th>Reasons</th>
                          </tr>
                        </thead>
                        <tbody>
                          {policyTestCases.map((testCase) => (
                            <tr key={testCase.id}>
                              <td>{testCase.name}</td>
                              <td>{testCase.action}</td>
                              <td>{testCase.expectedAllow ? "allow" : "deny"}</td>
                              <td>
                                {testCase.expectedReasons.length > 0
                                  ? testCase.expectedReasons.join(", ")
                                  : "-"}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                    <div className="table-shell">
                      <table>
                        <thead>
                          <tr>
                            <th>Run</th>
                            <th>Status</th>
                            <th>Pass</th>
                            <th>Fail</th>
                            <th>When</th>
                          </tr>
                        </thead>
                        <tbody>
                          {policyTestRuns.map((run) => (
                            <tr key={run.id}>
                              <td>{run.id.slice(0, 8)}</td>
                              <td>{run.status}</td>
                              <td>{run.passedCount}</td>
                              <td>{run.failedCount}</td>
                              <td>{new Date(run.createdAt).toLocaleString()}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </div>
                </div>
              ) : null}
            </section>

            <section className="subpanel">
              <h2>Version History</h2>
              {selectedPolicy ? (
                <div className="table-shell">
                  <table>
                    <thead>
                      <tr>
                        <th>Version</th>
                        <th>Status</th>
                        <th>Published</th>
                        <th>Compiled</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {policyVersions.map((policy) => (
                        <tr key={policy.id}>
                          <td>{policy.version}</td>
                          <td>{policy.status}</td>
                          <td>
                            {policy.publishedAt
                              ? new Date(policy.publishedAt).toLocaleString()
                              : "-"}
                          </td>
                          <td>
                            {policy.lastCompiledAt
                              ? new Date(policy.lastCompiledAt).toLocaleString()
                              : "-"}
                          </td>
                          <td>
                            <div className="actions">
                              <button type="button" onClick={() => onSelectPolicy(policy)}>
                                Inspect
                              </button>
                              <button
                                type="button"
                                className="secondary"
                                onClick={() => void onRollbackPolicy(policy.id)}
                              >
                                Roll back
                              </button>
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : null}
            </section>

            <section className="subpanel">
              <h2>Audit View</h2>
              <form className="form filter-grid" onSubmit={onApplyAuditFilters}>
                <label>
                  Actor
                  <input
                    value={auditFilters.actor}
                    onChange={(event) => onAuditFilterChange("actor", event.target.value)}
                  />
                </label>
                <label>
                  Action
                  <input
                    value={auditFilters.action}
                    onChange={(event) => onAuditFilterChange("action", event.target.value)}
                  />
                </label>
                <label>
                  Resource type
                  <input
                    value={auditFilters.resourceType}
                    onChange={(event) =>
                      onAuditFilterChange("resourceType", event.target.value)
                    }
                  />
                </label>
                <label>
                  Resource name
                  <input
                    value={auditFilters.resourceName}
                    onChange={(event) =>
                      onAuditFilterChange("resourceName", event.target.value)
                    }
                  />
                </label>
                <label>
                  Decision
                  <input
                    value={auditFilters.decision}
                    onChange={(event) => onAuditFilterChange("decision", event.target.value)}
                  />
                </label>
                <label>
                  Trace ID
                  <input
                    value={auditFilters.traceId}
                    onChange={(event) => onAuditFilterChange("traceId", event.target.value)}
                  />
                </label>
                <label>
                  Since
                  <input
                    type="datetime-local"
                    value={auditFilters.since}
                    onChange={(event) => onAuditFilterChange("since", event.target.value)}
                  />
                </label>
                <label>
                  Until
                  <input
                    type="datetime-local"
                    value={auditFilters.until}
                    onChange={(event) => onAuditFilterChange("until", event.target.value)}
                  />
                </label>
                <label>
                  Limit
                  <input
                    value={auditFilters.limit}
                    onChange={(event) => onAuditFilterChange("limit", event.target.value)}
                  />
                </label>
                <div className="actions">
                  <button type="submit">Apply filters</button>
                  <button
                    type="button"
                    className="secondary"
                    onClick={() => void onResetAuditFilters()}
                  >
                    Reset
                  </button>
                </div>
              </form>
              <div className="table-shell">
                <table>
                  <thead>
                    <tr>
                      <th>When</th>
                      <th>Actor</th>
                      <th>Action</th>
                      <th>Resource</th>
                      <th>Decision</th>
                      <th>Trace</th>
                    </tr>
                  </thead>
                  <tbody>
                    {auditEvents.map((event) => (
                      <tr key={event.id}>
                        <td>{new Date(event.createdAt).toLocaleString()}</td>
                        <td>{event.actor}</td>
                        <td>{event.action}</td>
                        <td>{`${event.resourceType}:${event.resourceName}`}</td>
                        <td>{event.decision ?? "-"}</td>
                        <td>
                          {event.traceId ? (
                            <button
                              type="button"
                              className="secondary"
                              onClick={() => void onOpenTrace(event.traceId ?? "")}
                            >
                              {event.traceId}
                            </button>
                          ) : (
                            "-"
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {selectedTraceId ? (
                <div className="result-shell">
                  <div className="actions">
                    <strong>Trace detail</strong>
                    <code>{selectedTraceId}</code>
                    <button type="button" className="secondary" onClick={onClearTrace}>
                      Clear trace
                    </button>
                  </div>
                  <div className="stack">
                    {traceEvents.map((event) => (
                      <div key={event.id} className="generated">
                        <strong>{`${event.action} (${event.decision || "n/a"})`}</strong>
                        <span>{`${event.actor} -> ${event.resourceType}:${event.resourceName}`}</span>
                        <span>{new Date(event.createdAt).toLocaleString()}</span>
                        <pre className="code-block">{prettyJson(event.metadata)}</pre>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}
            </section>
          </>
        ) : null}

        {activeTab === "observability" ? (
          <>
            <section className="subpanel">
              <h2>Metrics</h2>
              <div className="observability-grid">
                <article className="card">
                  <div className="card-header">
                    <strong>Models</strong>
                    <span className="metric-value">{models.length}</span>
                  </div>
                  <p>Registered models in catalog</p>
                </article>
                <article className="card">
                  <div className="card-header">
                    <strong>Cache entries</strong>
                    <span className="metric-value">
                      {observability?.cache_entries ?? "—"}
                    </span>
                  </div>
                  <p>Chat response cache keys in Redis</p>
                </article>
                <article className="card">
                  <div className="card-header">
                    <strong>Chat requests</strong>
                    <span className="metric-value">
                      {observability?.chat_requests ?? "—"}
                    </span>
                  </div>
                  <p>Total chat completion requests to gateway</p>
                </article>
                <article className="card">
                  <div className="card-header">
                    <strong>Cache hits</strong>
                    <span className="metric-value">
                      {observability?.cache_hits ?? "—"}
                    </span>
                  </div>
                  <p>Requests served from cache</p>
                </article>
                <article className="card">
                  <div className="card-header">
                    <strong>Access requests</strong>
                    <span className="metric-value">{accessRequestsCount}</span>
                  </div>
                  <p>Total access requests (model/tool grants)</p>
                </article>
              </div>
            </section>
          </>
        ) : null}

        {activeTab === "setup" ? (
          <div className="setup-panel">
            <section className="setup-card">
              <h3>Tenant Management</h3>
              <div className="setup-row">
                <form className="setup-form" onSubmit={onCreateTenant}>
                  <label>
                    <span className="setup-label">Name</span>
                    <input
                      value={tenantForm.name}
                      onChange={(event) => onTenantFieldChange("name", event.target.value)}
                      placeholder="Acme Inc"
                    />
                  </label>
                  <label>
                    <span className="setup-label">Slug</span>
                    <input
                      value={tenantForm.slug}
                      onChange={(event) => onTenantFieldChange("slug", event.target.value)}
                      placeholder="acme"
                    />
                  </label>
                  <label>
                    <span className="setup-label">Environment</span>
                    <select
                      value={tenantForm.environment}
                      onChange={(event) => onTenantFieldChange("environment", event.target.value)}
                    >
                      <option value="dev">dev</option>
                      <option value="staging">staging</option>
                      <option value="prod">prod</option>
                    </select>
                  </label>
                  <button type="submit" className="btn-compact" disabled={!identity || !isAdmin}>
                    Create tenant
                  </button>
                </form>
                <div className="setup-table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Slug</th>
                        <th>Env</th>
                      </tr>
                    </thead>
                    <tbody>
                      {tenants.map((tenant) => (
                        <tr key={tenant.id}>
                          <td>{tenant.name}</td>
                          <td>{tenant.slug}</td>
                          <td>{tenant.environment}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>

            <section className="setup-card">
              <h3>API Key Generation</h3>
              <div className="setup-row">
                <form className="setup-form" onSubmit={onCreateApiKey}>
                  <label>
                    <span className="setup-label">Key label</span>
                    <input
                      value={apiKeyLabel}
                      onChange={(event) => onApiKeyLabelChange(event.target.value)}
                      placeholder="My key"
                    />
                  </label>
                  <button type="submit" className="btn-compact" disabled={!identity || !isAdmin}>
                    Generate key
                  </button>
                  {generatedKey ? (
                    <div className="setup-generated">
                      <span className="setup-label">Raw API key</span>
                      <code>{generatedKey}</code>
                    </div>
                  ) : null}
                </form>
                <div className="setup-table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Label</th>
                        <th>Tenant</th>
                        <th>Created</th>
                      </tr>
                    </thead>
                    <tbody>
                      {apiKeys.map((apiKey) => (
                        <tr key={apiKey.id}>
                          <td>{apiKey.label}</td>
                          <td>{apiKey.tenantId}</td>
                          <td>{new Date(apiKey.createdAt).toLocaleString()}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>
          </div>
        ) : null}
      </div>
    </section>
  );
}
