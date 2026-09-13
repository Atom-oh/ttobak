# QA retrieval helpers implementation plan

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

**Goal:** Validate current-source search candidates and conversation dependencies before connecting them to the live QA handler.

**Architecture:** `indexed_retrieval.py` uses the existing `SourceReader` for fresh authorization and source bindings. `manual_kb.py` handles private and authenticated-shared immutable binary snapshots. `session_provenance.py` tracks source revisions separately from model messages.

**Tech Stack:** Python, boto3 resource conditions, standard-library unittest.

**Spec:** [Source contract](../../../backend/python/qa/SOURCE_CONTRACT.md), [index source contract](../../../backend/internal/service/INDEX_SOURCE_CONTRACT.md).

## Constraints

- Preserve the current public handler, tools, IAM, and manual/shared KB behavior in this prerequisite.
- Never accept a stale indexed body as current saved text.
- Authorize canonical source reads again after discovery; parent account membership grants no child access.
- S3 failures propagate; an unavailable source is not a deleted source.
- Keep every existing test in the canonical `python3 -m unittest test_handler -v` command.
- The later runtime release requires source-read IAM, `KB_BUCKET_NAME`, the private/shared snapshot producer, and the remaining integration tests.

## Tasks

- Recorded at the time: Verify the current main QA suite before preparing the split.
- Recorded at the time: Add direct contract tests in `test_retrieval_helpers.py`: paginated discovery, post-discovery revocation, current saved text, stale binary snapshots, and DynamoDB-deserialized session versions.
- Recorded at the time: Bring the three existing integration helper modules into this isolated prerequisite.
- Recorded at the time: Verify `restore_sources` after an actual boto3 `TypeSerializer`/`TypeDeserializer` round trip. Keep changed or untracked histories non-replayable.
- Recorded at the time: Register the tests through `test_handler.load_tests` and verify the complete suite.
- Recorded at the time: Open main-targeted PR #215 below the review size limit.
- Recorded at the time: Reproduce the review's bare `permission` projection failure, then add its DynamoDB attribute-name alias.

## Release boundary

This change does not activate retrieval, migrate old files, run an ingestion job, or change model prompts. The active handler still uses its existing search and session implementation. The follow-up integration must add source details through an explicit public-field allowlist and verify live create/edit/delete/revoke behavior before indexing activation.
