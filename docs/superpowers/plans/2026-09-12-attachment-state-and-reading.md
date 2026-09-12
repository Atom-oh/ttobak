# Attachment state and verified reading

This service slice adds durable extraction state and bounded document reads.
It does not register REST routes, queue existing uploads or change summary
generation. Deploy the asynchronous worker before enabling those producers.

Canonical attachments use `MEETING#{meetingId} / ATTACH#{attachmentId}`.
`ATTEXT#{attachmentId}` stores a separate run, status, millisecond lease, source
identity/ETag and immutable result key. Queue/failure writes condition on current
parent, attachment and run. Failed publication or extraction preserves prior
results. Expired work becomes failed/INTERRUPTED; unsupported formats fail
explicitly. Owner/edit access is required to request work.

Every read authorizes the current meeting before reading attachment objects.
The configured bucket, exact uploader/meeting/result prefix, source identity,
ETag and JSON metrics must agree. Result objects are at most 1 MiB. Text pages
are at most 14,000 encoded bytes and 50 chunks, use Unicode codepoint offsets,
and bind continuation to the result revision. Partial and retained results are
explicit; they cannot masquerade as a successful current attempt.

Repository attachment enumeration now paginates, and document-upload metadata
uses a conditional field update. Internal extraction/summary fields are excluded
from DynamoDB serialization except the saved summary's source-revision record.
`AttachmentResponse.textExtraction` is an optional DTO slot for later API wiring.

Validation: standalone Go tests/vet and ARM64 API build, including transaction
guards, source/permission changes, failed event publication, expired leases,
source/result tampering, Korean pagination and the encoded-response limit.
