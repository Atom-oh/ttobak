# Note QA follow-up evidence — 2026-09-13

This dated record complements the [public QA archive](../2026-09-13-public-qa/README.md).
The [normalized results](results.json) retain case outcomes and hashes of private
operator-held evidence; no credentials, raw answers or user identifiers are published.
Journal bindings hash only the recorded byte prefix, allowing later independent
observations to append without rewriting the historical evidence.

Authenticated public WebSocket and direct async job checks covered saved human
notes, five PDF/PPTX/DOCX/Markdown attachment results, private PDF/shared DOCX
V1 and V2 retrieval, same-session source deletion, and received-document revocation.
Exact current source metadata and revisions were checked before answer inspection.
Deletion replies contained neither generation's facts nor source attribution.
Normal scheduled processing removed all four V1/V2 provider snapshot identifiers.
The received-document grants were prepared through scoped IAM operations and
consumed by the actual Demo identity; this is not an owner sharing-UI test.

Both transports preserved the conversation label and current saved-source
provenance. The live-input checks also exposed incorrect temporal narration:
correct values were returned, but the model sometimes claimed no current context
had been supplied or described a client update as its own earlier mistake.
PR252 added per-turn input receipts; later changes removed model-facing character
counts, increased the completion ceiling, and presented current input beside the
current question. A new six-turn run against verified deployment `f3325ce`
completed three WebSocket and three async turns. Inspection of all six full
answers and their request timeline accepted current-input presence, temporal
attribution, label continuity, saved-source provenance and omission of superseded
values. Original narration and completion failures remain unchanged.

Scope limits remain explicit. These are short synthetic cases, not broad accuracy
claims. Async UI activation was merged separately in PR260; its deployed browser
acceptance remains unproven. Some prose abbreviates S3 URIs even when
structured source details retain exact identifiers. Original failed and unknown
runs are preserved. The two manual fixtures' ten exact keys had twelve data
versions and ten delete markers physically removed after byte/metadata checks and
provider absence verification. Data versions were removed before delete markers;
the final inventory was empty.

Canonical all mode, its stream and its schedule were deployed and independently
observed after PR227. Canonical backfill and file-backed DocHub lifecycle
acceptance, async UI activation, final sessions and remaining fixture cleanup
are still in progress. Activation alone does not establish those outcomes.

PR258 added exact authorized canonical document selection. Its deployed QA
package matched all 28 runtime modules at `d5beaae`, and Python 3.9 and 3.12
each passed 372 tests. Both existing V1 PDFs then passed fresh original-byte,
projection/metadata and provider INDEXED checks.

Actual consumer acceptance did not pass: the first model call failed with
`ServiceUnavailableException`, before any response or tool call. One explicitly
reserved recovery lineage failed the same way on its first call. No failed job
was replayed; no further recovery lineage is authorized by that run. The records
do not establish the provider's underlying cause, billing outcome or a global
outage. V1 documents remain intact; V2 replacement and deletion were not started.

PR259 merges bounded reconciliation improvements without increasing the
four-member ingestion batch. Its deployed liveness observation and the PR260
browser check remain separate from code/test acceptance. Model-dependent
checks must resume from a reviewed recovery decision after availability is
established; do not silently reset an intent or mark these failures passed.
