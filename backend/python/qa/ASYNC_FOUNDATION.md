# Inactive asynchronous QA helpers

This foundation registers no HTTP route, queue consumer or AWS resource and does
not change the frontend transport. The existing handler never creates a delivery
collector; conditional hooks in tool helpers remain inactive for normal requests.
Runtime wiring, deployment, capability activation and public acceptance are separate.

`QAJobs` supplies user-bound idempotent creation, one execution claim, bounded
request/result/proof rows, active one-hour TTL and safe uncertain-write handling.
Its injected executor owns model/tool calls. Running jobs are never taken over.
`MutationGuard` never treats an arbitrary error result as proof that a side effect
did not happen. Confirmed receipts can be reused; uncertainty blocks more creation.

`DeliveryProof` captures complete current reads before the smaller history budget
can discard their replay bookkeeping. It bounds proofs independently and rechecks
current source callbacks/strict read-only fingerprints, never replaying a mutation.
Typed fingerprint defaults preserve existing history behavior. No job can publish
a result without the runtime supplying these checks.

The checked-in table configuration leaves the AWS-owned KMS default, consistent
with the host's live `DescribeTable` receipt (ACTIVE, absent SSEDescription).
[AWS documents KMS encryption for all table data](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/EncryptionAtRest.html).
These helpers add active TTL only to their own rows; they do not migrate key
ownership, add customer-key controls, or fix legacy conversation TTL retention.
UTF-8 item bounds include names, payloads and metadata, with reserved control space;
see [AWS item-size guidance](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html).

`test_handler` loads the standalone store/proof suites. Tests use synthetic table,
model and network responses plus actual boto3 serialization. Active HTTP/worker
integration tests stay with the subsequent runtime patch, not this foundation.
