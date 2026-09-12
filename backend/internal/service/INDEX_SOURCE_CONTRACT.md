# Index source and provider read contracts

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
This prerequisite does not activate a worker, stream, schedule, or IAM grant.
Lifecycle callers must still check immutable projection inventory and fresh source
revision/CAS after observing provider status.

Official AWS contracts:

- https://docs.aws.amazon.com/bedrock/latest/userguide/kb-direct-ingestion-view.html
- https://docs.aws.amazon.com/bedrock/latest/APIReference/API_agent_GetKnowledgeBaseDocuments.html
- https://docs.aws.amazon.com/bedrock/latest/APIReference/API_agent_KnowledgeBaseDocumentDetail.html
- https://docs.aws.amazon.com/bedrock/latest/userguide/kb-direct-ingestion.html

The last document distinguishes status reads from direct writes and forbids
concurrent `IngestKnowledgeBaseDocuments` / `StartIngestionJob`. The worker
continues to use full S3 sync only.
