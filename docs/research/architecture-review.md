# Historical architecture research

Historical assessment dated 2026-03-24: STT, live processing, summarization,
persistence, costs and security. Its architecture diagrams, model names, prices
and improvement estimates describe the assessment baseline, not current operation.

## Original observations and proposals

The report questioned duplicate live/batch transcription, synchronous ECS startup,
coarse audio handling, expensive use of large models for small tasks, missing
acoustic diarization, full-table meeting lookups, DynamoDB item limits, per-share
lookups, JWT verification and broad resource configuration. It proposed indexed
meeting lookup, model specialization, transcript spill storage, post-processing
diarization, browser VAD, alternate managed STT, Spot resilience and more integrations.

It estimated a 30-minute meeting at about $1.08 versus $0.30 after proposed changes,
with examples of $21.60/$6 for 20 meetings and $1,080/$300 for 1,000 meetings.
These were modelled historical scenarios, not measured bills or current prices;
the assumptions included a now-obsolete duplicate-transcription architecture.
Do not use them for current budgeting or as evidence to change a model.

The original tradeoff favored scale-to-zero over always-on GPU for intermittent
use, accepted startup latency, and considered external STT for lower operating
complexity. Provider quality/price claims require fresh independent evaluation.

## Current reconciliation

Current live captions use browser Transcribe Streaming; batch Whisper runs on GPU
Spot ECS. GSI3 lookup, S3 transcript spilling, acoustic pyannote diarization, JWT
signature verification, specialized lightweight tasks and Notion/MCP integrations
are implemented. The old ALB/ECS live-STT diagram and claims that these features
are absent must not guide current PR review.

Use [architecture](../architecture.md), [infrastructure](../INFRA-SPEC.md),
[ADR-009](../decisions/ADR-009-whisper-gpu-ecs-spot-zero-scale.md),
[ADR-012](../decisions/ADR-012-gsi3-sort-key-for-meeting-lookup.md), and
[ADR-035](../decisions/ADR-035-diarization-pyannote4-community1-asr-pins.md).
Current code/configuration still needs scrutiny for new regressions; historical
improvement proposals are neither blanket exemptions nor active requirements.
