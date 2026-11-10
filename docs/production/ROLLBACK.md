# Rollback runbook

## Application release

1. Identify the last good version (image tag or git commit).
2. Check the CHANGELOG between the two versions for migrations. All
   migrations up to this release are additive, so older services run on the
   newer schema; do not try to "down-migrate".
3. Deploy the previous images: `git checkout <good>` and
   `docker compose --env-file .env.production -f infra/compose/docker-compose.prod.yml up -d --build --wait`.
4. Verify readiness through the edge and run a chat and a key check.
5. Only if the schema itself is damaged: restore the pre-upgrade backup ([BACKUP-RESTORE.md](BACKUP-RESTORE.md)). This loses writes made since the backup.

## Policy change

- `POST /api/v1/policies/{id}/rollback` (permission `policy.manage`) republishes an earlier version of a tenant's policy as a new version. It runs the stored policy tests first and refuses if they fail.
- Effect is immediate for that tenant only. Check with a chat request or `GET /api/v1/audit-log?action=policy`.
- If OPA itself is inconsistent (for example after an OPA restart), the reconciler restores the published set within 30 s; restarting the control plane forces an immediate sync.

## Context or memory setting

`PUT /api/v1/memory/settings` and `PUT /api/v1/memory/context-policies` take
effect on the next request; the previous values are in the audit log
(`memory.settings.update`, `context_policy.update`).
