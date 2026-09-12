# QA retrieval helpers implementation plan

> **For agentic workers:** Execute this independently testable prerequisite inline; the public QA integration follows separately.

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

- [x] Verify the current main QA suite before preparing the split.
- [x] Add direct contract tests in `test_retrieval_helpers.py`: paginated discovery, post-discovery revocation, current saved text, stale binary snapshots, and DynamoDB-deserialized session versions.
- [x] Bring the three existing integration helper modules into this isolated prerequisite.
- [x] Verify `restore_sources` after an actual boto3 `TypeSerializer`/`TypeDeserializer` round trip. Keep changed or untracked histories non-replayable.
- [x] Register the tests through `test_handler.load_tests` and verify the complete suite.
- [x] Open main-targeted PR #215 below the review size limit.
- [x] Reproduce the review's bare `permission` projection failure, then add its DynamoDB attribute-name alias.

## Release boundary

This change does not activate retrieval, migrate old files, run an ingestion job, or change model prompts. The active handler still uses its existing search and session implementation. The follow-up integration must add source details through an explicit public-field allowlist and verify live create/edit/delete/revoke behavior before indexing activation.
