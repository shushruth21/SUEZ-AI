# 0003. Per-tenant OPA packages and sandboxed policy tests

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

Tenants author Rego policies. All published policies used to load into the
shared `suez.authz` package, so one tenant's rule could affect another
tenant's decisions, or override the base checks. Policy "tests" also ran
against the live policy set.

## Decision

- Tenant policies must start with `package suez.authz`. The control plane
  rewrites that to `suez.generated.t_<tenant>.p_<policy>` and then checks the
  package that OPA actually parsed. Any other package is rejected.
- The base policy (`infra/opa/policies/suez/phase0.rego`) reads **only** the
  caller's tenant package. Generated modules cannot add rules to the base
  package.
- Drafts are validated and tested in a private sandbox package,
  `suez.sandbox.r_<random>`. The policy the draft replaces is excluded for the
  test, and the sandbox is removed afterwards.
- Publish, rollback and sync are serialized across replicas with a PostgreSQL
  advisory lock. A reconciler re-syncs OPA from the database every 30 seconds.

## Consequences

- One tenant cannot change another tenant's decisions or skip the identity,
  resource and high-risk-tool checks. This is covered by OPA and e2e tests.
- The database is the source of truth, and OPA can be rebuilt from it at any
  time.
- Multi-replica behaviour has been reviewed and exercised on a single replica
  only; it has not been load-tested.

## Alternatives considered

- **One OPA instance per tenant:** stronger isolation, but far more costly to
  operate.
- **A regex check on the package line:** rejected after it was shown to be
  bypassable. The parsed-package check replaced it.
