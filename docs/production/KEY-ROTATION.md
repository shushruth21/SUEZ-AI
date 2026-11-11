# Key rotation

| Secret | How | Downtime |
| --- | --- | --- |
| SUEZ API key | Create a new key with the same scope, switch the client, `DELETE /api/v1/api-keys/{old}` | None |
| Bootstrap key | Create named admin keys/users, revoke the bootstrap key (it also expires after 30 days by default) | None |
| Identity provider signing key | Publish the new key in the JWKS before signing with it. SUEZ fetches an unknown `kid` once (at most every 30 s); removed keys stop working when the cache expires | None |
| `SUEZ_INTERNAL_TOKEN` | Set the new value on both services and restart both together | Brief (requests in flight between restarts get 503) |
| Data key (`SUEZ_DATA_KEY`) | 1. Generate a new key file. 2. Set it as `SUEZ_DATA_KEY` and add the old key to `SUEZ_DATA_KEY_PREVIOUS`. 3. Restart the control plane. New content uses the new key; old content stays readable. 4. Keep the old key until all content sealed with it has expired or been deleted (memory retention period) | Restart only |
| Provider API keys | Replace the secret file; restart the gateway | Restart only |
| PostgreSQL / Redis passwords | Change in the database, update `.env.production`, restart services | Restart only |

Verified: `TestContentSealer` covers reading old content after data-key
rotation and refusing it when the previous key is not configured;
`TestJWKSRotationAndFailClosed` covers IdP key rotation.

Re-wrapping existing items under a new key-encryption key without keeping the
old one is not implemented in this release (see LIMITATIONS).
