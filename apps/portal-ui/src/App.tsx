import { FormEvent, useEffect, useMemo, useState } from "react";

import { AdminView } from "./components/AdminView";
import { LandingPage } from "./components/LandingPage";
import { RequesterView } from "./components/RequesterView";
import {
  AccessGrant,
  AccessRequest,
  ApiKeySummary,
  AuditEvent,
  AuditFilters,
  AuthMode,
  FetchState,
  IdentityContext,
  ModelRecord,
  PolicyRecord,
  PolicyTestCase,
  PolicyTestRun,
  PortalTab,
  RequesterUser,
  Tenant,
  ToolRecord
} from "./types";

const controlPlaneBaseUrl =
  import.meta.env.VITE_CONTROL_PLANE_URL ?? "http://localhost:8081";
const gatewayBaseUrl =
  import.meta.env.VITE_GATEWAY_URL ?? "http://localhost:8080";

const defaultPolicyTestForm = {
  name: "Requester denied for high-risk tool",
  description: "Requester should be denied when invoking a high-risk tool.",
  action: "tools.execute",
  identity:
    '{"tenantId":"00000000-0000-0000-0000-000000000001","tenantSlug":"demo","tenantName":"Demo Tenant","actor":"requester@example.com","authMethod":"jwt","roles":["user"]}',
  resource: '{"type":"tool","name":"finance.transfer","enabled":true,"mode":"http","riskTier":"high"}',
  request: '{"name":"finance.transfer","input":{"amount":100}}',
  expectedAllow: false,
  expectedReasons: "draft_high_risk_tool_block"
};

const defaultAuditFilters: AuditFilters = {
  actor: "",
  action: "",
  resourceType: "",
  resourceName: "",
  decision: "",
  traceId: "",
  since: "",
  until: "",
  limit: "100"
};

function encodeBase64Url(value: string) {
  return window
    .btoa(value)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/g, "");
}

function buildUnsignedJwt(subject: string, role: string) {
  const header = encodeBase64Url(JSON.stringify({ alg: "none", typ: "JWT" }));
  const payload = encodeBase64Url(
    JSON.stringify({
      sub: subject,
      tenant_slug: "demo",
      tenant_name: "Demo Tenant",
      roles: [role]
    })
  );
  return `${header}.${payload}.`;
}

function parseJsonText(value: string) {
  return JSON.parse(value) as unknown;
}

function parseExpectedReasons(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function normalizeAuditTime(value: string) {
  if (!value) {
    return "";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return "";
  }
  return parsed.toISOString();
}

function buildAuditQuery(filters: AuditFilters) {
  const params = new URLSearchParams();
  const entries: Array<[keyof AuditFilters, string]> = [
    ["actor", filters.actor],
    ["action", filters.action],
    ["resourceType", filters.resourceType],
    ["resourceName", filters.resourceName],
    ["decision", filters.decision],
    ["traceId", filters.traceId]
  ];
  for (const [key, value] of entries) {
    if (value.trim()) {
      params.set(key, value.trim());
    }
  }
  if (filters.limit.trim()) {
    params.set("limit", filters.limit.trim());
  }
  const since = normalizeAuditTime(filters.since);
  const until = normalizeAuditTime(filters.until);
  if (since) {
    params.set("since", since);
  }
  if (until) {
    params.set("until", until);
  }
  return params.toString();
}

const demoRequesterJwt = buildUnsignedJwt("requester@example.com", "user");

type AppPage = "landing" | "admin" | "requester";

export default function App() {
  const [states, setStates] = useState<FetchState[]>([]);
  const [page, setPage] = useState<AppPage>("landing");
  const [selectedRole, setSelectedRole] = useState<PortalTab>("admin");
  const [selectedRequesterUser, setSelectedRequesterUser] = useState<RequesterUser>("user1");
  const [authMode, setAuthMode] = useState<AuthMode>("apiKey");
  const [authToken, setAuthToken] = useState<string>("");
  const [statusMessage, setStatusMessage] = useState<string>("");
  const [identity, setIdentity] = useState<IdentityContext | null>(null);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [apiKeys, setApiKeys] = useState<ApiKeySummary[]>([]);
  const [accessRequests, setAccessRequests] = useState<AccessRequest[]>([]);
  const [accessGrants, setAccessGrants] = useState<AccessGrant[]>([]);
  const [models, setModels] = useState<ModelRecord[]>([]);
  const [tools, setTools] = useState<ToolRecord[]>([]);
  const [policies, setPolicies] = useState<PolicyRecord[]>([]);
  const [policyVersions, setPolicyVersions] = useState<PolicyRecord[]>([]);
  const [policyTestCases, setPolicyTestCases] = useState<PolicyTestCase[]>([]);
  const [policyTestRuns, setPolicyTestRuns] = useState<PolicyTestRun[]>([]);
  const [auditEvents, setAuditEvents] = useState<AuditEvent[]>([]);
  const [traceEvents, setTraceEvents] = useState<AuditEvent[]>([]);
  const [selectedTraceId, setSelectedTraceId] = useState("");
  const [auditFilters, setAuditFilters] = useState<AuditFilters>(defaultAuditFilters);
  const [generatedKey, setGeneratedKey] = useState<string>("");
  const [tenantForm, setTenantForm] = useState({
    name: "",
    slug: "",
    environment: "dev"
  });
  const [apiKeyLabel, setApiKeyLabel] = useState("Generated Key");
  const [modelRequestForm, setModelRequestForm] = useState({
    resourceName: "demo-model",
    justification: ""
  });
  const [toolRequestForm, setToolRequestForm] = useState({
    resourceName: "weather.lookup",
    justification: ""
  });
  const [decisionReason, setDecisionReason] = useState(
    "Reviewed in the admin portal."
  );
  const [modelForm, setModelForm] = useState({
    externalName: "customer-support-stub",
    provider: "stub",
    modelName: "customer-support-stub",
    upstreamModel: "",
    upstreamBaseUrl: "",
    maxRpm: "60",
    maxTpm: "120000",
    enabled: true
  });
  const [toolForm, setToolForm] = useState({
    name: "weather.lookup.internal",
    description: "Tenant-specific stub tool.",
    executionMode: "stub",
    targetUrl: "",
    timeoutMs: "5000",
    inputSchema:
      '{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}',
    outputSchema:
      '{"type":"object","properties":{"city":{"type":"string"},"forecast":{"type":"string"}}}',
    riskTier: "standard",
    enabled: true
  });
  const [policyForm, setPolicyForm] = useState({
    name: "Tenant Runtime Guard",
    scope: "runtime",
    targetName: "*",
    naturalLanguageRule:
      "Allow governed model and tool execution only after the runtime checks succeed.",
    regoPackage: "suez.authz",
    regoSource:
      'package suez.authz\n\ndefault allow := false\n\nallow if {\n  input.identity.actor != ""\n}',
    status: "draft",
    enabled: true
  });
  const [policyTestForm, setPolicyTestForm] = useState(defaultPolicyTestForm);
  const [selectedPolicyId, setSelectedPolicyId] = useState("");
  const [chatForm, setChatForm] = useState({
    model: "demo-model",
    prompt: "Summarize how this local governed gateway works.",
    maxTokens: "128"
  });
  const [toolRunForm, setToolRunForm] = useState({
    name: "weather.lookup",
    input: '{"city":"Paris"}'
  });
  const [chatResult, setChatResult] = useState("");
  const [toolResult, setToolResult] = useState("");

  const targets = useMemo(
    () => [
      {
        label: "Control Plane",
        url: `${controlPlaneBaseUrl}/healthz`
      },
      {
        label: "Gateway",
        url: `${gatewayBaseUrl}/healthz`
      },
      {
        label: "Bootstrap API",
        url: `${controlPlaneBaseUrl}/api/v1/bootstrap/status`
      }
    ],
    []
  );

  const authHeaders = useMemo(() => {
    const headers: Record<string, string> = {};
    if (!authToken) {
      return headers;
    }
    headers.Authorization =
      authMode === "apiKey" ? `ApiKey ${authToken}` : `Bearer ${authToken}`;
    return headers;
  }, [authMode, authToken]);

  const isAdmin = identity?.roles.some((role) => role.toLowerCase() === "admin");
  const pendingRequests = accessRequests.filter((request) => request.status === "pending");

  // When on landing and role changes, set default credential type and clear status
  useEffect(() => {
    if (page !== "landing") return;
    if (selectedRole === "admin") {
      setAuthMode("apiKey");
    } else {
      setAuthMode("jwt");
    }
    setStatusMessage("");
  }, [selectedRole, page]);

  // After successful sign-in from landing, go to the selected role page
  useEffect(() => {
    if (page === "landing" && identity) {
      setPage(selectedRole);
    }
  }, [page, identity, selectedRole]);

  useEffect(() => {
    const load = async () => {
      const results = await Promise.all(
        targets.map(async (target): Promise<FetchState> => {
          try {
            const response = await fetch(target.url);
            if (!response.ok) {
              return {
                label: target.label,
                url: target.url,
                ok: false,
                detail: `HTTP ${response.status}`
              };
            }
            return {
              label: target.label,
              url: target.url,
              ok: true,
              detail: "reachable"
            };
          } catch (error) {
            return {
              label: target.label,
              url: target.url,
              ok: false,
              detail: error instanceof Error ? error.message : "request failed"
            };
          }
        })
      );
      setStates(results);
    };
    void load();
  }, [targets]);

  useEffect(() => {
    if (!identity) {
      return;
    }
    void loadProtectedData();
  }, [identity]);

  useEffect(() => {
    if (!isAdmin || !selectedPolicyId) {
      return;
    }
    void loadSelectedPolicyData(selectedPolicyId);
  }, [selectedPolicyId, isAdmin]);

  useEffect(() => {
    if (models.length > 0) {
      setModelRequestForm((current) => ({
        ...current,
        resourceName: models.some((model) => model.externalName === current.resourceName)
          ? current.resourceName
          : models[0].externalName
      }));
      setChatForm((current) => ({
        ...current,
        model: models.some((model) => model.externalName === current.model)
          ? current.model
          : models[0].externalName
      }));
    }
  }, [models]);

  useEffect(() => {
    if (tools.length > 0) {
      setToolRequestForm((current) => ({
        ...current,
        resourceName: tools.some((tool) => tool.name === current.resourceName)
          ? current.resourceName
          : tools[0].name
      }));
      setToolRunForm((current) => ({
        ...current,
        name: tools.some((tool) => tool.name === current.name) ? current.name : tools[0].name
      }));
    }
  }, [tools]);

  async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
    const response = await fetch(url, init);
    const payload = (await response.json()) as T & { error?: string };
    if (!response.ok) {
      throw new Error(payload.error ?? `Request failed with HTTP ${response.status}`);
    }
    return payload;
  }

  async function authenticate() {
    setGeneratedKey("");
    try {
      const payload = await fetchJSON<{ identityContext?: IdentityContext }>(
        `${controlPlaneBaseUrl}/api/v1/auth/session`,
        { headers: authHeaders }
      );
      if (!payload.identityContext) {
        throw new Error("Authentication response did not include identity context.");
      }
      setIdentity(payload.identityContext);
      setStatusMessage(
        `Authenticated as ${payload.identityContext.actor} using ${payload.identityContext.authMethod}.`
      );
    } catch (error) {
      logout(false);
      setStatusMessage(error instanceof Error ? error.message : "Login failed.");
    }
  }

  async function loadAuditData(filters: AuditFilters = auditFilters) {
    const query = buildAuditQuery(filters);
    const payload = await fetchJSON<{ auditEvents?: AuditEvent[] }>(
      `${controlPlaneBaseUrl}/api/v1/audit-log${query ? `?${query}` : ""}`,
      { headers: authHeaders }
    );
    setAuditEvents(payload.auditEvents ?? []);
  }

  async function loadSelectedPolicyData(policyId: string) {
    const [testsPayload, versionsPayload] = await Promise.all([
      fetchJSON<{ testCases?: PolicyTestCase[]; testRuns?: PolicyTestRun[] }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/tests`,
        { headers: authHeaders }
      ),
      fetchJSON<{ versions?: PolicyRecord[] }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/versions`,
        { headers: authHeaders }
      )
    ]);
    setPolicyTestCases(testsPayload.testCases ?? []);
    setPolicyTestRuns(testsPayload.testRuns ?? []);
    setPolicyVersions(versionsPayload.versions ?? []);
  }

  async function loadProtectedData() {
    const [tenantPayload, apiKeyPayload, requestPayload, grantPayload, modelPayload, toolPayload] =
      await Promise.all([
        fetchJSON<{ tenants?: Tenant[] }>(`${controlPlaneBaseUrl}/api/v1/tenants`, {
          headers: authHeaders
        }),
        fetchJSON<{ apiKeys?: ApiKeySummary[] }>(`${controlPlaneBaseUrl}/api/v1/api-keys`, {
          headers: authHeaders
        }),
        fetchJSON<{ accessRequests?: AccessRequest[] }>(
          `${controlPlaneBaseUrl}/api/v1/access-requests`,
          { headers: authHeaders }
        ),
        fetchJSON<{ accessGrants?: AccessGrant[] }>(
          `${controlPlaneBaseUrl}/api/v1/access-grants`,
          { headers: authHeaders }
        ),
        fetchJSON<{ models?: ModelRecord[] }>(`${controlPlaneBaseUrl}/api/v1/models`, {
          headers: authHeaders
        }),
        fetchJSON<{ tools?: ToolRecord[] }>(`${controlPlaneBaseUrl}/api/v1/tools`, {
          headers: authHeaders
        })
      ]);

    setTenants(tenantPayload.tenants ?? []);
    setApiKeys(apiKeyPayload.apiKeys ?? []);
    setAccessRequests(requestPayload.accessRequests ?? []);
    setAccessGrants(grantPayload.accessGrants ?? []);
    setModels(modelPayload.models ?? []);
    setTools(toolPayload.tools ?? []);

    if (identity?.roles.some((role) => role.toLowerCase() === "admin")) {
      const policyPayload = await fetchJSON<{ policies?: PolicyRecord[] }>(
        `${controlPlaneBaseUrl}/api/v1/policies`,
        {
          headers: authHeaders
        }
      );
      const nextPolicies = policyPayload.policies ?? [];
      setPolicies(nextPolicies);
      await loadAuditData(auditFilters);
      if (selectedPolicyId) {
        await loadSelectedPolicyData(selectedPolicyId);
      } else if (nextPolicies.length > 0) {
        populatePolicyForm(nextPolicies[0]);
      }
    } else {
      setPolicies([]);
      setPolicyVersions([]);
      setPolicyTestCases([]);
      setPolicyTestRuns([]);
      setAuditEvents([]);
      setTraceEvents([]);
      setSelectedPolicyId("");
      setSelectedTraceId("");
    }
  }

  async function handleLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await authenticate();
  }

  async function handleCreateTenant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = await fetchJSON<{ tenant?: Tenant }>(
        `${controlPlaneBaseUrl}/api/v1/tenants`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify(tenantForm)
        }
      );
      if (!payload.tenant) {
        throw new Error("Failed to create tenant.");
      }
      setTenantForm({ name: "", slug: "", environment: "dev" });
      setStatusMessage(`Created tenant ${payload.tenant.slug}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to create tenant.");
    }
  }

  async function handleCreateApiKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = await fetchJSON<{ rawApiKey?: string }>(
        `${controlPlaneBaseUrl}/api/v1/api-keys`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify({
            label: apiKeyLabel,
            tenantId: identity?.tenantId
          })
        }
      );
      if (!payload.rawApiKey) {
        throw new Error("Failed to create API key.");
      }
      setGeneratedKey(payload.rawApiKey);
      setStatusMessage("Created a new API key. Copy it now because it is only shown once.");
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to create API key.");
    }
  }

  async function handleDecision(requestId: string, action: "approve" | "deny") {
    try {
      await fetchJSON(`${controlPlaneBaseUrl}/api/v1/access-requests/${requestId}/${action}`, {
        method: "POST",
        headers: {
          ...authHeaders,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({ reason: decisionReason })
      });
      setStatusMessage(
        `${action === "approve" ? "Approved" : "Denied"} request ${requestId}.`
      );
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(
        error instanceof Error ? error.message : `Failed to ${action} access request.`
      );
    }
  }

  function applyDemoCredential(mode: AuthMode, token: string) {
    setAuthMode(mode);
    setAuthToken(token);
  }

  function goToLanding() {
    logout(true);
    setPage("landing");
    setStatusMessage("");
  }

  function logout(resetToken = true) {
    setIdentity(null);
    setTenants([]);
    setApiKeys([]);
    setAccessRequests([]);
    setAccessGrants([]);
    setModels([]);
    setTools([]);
    setPolicies([]);
    setPolicyVersions([]);
    setPolicyTestCases([]);
    setPolicyTestRuns([]);
    setAuditEvents([]);
    setTraceEvents([]);
    setGeneratedKey("");
    setSelectedPolicyId("");
    setSelectedTraceId("");
    if (resetToken) {
      setAuthToken("");
    }
    setStatusMessage("");
    setChatResult("");
    setToolResult("");
  }

  async function submitAccessRequest(
    resourceType: "model" | "tool",
    resourceName: string,
    justification: string
  ) {
    const payload = await fetchJSON<{ accessRequest?: AccessRequest }>(
      `${controlPlaneBaseUrl}/api/v1/access-requests`,
      {
        method: "POST",
        headers: {
          ...authHeaders,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          resourceType,
          resourceName,
          justification
        })
      }
    );
    if (!payload.accessRequest) {
      throw new Error("Failed to submit access request.");
    }
    setStatusMessage(
      `Submitted request ${payload.accessRequest.id} for ${payload.accessRequest.resourceName}.`
    );
    await loadProtectedData();
  }

  async function handleSubmitModelRequest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await submitAccessRequest(
        "model",
        modelRequestForm.resourceName,
        modelRequestForm.justification
      );
      setModelRequestForm((current) => ({ ...current, justification: "" }));
    } catch (error) {
      setStatusMessage(
        error instanceof Error ? error.message : "Failed to submit model access request."
      );
    }
  }

  async function handleSubmitToolRequest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await submitAccessRequest(
        "tool",
        toolRequestForm.resourceName,
        toolRequestForm.justification
      );
      setToolRequestForm((current) => ({ ...current, justification: "" }));
    } catch (error) {
      setStatusMessage(
        error instanceof Error ? error.message : "Failed to submit tool access request."
      );
    }
  }

  async function handleCreateModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await fetchJSON(`${controlPlaneBaseUrl}/api/v1/models`, {
        method: "POST",
        headers: {
          ...authHeaders,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          externalName: modelForm.externalName,
          provider: modelForm.provider,
          modelName: modelForm.modelName,
          upstreamModel: modelForm.upstreamModel,
          upstreamBaseUrl: modelForm.upstreamBaseUrl,
          maxRpm: Number(modelForm.maxRpm),
          maxTpm: Number(modelForm.maxTpm),
          enabled: modelForm.enabled
        })
      });
      setStatusMessage(`Created model ${modelForm.externalName}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to create model.");
    }
  }

  async function handleCreateTool(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await fetchJSON(`${controlPlaneBaseUrl}/api/v1/tools`, {
        method: "POST",
        headers: {
          ...authHeaders,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          name: toolForm.name,
          description: toolForm.description,
          executionMode: toolForm.executionMode,
          targetUrl: toolForm.targetUrl,
          timeoutMs: Number(toolForm.timeoutMs),
          inputSchema: parseJsonText(toolForm.inputSchema),
          outputSchema: parseJsonText(toolForm.outputSchema),
          riskTier: toolForm.riskTier,
          enabled: toolForm.enabled
        })
      });
      setStatusMessage(`Created tool ${toolForm.name}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to create tool.");
    }
  }

  async function handleCreatePolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = await fetchJSON<{ policy?: PolicyRecord }>(
        `${controlPlaneBaseUrl}/api/v1/policies`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify(policyForm)
        }
      );
      if (payload.policy) {
        populatePolicyForm(payload.policy);
      }
      setStatusMessage(`Saved draft ${policyForm.name}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to create policy.");
    }
  }

  async function handleCreatePolicyTest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedPolicyId) {
      setStatusMessage("Select a policy before creating test cases.");
      return;
    }
    try {
      await fetchJSON(`${controlPlaneBaseUrl}/api/v1/policies/${selectedPolicyId}/tests`, {
        method: "POST",
        headers: {
          ...authHeaders,
          "Content-Type": "application/json"
        },
        body: JSON.stringify({
          name: policyTestForm.name,
          description: policyTestForm.description,
          action: policyTestForm.action,
          identity: parseJsonText(policyTestForm.identity),
          resource: parseJsonText(policyTestForm.resource),
          request: parseJsonText(policyTestForm.request),
          expectedAllow: policyTestForm.expectedAllow,
          expectedReasons: parseExpectedReasons(policyTestForm.expectedReasons)
        })
      });
      setPolicyTestForm(defaultPolicyTestForm);
      setStatusMessage("Added a policy test case.");
      await loadSelectedPolicyData(selectedPolicyId);
    } catch (error) {
      setStatusMessage(
        error instanceof Error ? error.message : "Failed to create policy test case."
      );
    }
  }

  function populatePolicyForm(policy: PolicyRecord) {
    setSelectedPolicyId(policy.id);
    setPolicyForm({
      name: policy.name,
      scope: policy.scope,
      targetName: policy.targetName ?? "*",
      naturalLanguageRule: policy.naturalLanguageRule ?? "",
      regoPackage: policy.regoPackage,
      regoSource: policy.regoSource,
      status: policy.status,
      enabled: policy.enabled
    });
  }

  async function handleGeneratePolicy() {
    try {
      const payload = await fetchJSON<{ policy?: PolicyRecord }>(
        `${controlPlaneBaseUrl}/api/v1/policies/generate`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify({
            name: policyForm.name,
            scope: policyForm.scope,
            targetName: policyForm.targetName,
            naturalLanguageRule: policyForm.naturalLanguageRule,
            regoPackage: policyForm.regoPackage
          })
        }
      );
      if (!payload.policy) {
        throw new Error("Failed to generate policy draft.");
      }
      populatePolicyForm(payload.policy);
      setStatusMessage(`Generated draft ${payload.policy.name}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(
        error instanceof Error ? error.message : "Failed to generate policy draft."
      );
    }
  }

  async function handleValidatePolicy(policyId: string) {
    try {
      const payload = await fetchJSON<{ policy?: PolicyRecord }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/validate`,
        {
          method: "POST",
          headers: authHeaders
        }
      );
      if (payload.policy) {
        populatePolicyForm(payload.policy);
      }
      setStatusMessage(`Validated policy ${policyId}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to validate policy.");
    }
  }

  async function handleRunPolicyTests(policyId: string) {
    try {
      const payload = await fetchJSON<{ testRun?: PolicyTestRun }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/tests/run`,
        {
          method: "POST",
          headers: authHeaders
        }
      );
      if (!payload.testRun) {
        throw new Error("Failed to run policy tests.");
      }
      setStatusMessage(
        `Executed policy tests: ${payload.testRun.passedCount}/${payload.testRun.totalCount} passed.`
      );
      await loadSelectedPolicyData(policyId);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to run policy tests.");
    }
  }

  async function handlePublishPolicy(policyId: string) {
    try {
      const payload = await fetchJSON<{ policy?: PolicyRecord }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/publish`,
        {
          method: "POST",
          headers: authHeaders
        }
      );
      if (payload.policy) {
        populatePolicyForm(payload.policy);
      }
      setStatusMessage(`Published policy ${policyId} to OPA.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to publish policy.");
    }
  }

  async function handleRollbackPolicy(policyId: string) {
    try {
      const payload = await fetchJSON<{ policy?: PolicyRecord }>(
        `${controlPlaneBaseUrl}/api/v1/policies/${policyId}/rollback`,
        {
          method: "POST",
          headers: authHeaders
        }
      );
      if (payload.policy) {
        populatePolicyForm(payload.policy);
      }
      setStatusMessage(`Rolled back policy lineage from ${policyId}.`);
      await loadProtectedData();
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to roll back policy.");
    }
  }

  async function handleApplyAuditFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      await loadAuditData(auditFilters);
      setStatusMessage("Applied audit filters.");
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to load audit filters.");
    }
  }

  async function handleResetAuditFilters() {
    setAuditFilters(defaultAuditFilters);
    setSelectedTraceId("");
    setTraceEvents([]);
    try {
      await loadAuditData(defaultAuditFilters);
      setStatusMessage("Reset audit filters.");
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to reset audit view.");
    }
  }

  async function handleOpenTrace(traceId: string) {
    if (!traceId) {
      return;
    }
    try {
      const payload = await fetchJSON<{ auditEvents?: AuditEvent[] }>(
        `${controlPlaneBaseUrl}/api/v1/audit-log/traces/${traceId}`,
        { headers: authHeaders }
      );
      setSelectedTraceId(traceId);
      setTraceEvents(payload.auditEvents ?? []);
      setStatusMessage(`Loaded trace ${traceId}.`);
    } catch (error) {
      setStatusMessage(error instanceof Error ? error.message : "Failed to load trace.");
    }
  }

  async function handleInvokeChat(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = await fetchJSON<Record<string, unknown>>(
        `${gatewayBaseUrl}/v1/chat/completions`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify({
            model: chatForm.model,
            messages: [{ role: "user", content: chatForm.prompt }],
            max_tokens: Number(chatForm.maxTokens)
          })
        }
      );
      setChatResult(JSON.stringify(payload, null, 2));
      setStatusMessage(`Executed governed chat request for ${chatForm.model}.`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Model invocation failed.";
      setChatResult(message);
      setStatusMessage(message);
    }
  }

  async function handleExecuteTool(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = await fetchJSON<Record<string, unknown>>(
        `${gatewayBaseUrl}/v1/tools/execute`,
        {
          method: "POST",
          headers: {
            ...authHeaders,
            "Content-Type": "application/json"
          },
          body: JSON.stringify({
            name: toolRunForm.name,
            input: parseJsonText(toolRunForm.input)
          })
        }
      );
      setToolResult(JSON.stringify(payload, null, 2));
      setStatusMessage(`Executed governed tool ${toolRunForm.name}.`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Tool execution failed.";
      setToolResult(message);
      setStatusMessage(message);
    }
  }

  if (page === "landing") {
    return (
      <LandingPage
        selectedRole={selectedRole}
        selectedRequesterUser={selectedRequesterUser}
        authMode={authMode}
        authToken={authToken}
        demoRequesterJwt={demoRequesterJwt}
        statusMessage={statusMessage}
        onRoleChange={setSelectedRole}
        onRequesterUserChange={setSelectedRequesterUser}
        onAuthModeChange={setAuthMode}
        onAuthTokenChange={setAuthToken}
        onApplyDemoCredential={applyDemoCredential}
        onSubmit={handleLogin}
      />
    );
  }

  return (
    <div className="app">
      <header className="app-header">
        <div className="app-brand">
          <h1 className="app-title">Suez</h1>
          <span className="app-tagline">AcroSystemsInc.ai</span>
        </div>
        <span className="app-page-label">{page === "admin" ? "Admin" : "Requester"}</span>
        <button type="button" className="app-signout secondary" onClick={goToLanding}>
          Sign out
        </button>
      </header>

      <main className="page">
        <section className="hero">
          <p className="hero-desc">
            {page === "admin"
              ? "Governed model and tool access, policy lifecycle, and audit observability."
              : "Request access, use models and tools, and view your grants."}
          </p>
        </section>

        {page === "admin" ? (
        <AdminView
          states={states}
          identity={identity}
          isAdmin={Boolean(isAdmin)}
          pendingRequests={pendingRequests}
          accessGrants={accessGrants}
          tenants={tenants}
          apiKeys={apiKeys}
          models={models}
          tools={tools}
          policies={policies}
          policyVersions={policyVersions}
          policyTestCases={policyTestCases}
          policyTestRuns={policyTestRuns}
          auditEvents={auditEvents}
          traceEvents={traceEvents}
          selectedTraceId={selectedTraceId}
          auditFilters={auditFilters}
          generatedKey={generatedKey}
          decisionReason={decisionReason}
          apiKeyLabel={apiKeyLabel}
          tenantForm={tenantForm}
          modelForm={modelForm}
          toolForm={toolForm}
          policyForm={policyForm}
          policyTestForm={policyTestForm}
          selectedPolicyId={selectedPolicyId}
          gatewayBaseUrl={gatewayBaseUrl}
          authHeaders={authHeaders}
          accessRequestsCount={accessRequests.length}
          onDecisionReasonChange={setDecisionReason}
          onApiKeyLabelChange={setApiKeyLabel}
          onTenantFieldChange={(field, value) =>
            setTenantForm((current) => ({ ...current, [field]: value }))
          }
          onModelFieldChange={(field, value) =>
            setModelForm((current) => ({ ...current, [field]: value }))
          }
          onToolFieldChange={(field, value) =>
            setToolForm((current) => ({ ...current, [field]: value }))
          }
          onPolicyFieldChange={(field, value) =>
            setPolicyForm((current) => ({ ...current, [field]: value }))
          }
          onPolicyTestFieldChange={(field, value) =>
            setPolicyTestForm((current) => ({ ...current, [field]: value }))
          }
          onAuditFilterChange={(field, value) =>
            setAuditFilters((current) => ({ ...current, [field]: value }))
          }
          onSelectPolicy={populatePolicyForm}
          onOpenTrace={handleOpenTrace}
          onClearTrace={() => {
            setSelectedTraceId("");
            setTraceEvents([]);
          }}
          onCreateTenant={handleCreateTenant}
          onCreateApiKey={handleCreateApiKey}
          onCreateModel={handleCreateModel}
          onCreateTool={handleCreateTool}
          onCreatePolicy={handleCreatePolicy}
          onCreatePolicyTest={handleCreatePolicyTest}
          onApplyAuditFilters={handleApplyAuditFilters}
          onResetAuditFilters={handleResetAuditFilters}
          onGeneratePolicy={handleGeneratePolicy}
          onValidatePolicy={handleValidatePolicy}
          onRunPolicyTests={handleRunPolicyTests}
          onPublishPolicy={handlePublishPolicy}
          onRollbackPolicy={handleRollbackPolicy}
          onDecision={handleDecision}
        />
      ) : (
        <RequesterView
          identity={identity}
          models={models}
          tools={tools}
          accessRequests={accessRequests}
          accessGrants={accessGrants}
          modelRequestForm={modelRequestForm}
          toolRequestForm={toolRequestForm}
          chatForm={chatForm}
          toolRunForm={toolRunForm}
          chatResult={chatResult}
          toolResult={toolResult}
          onModelRequestFieldChange={(field, value) =>
            setModelRequestForm((current) => ({ ...current, [field]: value }))
          }
          onToolRequestFieldChange={(field, value) =>
            setToolRequestForm((current) => ({ ...current, [field]: value }))
          }
          onChatFieldChange={(field, value) =>
            setChatForm((current) => ({ ...current, [field]: value }))
          }
          onToolRunFieldChange={(field, value) =>
            setToolRunForm((current) => ({ ...current, [field]: value }))
          }
          onSubmitModelRequest={handleSubmitModelRequest}
          onSubmitToolRequest={handleSubmitToolRequest}
          onInvokeChat={handleInvokeChat}
          onExecuteTool={handleExecuteTool}
        />
      )}
      </main>
    </div>
  );
}
