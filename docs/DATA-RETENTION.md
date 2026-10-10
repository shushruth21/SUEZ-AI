# Data Retention

This page describes **current behaviour**. Nothing here is a policy commitment;
proposed changes are listed separately at the end.

## Context Guardian (local machine)

| Data | Location (app-data dir) | Retention today |
| --- | --- | --- |
| Checkpoints | `checkpoints/<id>.json` | Until you delete them in the app (Delete) or on disk |
| v1 originals backed up before an upgrade | `checkpoints/.backups/<id>.v1.<ms>.json` | **Indefinitely**; deleting a checkpoint keeps its backup. No UI; remove by hand |
| Project memory (current) | `projects/<id>/memory.json` | Until deleted on disk; the app has no "delete project" action |
| Memory revisions | `projects/<id>/revisions/<n>.json` | Latest **50** per project; older revisions are deleted automatically when a new one is written. The current memory is never deleted by retention |
| Quarantined unreadable memory | `projects/<id>/memory.corrupt-<ms>.json` | Indefinitely; never deleted by the app |
| Settings | `settings.json` | Until deleted |
| Gateway API key | OS credential store | Until **Remove key** in Settings |
| Clipboard | System clipboard | Until overwritten (the app does not clear it) |

Unreadable or future-version checkpoint files are never deleted or rewritten.

## Gateway stack (your Docker host)

| Data | Store | Retention today |
| --- | --- | --- |
| Tenants, API keys (hashed), models, tools, grants, policies, policy tests | PostgreSQL | Until changed or deleted through the APIs/portal |
| Audit log | PostgreSQL `audit_log` | **Indefinitely** (no cleanup job) |
| Tool executions, **including tool input and output payloads** | PostgreSQL `tool_executions` | **Indefinitely** |
| Context-usage measurements | PostgreSQL `context_usage_measurements` (after migration 0006) | **Indefinitely** (no cleanup job). Contains tenant, actor, session id, model, token counts, trace id — not message content |
| Cached chat responses | Redis | **60 seconds** |
| Quota counters | Redis | Per-minute keys |
| Database backups / audit exports | `tmp/` by default (git-ignored) | Until you delete them |
| Everything | Docker volume `postgres-data` | `make clean` deletes it (irreversible) |

Chat message content is not written to PostgreSQL (the audit row holds token
counts, provider and session id); it is held in Redis only as part of a cached
response. Tool inputs and outputs **are** stored in `tool_executions`.

## Proposed (not implemented)

- A configurable retention period and cleanup job for
  `context_usage_measurements` and `audit_log`.
- A UI to list, restore and prune checkpoint backups.
- A "delete project" action that archives memory and revisions.
