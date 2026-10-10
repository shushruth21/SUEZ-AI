# Security and Privacy

This document describes how SUEZ handles credentials and data **as
implemented**, and its known security limitations. For reporting
vulnerabilities see [SECURITY.md](../SECURITY.md).

## Summary

| Area | Context Guardian (desktop) | Gateway stack (local) |
| --- | --- | --- |
| Credentials | Gateway API key in the OS credential store (macOS Keychain), accessed only from Rust | Provider keys from env vars or `infra/secrets/*`; client API keys stored as SHA-256 hashes |
| Data at rest | Plain JSON in the app-data directory (not encrypted by the app) | PostgreSQL volume, Redis (not encrypted by SUEZ) |
| Network | HTTP(S) to the configured gateway URL only | Local Docker network; HTTPS edge with a locally generated certificate |
| Hardening | Secret guard on save and copy; key scrubbing in errors | **Not production-hardened** (see below) |

## Context Guardian

### Credential handling

- The gateway API key is sent from the UI to Rust **once**, when you save it,
  and stored in the OS credential store (service
  `ai.acrosystems.suez.contextguardian`, account `gateway-api-key`).
- It is **never** returned to the UI, written to `settings.json`, checkpoints,
  project memory, revisions, logs or error messages. Error text from the
  gateway is scrubbed of the key before display.
- Gateway requests are made by Rust with the key in the `X-API-Key` header and
  a 5 s timeout.
- **Secret guard:** Rust refuses to save a checkpoint or project memory, and
  refuses to copy a continuation prompt, if the text contains the stored key
  (raw or JSON-escaped). If the credential store cannot be read, the guard
  cannot compare and the save proceeds — drafts are never dropped.
- The guard only knows the gateway key. It does **not** detect other secrets
  you paste into a checkpoint (provider keys, passwords). Do not paste secrets
  into checkpoints or memory.

### Local data

- Checkpoints, backups, project memory, revisions, quarantined files and
  settings are stored as plain JSON under the app-data directory (macOS:
  `~/Library/Application Support/ai.acrosystems.suez.contextguardian/`). The app
  does not encrypt them; rely on OS account security and disk encryption
  (FileVault).
- The app **never uploads** checkpoints, memory or prompts. A continuation
  prompt leaves the machine only if you paste it somewhere.
- Treat these files as private: they can contain project details, file paths
  and anything you typed. Do not attach them to issues or commit them.

### What is sent to the gateway

| Sent | Received |
| --- | --- |
| `GET /v1/context/usage?session=<session id>` with `X-API-Key` | Token counts, percent, context window, model name, provider, measurement source, cache status, timestamps, status/message |
| `GET /v1/identity` (connection test) | The identity the key resolves to |

The app accepts both `http://` and `https://` gateway URLs for any host. Use
`https://` for any gateway that is not on your own machine, otherwise the API
key travels in clear text.

### Platform notes

- Tested on macOS only. The build is ad-hoc signed and not notarized; macOS may
  re-prompt for Keychain access after each rebuild.

## Gateway stack

### What it protects

- Every `/v1` route requires an API key or JWT, resolved by the control plane
  into a tenant/actor identity.
- Chat and tool calls require an active access grant, pass an OPA decision and
  per-minute quotas, and are written to the audit log.
- Context-usage measurements are only returned to the **same tenant and
  actor** that created them.
- API keys are stored hashed (SHA-256).

### Known limitations (local-development runtime)

| Limitation | Risk | Mitigation |
| --- | --- | --- |
| **JWT signatures are not verified** — claims are decoded only | Anyone who can reach the API can impersonate any user with a crafted JWT | Never expose the stack to untrusted networks; implement verification before any shared deployment |
| Demo API key and demo identities are seeded (`suez-demo-admin-key`, `*@example.com`) | Well-known admin credential | Local use only; create new keys and remove demo data for anything shared |
| Default DB password `suez` | Trivial DB access if the port is reachable | Set `POSTGRES_PASSWORD` in `.env` |
| All ports published on `0.0.0.0` | Services reachable from the local network | Bind to `127.0.0.1` or firewall |
| `Access-Control-Allow-Origin: *` on gateway and control plane | Any website can call the APIs from a browser that holds credentials | Restrict origins before sharing |
| Self-signed / locally trusted TLS on the edge; internal traffic is plain HTTP | Not suitable beyond a single machine | Use real certificates and TLS between services |
| Chat messages are forwarded to external providers for provider-backed models; responses are cached in Redis for 60 s | Prompt content leaves the machine / persists briefly | Use the stub or local models for sensitive data |
| Tool inputs and outputs are stored in `tool_executions` | Sensitive tool data persists in PostgreSQL | Avoid sensitive payloads; plan a retention policy |
| Audit log and usage measurements have no retention policy | Unbounded growth, long-lived metadata | See [DATA-RETENTION.md](DATA-RETENTION.md) |

### Secrets in this repository

- `.env`, `.env.*` (except `.env.example`), `infra/secrets/*`, generated TLS
  material, key/certificate files, dumps and local app data are git-ignored.
- Example templates contain placeholders only. Demo values in seeds and tests
  (`suez-demo-admin-key`, `suez`/`suez`, `cgtest`) are intentionally public and
  for local use only.
- If a real secret is ever committed, rotate it — deleting it in a later commit
  does not remove it from Git history.

## Provider interactions

Only models configured with provider `openai-compatible` or `groq` cause
outbound calls; these may be **billable**. The `stub` provider is in-process
and free. No test or CI job calls a paid provider.
