# Index source and provider read contracts

Code checked: 2026-09-13. These readers and the worker are implemented.
This activation configuration selects `all` and retains the schedule. The active
QA handler registers source and history readers in REST and streaming paths.
Configuration and registration do not prove deployed acceptance. Follow the
[bootstrap runbook](../../../docs/runbooks/knowledge-index-bootstrap.md) and
[QA rollout](../../../docs/runbooks/qa-current-source-rollout.md) for deployment
evidence and the remaining public acceptance gates.

`IndexSourceReader` takes only `GetIndexSource`, `Head`, and pinned `Read`
interfaces. It reads canonical USER#/MEETING#, USER#/DOC#, and ACCOUNT#/DOC#
resources. It cannot mutate source records, publish projections, or start jobs.
Account document `sourceUserId` is the creation author; later members may replace
the validated file key. The source account binding remains mandatory.

Source revisions frame each UTF-8 string as decimal byte length + `:` + bytes.
Inputs include canonical keys/outcome, sorted present fields (null and empty
remain different), and sorted S3 keys/ETags/version IDs/sizes/converter bindings.
The checked-in vectors are shared with QA. Selected transcript spill keys bind
the configured bucket, meeting and field. Missing bytes, invalid text and stale
PPT previews cannot become successful empty document projections.

`IndexAWSProvider.Documents` uses only **GetKnowledgeBaseDocuments**. It submits
S3 identifiers in batches of at most ten, pins the configured KB/source/bucket,
and matches replies by exact URI. Missing/duplicate/foreign replies and SDK errors
are errors, not document success or deletion. `INDEXED` is full document success;
`PARTIALLY_INDEXED` and metadata partial/failure states are not full success.
`NOT_FOUND` can prove removal of an explicitly requested former projection.
Neither this reader nor that status method calls direct ingestion.

The deployment role needs `bedrock:GetKnowledgeBaseDocuments` on the configured
knowledge-base ARN in addition to existing ingestion-job permissions. S3 data
sources must have completed their initial sync before document-status reads work.
These read prerequisites alone do not establish worker activation or deployment.
Lifecycle callers must still check immutable projection inventory and fresh source
revision/CAS after observing provider status.

`IndexingService.Tick` starts a full sync only when a batch member staged keys or
still carries a removal the provider has not confirmed. That includes a member
detached in this or an earlier tick of the batch, or by a cooldown, whose job
still records removals (for example after cleanup deleted published
projections); an unreadable job record counts as pending. After
cleanup proves projection-prefix and legacy-export objects absent from S3, a
failed job drops removals already reported `NOT_FOUND`; status errors keep them.
Finalization clears a failed job's remaining removals only on provider success or
exact `NOT_FOUND`; its failure state and cooldown are unchanged. A batch without
such work returns to IDLE without minting a submission token.

## Private/shared original migration

`IndexingService` also reads S3-only job identities, without inventing canonical
DynamoDB source records. Private originals are `kb/{owner}/{filename}` and shared
originals are `shared/**`; originals are never rewritten/deleted by the worker.
Immutable paths are:

- `manual-kb/v1/{ownerId}/{resourceId}/{sourceRevision}/{indexRunId}/document.{ext}`
- `shared-kb/v1/{resourceId}/{sourceRevision}/{indexRunId}/document.{ext}`

`resourceId` hashes the exact UTF-8 source key. Revision frames schema, configured
KB bucket, key, exact ETag, version ID (or empty) and decimal size, using the same
UTF-8 byte-length framing described above. Sidecars carry `sourceBucket`,
`sourceKey`, `sourceETag`, `sourceVersionId`, `sourceSize`, `resourceId`,
`sourceRevision` and `indexRunId`. Private metadata uses `indexSchema=manual-kb-v1`,
`resourceKind=manualKbDocument` and `ownerId`; shared metadata uses
`indexSchema=shared-kb-v1`, `resourceKind=sharedKbDocument` and
`visibility=authenticated-shared`, with no ownerId. Neither grants authorization.

PPTX originals publish `document.md` instead of a byte copy, with sidecar
`sourceProjection=pptx-markdown-v1` and the same source revision, so a changed
ETag/version/size yields a new immutable projection. `PPTXToMarkdown` (pure Go)
runs only after the pinned read and freshness HEAD. It follows `sldIdLst` order
and emits slide title headings, placeholder/text-box paragraphs, tables and
notes-page body text; slide numbers, dates, footers, masters, charts, diagrams,
media and external targets are not read. Limits: 4,096 entries, 16 MiB per XML
part, 64 MiB decompressed total, 200:1 ratio above 1 MiB, actual stream length
checked against the header, 500 slides, XML depth 128, 4 million tokens and
4 MiB Markdown. DTDs are rejected. Failures are FAILED jobs with
`PPTX_MALFORMED`, `PPTX_ENCRYPTED_OR_LEGACY`, `PPTX_TOO_MANY_ENTRIES`,
`PPTX_EXPANSION_LIMIT`, `PPTX_TOO_MANY_SLIDES`, `PPTX_OUTPUT_TOO_LARGE` or
`PPTX_NO_TEXT`; binary `.ppt` is `LEGACY_PPT_UNSUPPORTED`. Converter changes do
not alter the QA-shared revision, so an existing projection is regenerated only
when its source changes or its job is re-requested.

`Documents` can read original binary identifiers solely to prove their former
vectors are absent after owner deletion; Put/Delete still reject original keys.
Existing QA validation is in [SOURCE_CONTRACT.md](../../python/qa/SOURCE_CONTRACT.md)
and the registered `manual_kb.py` reader. Verified snapshots and public strict-QA
acceptance remain prerequisites for canonical activation.
[ADR-038](../../../docs/decisions/ADR-038-canonical-note-indexing.md)
defines the manual-only → snapshot verification → strict QA → all-mode rollout.

Implementation and verification: `index_source.go`, `index_provider.go`,
`index_knowledge_source.go`, `index_knowledge_provider.go` and their adjacent
tests. `testdata/index-revisions.json` and `knowledge-revisions.json` pin the
shared framing contract. Status reads use `GetKnowledgeBaseDocuments`;
ingestion continues through full S3 sync with `StartIngestionJob`, never
concurrent direct `IngestKnowledgeBaseDocuments` writes.
