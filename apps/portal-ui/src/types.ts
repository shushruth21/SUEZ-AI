export type FetchState = {
  label: string;
  url: string;
  ok: boolean;
  detail: string;
};

export type IdentityContext = {
  tenantId: string;
  tenantSlug: string;
  tenantName: string;
  actor: string;
  authMethod: string;
  apiKeyId?: string;
  jwtSubject?: string;
  roles: string[];
  authenticatedAt: string;
  claims?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
};

export type Tenant = {
  id: string;
  name: string;
  slug: string;
  environment: string;
};

export type ApiKeySummary = {
  id: string;
  tenantId: string;
  label: string;
  createdAt: string;
};

export type AccessRequest = {
  id: string;
  tenantId: string;
  subject: string;
  resourceType: string;
  resourceName: string;
  justification: string;
  status: string;
  reviewedBy?: string;
  decisionReason?: string;
  createdAt: string;
  reviewedAt?: string;
};

export type AccessGrant = {
  id: string;
  requestId?: string;
  tenantId: string;
  subject: string;
  resourceType: string;
  resourceName: string;
  grantedBy: string;
  status: string;
  createdAt: string;
};

export type ModelRecord = {
  id: string;
  tenantId: string;
  externalName: string;
  provider: string;
  modelName: string;
  upstreamModel?: string;
  upstreamBaseUrl?: string;
  maxRpm: number;
  maxTpm: number;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};

export type ToolRecord = {
  id: string;
  tenantId: string;
  name: string;
  description: string;
  inputSchema: unknown;
  outputSchema: unknown;
  executionMode: string;
  targetUrl?: string;
  timeoutMs: number;
  httpHeaders: unknown;
  riskTier: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};

export type PolicyRecord = {
  id: string;
  tenantId: string;
  parentPolicyId?: string;
  name: string;
  scope: string;
  targetName?: string;
  naturalLanguageRule?: string;
  generationPrompt?: string;
  generatorModel?: string;
  regoPackage: string;
  regoSource: string;
  status: string;
  version: number;
  enabled: boolean;
  validationErrors: string[];
  publishedAt?: string;
  publishedBy?: string;
  lastCompiledAt?: string;
  createdAt: string;
  updatedAt: string;
};

export type PolicyTestCase = {
  id: string;
  policyId: string;
  tenantId: string;
  name: string;
  description: string;
  identity: unknown;
  action: string;
  resource: unknown;
  request: unknown;
  expectedAllow: boolean;
  expectedReasons: string[];
  createdAt: string;
  updatedAt: string;
};

export type PolicyTestRun = {
  id: string;
  policyId: string;
  tenantId: string;
  initiatedBy: string;
  status: string;
  totalCount: number;
  passedCount: number;
  failedCount: number;
  results: unknown;
  startedAt: string;
  completedAt?: string;
  createdAt: string;
};

export type AuditEvent = {
  id: string;
  tenantId?: string;
  actor: string;
  action: string;
  resourceType: string;
  resourceName: string;
  decision?: string;
  traceId?: string;
  metadata: unknown;
  createdAt: string;
};

export type AuditFilters = {
  actor: string;
  action: string;
  resourceType: string;
  resourceName: string;
  decision: string;
  traceId: string;
  since: string;
  until: string;
  limit: string;
};

export type AuthMode = "apiKey" | "jwt";
export type PortalTab = "admin" | "requester";
export type RequesterUser = "user1" | "user2";
