# Historical audio pipeline review

Historical review dated 2026-03-27, authored by Claude Opus 4.6 using three parallel
exploration tasks and user symptoms. Other model names in the original denoted
review lenses; Gemini/Codex/Kiro were not actually called. The original reported
24 candidates (3 Critical, 7 High, 3 Medium-High, 8 Medium, 3 Low). Counts and severity
are historical observations, not a current vulnerability inventory.

## Recorded candidates

| ID | Historical concern |
|---|---|
| FR-001 | Missing MediaRecorder error handling could silently lose audio |
| FR-002 | Suspended AudioContext lacked resume handling; subsequently marked fixed |
| FR-003 | Ended device tracks lacked a visible disconnect path |
| FR-004 | Repeated checkpoint Blob construction could accumulate memory |
| FR-005 | Unspecified codec/bitrate could reduce capture quality |
| FR-006 | Stop callback order could race final MediaRecorder data |
| FR-007 | Checkpoint timer might not restart after pause/resume |
| FR-008 | Unsupported MIME fallback could select an invalid format |
| FR-009 | iPad desktop user-agent detection; later marked inapplicable |
| FR-010 | Preview and capture could create redundant AudioContexts |
| FS-001 | Web Speech restarts could introduce caption gaps |
| FS-002 | Independent recording/caption state needed visible separation |
| FS-003 | Tab switching interrupted recognition/restart |
| FS-004 | Treating network errors as fatal could permanently stop captions |
| BT-001 | Hardcoded Korean language limited batch recognition |
| BT-002 | Format/sample-rate assumptions could mismatch recorded media |
| BT-003 | Transcribe job names could collide |
| BT-004 | Multiple state writers could overwrite progress |
| BT-005 | Nova Sonic path was an incomplete experiment |
| BS-001 | Event filtering needed protection from recursive processing |
| BS-002 | The then-short summarize timeout could terminate work |
| BS-003 | Transcript segments lacked sufficient S3 spill handling |
| BS-004 | Swallowed summarize errors could prevent recovery |
| IN-001 | Fixed total upload timeout rejected large/slow transfers |

User symptoms included recovery after stop/resume, missing microphone input,
caption loss on tab changes, network errors and failed live summaries. Those
symptoms motivated prioritizing recording integrity and visible failures before
model changes. The original plan grouped work into immediate loss prevention,
recording quality, backend reliability and edge cases; its durations were estimates.

## Current interpretation

The old report's Web-Speech-only/current-versus-removed-ECS narrative is obsolete.
Current live captions use browser Transcribe Streaming with guarded fallback;
batch STT uses configured GPU Whisper. Mobile wake-lock, stall/reconnect and
manual gesture recovery are implemented (ADR-030). Final native WAV transport is
Rust streaming, with small PCM events for captions (ADR-024).

Summarize timeout/retry policy is now ADR-031. Transcript-family spill budgets and
UTF-16 condition guards live in repository code. Upload timeouts track stalled
progress, not the historical proposed file-size-based total timeout. Current
diarization/ASR pins are ADR-035. Do not reapply old code snippets or assert that
any candidate above is still unfixed without a present-revision reproduction.

Inspect `frontend/src/components/RecordButton.tsx`, `frontend/src/lib/sttManager.ts`,
`transcribeStreamingClient.ts`, `speechRecognition.ts`, recording hooks,
`backend/cmd/{transcribe,summarize}/`, and repository code. Current guidance:
[UI](DESIGN-SPEC.md), [API](API-SPEC.md), [project guide](../CLAUDE.md).
