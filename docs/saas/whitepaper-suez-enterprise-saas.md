# Suez: Enterprise SaaS for Governed Model and Tool Access

**A technical white paper for software architects and engineering leads**

---

## 1. Context and audience

Enterprises that adopt third-party or internal language models and tools face a recurring design problem: how to expose those capabilities to teams and applications in a controlled way. Ad hoc API keys, scattered credentials, and per-application policy logic do not scale. Compliance and audit require a single place where access decisions are made and recorded.

This white paper describes **Suez** as an enterprise SaaS product that addresses that problem. It is written for **software architects**, **platform engineers**, and **security leads** who need a clear picture of what the system does, how it is structured, and how it fits into an existing AWS and identity landscape. The document avoids marketing language and focuses on architecture, data flow, and operational boundaries.

---

## 2. ELI5: What is Suez?

**ELI5 (Explain Like I’m 5)**

Imagine a building with a **front desk**. Before you use anything inside—the gym, the library, the printer—you stop at the front desk. The person there checks:

- **Who you are** (your badge or name).
- **Whether you’re allowed** to use the thing you asked for (some people can use the gym, others can’t; some can use the printer, others can’t).
- **What you did** (they write it down so someone can check later).

**Suez is that front desk for models and tools.**

- **Models** = services that answer questions or generate text (like the gym or library).
- **Tools** = services that do a specific job (like the printer).
- **Front desk** = one place (the “gateway”) that:
  - Checks who you are (identity).
  - Decides if you’re allowed to use that model or tool (policy and approvals).
  - Records what you did (audit).

Your company runs **one** front desk. Different teams (tenants) use the same building, but each team has its own list of who can use what. A team’s admin can add users and decide which models and tools those users can use. Everything still goes through the same front desk, so the company has one place to see who did what and to enforce rules.

**In one sentence:** Suez is a single gateway that checks identity, applies policy, and records usage whenever someone in your organization calls a model or a tool.

---

## 3. Problem statement

Organizations that standardize on language models and tool-calling APIs need:

1. **Centralized access control**  
   Decisions about who can call which model or tool should be made in one system, not duplicated in every application.

2. **Auditability**  
   Every call (who, what, when, outcome) should be logged in a form suitable for compliance and incident review.

3. **Policy in one place**  
   Business rules (e.g. “this model only for this group”, “this tool requires approval”) should be defined and updated without redeploying applications.

4. **Multi-tenant isolation**  
   Multiple business units or customers (tenants) can share the same platform while keeping data and permissions separate.

5. **Integration with existing identity**  
   Users should sign in with the same credentials they already use (e.g. corporate SSO), and programmatic access should use issued API keys tied to identity.

Suez is built to satisfy these requirements. It does not replace your models or tools; it sits in front of them and enforces governance before forwarding requests.

---

## 4. Product overview

### 4.1 What Suez is

- **A gateway**  
  All traffic to governed models and tools goes through a single runtime (the “gateway”). The gateway resolves identity, checks grants and policy, enforces quotas, and optionally caches responses. It then forwards allowed requests to the underlying provider (e.g. OpenAI-compatible API, Groq, or in-process stubs).

- **A control plane**  
  A separate API and data store hold configuration: tenants, users, models, tools, access requests and grants, and policy definitions. The control plane is the source of truth for “who can use what”; the gateway consults it (e.g. for identity resolution and grant checks) but does not store long-lived config.

- **A policy engine**  
  Authorization decisions are made by a policy engine (Open Policy Agent, OPA). Policies are written in a declarative language (Rego). They can be drafted from natural language using an open source LLM (Ollama), then validated, tested, and published. The gateway calls the policy engine on each relevant request.

- **A portal**  
  A web UI lets tenant admins and users manage access requests, view grants, invoke models and tools, and view audit data. Admins manage catalog, approvals, and policy lifecycle.

### 4.2 What Suez is not

- It is **not** a model training or hosting platform. It routes to existing model and tool endpoints.
- It is **not** a generic API management product. It is specialized for model and tool governance (approvals, quotas, policy, audit).
- It does **not** require you to move all traffic through it; you can adopt it for a subset of use cases and expand over time.

---

## 5. Architecture

### 5.1 Layering

- **Edge and auth**  
  Traffic enters via a load balancer (in AWS, an ALB). TLS is terminated at the edge. Authentication for the portal can be API key, JWT, or SAML SSO (e.g. AWS IAM Identity Center with Cognito). The gateway does not implement SAML; it consumes tokens or API keys and delegates identity resolution to the control plane.

- **Control plane**  
  Stateless API service. It owns: tenant and user lifecycle, API keys, catalog (models and tools), access requests and grants, policy drafts and published policy, and audit log storage. It exposes an internal endpoint used by the gateway to resolve an incoming credential (API key or JWT) to a structured identity context (tenant, actor, roles). All tenant-scoped data is keyed by `tenant_id`; row-level isolation is enforced in the data layer.

- **Gateway**  
  Stateless runtime. For each request it: resolves identity via the control plane, loads model or tool metadata and checks grants from the same data store, calls the policy engine (OPA) with identity and resource context, enforces quotas (e.g. Redis-backed rate and token limits), and optionally checks response cache. It then calls the upstream provider or tool and records the outcome in the audit log.

- **Policy engine (OPA)**  
  Receives a structured input (action, identity, resource, request payload) and returns allow/deny and reasons. Policies are versioned and published from the control plane; the gateway uses the published bundle.

- **Data**  
  Relational store (PostgreSQL) for config and audit; cache and quota counters in Redis. In AWS, these are RDS and ElastiCache. Tenant isolation is by `tenant_id` and, where needed, row-level security.

### 5.2 Multi-tenancy

- **Tenant** = one customer or business unit. All users, models, tools, requests, grants, and audit rows for that tenant are scoped by `tenant_id`.
- **Platform admin**  
  Operates across tenants (create/suspend tenants, platform config, support). Not tenant-scoped.
- **Tenant admin**  
  Operates only within one tenant: provision users, manage API keys, register models and tools, approve or deny access requests, manage policy, view tenant audit.
- **User (tenant user)**  
  Requests access to models/tools, uses granted resources, and (when enabled) runs agents. No access to other tenants’ data.

The gateway and control plane enforce tenant scope on every request; the UI and API only return data for the caller’s tenant (or all tenants for platform admin).

### 5.3 Identity and authorization flow

1. Caller sends a request to the gateway with an API key or Bearer JWT.
2. Gateway calls the control plane’s internal resolve endpoint with that credential.
3. Control plane validates the credential, looks up tenant and user, and returns an identity context (tenant id, actor, roles, etc.).
4. Gateway loads the requested resource (model or tool) and checks that the identity has an active grant for it.
5. Gateway calls OPA with action, identity, resource, and request payload; OPA returns allow or deny and reasons.
6. If allowed, gateway enforces quotas, then forwards to the provider or tool and records the result in the audit log.

All of this is synchronous from the caller’s perspective; the gateway returns the provider’s response (or an error) after the checks.

### 5.4 Agents

The product supports **agents**: workflows that combine one or more model calls and tool calls in a loop (e.g. plan → call model → call tool → repeat). Agent definitions are tenant-scoped. Execution still goes through the gateway for each model and tool call, so the same governance (grants, policy, quotas, audit) applies. Agents can be implemented as a runner in the gateway or a separate service that issues gateway requests; optional integration with a provider’s agent runtime (e.g. Bedrock) can sit behind the same gateway boundary.

---

## 6. Security and compliance

- **Identity**  
  No long-lived credentials are stored in the gateway. Identity is resolved on each request. API keys are hashed at rest; JWTs are validated (signature, issuer, audience) and then discarded. SAML is handled by an IdP and Cognito; the application only sees JWTs.

- **Least privilege**  
  The gateway and control plane need only the permissions required to read/write their data stores, call OPA, and (for the gateway) call upstream providers. In AWS, EKS workloads use IRSA; no broad account roles.

- **Audit**  
  Every governed action (model invocation, tool execution, access decision, policy publish) is written to an audit log with tenant, actor, action, resource, decision, and trace id. Logs are queryable and can be exported for SIEM or compliance tooling.

- **Tenant isolation**  
  Data is partitioned by `tenant_id`. Queries and APIs are scoped to the caller’s tenant. Row-level security in the database can add a second layer of enforcement.

- **Policy as code**  
  Authorization rules live in versioned, testable Rego. Changes are validated and tested before publish; rollback is supported. This gives a clear audit trail for who changed policy and when.

---

## 7. Deployment model (AWS)

The enterprise SaaS deployment is EKS-based:

- **Compute**  
  Control plane, gateway, OPA, and (optionally) an open source LLM for policy drafting run as Kubernetes deployments. Scaling is by pod count or cluster autoscaler.

- **Data**  
  PostgreSQL (RDS) and Redis (ElastiCache) in private subnets. Access from EKS only; credentials from Secrets Manager, synced into the cluster via an operator (e.g. External Secrets).

- **Edge**  
  ALB with TLS (ACM). Path-based routing to control plane and gateway. Portal static assets from S3 and CloudFront.

- **Identity**  
  Optional Cognito User Pool with SAML IdP (e.g. IAM Identity Center). Portal and API accept Bearer JWT; API keys remain supported for programmatic access.

Deployment is automated via CloudFormation; a separate implementation plan describes stack order, IAM (including a dedicated deployment user with least-privilege policies), and operational steps. The platform is designed so that a single deployment serves multiple tenants; there is no per-tenant cluster or database by default.

---

## 8. Summary

Suez provides a **single governance layer** for model and tool access: one gateway, one control plane, one policy engine, and one audit log. It is built for **multi-tenant** use, with clear roles (platform admin, tenant admin, user) and tenant-scoped data. Authorization is **declarative and versioned** (OPA/Rego); policy can be drafted from natural language using an open source LLM and then validated and published. **Identity** is resolved at request time from API keys or JWTs (including SSO); no credential sprawl in applications. **Audit** is first-class and suitable for compliance. The **deployment model** is cloud-native (EKS, RDS, ElastiCache, ALB, optional Cognito) and can be provisioned with restricted IAM and a `.env`-driven workflow for credentials.

For detailed architecture diagrams, data models, and implementation steps, see the companion documents **../gateway-architecture.md** (local/Compose architecture), **enterprise-saas-aws.md**, and **implementation-plan-aws.md** in this directory.
