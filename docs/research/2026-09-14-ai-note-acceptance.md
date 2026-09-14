# AI note acceptance — 2026-09-14

This dated record supplements the historical
[September 12 delivery record](2026-09-12-ai-note-improvements.md).
It records deployed behavior observed with synthetic fixtures, scoped cleanup
and the limits of that evidence. It is not a general model-accuracy claim.

## Current acceptance

| Behavior | Observed result | Limit |
| --- | --- | --- |
| Action extraction recovery | The delivered parser, authorization, retry/status, completion persistence, concurrency and UI changes passed their recorded tests and deployment checks in PRs #194–198. | This record adds no separate live retry/checkbox mutation campaign. |
| Meeting, personal-note and account-note indexing | The original versions, edited versions and logical deletions reached the canonical projections and provider index through the deployed pipeline. Queries used the same session throughout. | Scoped to three synthetic sources. |
| Current-source answers | Original and edited answers contained the three expected values with the corresponding current source identities/revisions. Old completed results returned `409 SOURCE_CHANGED` without an answer after edits. | The edited answer violated the harness's exact JSON-format instruction by adding one Markdown code fence. The original failed check remains recorded; source-contract qualification does not erase that failure. |
| Deleted-source answers | After all three sources and provider documents were absent, the same session returned exactly three `NOT_AVAILABLE` values, no sources and no old values. Both strict assertions and full-answer review passed. | This checks those deleted sources; it is not a blanket retention guarantee. |
| Browser asynchronous Q&A | An actual signed-in browser submitted one QA job, polled the same job eight times and rendered the expected saved-note answer without page errors or a legacy request fallback. | Meeting-detail reads also included two authorized attachments. The answer attributed the fact to the saved note; this is not proof that every note question avoids attachment reads. |
| Indexed personal/account PDFs | Original and replacement answers over asynchronous HTTP and WebSocket delivery contained the expected body codes and allocation values, current `verified_indexed_file` provenance and explicit partial scope. Replacement invalidated the old completed result. Both sources were subsequently deleted and the provider reported them absent. | Replacement WS prose incorrectly included a pending status and inaccurate abbreviated revision suffixes; the exact structured revisions remained correct. This qualifies source lifecycle behavior, not unqualified whole-answer reliability. |
| Deleted PDF answers | The existing asynchronous and WebSocket sessions returned only the two unavailable values with zero sources or source details. | Both answers used a Markdown fence and failed the harness's exact JSON-format instruction. Those failures remain recorded; this is source-absence qualification, not strict-format acceptance. |
| Attachment extraction, re-summary and Q&A | Actual PDF/PPTX/DOCX/MD extraction, a fifth-file failure/repair, five-attachment re-summary and six reviewed live-context Q&A turns exercised attachment grounding and current-input attribution. | One synthetic meeting and five attachments; not exhaustive format coverage or a general grounding guarantee. |
| Summary evaluation | The retained four-case evaluation has 25 passing checks; all eight archived request/response hashes were rechecked without new model calls. | A small synthetic corpus, not a production accuracy estimate. See the [evaluation and raw evidence](evaluations/2026-09-12-note-quality/README.md). |
| Bounded MCP distribution | The deployed bundle returned HTTP 200 and matched the repository bundle: 766,711 bytes, SHA-256 `e5d46fe38019b6dd881110ac30a8efdfdd24589ee1b42f4a3ee7c193052fd4bb`. | Delivery evidence complements the existing protocol tests; it does not assert universal client behavior. |

The text/browser observations used deployed commit
`7aa50ad23a84941ec0290fb87a4aa43fbc90b69a` (deployment run `34768082600`).
The actual QA runtime files, WebSocket binary, asynchronous route configuration
and all-mode indexing readiness were checked before the acceptance calls.

The replacement PDF and asynchronous deletion observations used deployed commit
`6dd351e81d9694130e4856e1d7ead1cd03420301` (run `34814045590`), after the
same runtime checks. PR #265's whole-file-reading guidance fix was deployed:
neither replacement answer suggested `get_document_detail` for reading the
whole PDF. PR #269 further makes pending guidance conditional and labels
verified excerpts positively. Its 376 QA tests passed; its effect on later live
model wording has not been measured.

PR #269 was deployed at `c7176d9926a8d65523f9193cc4f23bf1e962e238`
(run `34816238185`); all 28 QA runtime files and the actual WebSocket build
matched before the final WebSocket deletion question. The question retained
the original session and text and was sent once. A separate, tested continuation
bound the already-qualified asynchronous source-absence result without editing
its failed assertion. A local journal-name error occurred before any network
request; both preparation versions and that error were retained.

Private evidence is retained outside the repository under
`~/.cache/ttobak-improvements/`. No authentication state, signed download URLs,
customer content or raw credentials belong in this report.

| Evidence | Private relative path |
| --- | --- |
| Text original-answer review | `public-note-validation-20260913/qa_canonical_text_f2/qa_public_async/qa_text_public_v1.json` |
| Edited-answer source qualification and format limitation | `public-note-validation-20260913/qa_canonical_text_f2/qa_public_async/qa_semantic_v2_source_contract_20260914.json` |
| Deleted-answer strict assertions and semantic review | `public-note-validation-20260913/qa_canonical_text_f2/qa_public_async/qa_text_public_deleted.json` |
| Browser request/render checks and semantic review | `browser-qa/async-ui-result.json`, `browser-qa/async-ui-semantic-review.json` |
| Stale completed-result suppression | `qa-acceptance-20260914/v1-results-after-text-edit.json`, `qa-acceptance-20260914/file-v1-result-after-edit.json` |
| PDF original-answer limitation | `public-note-validation-20260914/qa_canonical_file_consumer_recovery_67cb11d8ef48444e96b5ae1b326a28bc/qa_semantic_v1_limitations.json` |
| Replacement PDF source qualification and wording limitations | `public-note-validation-20260914/qa_canonical_file_consumer_recovery_67cb11d8ef48444e96b5ae1b326a28bc/qa_semantic_v2_source_scope_decision.json`, `qa_semantic_v2_limitations.json` in the same directory |
| Deleted PDF asynchronous source qualification and format failure | `public-note-validation-20260914/qa_canonical_file_consumer_recovery_67cb11d8ef48444e96b5ae1b326a28bc/qa_deleted_async_source_scope_review.json` |
| Both deleted PDF answers, original failures and one-call WebSocket evidence | `public-note-validation-20260914/qa_canonical_file_consumer_recovery_67cb11d8ef48444e96b5ae1b326a28bc/qa_deleted_both_source_scope_review.json` |
| Retained evaluation and deployed MCP checks | `qa-acceptance-20260914/retained-quality-and-mcp-verification.json` |
| Attachment API and re-summary | `public-note-validation-20260913/attachment-api-verified.json`, `public-note-validation-20260913/resummary-semantic-review.json` |
| Six final live-context Q&A turns | `public-note-validation-20260913/qa_final_live_8bb138a10eba4673/qa_semantic_review.completed.json` |

## Cleanup observations

The original IAM-created document used for received-document permission tests
was deleted only after matching its complete receipt-bound source row and
confirming its sharing references were absent. The conditional transaction
deleted that one source; subsequent strong reads confirmed absence. Normal
deindexing then reached `DELETED`, with no current projection objects and a
provider `NOT_FOUND` result. This is IAM fixture cleanup, not a claim that the
Main user's owner-API authentication was exercised.

The source receipt is
`received-source-cleanup-20260914-8bccb668/source-applied.json`; deindex evidence
is `received-deindex-20260914-17875e83/observation-02.json`. Historical object
versions and QA companions were included in the final scoped cleanup below.

The synthetic account's `META` and Demo owner `MEMBER` rows were removed in one
conditional transaction. A fresh filtered read found no child-account or
pending-invitation references; strongly consistent partition reads confirmed
only those two rows remained after the related documents were gone. Fixture
reference writers remained quiescent throughout. The subsequent partition read
was empty. Evidence is
`account-two-row-cleanup-20260914-46c48871/account-applied.json`.
Cleanup makes no global table or bucket absence claim. Both users and their
profiles remain intact.

Final cleanup removed 107 receipt-bound targets: 37 S3 data versions, 26 delete
markers and 44 QA-related DynamoDB rows. The data totaled 28,644 bytes. Each
delete had an acknowledged request and a subsequent absence check. The first
execution reached its time budget after 84 acknowledged deletes; read-only
reconciliation confirmed the last deletion, and the same immutable plan then
skipped completed targets and removed the remaining 23. No delete was replayed.

A fresh post-cleanup read confirmed zero remaining targets, absent original
sources, settled source-index deletion and all historical canonical provider
documents `NOT_FOUND`. Main and Demo both remained enabled, confirmed Cognito
users with their profile rows present. The earlier manual-KB cleanup's 22 object
versions were already removed and were not repeated in this plan.

Final receipts are under
`remaining-cleanup-final-20260914-1f8c376f/`:

- `residual-plan.json`: SHA-256 `8c0214e6acc72d011a1b6a98bef619b231938be7967cf426c7cf255ee5e20cd7`.
- `residual-applied-complete.json`: SHA-256 `0382519cfedcda18fb66d0c4725cc27d07d148c806fec8e855737d0490e1dbeb`.
- `cleanup_journal.jsonl`: SHA-256 `a41c312383d57ea6ab4f5f18d4f4339bdea624dd3d9f6a768fdf40657967b345`.
- `post-cleanup-verified.json`: fresh scoped absence and user-preservation checks.

## Acceptance boundary

The five requested improvement areas are accounted for at the evidence levels
above: extraction recovery, automatic current-source indexing, attachment
grounding, measured summary evaluation and bounded notes-first MCP reading.
This record preserves the original failed format assertions and the observed
WS wording limitations. It does not turn scoped lifecycle success into perfect
whole-answer reliability, a production accuracy estimate, universal client
compatibility or a claim of global data erasure.
