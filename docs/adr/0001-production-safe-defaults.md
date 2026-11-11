# 0001. Production-safe defaults that refuse unsafe settings

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

The first version of SUEZ was built for local development. It seeded a
well-known admin key, accepted unverified JWT claims, allowed `*` CORS and
published every port. Those are the right defaults for a laptop and the wrong
ones for anything shared. If a deployment forgot to override one of them, the
system was still running, and still exposed.

## Decision

- `SUEZ_ENV` defaults to `production`. Development behaviour must be asked for
  explicitly with `SUEZ_ENV=development`.
- At startup in production, the services validate their configuration and
  **refuse to start** if they find:
  - demo credentials, or a request to expose them;
  - `JWT_MODE=dev-hs256`;
  - `*` CORS;
  - a missing or short internal token;
  - a missing data key.
- Development, production, staging and e2e each have their own compose file.
  Only the production and staging files are built for exposure, and in those
  only the edge is published.

## Consequences

- A misconfigured deployment fails loudly at startup, not quietly at runtime.
- Developers set `SUEZ_ENV=development`. The dev compose file already does.
- Every new unsafe convenience must be added to the startup validation.

## Alternatives considered

- **Warnings instead of refusal:** rejected, because warnings in logs are
  easy to miss.
- **One compose file with profiles:** rejected, because it makes it too easy
  to deploy development settings.
