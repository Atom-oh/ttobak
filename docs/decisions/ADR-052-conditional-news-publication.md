# ADR-052: Conditional News Publication and Cost Reservations

- Status: Accepted design; storage implementation staged and inactive.
- Decision date: 2026-10-21.
- Scope: Recovered knowledge-grade news pipeline; the legacy crawler stays active
  until consumer and production acceptance gates complete.

## Context

The recovered prototype wrote fixed S3 keys before checking publication state,
allowed concurrent calls sharing a run ID to share a claim, and added costs again
when finalization retried. An S3 upload is discoverable independently of a
DynamoDB metadata write. A failed model response can also have unknown usage.

## Decision

Use a fresh 32-hex attempt token for each worker invocation, separate from the
source run ID and canonical URL hash. The candidate ID is SHA-256 of source ID
and normalized URL; the existing 16-hex news document hash remains compatible
with historical document keys. Always compare canonical source configuration
before reserving and publishing. Transaction conditions pin every observed
publication-relevant field, including absent fields.

Reserve the caller's enforced worst-case model cost before making model calls.
One transaction checks the current source and unpublished document, acquires an
exclusive numeric lease, creates an attempt ledger, and reserves the same amount
against source/month and source/run limits. Reusing a run ID never permits a
second invocation to acquire a live claim. Limits and rates must come from
verified deployment configuration; this storage module does not supply prices
or prove that a caller's reservation covers its model limits.

Rejected and failed attempts retain known usage. Unknown usage keeps at least
the full reservation. Abandoned attempts also retain their reservation; a lease
takeover makes a new reservation. A known overrun is recorded and blocks further
reservations against that budget instead of silently capping reported cost.
Refunding unused reservation, storing the terminal receipt, deleting the owned
claim, and publishing accepted metadata happen in one transaction. Exact
completion retries return the saved receipt without another charge or refund.
Conditional row state protects idempotency beyond AWS request-token retention.
Settlement binds the stored run, document, month, lease, token, revision and
reservation before selecting any budget row. A reconstructed completion cannot
move a refund to a different month or extend its original lease. Completion
digests preserve scalar types while treating equal numeric scales identically.

The injected storage adapter takes `now` from its trusted caller. Callers must
sample their current clock separately before reservation and settlement; they
must not accept an external timestamp or reuse a pre-inference timestamp.
Immutable recorded lease checks prevent lease reconstruction, while actual clock
freshness remains a caller/deployment obligation. The staged worker samples its
injected clock for each storage operation; no HTTP timestamp selects this clock.

Store immutable objects at:

- Private artifact: `knowledge-artifacts/news/v1/{source}/{hash}/{attempt}.json`.
- KB Markdown: `news/v1/{source}/{hash}/{attempt}.md`.

The Markdown prefix is outside the existing `shared/` legacy reader. Before
writers activate, every new consumer must require a current schema-v1
`PUBLISHED` document with the exact attempt/key/content hash and current source
state, before and after reading bytes and before replaying history. Unreferenced
objects must remain unreadable even if an ingestion job indexes them. New
consumers and their acceptance tests therefore precede publication writers.

Transport errors and ambiguous transaction outcomes propagate. Do not release
reservations or delete immutable objects based only on a missing acknowledgment.
Strongly consistent published-identity batches fail on unprocessed or malformed
responses instead of assuming absent documents.

## Delivery and acceptance

`news_store.py` is an injected storage adapter without a Lambda handler. It does
not upload S3 objects, start a scheduler, finalize a run, validate a generated
article, grant an IAM permission, or activate consumers. Worker request limits,
verified pricing, immutable object writes, run finalization, retention,
current-source QA/UI reads, migration and rollback remain required delivery work.
The activation gate must record positive end-to-end publication and QA evidence;
zero-result or rejected-only runs do not establish acceptance.

Dependency-free boundary tests run through `test_crawlers`. Transaction
integration cases in `test_news_store.py` additionally require
`moto[dynamodb]>=5,<6`; they are explicitly skipped when it is absent. Run them in
an isolated Python 3.12 environment for storage changes. Moto exercises
conditional expressions and transaction state; it does not validate AWS IAM,
service concurrency, actual billing, or deployed acceptance.

## Evidence

- `backend/python/crawler/news_pipeline.py`
- `backend/python/crawler/news_discovery.py`
- `backend/python/crawler/news_enrichment.py`
- `backend/python/crawler/news_store.py`
- `backend/python/crawler/test_news_store.py`
