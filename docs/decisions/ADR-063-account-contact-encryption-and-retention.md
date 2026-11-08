# ADR-063: Account contact encryption and retention

- Status: Accepted; contact rows implemented in the account contacts PR.
  Account events (planned) follow the same rule for attendee data.
- Decision date: 2026-11-08.
- Related: ADR-015 (Account entity; its member-email exception is not a waiver
  for new personal data), ADR-045 (connector writes).

## Context

Account contacts hold third-party personal data imported from business cards:
names, emails, phone, fax and address, and free-form notes up to 8 KiB.
`ttobak-main` uses the default AWS-owned key (`infra/lib/storage-stack.ts`), so
table-level encryption alone does not meet the project rule that sensitive
DynamoDB data needs KMS encryption and a retention design.

## Decision

- Contact fields are envelope-encrypted in the API before any DynamoDB write
  (`backend/internal/sealing`). Each row gets a fresh AES-256-GCM data key from
  `GenerateDataKey` on the existing AiStack key (`KMS_KEY_ID`, already granted
  to the API role via `grantEncryptDecrypt`).
- The KMS encryption context and the GCM additional data name the record
  (`purpose`, `accountId`, `contactId`), so a sealed value cannot be opened for
  another account or contact.
- At rest a row keeps only `PK`, `SK`, `contactId`, `revision` and `sealed`.
  Revision stays plaintext for conditional writes; it carries no personal data.
- Without a configured key, contact reads and writes return 503
  `CONTACTS_UNAVAILABLE` and never touch plaintext. Unsealed rows are not served.
- Connector receipts (`CONNECTOR_MUTATION#`) store identifiers and an
  arguments hash only, never contact fields.
- Retention: a contact remains until a direct member deletes it. There is no
  TTL. Deleted data can persist in point-in-time recovery for the table's
  recovery window (35 days) and in KMS-protected form only.

## Consequences

- A contact list page makes up to 25 KMS `Decrypt` calls (8 concurrent); a
  write makes one `GenerateDataKey` call.
- Disabling or deleting the key makes contacts unreadable; key deletion must
  follow the KMS waiting-period process like any other KMS-protected data.
- Server-side search over contact fields is not possible; duplicate detection
  stays in the client after an authorized list.
- Account deletion has no cleanup routine today; if one is added it must delete
  `CONTACT#` rows.
