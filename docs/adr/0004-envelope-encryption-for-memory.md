# 0004. Envelope encryption and redaction for SUEZ Memory

- **Status:** Accepted
- **Date:** 2026-11-10

## Context

SUEZ Memory stores conversation evidence, summaries and checkpoints. That
content can include customer data and pasted secrets. A database backup or a
read-only SQL leak should not expose it in plain text.

## Decision

- **Encryption:**
  - Content columns use AES-256-GCM envelope encryption. Each record gets a
    random data key, and that key is wrapped by a key-encryption key.
  - The key-encryption key comes from a `keyProvider`. The shipped provider
    is `local`, which reads `SUEZ_DATA_KEY`; `SUEZ_DATA_KEY_PREVIOUS` allows
    rotation.
  - The ciphertext is bound to its tenant and row as additional
    authenticated data, so it cannot be copied into another tenant or row.
- **Redaction:** before storage, a pattern-based redactor removes:
  - common API-key formats and bearer tokens;
  - private keys;
  - connection strings;
  - `password=`-style assignments.
- **Retention:** settings are per tenant, and an hourly job enforces them.
  Export and deletion are available through the API.

## Consequences

- Backups and database reads expose ciphertext, not content.
- Losing `SUEZ_DATA_KEY` makes stored memory unreadable. It must be backed up
  separately from the database (see
  [BACKUP-RESTORE](../production/BACKUP-RESTORE.md)).
- Redaction is best-effort, and unusual secret formats can pass. Encryption
  still applies to them.
- A cloud KMS provider is not implemented yet. The `keyProvider` interface is
  the place to add one.

## Alternatives considered

- **PostgreSQL TDE or disk encryption only:** this does not protect against
  SQL-level reads or logical backups.
- **pgcrypto in the database:** this puts keys inside the database session.
