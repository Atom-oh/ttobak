# Asynchronous QA foundation

The helpers alone register no route, queue consumer or AWS resource. This revision
wires them into async job execution; frontend job activation still defaults off.
See [the runtime contract](ASYNC_CONTRACT.md) for registration, activation and
deployment requirements. Calls without a delivery collector retain the existing
history behavior.

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

The checked-in [table configuration](../../../infra/lib/storage-stack.ts) leaves
the AWS-owned KMS default. This is an IaC statement, not deployment evidence.
[AWS documents KMS encryption for all table data](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/EncryptionAtRest.html).
These helpers add active TTL only to their own rows; they do not migrate key
ownership, add customer-key controls, or fix legacy conversation TTL retention.
UTF-8 item bounds include names, payloads and metadata, with reserved control space;
see [AWS item-size guidance](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html).

A conditional claim failure is reconfirmed with a strong read: only the same
invocation's `runId` may continue after a lost acknowledgement and SDK retry.
An attachment lookup/read failure marks delivery proof invalid independently of
history-capacity limits. Later valid reads or capacity overflow cannot clear it.
These source-access hooks are inert when no delivery collector is supplied.

`test_handler` loads the store/proof suites and all five active HTTP/worker
integration tests from `test_async_runtime`. Tests use synthetic table, model
and network responses plus actual boto3 serialization.
