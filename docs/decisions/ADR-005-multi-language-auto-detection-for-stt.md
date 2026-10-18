# ADR-005: Korean and English Detection in Transcribe

- Status: Accepted for AWS Transcribe paths; primary batch processing later
  changed under [ADR-009](ADR-009-whisper-gpu-ecs-spot-zero-scale.md).
- Decision date: Not recorded; original implementation referenced commit `ad5905b`.
- Implementation checked: 2026-09-13.

## Context and decision

Meetings mix Korean speech with English technical terms. A fixed Transcribe
language setting often distorted code-switching. Choose automatic detection
restricted to `ko-KR` and `en-US`, avoiding a manual language selector or two
parallel single-language jobs with an ambiguous merge.

## Current implementation

- Standard batch Transcribe uses `IdentifyMultipleLanguages: true` with both
  language options. Its custom vocabulary is attached through the Korean entry
  in `LanguageIdSettings`.
- The legacy `StartNovaSonicTranscription` method also calls Transcribe
  `StartTranscriptionJob` with both language options. Its name does not establish
  a separate Nova Sonic inference path.
- Both Transcribe Streaming paths in `sttManager.ts` enable multiple-language
  identification, prefer `ko-KR`, and pass `ko-KR,en-US`. The client's explicit
  opt-out branch still supports a single `LanguageCode`.
- Web Speech fallback defaults to Korean; it does not implement equivalent
  automatic switching. Mobile fallback restrictions in ADR-030 protect recording
  from microphone contention.
- Production batch Whisper is separate: `transcribe.py` explicitly sets
  `language="ko"` and uses vocabulary prompts. This ADR does not claim that every
  current STT engine auto-detects multiple languages.

## Note language amendment, 2026-10-18

STT detection stays automatic, but the generated-note language is now a
per-meeting choice. `Meeting.noteLanguage` accepts only `""` (automatic), `ko`
and `en` through the existing meeting update with edit permission.
`noteLanguageDirective` in `backend/internal/service/bedrock.go` maps it to a
fixed sentence for both batch and saved-source summaries; stored text never
enters the prompt verbatim, and automatic keeps the previous sentence byte-for-byte
so recorded evaluation request hashes still hold. English notes translate the
section headings and keep `[TS:NNN]`/`[DOC:...]` markers. Batch and re-summary
captures pin `noteLanguage` in their source conditions (ADR-040), so a mid-run
change yields `SOURCE_CHANGED` instead of publishing notes in the old language.
Live captions, live summaries and translation keep their own language handling.

## Consequences and risks

Transcribe can identify either expected language without an extra user choice.
Restricting candidates reduces ambiguity but does not guarantee accurate
technical terms or support for unexpected languages. Initial detection can delay
captions; Web Speech and Whisper retain different language behavior. The original
accuracy and latency statements were expectations, not guarantees.

## Evidence

- [transcribe.go](../../backend/internal/service/transcribe.go): both batch methods.
- [sttManager.ts](../../frontend/src/lib/sttManager.ts),
  [transcribeStreamingClient.ts](../../frontend/src/lib/transcribeStreamingClient.ts),
  [speechRecognition.ts](../../frontend/src/lib/speechRecognition.ts): live paths.
- [transcribe.py](../../backend/whisper/transcribe.py): production Whisper options.
