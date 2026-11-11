# 0002. One permission table, and scoped API keys

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

Authorization used to be scattered through the handlers as role-name checks.
Adding a route meant remembering to add a check, and API keys inherited
everything their tenant could do. A leaked inference key was therefore an
admin key.

## Decision

- Every control-plane route maps to exactly one permission in
  `routePermission` (`apps/control-plane/cmd/server/authz.go`). A route
  missing from the table is **refused**. The gateway checks the same
  permission names for its own routes.
- Roles and API-key scopes are only named bundles of permissions. Nothing
  checks a role name directly.
- API keys:
  - are random and stored only as SHA-256 hashes;
  - carry scopes, an optional expiry, model and tool allowlists, and a rate
    limit;
  - record who created them and can be revoked.
- A key can only be given scopes whose permissions its creator already holds,
  so keys can never escalate.

## Consequences

- Authorization can be reviewed in one file, and new routes are denied until
  someone adds them to the table on purpose.
- Clients can be given least-privilege keys; for example, Context Guardian
  uses `gateway-inference`.
- Tests assert the table, so a missing entry fails CI.

## Alternatives considered

- **Middleware annotations per handler:** they scatter the policy again.
- **OPA for control-plane authorization:** this adds latency and an extra
  dependency to every admin call. OPA stays the place for tenant-authored
  runtime policy (see ADR 0003).
