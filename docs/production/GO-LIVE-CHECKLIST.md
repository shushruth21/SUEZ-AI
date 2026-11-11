# Go-live checklist

Tick every item on the production deployment, not on a test stack.

## Configuration
- [ ] `SUEZ_ENV=production` (or unset) on both services; both started without configuration errors.
- [ ] `JWT_MODE` is `jwks` with your IdP's issuer and audience, or `disabled` if only API keys are used.
- [ ] `SUEZ_DATA_KEY` set from a file; a copy is in the secret manager **separately** from database backups.
- [ ] `SUEZ_INTERNAL_TOKEN` ≥ 32 random characters, identical on gateway and control plane.
- [ ] `CORS_ALLOWED_ORIGINS` lists only your https origins; `TRUSTED_PROXY_CIDRS` lists only your edge/load balancer.
- [ ] Database password is strong; managed database uses `sslmode=verify-full`.
- [ ] Redis requires a password and is not reachable from outside.
- [ ] No seeds applied (`select name from schema_migrations where kind = 'seed'` returns nothing).

## Exposure
- [ ] Only the edge has a public listener (`docker compose ps` / cloud security groups).
- [ ] `https://<host>/opa/v1/policies`, `/control-plane/internal/auth/resolve`, `/gateway/internal/x` → 404.
- [ ] HSTS, CSP, X-Frame-Options and nosniff headers present on `https://<host>/`.
- [ ] `https://<host>/control-plane/api/v1/bootstrap/status` returns only service and status.
- [ ] Gateway egress is limited to your providers, IdP and approved tool hosts.

## Identity and access
- [ ] Bootstrap key replaced by named admin users/keys and revoked.
- [ ] Every person who signs in has a `users` row with the least role they need.
- [ ] Service keys use the narrowest scope (`gateway-inference`, `read-only`, `audit-reader`) and, where possible, model/tool allowlists, a rate limit and an expiry.
- [ ] A forged/unsigned token and a revoked key are refused (401) — try both once.

## Governance
- [ ] Each model has `contextWindow` and sensible `maxRpm`/`maxTpm`.
- [ ] Tenant context policies and memory settings reviewed (capture and injection are off by default).
- [ ] At least one policy test exists for every published policy.

## Operations
- [ ] `/readyz` monitored for both services; alerts on `level=ERROR` and on 503 rates.
- [ ] Database backups scheduled, encrypted, and one restore drill completed with the data key.
- [ ] Retention periods agreed with your data owner (memory, audit, usage) and set per tenant.
- [ ] Runbooks ([ROLLBACK](ROLLBACK.md), [INCIDENT-RESPONSE](INCIDENT-RESPONSE.md), [KEY-ROTATION](KEY-ROTATION.md)) read by the on-call team.
- [ ] Items in [LIMITATIONS.md](LIMITATIONS.md) accepted or mitigated (especially KMS, tool egress, real-provider test).
