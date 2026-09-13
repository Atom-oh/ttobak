# Current search status

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

Readers need to distinguish queued, running, waiting-for-file, indexed and failed
work. Add authenticated status routes for meetings, personal/shared documents
and account documents. Use current owner/direct-share/exact account membership
before the indexing reader can inspect S3. Parent membership is not a grant.
Document share lookup must be strongly consistent after revocation.

Return only `state`, optional fixed `errorCode`, and optional RFC3339 `updatedAt`.
Never expose job keys, raw source fields, leases or provider IDs. A missing job
is `UNTRACKED`, not a successful index. The existing worker status reader checks
current source revisions and projection inventory; stale success becomes pending.
Suppress prior-run errors in pending/success responses. Reads are non-cacheable
and status computation is bounded; failures return a visible 503.

The API reuses existing KB configuration (`KB_ID`, `KB_DATASOURCE_ID`,
`KB_BUCKET_NAME`). Missing configuration affects these status routes only.
This slice depends on the canonical worker implementation; it does not turn on
stream/scheduled delivery. UI and activation follow their own integration gates.

Validation: service tests cover direct-share revocation/forged grants, exact
account membership, authorization before S3/status calls and private-field
projection. Repository wire tests pin strong grant reads. Handler tests verify
identity routing, error redaction and no-store responses.
