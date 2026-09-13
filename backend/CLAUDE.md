# Go backend

Follow the root guide. `cmd/` has eight zip Lambda entry points, a separate
`convert-doc` container, and maintenance commands; do not treat every directory
as a zip deployment. GatewayStack defines artifact packaging.

HTTP handlers call services, which call repositories. Model files define keys
and request/response contracts. Use Go's DynamoDB expression builder, conditional
updates for shared fields, transactions for relation sets/reverse refs, pagination,
and sentinel errors checked through `errors.Is`.

Go chi integration requires API Gateway payload 1.0. Python QA is separate and uses
2.0. Existing OriginVerify, verified JWT parsing, ownership checks and service
permissions must be inspected before alleging an authorization bypass.

## Current contracts

- WebSocket `$connect` requires the CloudFront origin proof and verified JWT.
  `WS_ORIGIN_SECRET_ARN` identifies a Secrets Manager value; no secret material is
  in Lambda env. Missing config/proof, duplicate headers, lookup errors and expired
  cache refresh failures deny access. The secret cache never caches JWT decisions.
- `UpdateMeetingFieldsIfMatch` copies input fields and spills to immutable
  `{field}.{32-lowercase-hex}.txt` keys. Conditional publication never overwrites
  referenced objects. Disable SDK retries when new spills make commit ambiguity
  material: definite rejection cleans only new objects, ambiguous responses retain
  them, and cleanup failures surface. Legacy converging writers retain fixed keys.
  Go/QA readers pin bucket, lookup meeting and exact field/version grammar; deploy
  those readers before writers (ADR-037).
- Bounded meeting reading uses metadata views for authorization, notes/summary and
  action state; hydrate only the requested transcript and eligible segments after
  access checks. Preserve exact Unicode-code-point pages, current revision/cursor
  binding, the 14,000-byte JSON limit and verified whole-segment timing.
- Action analysis uses MEETING#/ANALYSIS#actionItems and the existing summarize
  Lambda. Run/source/prior-item conditions atomically publish items plus success.
  Keep unchanged IDs/completion and prior results on failure; legacy state is
  unknown. Retry needs a done meeting and nonblank saved summary, with no transcript
  fallback. Do not infer successful analysis from an empty array.
- Saved notes are untrusted reference input, separately attributed from speech;
  note-only evidence cannot generate transcript anchors or invented agreements.
- Initial meeting preparation uses optional `MeetingPreparation` on create:
  validate notes (32,000 code points) and account membership before the first
  PutItem. Acknowledge persisted request context with `preparationApplied`; keep
  the recording lifecycle and private account classification unchanged.
- Private account linking atomically sets `accountId` and clears
  `sharedToAccount`, including reclassification of a team-shared meeting.
  Preserve notes and independent direct shares; team publication stays explicit.
- Notes-only `expectedNotes` updates use `UpdateMeetingNotesIfMatch`: require the
  meeting to exist, match notes exactly (absent also matches expected empty),
  and return 409 CONFLICT on a failed comparison. Optional `expectedNotesRevision`
  also matches the version atomically (empty matches absent legacy versions).
  Every notes write, including legacy and same-text writes, creates a fresh
  server-owned UUID `notesRevision`; clients cannot assign it. Create/detail/update
  and bounded notes reads expose the version. Notes cursors bind it as well as
  text, and `supportsNotesComparison` means revision-capable comparison.
  Generic field CAS stays exact.
- Meeting detail exposes account classification and bounded `fieldInsights`;
  project IDs are owner-only. Evidence remains meeting-authorized, freshness is
  unknown, and malformed/truncated insight projections are explicit. KB file
  listing follows all S3 pages and propagates failures instead of partial success.
- Attachment queue/status/read services bind canonical ATTACH#/ATTEXT# rows,
  owner/uploader, run/lease and original ETag to immutable extraction results.
  Attempt status differs from retained-result completeness. Upload completion and
  attachment text/status/retry routes instantiate these services; the summary
  provider consumes verified DOCUMENT text. Saved re-summary GET/POST routes use
  run/source/lease CAS and current edit grants; failures retain previous content.
  Code wiring does not establish deployed acceptance.
- Canonical index workers reread identities, bind source/S3 revisions and coalesce
  full S3 sync. Do not equate job acceptance/partial status with indexed documents.
  The activation configuration selects `all` and retains the enabled schedule;
  deployment remains gated on snapshot verification and public strict-QA
  acceptance. The [bootstrap](../docs/runbooks/knowledge-index-bootstrap.md) and
  [QA rollout](../docs/runbooks/qa-current-source-rollout.md) runbooks distinguish
  deployed readiness from remaining acceptance. Existing `/api/kb/*` routes stay
  in the API Lambda; cmd/kb accepts stream/schedule/tick envelopes.

Use [API contracts](../docs/API-SPEC.md),
[index source/provider contracts](internal/service/INDEX_SOURCE_CONTRACT.md),
[binary migration](cmd/kb/KNOWLEDGE_MIGRATION.md), and
[extraction worker contracts](python/document-extract/LAMBDA.md).

## Verification

```bash
/usr/local/go/bin/go test ./... -count=1
/usr/local/go/bin/go vet ./...
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

Use stdlib testing and mock repositories. Tests in `cmd/summarize` and
`cmd/transcribe` are required; `./internal/...` alone is incomplete.
