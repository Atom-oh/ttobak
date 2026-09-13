# Historical issue ledger

Historical user reports and recorded resolutions. Original issue IDs are retained;
ISSUE-021 was accidentally used twice. A historical Resolved label records the
old author's conclusion, not a fresh test of current code. Use current source,
ADRs and the project guide before reopening any item.

## Unresolved legacy report

**ISSUE-021 (recording start):** entering New Recording and pressing Record reportedly
failed, with `/api/meetings` returning 401 in the supplied browser log. No reliable
current reproduction or resolution was recorded. Old minified stack traces and
extension logs did not establish a cause. If reproduced, trace current Cognito
refresh, runtime config and recording initialization; do not infer a missing JWT
implementation from this old 401.

## Recorded resolutions

| ID | Original symptom | Recorded resolution / historical scope |
|---|---|---|
| ISSUE-001 | Live QA returned 500 | Surface Bedrock invocation errors |
| ISSUE-002 | Attachment update helper alleged absent | Confirmed updateAttachmentByKey already implemented |
| ISSUE-003 | Summarize event contract alleged inconsistent | Confirmed matching EventBridge S3 event handling |
| ISSUE-004 | Short QA questions detected too slowly | Tune text/time/debounce thresholds |
| ISSUE-005 | Per-shared-meeting lookups | Introduce batch retrieval; old implementation description is not today's query contract |
| ISSUE-006 | AudioContext cleanup alleged absent | Confirmed stop closes context |
| ISSUE-007 | Backend JWT only decoded | Add JWKS/signature validation; current ParseVerifiedJWT contract supersedes the old fallback description |
| ISSUE-008 | S3 percent-encoded keys mishandled | Decode URL-encoded event keys |
| ISSUE-009 | Selected microphone had no preview level | Create/clean preview analyser and switch to recording analyser |
| ISSUE-010 | Captured image appeared unprocessed | Notify upload completion and refresh attachment data |
| ISSUE-011 | Sharing search looked inactive | Explain minimum search length and empty results |
| ISSUE-012 | Agentic QA 500 regression | Handle Converse and tool failures visibly |
| ISSUE-013 | Overlong summaries | Tighten then-current prompt/output budget; not a permanent current token cap |
| ISSUE-014 | Upload/create flow stuck | Use server meeting ID for presigned path and parent-managed upload |
| ISSUE-015 | QA model invocation always failed | Correct then-current inference-profile/region/IAM pairing; old aliases are historical |
| ISSUE-016 | External microphone ignored | Request exact selected device rather than an ideal fallback |
| ISSUE-017 | Upload/retranscription blocked navigation | Save live context and move upload work; later batch pipeline design supersedes the old archive-only description |
| ISSUE-018 | Synchronous ECS live startup exceeded Lambda timeout | Old realtime service used async start/status; those removed endpoints are not current requirements |
| ISSUE-019 | Cascading recognition restarts after a few seconds | Guard overlapping restart/onend paths |
| ISSUE-020 | Post-record processing toast never ended | Bound API waits and provide dismiss/error paths |
| ISSUE-021 (ECS) | Old realtime start lacked ECS permissions | Historical policy addition; do not repeat out-of-band IAM edits from this record |
| ISSUE-022 | Leading interim text lost on restart | Promote interim text and ignore stale recognition-instance events |
| ISSUE-023 | Long sentences fragmented | Stop treating connective endings as sentence boundaries |
| ISSUE-024 | Awaited realtime stop blocked processing | Bound old stop request and avoid blocking the UI |

Other recorded UI work improved question-detection timing, persistent desktop QA
and mobile sheets, transcript entry separation/deduplication, stalled recognition
recovery, and nonblocking post-record banners.

Current recovery is specified in ADR-024/030/031, current API routes in
[API-SPEC](docs/API-SPEC.md), and known limitations in [CLAUDE.md](CLAUDE.md).
New issue reports should include revision, reproduction, observed/expected behavior,
relevant logs with secrets/content removed, and evidence separating a new defect
from an existing limitation.
