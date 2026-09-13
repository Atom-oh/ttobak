# QA source access release plan

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

**Goal:** Keep the complete REST/WebSocket integration and its tests reviewable within the 100 KB patch limit.

**Scope:** Extract the existing prepared source-context and source-tool implementation into independently tested modules. This prerequisite does not import them into the live handler or register new tools.

## Modules and contracts

- `backend/python/qa/source_access.py`: `SourceAccess` receives a current `SourceReader`, `AttachmentReader`, and shared-meeting discovery callback. Optional search dependencies (`query_all`, provider, KB ID, legacy-identity cache callback) are supplied only by the retrieval caller. It owns authorized source contexts, fresh retrieval, attachment continuation, and source dependency checks.
- `backend/python/qa/source_tools.py`: typed document/attachment tool definitions, bounded result formatting and execution through authenticated context callbacks. The later `tools.py` wrapper retains its existing error boundary.
- `backend/python/qa/test_kb_fixtures.py`: synthetic PDF/DOCX bytes and immutable snapshot fixtures, shared by direct source-access tests and the full private/shared KB integration suite.
- `backend/python/qa/test_source_access.py`: direct tests for new-term discovery, post-discovery revocation, current notes with live transcript, optional attachment failure, document continuation, retained extraction results, source metadata allowlisting, and private/shared producer revision vectors.

## Validation and integration

- Recorded at the time: Preserve every existing test through `test_handler.load_tests`.
- Recorded at the time: Run the standalone prerequisite through the canonical QA suite.
- Recorded at the time: Apply the module extraction to the complete prepared handler/tools integration and preserve every original private/shared and REST/WebSocket regression.
- Recorded at the time: Preserve the PR #215 Decimal-session and reserved-permission-alias fixes.
- Recorded at the time: Reproduce and fix duplicate-score ordering, keyword crowding of verified files, false file-pending state after conversion to Markdown, and unbounded legacy excerpts.
- Recorded at the time: Preserve verified body evidence when one result is requested and keep title-only keyword candidates behind body evidence.
- Recorded at the time: Move three direct retrieval-engine tests and two producer-vector tests beside their implementations; share the private/shared binary fixtures and retain every handler/model/session integration test for the runtime release.
- Not verified by this record: Obtain the latest-HEAD AI review and CI results.
- Not verified by this record: Merge after prerequisite PR #215.

The complete runtime integration now delegates to these modules. No fallback is removed to fit the patch cap; no tests are dropped. The full runtime still needs source-read IAM and `KB_BUCKET_NAME`, automatic private/shared binary snapshots, and deployed create/edit/delete/revoke acceptance before the indexing worker is activated.
