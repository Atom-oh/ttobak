# ADR-005: Speech languages, live translation and the dictionary

- Status: Accepted. Consolidates the former ADR-005 (Korean and English
  detection, note language, live interpretation, meeting translation) and
  ADR-008 (dictionaries for speech recognition).
- Implementation checked: 2026-11-11.

## Decisions

### Recognition languages

- Amazon Transcribe, batch and streaming, identifies multiple languages from
  the candidates `ko-KR` and `en-US`; streaming prefers `ko-KR`. The Mac caption
  overlay may add one extra live language (ADR-069).
- The Web Speech fallback starts in Korean. Production Whisper forces Korean.
- Final and regenerated notes are written in Korean. `Meeting.noteLanguage`
  accepts only `""`, `ko` and `en` and is pinned in summary source conditions,
  but it does not select the prompt language; English notes come only from the
  separate English-note generation (ADR-070).

### Live interpretation

- Draft (partial-caption) translation is opt-in; finals are always translated.
- Finals request `quality: high` with up to two previous final source texts as
  context (source text, never earlier translations). Drafts use `fast`.
- `/api/translate` counts requests per user in hourly UTC windows. Above
  `TRANSLATE_HOURLY_LIMIT` it returns 429 `RATE_LIMITED`; above
  `TRANSLATE_HIGH_HOURLY_LIMIT` it downgrades `high` to `fast` and reports
  `limited`. Counter errors fail open: the guard is a cost brake, not an access
  boundary.
- The interpreter call has its own deadline (`TRANSLATE_HIGH_TIMEOUT_MS`) so the
  Amazon Translate fallback still fits the browser timeout.
- Model output must pass `validateInterpretation` (meta labels, echoed
  structure, length, target-script share); rejected output falls back to Amazon
  Translate. The UI labels each line as AI, machine or machine fallback.
- Amazon Translate to Korean uses the formal register; profanity masking is off.
- Finals that are only hesitation sounds are skipped without a request.
- The metering log records engine, languages, byte counts, latency and fallback
  reason, never text or a user ID.

### Meeting translation on demand

- Live translations are not stored. `POST /api/meetings/{id}/translation`
  translates final notes or the selected transcript; the result lives only in
  the response and client memory.
- Pages come only from `MeetingReadingService`, so authorization, restricted
  summaries and revision-bound cursors are the reading endpoint's own.
- Links, code, HTML and URLs travel as placeholders that must round-trip; a line
  with a failed piece keeps its source text.
- Requests are bounded by chunk count, chunk size, concurrency, a time budget, a
  response size and a per-user daily byte brake.
  `MEETING_TRANSLATION_ENABLED` defaults off.

### Dictionary

- Three layers: Personal (`USER#{userId}`), Account (`ACCOUNT#{accountId}`) and
  Common (`DICT#COMMON`). Terms are `DICTTERM#{termKey}` rows and observations
  `DICTOBS#` rows; `termKey` is the SHA-256 of the normalized term.
- Caps per scope: 300 automatic terms, 500 manual terms, 2,000 tombstones.
- `ValidateTermInput` and the embedded blocklist validate every manual write.
- Every Account write carries a membership condition check on
  `ACCOUNT#{id}` / `MEMBER#{userId}` in the same transaction.
- Any signed-in user may edit or delete Common terms; each write records an
  audit row, and deletes are restorable tombstones. A Common term a user edited
  is left out of the transcript-correction glossary, so open editing cannot
  become silent transcript rewriting.
- Automatic terms from a tainted meeting are stored as `suggested`, not active.
- Consumers: one personal `ko-KR` Transcribe vocabulary per user, built by the
  A/B builder (`ttobak-vocab-*`), and a Whisper `INITIAL_PROMPT` of at most 300
  runes built from `displayAs`, else the phrase. An inactive personal row does
  not shadow the same key from Account or Common.
- Batch Transcribe always attaches a vocabulary to `ko-KR`: the caller's name,
  else the fixed base vocabulary `ttobak-aws-tech-terms`.
- Live streams receive no vocabulary name.
- Glossary corrections are applied silently; a refined segment that gains
  glossary forms without a kept correction record is re-run on the plain prompt
  and then falls back to raw text.
- Daily news feeds Common only, needs two distinct sources, and excludes
  custom-URL documents. `ttobak:newsTermsSources=all` makes every searched,
  non-synthetic source eligible (owner decision 2026-11-09).

## Not implemented or off by default

- Automatic term extraction runs only with `AUTO_TERMS_MODE=personal`.
- `ttobak:accountTermsMode`, `transcriptCorrection`, `kbTermsMode`,
  `researchTermsMode` and `newsTermsMode` default off; enable one per deployment.
- Account-layer cleanup when a source is unlinked from an Account is not
  implemented and is required before `accountTermsMode`, or the Account mode of
  `kbTermsMode` or `researchTermsMode`, is turned on. Only meeting deletion
  sweeps an Account today.
- `en-US` vocabularies, KB terms from PDF or Office files, promotion between
  Personal and Account, and merging short finals are not implemented.

## Limits and accepted risks

- Candidate restriction does not guarantee technical-term accuracy or support
  unexpected languages. Vocabulary and prompt hints are probabilistic.
- Tombstones keep the unsalted `termKey` with no TTL, so a table reader can
  confirm a deleted term by hashing candidates.
- With `newsTermsSources=all`, a term from one user's subscribed sources becomes
  visible to all users once two sources name it (owner accepted, 2026-11-10).
- Translation and web-search limiters fail open on counter errors.

## Source

`backend/internal/service/transcribe.go`, `translate.go`, `dictionary_*.go`,
`terms_*.go`, `vocabulary_*.go`, `backend/internal/model/dictionary.go`,
`backend/cmd/transcribe/main.go`, `backend/whisper/transcribe.py`,
`frontend/src/lib/sttManager.ts`, `transcribeStreamingClient.ts`,
`liveTranslation.ts`, `infra/lib/gateway-stack.ts`.
