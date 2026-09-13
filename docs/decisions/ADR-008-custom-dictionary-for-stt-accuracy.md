# ADR-008: Per-User Dictionaries for Speech Recognition

- Status: Accepted; phonetic TSV uploads and automatic suggestion ingestion remain
  original plans, not current behavior.
- Decision date: Not recorded; original rationale references the 2026-04-23 STT evaluation.
- Implementation checked: 2026-09-13.

## Context and decision

General speech recognition misrecognized service names, acronyms, and
customer-specific terminology. A fixed system vocabulary could not cover each
user's meetings. Store a personal dictionary and use it for Transcribe custom
vocabularies, with prompt guidance as the complementary Whisper path.

A shared organization dictionary was rejected as insufficiently personalized.
Whisper-only prompting avoided resource management but did not serve Transcribe
and could not guarantee term recognition.

## Current implementation

- Dictionary data uses `PK=USER#{userId}`, `SK=DICTIONARY`; terms retain
  `phrase`, `soundsLike`, and `displayAs`, plus vocabulary name/status.
- Authenticated settings routes support GET/PUT `/api/settings/dictionary` and
  DELETE `/api/settings/dictionary/term`.
- `DictionaryService` creates/updates a Korean Transcribe vocabulary using the
  **`Phrases` list**. It does not upload the proposed four-column TSV, send
  `soundsLike`/`displayAs` mappings to Transcribe, or merge system base terms into
  that list. The resource name uses `vocabSuffix(userID)`, not the full literal
  user ID promised in the old example.
- Reads refresh pending vocabulary status. Batch Transcribe selects a ready
  personal vocabulary or its configured base fallback through
  `LanguageIdSettings["ko-KR"]`. Live streaming can also receive a vocabulary name.
- The transcribe Lambda constructs Whisper's `INITIAL_PROMPT` from `displayAs`
  or, when absent, `phrase`. Production Whisper combines it with its optional
  S3 vocabulary prompt.

The old crawler/research extraction Lambda, approval inbox, CSV import, and
fixed vocabulary build time were plans or estimates. They are not requirements
that current PRs must implement, nor evidence that phonetic mappings affect ASR.

## Consequences and risks

Terms persist across meetings and customize both recognition paths. Transcribe
vocabulary builds can be pending or fail, and prompt hints remain probabilistic.
The engines consume different fields; storing a pronunciation does not mean either
path uses it. Resource lifecycle and prompt-length limits remain maintenance
concerns. This feature does not remove the need to review transcripts.

## Evidence

- [dictionary.go](../../backend/internal/model/dictionary.go): stored fields.
- [dictionary.go](../../backend/internal/service/dictionary.go): resource naming,
  status, and phrase-only vocabulary construction.
- [transcribe.go](../../backend/internal/service/transcribe.go),
  [main.go](../../backend/cmd/transcribe/main.go),
  [transcribe.py](../../backend/whisper/transcribe.py): vocabulary consumers.
- [CustomDictionary.tsx](../../frontend/src/components/CustomDictionary.tsx): UI.
- [main.go](../../backend/cmd/api/main.go): registered settings routes.
