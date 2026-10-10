# Suez Enterprise — Must‑Have Features (v1)

This document enumerates the **must-have** features for a Suez Enterprise product and groups them into **Operations**, **Security**, **Observability**, and **End User UI**. Each feature is described in 1–2 lines.

## Operations

- **Multi-tenant onboarding & lifecycle**: Create, suspend, and delete tenants with clear isolation boundaries and automated provisioning.
- **Model & tool catalog management**: Register models/tools, set provider config, limits, and enable/disable controls per tenant.
- **Access request & approval workflows**: Request/approve/deny access with optional justifications, expirations, and re-approval triggers.
- **Policy lifecycle management**: Draft, validate, test, publish, version, and roll back policies with safe promotion gates.
- **Change management & releases**: Environment promotion, release notes, and controlled rollout of policy and catalog changes.
- **Backup, restore, and export**: Scheduled backups and point-in-time recovery for governance data plus export for audits and incident response.
- **Admin roles & delegation**: Platform admin vs tenant admin responsibilities with scoped UI/API capabilities.
- **Integration configuration**: Manage IdP, secrets, upstream providers, and runtime settings via repeatable config (IaC-friendly).

## Security

- **Centralized authentication (SSO + API keys)**: Support enterprise SSO (OIDC/SAML via IdP) and tenant-scoped API keys for automation.
- **Policy-based authorization (OPA/Rego)**: Enforce allow/deny decisions with reasons on every model/tool action using versioned policies.
- **Least-privilege access grants**: Fine-grained grants for models, tools, and admin functions; default-deny with explicit approvals.
- **Secrets management**: Store/rotate upstream keys and internal credentials in a managed vault with tight access and audit trails.
- **Data isolation & tenant boundaries**: Enforce tenant scoping at every query and request; optional DB-layer controls (e.g., RLS) for defense-in-depth.
- **Egress controls for tools**: Restrict tool targets (domains/IPs), require allowlists, and block high-risk operations by default.
- **PII/sensitive data protections**: Detect/limit sensitive data in prompts/tool inputs, and support redaction in logs and exports.
- **Security posture controls**: Force TLS, disable weak auth modes, enforce approval thresholds, and apply risk-tier policies for tools/models.

## Observability

- **End-to-end audit log**: Immutable, queryable audit events for auth, approvals, policy decisions, model calls, tool executions, and admin changes.
- **Trace IDs across components**: Single trace ID per request linking gateway, policy evaluation, upstream calls, and persistence for fast debugging.
- **Policy decision explainability**: Store decision inputs/outputs (with safe redaction) and “why” reasons for allow/deny outcomes.
- **Usage & cost reporting**: Token/request metrics by tenant, actor, model/tool, and time window to support chargeback and budgeting.
- **Quota & rate-limit dashboards**: Visibility into throttles, limit breaches, and top consumers with actionable recommendations.
- **Reliability monitoring & alerting**: SLO-oriented metrics (latency, error rate, dependency health) with alerts and incident timelines.
- **Security analytics**: Detection views for anomalous access, repeated denials, risky tool usage, and suspicious credential patterns.
- **Exportable compliance reports**: One-click export of audit and access posture for a tenant, including policy versions and approvals history.

## Governance

- **Policy ownership & approvals**: Every policy has an owner, reviewers, and a required approval step before it can be published.
- **Policy change history**: Track policy versions, what changed, who changed it, and when; allow rollbacks to a known-good version.
- **Policy testing gates**: Require test cases to pass before publish; keep a record of test inputs and results for each version.
- **Exception workflow**: Allow time-bound exceptions (who/what/why) with automatic expiry and audit logging.
- **Compliance evidence export**: Export an “audit packet” for a time range (policies, approvals, tests, and enforcement/audit events) for SOC2/ISO reviews.

## End User UI

- **Guided sign-in + tenant context**: Clear role selection (admin vs requester), SSO entrypoint, and visible tenant/identity context to prevent mistakes.
- **Model invocation workspace**: A simple “chat” experience with selectable approved models, parameter controls, and readable responses plus raw payload for debugging.
- **Tool execution workspace**: Form-driven tool selection with JSON schema-aware input assistance and clear output rendering (success vs error).
- **Access requests in-product**: Request access to models/tools with justification and status tracking; show what’s available vs what requires approval.
- **My grants & entitlements**: A single view of what the user can do (models/tools) with limits/quotas and expiration/renewal prompts.
- **Policy-aware error messaging**: Denials explain “what happened” and “how to fix” (e.g., request access, change input, contact admin) without leaking sensitive policy details.
- **Safety & data handling UX**: Prominent indicators for sensitive-data handling (redaction, retention), plus “copy/share” affordances that respect governance constraints.
- **Activity history for users**: Per-user request history (models/tools) with trace IDs and lightweight replay context for support and self-diagnosis.

