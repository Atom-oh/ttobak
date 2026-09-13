# Multi-Language STT Implementation Plan

> Historical implementation plan. Original date: not recorded. Based on ADR-005.
> The original batch-complete/streaming-pending table is outdated; phase labels and
> optional UI ideas are not current requirements or proof of validation.

## Goal and original priorities

Improve mixed Korean/English meeting transcription without forcing users to select
one language in advance. The original plan treated batch Transcribe as already
configured for language identification, prioritized streaming identification next,
then dynamic translation source selection, with language controls/badges optional.

## Proposed phases

1. **Streaming:** use `IdentifyMultipleLanguages` with `ko-KR,en-US` and a Korean
   preference hint instead of an unconditional single-language parameter. Pass
   detection through the streaming client, manager, and recording session. Preserve
   Web Speech's single-language fallback and verify provider selection rather than
   assume that it can identify multiple languages.
2. **Translation:** use detected language, skip translation when it matches the
   target, and optionally persist batch segment language metadata. This was meant
   to avoid translating already-target-language speech.
3. **Optional UI:** offer automatic/Korean/English modes and detected-language badges.
   These were proposed conveniences, not prerequisites for the core live path.

The plan reused recording and Transcribe infrastructure. Its copied source-line
references and replacement snippets were implementation sketches, not instructions
to remove current fallback/reconnect behavior.

## Current evidence and limits

[ADR-005](decisions/ADR-005-multi-language-auto-detection-for-stt.md) records the current
split. Both browser and native paths in [sttManager.ts](../frontend/src/lib/sttManager.ts)
enable Korean/English detection through
[transcribeStreamingClient.ts](../frontend/src/lib/transcribeStreamingClient.ts).
The manager stores the latest detected language and uses it for interim/batched
translation, with Korean fallback and same-target skipping. That is not a guarantee
of independent language classification for every sentence in a mixed batch.

[TranscribeService](../backend/internal/service/transcribe.go) enables multiple
languages on its batch calls, including the legacy method named Nova Sonic, which
still calls Transcribe. Production [Whisper](../backend/whisper/transcribe.py)
explicitly uses Korean ASR plus vocabulary prompts; it is a different batch path.
The [Cognito Identity Pool](../infra/lib/auth-stack.ts) remains configured for
browser service access, so the old suggestion that its removal blocks streaming
is not the current declared architecture.

Web Speech remains Korean-default and has mobile recording-protection restrictions
under [ADR-030](decisions/ADR-030-mobile-live-captions-never-sacrifice-recording.md).
Do not disable those restrictions or force a competing microphone stream to satisfy
this older plan. Optional selectors, badges, and batch-language storage must be
verified separately rather than inferred from streaming support.

## Risks and validation intent

The original checks covered code-switching, batch compatibility, provider fallback,
and skipping unnecessary translation. Detection delay, unexpected languages,
translation batching, and missing streaming configuration remain relevant test cases.
The original SDK-support question has an implemented code path now; this document
does not prove device testing or current service availability. Historical claims of
cost neutrality were assumptions, not a current price assessment.

Current references: [documentation map](README.md), [API](API-SPEC.md),
[architecture](architecture.md), and [project guide](../CLAUDE.md).
