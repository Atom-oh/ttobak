# Canonical indexing foundation

This first slice supplies canonical resource identities, durable job/coordinator
records and the S3/Bedrock provider. It does not enable an indexing worker or
change retrieval.

Canonical sources are owner-partition meetings and personal/account documents.
Index job keys hash both source keys. Conditional field updates bind job
versions, run IDs and active leases; source-aware completion uses a transaction
that also checks every present or absent projection field. Reconciliation
primitives return continuation cursors for both source scans and job queries.

The provider reads bounded asset bytes pinned to ETag/version, writes immutable
`canonical/v1/` objects and limits cleanup to that prefix plus legacy
`meetings/` exports. Its only ingestion mode is the existing S3 data source's
`StartIngestionJob`; it never mixes direct ingestion with full synchronization.
An accepted job is not evidence that ingestion completed successfully.

The next slice adds source revision calculation, projection generation,
coalescing, reconciliation and the existing KB Lambda entry point. Separate
retrieval and infrastructure changes must enforce current source authorization,
revisions, stream delivery and scheduled recovery before enabling the pipeline.

Validation: standalone Go internal tests and vet, including real SDK wire tests
for conditional expressions, source transactions, pagination, pinned object
reads, uncertain writes and ingestion identity. No live AWS calls are made.
