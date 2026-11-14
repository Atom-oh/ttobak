# ADR-005: Speech languages and live translation

- Status: Accepted. Covers Korean and English detection, note language, live
  interpretation and on-demand meeting translation. The dictionary is ADR-008.
- Implementation checked: 2026-11-11.

## Decisions

### Recognition languages

- Amazon Transcribe, batch and streaming, identifies multiple languages from
  the candidates `ko-KR` and `en-US`; streaming prefers `ko-KR`. The Mac caption
  overlay may add one extra live language (ADR-018). The streaming client still
  supports a single fixed language as an explicit opt-out.
- The Mac interpret screen asks Transcribe Streaming for speaker labels
  (`ShowSpeakerLabel`) and colours each caption line by the speaker who said
  most of its words; the request was accepted for `ko-KR`, `en-US`, `zh-CN`,
  `ja-JP` and two-language identification in `ap-northeast-2` (checked
  2026-11-14). Labels are kept in memory with the captions. On that screen
  the floating caption windows colour final lines the same way (ADR-018,
  Mac app 0.2.2 or later). Recording captions do not request labels.
- A batch Transcribe job attaches its vocabulary through the Korean entry of
  the language settings (ADR-008). The legacy Nova Sonic method is a Transcribe
  job with the same language options and no vocabulary; its name does not mean
  a separate inference path.
- The Web Speech fallback starts in Korean. Production Whisper forces Korean.
- Final and regenerated notes are written in Korean. `Meeting.noteLanguage`
  accepts only `""`, `ko` and `en` and is pinned in summary source conditions,
  but it does not select the prompt language; English notes come only from the
  separate English-note generation (ADR-029).

### Live interpretation

- Draft (partial-caption) translation is opt-in; finals are always translated.
- Finals request `quality: high` with up to two previous final source texts as
  context, at most 2,000 bytes, frozen when the final is queued. Source text is
  sent, never earlier translations, so a wrong translation cannot steer the
  next one. Drafts use `fast`. The model input cap is 3,000 bytes.
- `/api/translate` counts requests per user in fixed hourly UTC windows with an
  atomic counter. Above `TRANSLATE_HOURLY_LIMIT` (3,000) it returns 429
  `RATE_LIMITED`; above `TRANSLATE_HIGH_HOURLY_LIMIT` (1,500) it downgrades
  `high` to `fast` and reports `limited`. Zero disables a limit. Counter errors fail open: the guard is a cost brake, not an access
  boundary.
- The interpreter call has its own deadline (`TRANSLATE_HIGH_TIMEOUT_MS`, 6,000
  by default) so the Amazon Translate fallback still fits the browser's
  10-second abort. The rate-limit downgrade applies before the model is called.
- Model output must pass `validateInterpretation` (meta labels, echoed
  structure, length, target-script share); rejected output falls back to Amazon
  Translate. Output longer than four times the source plus 200 bytes and a
  Korean target under 30 percent Hangul are rejected. Only forms that announce
  a translation are rejected as meta labels, and a Korean target may keep at most four Latin words that all occur
  in a Latin-only source. The UI labels each line as AI, machine or machine
  fallback.
- Amazon Translate to Korean uses the formal register; profanity masking is off.
- Finals that are only hesitation sounds are skipped without a request. The
  filler set is fixed and excludes meaningful short Korean words and numbers.
- The metering log records engine, languages, byte counts, latency and fallback
  reason, never text or a user ID.

### Meeting translation on demand

- Live translations are not stored. `POST /api/meetings/{id}/translation`
  translates final notes or the selected transcript; the result lives only in
  the response and client memory.
- Pages come only from `MeetingReadingService`, so authorization, restricted
  summaries and revision-bound cursors are the reading endpoint's own.
- A non-final page ends at a line, a speaker header or a whole segment; a
  wrapper cursor carries an open fence or the speaker. No binding is invented
  and no text is skipped.
- Links, code, HTML and URLs travel as placeholders that must round-trip; a line
  with a failed piece keeps its source text and is counted. Transcript text is
  sent without labels or times.
- A line is treated as Korean at 30 percent Hangul or more, as English when it
  is Latin only, and otherwise as the non-target language; other scripts are
  left as they are.
- A request is bounded to 12 chunks of 3,000 bytes, four at a time, a 20-second
  budget and a 64 KB response, with a per-user daily byte brake charged on
  attempt (`MEETING_TRANSLATE_DAILY_BYTES`, 2 MB by default).
  `MEETING_TRANSLATION_ENABLED` defaults off.

## Not implemented or off by default

- `MEETING_TRANSLATION_ENABLED` defaults off.
- Merging short finals is not implemented; fragments are measured first and
  merging is revisited only if they exceed 15 percent.
- The meeting glossary is not applied to on-demand translation.

## Limits and accepted risks

- Candidate restriction does not guarantee technical-term accuracy or support
  unexpected languages. Initial detection can delay captions.
- Web Speech and Whisper do not switch languages automatically.
- Live speaker labels are acoustic guesses on one mixed channel (microphone
  plus system audio): only final results carry them, some finals carry none
  (seen with two-language identification), similar voices can merge, and the
  numbering restarts when the caption stream reconnects, so a colour can move
  to another person mid-session.
- The translation limiter fails open on counter errors.
- Real per-meeting request rates, the interpreter's latency distribution and
  the validation rejection rate have not been measured in production; the
  limits and the timeout are deliberately generous and should be tuned from
  the metering logs.

## Source

`backend/internal/handler/translate.go`,
`backend/internal/service/transcribe.go`, `translate.go`,
`meeting_translation.go`, `ratelimit.go`, `backend/cmd/transcribe/main.go`,
`backend/whisper/transcribe.py`, `frontend/src/lib/sttManager.ts`,
`transcribeStreamingClient.ts`, `liveTranslation.ts`,
`infra/lib/gateway-stack.ts`.
