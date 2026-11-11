## What and why

<!-- What does this change, and why is it needed? Link the issue: Fixes #123 -->

## How it was tested

<!-- List the commands you ran and their results. Do not list tests you did not run. -->

- [ ] Go: `go vet ./... && go test ./...` (gateway / control plane)
- [ ] OPA: `opa test infra/opa/policies`
- [ ] End-to-end: `sh scripts/e2e.sh`
- [ ] Context Guardian: `npm run typecheck && npm test` / `npm run test:rust`
- [ ] Portal: `npm run build`
- [ ] Manually checked (describe below)

## Risk and rollout

- [ ] Database migration: new numbered file only; existing migrations untouched
- [ ] New route added to `routePermission` (control plane) or the gateway's permission list
- [ ] New configuration documented in `docs/production/CONFIGURATION.md`, with a safe default
- [ ] Security-relevant change. Threat model or ADR updated
- [ ] Breaking API change. Noted in `CHANGELOG.md`

## Checklist

- [ ] No secrets, `.env` files, keys, dumps or real checkpoint/memory data
- [ ] Docs and `CHANGELOG.md` updated where behaviour changed
