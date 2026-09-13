# Bounded indexing reconciliation

Status: historical implementation plan; current operational limits belong in
the knowledge-index bootstrap runbook.

At the reported approximately 2,850 table items, Scan Limit 25 needs at least
114 successful reconcile opportunities per source sweep. Approximately 107
known jobs require at least 27 four-item query pages. These are page-count
lower bounds, not elapsed-time guarantees: reconciliation runs only while the
coordinator is IDLE and the provider is not busy. Scan's pre-filter evaluation
and one-MiB page read limit also matter.

Increase the source scan limit to 100. Inspect at most four job pages per
reconcile, requesting no more than the remaining capacity of the unchanged
four-member batch. Process each returned page completely, stop on full capacity
or end-of-query without wrapping, and publish the job cursor only after the
bounded walk succeeds. Preserve the ten-minute context, twenty-minute lease,
provider freeze, current-source validation, cooldowns and conditional writes.

Tests cover ineligible pages before pending work, mixed capacity, cursor
rotation after permanent failures, exact read bounds and durable cursor
preservation on errors/cancellation. Validate actual Tick progression with
synthetic repositories/provider state only. No manual AWS ticks or control
writes are part of implementation verification.

The nominal source lower bound becomes 29 reconciles. With entirely ineligible
full job pages, 27 pages can fit into seven reconcile opportunities. Eligible
work, provider waits, large items, retries and deadlines can make either longer.
Per-reconcile source evaluation rises up to fourfold, and job metadata/read
checks can rise from four to sixteen. No ingestion batch/concurrency increase
is proposed.
