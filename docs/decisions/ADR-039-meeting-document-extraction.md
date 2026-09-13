# ADR-039: Meeting document text extraction

- Status: Accepted, 2026-09-12.
- Code checked: 2026-09-13. Parser, Lambda parent, infrastructure and Go/QA
  state/read foundations exist; `enableDocumentExtraction=true` is configured.
  Upload/API, summary, active QA and UI integration remain separate from these
  foundations and from deployed acceptance.

## Context and decision

Meeting documents previously contributed filenames only. Use asynchronous
extraction instead of parsing untrusted files inside the API request budget.
The worker supports native PDF, PPTX, DOCX and UTF-8 Markdown text; it does
not provide OCR or legacy Office conversion.

Input is capped at 20 MiB; the child has 512 MiB address space, 8/9-second
CPU limits and a 12-second wall deadline. Completeness covers declared native
text, not rendered appearance. Hyperlinks remain inert. Internal PPTX chart
XLSX workbooks remain opaque and make extraction partial; macros, OLE and
other embedded packages are rejected.

## Publication and reader contract

A producer must persist canonical `ATTACH#` and queued `ATTEXT#` state before
publishing `ttobak.upload / DocumentUploadCompleted`. The worker rereads the
meeting, attachment and run and conditionally claims an active lease.
Event-supplied keys do not authorize reads.

Read bounded source bytes pinned to ETag, parse in the restricted child, and
write immutable JSON to
`files/{uploader}/{meeting}/text/{attachment}/{run}.json`. Results bind source
identity and page/slide/paragraph locations. A completion transaction checks
the same parent, attachment, run and lease. Empty/scanned/unsupported input
cannot become empty success; partial output remains explicit.

Failure preserves previous result metadata. A retained result is not success
of the current run. Readers must verify current source bindings: S3 HEAD and
DynamoDB completion are not atomic. Interrupted work needs a fresh run ID.

## Isolation and retention

CDK configures isolated subnets with HTTPS egress only to S3/DynamoDB gateway
endpoint prefix lists. The role reads `files/*`, writes result JSON keys and
accesses required table records. Verify live routes/endpoints separately.
The role still spans tenants within those permissions.

Code/dependencies are packaged together without fixtures/tests. The child
receives no cloud environment; resource limits and audit hooks are not an
RCE-proof sandbox, and same-UID compromise can expose parent credentials.
Logs omit document text and parser stderr.

Results use the existing private, versioned assets bucket with S3-managed
encryption and Block Public Access. No `files/` expiration is configured.
Attachment/meeting deletion removes canonical access and `ATTEXT#` rows but
does not erase result object versions. Ambiguous commits retain objects;
unreferenced-object reclamation remains a gap. Never expire active results
without checking references.

## Consequences and evidence

Extraction adds asynchronous delay, partial/failure/retry states and retained
objects. The active summary builder still lists document filenames until its
consumer integration changes; the old claim that no parser exists is obsolete.

- [Parser scope and limits](../../backend/python/document-extract/README.md),
  [Lambda contract](../../backend/python/document-extract/LAMBDA.md).
- [State service](../../backend/internal/service/attachment_text.go),
  [bounded reads](../../backend/internal/service/attachment_text_read.go),
  [QA reader](../../backend/python/qa/attachment_context.py).
- [Infrastructure](../../infra/lib/document-extraction.ts),
  [current summary attachment context](../../backend/internal/service/bedrock.go).
