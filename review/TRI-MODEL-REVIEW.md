# Historical three-model review

Historical review dated 2026-03-25, recorded as Claude Opus (architecture), Gemini
2.5 Pro (review), and Codex gpt-5.4 (verification). The original Codex addendum
reported over 30 files examined and 126K tokens. This document preserves the
review's topics; model agreement and historical severity do not validate them
against current code.

| Recorded severity | Claude | Gemini | Codex |
|---|---|---|---|
| Critical | 2 | 3 | 2 |
| High | 4 | 3 | 6 |
| Medium | 8 | 4 | 7 |
| Low | 6 | 1 | 2 |

## Candidate groups

Shared concerns were image processing racing attachment creation, JWT trust,
shared IAM roles and per-meeting read cost. Two-model concerns included deletion
atomicity, hardcoded ARNs and inline edge code.

Additional candidates included public AOSS networking, browser token storage,
whole-item concurrent overwrites, unused DynamoDB streams, S3 key decoding,
string-matched errors, refresh lifetime, ambiguous access-helper results,
Gateway/QA authorization, upload ownership, Notion-key encryption, KB tenant
filters, incomplete KB handlers, read-triggered status reconciliation, session
scoping, cold-start client setup, KB deletion scans, ignored detail-read errors,
missing tests and stale README/scaffold files.

The reviewers disagreed on IAM/N+1 severity and image-race remedies. Proposed work
was grouped into data/security protection, performance, reliability and cleanup.
Those proposals do not authorize new changes or establish current missing features.

## Current reconciliation

Image processing now uses custom ImageUploadCompleted, after attachment creation;
raw writes under images/ are not the trigger. JWT verification, HTTP authorizers,
server ownership checks, conditional updates and multiple test suites exist.
GetMeeting/SimRun read-triggered reconciliation is intentional current behavior,
not automatically a defect because it has a write side effect.

Root [project guidance](../CLAUDE.md) records precise invariants and accepted gaps.
Public AOSS declarations and optional origin verification remain documented
infrastructure discrepancies; do not claim complete compliance. Recheck every old
candidate against the proposed diff and current source. The live review process is
[PR review operations](../docs/runbooks/pr-review.md), not this 2026-03 roster.
