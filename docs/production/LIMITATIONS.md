# Remaining limitations (verified)

Each item is a real property of this release, checked in code or by test.

1. **No cloud KMS integration.** Envelope encryption ships with the `local` key provider (key-encryption key in `SUEZ_DATA_KEY`). The `keyProvider` interface in `apps/control-plane/cmd/server/crypto.go` is the seam for AWS KMS / GCP KMS / Vault; none is implemented. Rotating requires keeping old keys in `SUEZ_DATA_KEY_PREVIOUS`; there is no re-wrap job.
2. **Tool targets can reach internal addresses.** A tenant admin registers tool URLs; the gateway follows no redirects, but a URL pointing at an internal service is called. Restrict the gateway's egress (network policy / firewall) to the tool hosts you allow.
3. **Secret redaction is pattern-based.** Common key formats, bearer tokens, private keys, connection strings and `password=`-style assignments are removed before memory storage; unusual secrets can pass. Content is encrypted at rest regardless.
4. **Checkpoint drafts are extractive, not summarized.** Threshold drafts copy and cite stored records (objective, last state, last request, tool outputs); decisions, assumptions and risks are left for the reviewer. No LLM summarizer is wired in.
5. **Real provider not exercised.** Provider calls were verified against a protocol-faithful mock only. Streamed usage counts depend on the provider honouring `stream_options.include_usage`; without it the gateway uses its own conservative estimate (labelled `gateway_estimate`).
6. **Multi-replica coordination not load-tested.** Policy changes are serialized with a PostgreSQL advisory lock and reconciled every 30 s; this was reviewed and exercised on one replica only. The control plane's per-IP rate limit is per instance.
7. **Context percentages need a configured window.** Models without `contextWindow` report token counts but no percentage, so no thresholds or hard block apply.
8. **Audit writes are best-effort.** A failed audit insert is logged with the trace id but does not fail the request.
9. **Token revocation is user-level.** There is no per-token denylist; deactivating a user or the IdP session ends access (checked on every request), otherwise a token is valid until `exp`.
10. **Tenant deletion is a database operation.** There is no API to delete a tenant.
11. **Claude-Mem and REA are not integrated.** SUEZ Memory is a native implementation; no Claude-Mem importer was built (it would be opt-in, developer-only). REA was not used.
12. **Native desktop smoke test not re-run** this pass (needs a desktop session); the app's TypeScript and Rust unit tests and the server APIs it uses are covered.
13. **Development stack conveniences.** `infra/compose/docker-compose.yml` enables demo credentials, a shared-secret JWT mode and permissive CORS by design; it binds to 127.0.0.1 by default and must not be deployed.
