# Suez Enterprise on AWS — Detailed Implementation Plan (v1)

This plan translates `docs/suez-enterprise-product-must-haves.md` into an implementable AWS-hosted roadmap. It assumes a **multi-tenant SaaS** with tenant-scoped data in PostgreSQL, a governed **Gateway**, a **Control Plane**, **OPA**, optional **Policy LLM (Ollama)**, and a browser **Portal UI**.

---

## 1. Target architecture (AWS mapping)

### 1.1 Core services

- **Compute**: **Amazon EKS** (recommended) or ECS Fargate
  - `gateway` (Go)
  - `control-plane` (Go)
  - `opa` (OPA server)
  - `policy-llm` (Ollama) *(optional in early phases)*
- **Data**:
  - **Amazon RDS for PostgreSQL** (tenant-scoped tables)
  - **Amazon ElastiCache for Redis** (gateway quotas, cache, stats)
- **Edge**:
  - **ALB** via **AWS Load Balancer Controller** (EKS Ingress)
  - **ACM** certificate for TLS
  - **Route 53** for DNS
- **Portal UI**:
  - **S3** for static site + **CloudFront** for CDN
- **Secrets**:
  - **AWS Secrets Manager** for DB creds + provider API keys
  - **KMS** for encryption
- **Observability**:
  - **CloudWatch Logs** (Fluent Bit)
  - **CloudWatch Metrics** + alarms
  - Optional: **AWS X-Ray** via OpenTelemetry collector
- **Identity (SSO)**:
  - **Amazon Cognito User Pool** (OIDC/JWT issuance)
  - Optional SAML federation via Cognito (or IAM Identity Center depending on org standards)

### 1.2 Recommended routing (paths)

Use a single domain and path prefixes (mirrors the local edge):

- `/control-plane/*` → control-plane service
- `/gateway/*` → gateway service
- `/opa/*` → OPA service *(optional; typically internal only)*
- `/policy-llm/*` → policy-llm *(optional; typically internal only)*

In EKS, implement with ALB Ingress path rules; within each service, keep **native paths**:

- control-plane: `/api/v1/...` and `/internal/...`
- gateway: `/v1/...`
- opa: `/v1/data/...`

---

## 2. Engineering deliverables (what to add to the repo)

### 2.1 Infrastructure as Code

Create `infra/aws/` using **Terraform** or **AWS CDK**:

- **Network**: VPC (3 AZ), private subnets for compute/data, public subnets for ALB/NAT
- **EKS**: cluster + node groups, IAM OIDC provider, IRSA roles
- **RDS**: PostgreSQL, Multi-AZ, automated backups + PITR, parameter groups
- **ElastiCache**: Redis, subnet group + SGs, TLS/auth as required
- **Ingress**: AWS Load Balancer Controller, IngressClass, ALB listeners
- **DNS/TLS**: Route 53 record(s), ACM cert + validation
- **Portal**: S3 bucket + CloudFront distribution (+ OAC/OAI)
- **Secrets**: Secrets Manager secrets + KMS keys/policies

### 2.2 Kubernetes manifests

Add Helm charts or Kustomize overlays for:

- Deployments/Services for `control-plane`, `gateway`, `opa`, optional `policy-llm`
- Ingress rules for `/control-plane` and `/gateway` (and optional `/opa`, `/policy-llm`)
- ConfigMaps for non-sensitive config
- External Secrets (or Secrets Store CSI) to sync from Secrets Manager

---

## 3. Data model plan (RDS PostgreSQL)

### 3.1 Minimum tables

Implement/extend schema for enterprise SaaS:

- `tenants`: `id`, `slug`, `name`, `status`, `created_at`
- `users`: `id`, `tenant_id`, `email`, `name`, `role` (`platform_admin` | `tenant_admin` | `user`), `status`, `sso_subject`, `created_at`
- `api_keys`: `id`, `tenant_id`, `user_id` nullable, `label`, `key_hash`, `created_at`, `revoked_at`
- `models`: `id`, `tenant_id`, `external_name`, `provider`, `upstream_base_url`, `upstream_model`, `max_rpm`, `max_tpm`, `enabled`
- `tools`: `id`, `tenant_id`, `name`, `execution_mode`, `target_url`, `timeout_ms`, `risk_tier`, `input_schema`, `enabled`
- `access_requests`: `id`, `tenant_id`, `subject`, `resource_type`, `resource_name`, `justification`, `status`, `created_at`
- `access_grants`: `id`, `tenant_id`, `subject`, `resource_type`, `resource_name`, `granted_by`, `status`, `expires_at`, `created_at`
- `policies`: `id`, `tenant_id`, `name`, `status`, `rego_source`, `version`, `created_by`, `created_at`
- `policy_versions`: `id`, `policy_id`, `version`, `rego_source`, `diff_summary`, `created_by`, `created_at`, `published_at`
- `policy_test_cases`: `id`, `policy_id`, `name`, `inputs`, `expected`, `created_at`
- `policy_test_runs`: `id`, `policy_version_id`, `status`, `results`, `ran_by`, `ran_at`
- `policy_approvals`: `id`, `policy_version_id`, `approver_user_id`, `approved_at`, `comment`
- `policy_exceptions`: `id`, `tenant_id`, `subject`, `resource_type`, `resource_name`, `reason`, `expires_at`, `created_by`, `created_at`
- `audit_log`: `id`, `tenant_id`, `actor`, `action`, `resource_type`, `resource_name`, `decision`, `trace_id`, `payload_redacted`, `created_at`

### 3.2 Tenant isolation

- Enforce `tenant_id` in every query (application-level).
- Optional Phase 2 hardening: Postgres **Row Level Security (RLS)** with `SET app.tenant_id`.

---

## 4. Identity and authentication (Cognito + API keys)

### 4.1 JWT (SSO)

- Portal authenticates via **Cognito Hosted UI** (or SPA auth with PKCE).
- Control plane validates JWT: issuer/audience, expiry, signature, and extracts:
  - `sub`, `email`
  - tenant mapping claim (e.g. `tenant_slug`) or a lookup
  - roles (claim-based) or role lookup from `users`

### 4.2 API keys (automation)

- Store **hash only** in DB.
- Resolve `Authorization: ApiKey <key>` and return identity context.

### 4.3 Centralized identity resolution

Keep `gateway` “thin”:

- Gateway calls control-plane `/internal/auth/resolve` for each protected request.
- Control plane returns identity context `{tenantId, actor, roles, authMethod, ...}`.

### 4.4 Tenant resolution strategy (choose one)

- **Claim-based**: `tenant_slug` in JWT (recommended for shared domain).
- **Domain-based**: `<tenant>.suez.example.com` maps to tenant.
- **Header-based**: `X-Tenant-Slug` (only for trusted internal/admin paths).

---

## 5. Policy enforcement (OPA + bundles)

### 5.1 OPA deployment

- Run OPA as an internal service (ClusterIP) and call:
  - `/v1/data/suez/authz/decision` for allow/deny + reasons.

### 5.2 Policy distribution (recommended approach)

**Bundle-based publishing**:

- Control plane publishes a bundle artifact to **S3**:
  - versioned key: `tenants/{tenantId}/bundles/{policyVersionId}.tar.gz`
  - current pointer: `tenants/{tenantId}/bundles/current.tar.gz`
- OPA loads the bundle (init container fetch or sidecar/updater).

Publish requirements:

- required approvals are met
- policy tests pass for the version
- publish event is audited

---

## 6. Gateway runtime controls (Redis, quotas, cache, tool egress)

### 6.1 Redis (ElastiCache) behavior

- **Quotas**: enforce RPM/TPM per `{tenantId, actor, resourceName}` with minute buckets.
- **Cache**: store chat responses for a short TTL; cache key must be tenant-scoped.
- **Stats**: cache hits and total requests (tenant-scoped if desired).

Decide and implement resilience:

- Redis outage behavior: fail-open for cache, fail-closed for quotas (or vice versa) — define explicitly and test.

### 6.2 Tool egress hardening

For HTTP tools:

- Domain allowlists per tenant
- Block private IP ranges and metadata endpoints (SSRF mitigation)
- Enforce timeouts and size limits
- Risk-tier defaults (e.g. `high` tools require admin or explicit policy allow)

### 6.3 Sensitive data handling

- Redact sensitive fields from logs/audit payload by default.
- Allow controlled “full payload” storage only with explicit admin configuration and access controls.

---

## 7. Portal UI (end user UI) implementation plan

### 7.1 Sign-in and tenant context

- Cognito sign-in flow (PKCE) for users.
- Admin bootstrap/API key login for initial setup.
- Always display active tenant, actor, and role in the UI header.

### 7.2 Requester screens

- **Requests**: request model/tool access with justification; status tracking.
- **Grants**: list entitlements with limits and expiry; renew prompts.
- **Models**: invoke approved model; show response text + raw JSON; surface trace ID.
- **Tools**: execute tools; schema-aware helpers; render output + trace ID.
- **Activity**: per-user recent activity with trace IDs; support “copy trace ID” for troubleshooting.

### 7.3 Tenant admin screens

- **Catalog**: manage models/tools; risk tiers; enable/disable; provider config.
- **Approvals**: approve/deny requests; add reasons; set expirations.
- **Governance**: policy drafts/tests/publish/rollback; exceptions.
- **Observability**: metrics + audit search + evidence export.

### 7.4 Platform admin (optional early)

- Tenant lifecycle UI (create/suspend/delete).
- Support tooling (read-only cross-tenant views; tightly gated).

---

## 8. Governance implementation (simple, buildable features)

- **Policy ownership & approvals**: assign owner/reviewers; require approval(s) before publish.
- **Policy change history**: store versions with who/what/when; support rollback.
- **Policy testing gates**: require tests pass before publish; store test run results per version.
- **Exception workflow**: time-bound exceptions with reason and audit trail; automatic expiry.
- **Compliance evidence export**: generate an “audit packet” for a date range (policies, approvals, tests, enforcement events) stored in S3 with a presigned download URL.

---

## 9. Observability (CloudWatch + optional tracing)

### 9.1 Logging

- Emit structured JSON logs with `trace_id`, `tenant_id`, `actor`, `action`.
- Ship logs to CloudWatch Logs with retention policies per environment.

### 9.2 Metrics + alerts

Gateway metrics:

- request count, latency (p50/p95), error rate
- cache hit rate
- quota rejects (429) and policy denies (403) by reason

Control plane metrics:

- approval throughput
- policy publish success/failure
- evidence export success/failure

Set CloudWatch alarms for SLOs and dependency failures (RDS/Redis/OPA).

### 9.3 Tracing (optional)

- Add OpenTelemetry instrumentation in gateway/control-plane.
- Export to AWS X-Ray and propagate trace IDs end-to-end.

---

## 10. CI/CD plan (GitHub Actions)

- Build/test for Go services
- Build portal UI and deploy artifacts to S3/CloudFront
- Build/push service images to ECR
- Deploy to EKS via Argo CD (recommended) or direct `kubectl apply`
- Add security scanning (deps + containers) and enforce on PR merges

Environments:

- `dev` → `staging` → `prod` (separate AWS accounts recommended)

---

## 11. Rollout phases (recommended)

### Phase 0 — Foundation

- IaC for VPC + EKS + RDS + ElastiCache + ALB + Secrets Manager
- Deploy control-plane + gateway + OPA (no Policy LLM)
- Portal hosted on S3/CloudFront
- API key auth works end-to-end

### Phase 1 — Multi-tenancy + roles

- Add `users` and tenant admin/user roles
- JWT validation in control plane (Cognito)
- Tenant-scoped UI and APIs

### Phase 2 — Governance MVP

- Policy versions, approvals, test gates
- Exceptions with expiry
- Evidence export to S3

### Phase 3 — Operations hardening

- RDS PITR + restore drills; runbooks
- Tool egress allowlists + SSRF defenses
- Admin delegation and safer defaults

### Phase 4 — Observability & compliance hardening

- CloudWatch dashboards + alarms
- Audit packet completeness and redaction policy
- Optional tracing rollout

---

## 12. Acceptance checklist (definition of done)

- **Operations**: tenant lifecycle, catalog, approvals, backups, admin roles are functional and tenant-scoped.
- **Security**: SSO+API keys, OPA enforcement, secrets management, SSRF/egress controls, redaction policies applied.
- **Observability**: audit log + trace IDs + dashboards + exportable evidence.
- **End User UI**: requester workflows (requests/grants/models/tools/activity) and admin workflows (catalog/approvals/governance/observability) are usable and consistent.

