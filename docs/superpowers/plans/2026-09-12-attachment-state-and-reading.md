# Attachment state and verified reading

This service slice adds durable extraction state and bounded document reads.
It does not register REST routes, queue existing uploads or change summary
generation. Deploy the asynchronous worker before enabling those producers.

Canonical attachments use `MEETING#{meetingId} / ATTACH#{attachmentId}`.
`ATTEXT#{attachmentId}` stores a separate run, status, millisecond lease, source
identity/ETag and immutable result key. Queue writes check the current parent, attachment and run; failure updates check the existing run/status/lease. Failed publication or extraction preserves prior
results. Expired work becomes failed/INTERRUPTED; unsupported formats fail
explicitly. Owner/edit access is required to request work.

Every read authorizes the current meeting before reading attachment objects.
The configured bucket, exact uploader/meeting/result prefix, source identity,
ETag and JSON metrics must agree. Result objects are at most 1 MiB. Text pages
are at most 14,000 encoded bytes and 50 chunks, use Unicode codepoint offsets,
and bind continuation to the result revision. Partial and retained results are
explicit; they cannot masquerade as a successful current attempt.

Repository attachment enumeration now paginates, and the unused document-upload creation helper uses a create-if-absent transaction with a parent check. Internal extraction/summary fields are excluded
from DynamoDB serialization except the saved summary's source-revision record.
`AttachmentResponse.textExtraction` is an optional DTO slot for later API wiring.

Validation: standalone Go tests/vet and ARM64 API build, including transaction
guards, source/permission changes, failed event publication, expired leases,
source/result tampering, Korean pagination and the encoded-response limit.

Deletion removes each attachment and its ATTEXT state in the same transaction.
Meeting deletion keeps those pairs within the existing 100-operation batches,
then strongly paginates remaining ATTEXT rows after the parent is gone to catch
late/orphan states. Conditional workers cannot recreate them. Cleanup errors are
returned rather than reported as success. Immutable S3 results are retained under
the existing object-retention policy; readers require the canonical attachment
and parent, and deletion never blindly removes potentially committed result bytes.

The publisher uses the default EventBridge bus, source `ttobak.upload`, detail-type
`DocumentUploadCompleted`, detail `{bucket,key,meetingId,ownerId,userId,attachmentId,runId}`.
Queue leases are five minutes; the bounded worker claims a shorter lease and must
complete inside it. Later heartbeat-based workers must preserve the same run/lease
conditions. The worker result schema is v1, source-bound JSON under
`files/{uploader}/{meeting}/text/{attachment}/{run}.json`.
