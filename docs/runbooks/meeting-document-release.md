# Meeting document and saved-summary release

Merge and deployment are separate gates. The host coordinates both; opening
these PRs does not activate or verify production processing.

## Order

1. Keep the merged parser (#202), attachment state/read foundation (#206),
   metadata reading (#198), and current-source QA readers (#207).
2. Deploy the document worker from #208 through `TtobakGatewayStack`. Verify
   the deployed artifact, `DocumentUploadCompleted` rule, scoped permissions,
   and bounded parser startup before merging the upload API producer.
   A merged worker PR or passing local test is not deployment evidence.
3. Land the summary/storage foundation. It adds guarded repository operations,
   prepared document evidence, and snapshot-only generation; it adds no upload
   event producer or saved-summary HTTP route.
4. Land the attachment API and summary wiring after step 2. Supported uploads
   now queue extraction. Legacy documents require an authorized retry.
5. Land saved-summary orchestration and its `SummaryRequested` consumer/rule
   before allowing its API producer to send events. Deploy the summarize
   consumer and rule before the API code, or enforce that dependency in CDK.
6. Release UI consumers after their APIs are deployed. Document/search status
   also depends on the index-status API; QA source links depend on the unified
   QA producer. Frontend deployment preserves runtime `config.json`.

The current push-to-main workflow deploys infrastructure for backend changes.
The host must hold producer merges until the prerequisite deployment finishes.
For a targeted manual release, deploy only the changed stack with
`npx cdk deploy TtobakGatewayStack --exclusively`; never `--all`. Do not
disable CI or review gates to shorten this sequence.

## Invariants and acceptance

- Metadata authorization precedes S3 reads. Cursors require an explicit version
  and revision; a changed source must clear the previous result indication.
- Document JSON is separate `DOCUMENT` evidence. Only verified locations may
  become document citations; document-only evidence does not establish speech.
- Saved-summary queue/start/completion check current source and editor grants.
  Failure retains human content. Success and coverage are one transaction.
- Meeting, action analysis, sim, and summary state deletes occupy four initial
  transaction entries. Source and summary state disappear atomically even if a
  later batch fails; subsequent attachment/share pairs stay within 100-item
  transaction boundaries.
- Definite write rejection cleans only new immutable spills. Ambiguous database
  outcomes retain them so a committed reference cannot become dangling.

Before activating producers, verify synthetic supported documents, visible
queue/failure/retry, retained-result caveats, late-document re-summary, editor
revocation, changed ETags, stale cursors, and multi-batch deletion failure.
Use repository fixtures for local checks; production acceptance belongs to the
host and uses explicitly authorized synthetic resources.

Rollback producer/UI code first and retain compatible workers/readers for
in-flight events. Do not delete state/results to make failures appear successful.
