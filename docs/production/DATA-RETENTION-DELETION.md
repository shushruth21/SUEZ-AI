# Data retention and deletion

## Defaults and controls

| Data | Default retention | Control |
| --- | --- | --- |
| Memory items and checkpoints | 90 days from creation | `retentionDays` in `PUT /api/v1/memory/settings` (applies to new items) |
| Deleted memory tombstones | Content wiped immediately; row removed after 30 days | — |
| Audit log and memory retrieval records | 365 days | `auditRetentionDays` (30–3650) |
| Usage measurements and tool executions | 90 days | `usageRetentionDays` (1–3650) |
| Chat response cache (Redis) | 60 seconds | — |
| Quota counters (Redis) | ~70 seconds | — |

Memory capture is **off** until a tenant admin enables it; injection into
model requests is also off by default. Each user can opt out of capture and
retrieval (`PUT /api/v1/memory/preferences`).

The retention job runs hourly in every control plane and can be triggered
with `POST /api/v1/memory/retention/run` (permission `memory.manage`). It only
removes data already past its period. Verified end to end (e2e `TestI`).

## Deletion requests

| Request | Endpoint | Effect |
| --- | --- | --- |
| One memory item | `DELETE /api/v1/memory/items/{id}` (owner or `memory.manage`) | Content, preview and provenance wiped at once |
| A whole session | `DELETE /api/v1/memory/sessions/{id}` | All items and checkpoints wiped; the session id cannot be reused for capture |
| Export before deletion | `GET /api/v1/memory/sessions/{id}/export` | JSON with items, checkpoints and provenance |
| A user | `PATCH /api/v1/users/{id} {"status":"inactive"}`, then delete their sessions | Stops access immediately |
| A tenant | Database: `DELETE FROM tenants WHERE id = ...` (cascades) | Removes everything owned by the tenant |

Deletions are audited (`memory.session.delete`, `memory.item.delete`). Audit
records themselves are not deleted on request; they expire with
`auditRetentionDays`. Database backups keep deleted data until the backups
expire — set backup retention accordingly.
