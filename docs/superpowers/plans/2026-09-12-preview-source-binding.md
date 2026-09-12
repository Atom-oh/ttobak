# Preview source binding

Automatic document indexing must distinguish the current slide upload from an
older PDF preview at the same sidecar key. The converter now records the exact
GET response ETag and optional S3 version ID as `source-etag` and
`source-version-id` metadata, and rechecks the source immediately before
publication. Downloads also have a 50 MiB byte limit.

Before conversion, HEAD the destination. Publish with If-None-Match for a new
preview or If-Match against its observed ETag for a replacement. Conditional
conflicts and uncertain writes fail visibly for normal Lambda retry; never
delete the existing preview. The converter role gains read access only to the
existing `docs-pdf/*` prefix so the HEAD is authorized.

Source HEAD and destination PUT are not a cross-object transaction. ETags also
identify bytes, not metadata-only revisions. Therefore consumers must verify
the binding against the current source before using a preview as evidence.
Unbound legacy previews must be regenerated during indexing backfill; they
must not be relabeled as current by copying metadata onto old bytes.

Validation uses Go tests for source/version changes, conditional creation and
replacement, unchanged prior results on failure, and actual SDK HTTP headers
and disabled retries. Infra assertions keep converter object permissions inside
`docs/*` and `docs-pdf/*`. Existing isolated-network placement remains in force.

Primary contract: https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html
