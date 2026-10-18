# ADR-005: Korean and English Detection in Transcribe

- Status: Accepted for AWS Transcribe paths; primary batch processing later
  changed under [ADR-009](ADR-009-whisper-gpu-ecs-spot-zero-scale.md).
- Decision date: Not recorded; original implementation referenced commit `ad5905b`.
- Implementation checked: 2026-09-13; amended 2026-10-18 (interpretation cost guard;
  high-quality finals and output validation).

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

## Amendment 2026-10-18: interpretation cost guard and Korean formality

Live interpretation reuses this detection to choose a direction per utterance;
its cost and register are decided here until a separate interpretation ADR exists.

- Partial-caption (draft) translation is opt-in in the recording controls. Every
  partial revision was a billed request; finals alone bound the request count to
  utterances. Finals still used `fast` in that change; the next amendment moves
  them to `high`.
- `/api/translate` counts requests per user in fixed hourly UTC windows
  (`service.WindowCounter`, atomic `ADD`, TTL = window end + 2h so a daily window
  would live at least 26h). Above `TRANSLATE_HOURLY_LIMIT` (3000) it returns 429
  `RATE_LIMITED`; above `TRANSLATE_HIGH_HOURLY_LIMIT` (1500) `high` is downgraded to
  `fast` with `limited: true`. 0 disables a limit. Counter errors fail open because
  the guard is a cost/abuse brake, not an access boundary, matching QA's web-search
  limiter. Values are CDK context rendered into Lambda env.
- A metering log line records engine, languages, byte counts, latency, `limited`
  and fallback reason, never text or user ID, so limits can be tuned from data.
- Amazon Translate output to Korean requests the formal register; meeting speech
  addresses colleagues and customers. Profanity masking stays off so translations
  do not alter what was said.

Rejected: a route-level API Gateway throttle (not per user, and it would also block
`fast` finals) and failing closed on counter errors (would stop captions during a
DynamoDB incident). Not yet measured: real per-meeting request rates; the defaults
are deliberately generous and should be revisited from the metering logs.

## Amendment 2026-10-18: high-quality finals, output validation and filler skip

- Finals request `quality: high`; drafts stay `fast`. Each final's `context` is the
  up to two previous final source texts (2,000 bytes, the API bound), frozen when
  the final is queued so the two concurrent requests cannot see each other's
  completion order. Source text, not translations, is sent: an earlier wrong
  translation must not steer the next one.
- The interpreter call has its own deadline (`TRANSLATE_HIGH_TIMEOUT_MS`, 6000) so
  the Amazon Translate fallback still fits the browser's 10s abort; the 3,000-byte
  model cap is unchanged. The rate-limit downgrade from the cost guard still
  applies before the model is called.
- Model output passes `validateInterpretation` (meta labels, echoed JSON structure,
  length, target-script share). Rejection uses Amazon Translate with
  `fallbackReason: invalid-output`; a wrong-language or annotated caption is worse
  than a plainer machine translation. `Here is`/`Here's` alone is accepted because
  it is ordinary speech; only translation-announcing forms are rejected. A Korean
  target may keep at most four Latin words that all occur in a Latin-only source.
- The UI labels each completed line AI, machine or machine-fallback from the
  response engine, so listeners know which lines came from the fallback.
- Finals that are only hesitation sounds are marked skipped without a request; the
  set (`FILLER_TOKEN`) is fixed and excludes meaningful short Korean words (yes,
  that) and numbers.
- Short-final fragments are measured first (`?debugCaptions=1` counter and the
  metering `inBytes` field). Merging short finals is not implemented; revisit only
  if measured fragments exceed 15%.

Not yet measured: the interpreter's latency distribution and validation rejection
rate in production; tune the timeout from the metering logs.

## Amendment 2026-10-18: on-demand meeting translation (G1)

- Live translations are not persisted. Readers translate final AI notes or the
  selected transcript with `POST /api/meetings/{id}/translation`; the result
  exists only in the response and client memory.
- Pages come only from `MeetingReadingService`, so read authorization, restricted
  summaries and revision-bound cursors are the reading endpoint's own. Non-final
  pages end at a line, speaker header or whole segment; a wrapper cursor carries an
  open fence or the text-transcript speaker. No binding is invented or text skipped.
- Links, code, HTML and URLs are atomic `⟦Ln⟧` placeholders that must round-trip,
  and transcript text is sent without labels or times. A line with any failed piece
  keeps its source and is counted. Lines go as Korean (≥30% Hangul), English
  (Latin only) or, if code-mixed, the non-target language; other scripts stay.
- Bounds: 12 chunks of 3,000 bytes, concurrency 4, a 20 s budget, a 64KB response
  and a daily per-user byte brake charged on attempt (`MEETING_TRANSLATE_DAILY_BYTES`).
  `MEETING_TRANSLATION_ENABLED` defaults off.
- Not included: the meeting glossary; a single line longer than a page splits at
  the reader's cut.

## Evidence

- [transcribe.go](../../backend/internal/service/transcribe.go): both batch methods.
- [sttManager.ts](../../frontend/src/lib/sttManager.ts),
  [transcribeStreamingClient.ts](../../frontend/src/lib/transcribeStreamingClient.ts),
  [speechRecognition.ts](../../frontend/src/lib/speechRecognition.ts): live paths.
- [transcribe.py](../../backend/whisper/transcribe.py): production Whisper options.
- [ratelimit.go](../../backend/internal/service/ratelimit.go),
  [translate.go handler](../../backend/internal/handler/translate.go),
  [translate.go service](../../backend/internal/service/translate.go),
  [liveTranslation.ts](../../frontend/src/lib/liveTranslation.ts): 2026-10-18 amendment.
- [meeting_translation.go](../../backend/internal/service/meeting_translation.go),
  [meeting translation handler](../../backend/internal/handler/meeting_translation.go),
  [MeetingTranslation.tsx](../../frontend/src/components/meeting/MeetingTranslation.tsx):
  on-demand meeting translation amendment.
