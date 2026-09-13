# Bounded QA output and completion diagnostics

Status: historical implementation plan, not deployment or root-cause proof.
Current runtime limits belong in `backend/python/qa/ASYNC_CONTRACT.md`.

The observed WS request ended with `MODEL_STREAM_INCOMPLETE` before the
acceptance helper ended. A minute-level metric reached 4096 output tokens, but
that metric aggregates several requests and the individual stop reason was
not logged. Output exhaustion is a plausible inference, not established cause.
The host's separate synthetic probe accepted `maxTokens=8192` and returned
`end_turn` with four output tokens; it does not prove completion of real QA.

Use one fixed 8192 output-token cap in both QA Converse call sites, including
the async executor which delegates to the nonstream handler. Keep completion
guards, source checks, deadlines, tool limits and no-resubmission behavior.
Do not alter the question detector's separate budget or `current_input.py`.

Add a small diagnostics collector that retains only closed stop categories,
bounded integer usage/counts and block state. Log one bounded structured record
at each existing model-completion failure. Never retain/log raw events, text,
queries, source metadata, model/request/session IDs, arbitrary metadata keys or
exception payloads. Unknown/malformed metadata is explicitly unavailable.

Prove the shared cap through actual sync/meeting/WS/async handlers. Preserve
negative completion cases and receipt/deadline regressions. Exercise malformed
metadata and redaction, then run the full QA suite and docs checks.

Proposed follow-up prompt, not activated by this change:

> For a latest-only request, answer concisely with current facts, the requested
> conversation label, required source IDs and relevant citations/locations.
> Avoid repeated source boilerplate. Omit ingestion run IDs, input/revision
> digests and transport bookkeeping unless explicitly requested. Keep live
> input distinct from saved-source evidence and preserve all requested source
> IDs and required provenance. Do not narrate superseded raw values.

The higher ceiling can increase output latency/cost and may still be exhausted.
It does not extend the legacy synchronous HTTP timeout, activate async UI,
raise storage/WS limits, or automatically continue an incomplete answer.
