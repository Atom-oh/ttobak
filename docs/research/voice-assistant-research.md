# Historical voice-assistant research

Historical research dated 2026-03-24 comparing transport, STT providers, diarization,
VAD, competitor features and possible product positioning. It is not current
provider documentation, pricing, benchmark evidence or a decision to migrate.

## Questions examined

- WebRTC for media/session negotiation, WebSocket for bidirectional cloud streams,
  and SSE for server-to-client results; latency depends on the measured system.
- Managed STT alternatives (AWS Transcribe, Nova Sonic experiments, Deepgram,
  AssemblyAI, hosted Whisper, RTZR) versus self-hosted GPU Whisper.
- Managed diarization versus a pyannote post-processing stage, and whether
  microphone/speaker differences justified hybrid capture/transcription paths.
- Browser VAD options including Silero, WebRTC VAD and RNNoise, balancing bandwidth
  savings against clipping, complexity and delayed speech detection.
- Meeting-bot, browser-extension and native-recording products, with differing
  permission/privacy friction and automation.

The original recommendations were to evaluate alternate managed STT, retain batch
fallback, test diarization/VAD and add collaboration/integrations. Its claims about
Korean-language availability, DER/WER, competitor capabilities, provider prices,
90% savings and subscription pricing were point-in-time research assertions without
project-wide controlled evaluation. Do not reuse them as verified current facts.

## Relationship to the implemented product

TTOBAK currently uses browser AWS Transcribe Streaming for live captions and GPU
Spot Whisper plus pyannote for batch transcription. The old statement that Korean
Transcribe streaming is unavailable does not describe this code path. Nova Sonic
experiments are not the production dual-STT architecture. Action items, Notion,
MCP and a macOS wrapper are no longer merely proposed capabilities.

A new provider/model decision needs current primary documentation, a reviewed
representative audio set, measured loss/latency/cost and data-egress analysis.
The current system is described in [architecture](../architecture.md), with
recording/diarization decisions in ADR-006/019/024/030/035.
