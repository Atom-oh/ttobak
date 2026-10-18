# ADR-008: Per-User Dictionaries for Speech Recognition

- Status: Accepted; phonetic TSV uploads and automatic suggestion ingestion remain
  original plans, not current behavior.
- Decision date: Not recorded; original rationale references the 2026-04-23 STT evaluation.
- Implementation checked: 2026-09-13; v2 storage and term CRUD amendments checked 2026-10-18.

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
- Reads refresh pending vocabulary status with a conditional update (see the
  2026-10-18 amendment); they no longer rewrite the dictionary item. Batch Transcribe selects a ready
  personal vocabulary or its configured base fallback through
  `LanguageIdSettings["ko-KR"]`. Live streaming can also receive a vocabulary name.
- The transcribe Lambda constructs Whisper's `INITIAL_PROMPT` from `displayAs`
  or, when absent, `phrase`. Production Whisper combines it with its optional
  S3 vocabulary prompt.

The old crawler/research extraction Lambda, approval inbox, CSV import, and
fixed vocabulary build time were plans or estimates. They are not requirements
that current PRs must implement, nor evidence that phonetic mappings affect ASR.

## Amendment 2026-10-18: v2 storage and union readers

Per-term rows now sit beside the legacy `SK=DICTIONARY` item so automatic terms
can be added later without rewriting a shared list. This covers storage and
readers only: no extraction, term CRUD route, vocabulary builder or UI change.

- **Terms** `DICTTERM#{termKey}` live in the owning partition (`USER#`; `ACCOUNT#`
  is reserved). `termKey` is 32 lowercase hex of SHA-256 over
  `NormalizeTermKey(displayAs)` (NFKC, lowercase, whitespace and `- . _ ·`
  removed). Rows carry `origin=manual|auto`, `state=active|suggested|deleted`
  and a `version` that guards every update. A user edit sets `userEditedAt`,
  which blocks automatic merge and eviction. Delete leaves a tombstone that
  drops the content, counters and recognition flags but keeps `termKey`, scope,
  origin, state, version and timestamps; creates require
  `attribute_not_exists(PK)`, so a tombstoned term is never re-added
  automatically. `termKey` is an unsalted hash of a short, often guessable
  term, so anyone who can read the table can confirm a deleted term by hashing
  candidates. Tombstones have no TTL and live as long as the owning partition;
  a salted key or a retention limit is an open decision for the term CRUD work.
- **Observations** `DICTOBS#{termKey}#{meetingId}` record provenance per meeting.
  A merge creates the observation and bumps the term (`ADD hits 1`) in one
  transaction, so re-running a meeting never inflates `hits`.
- **Vocabulary state** `VOCAB#{ctx}#{lang}` holds the active Transcribe name and
  build status under a version guard.
- **Non-destructive migration.** `MigrateLegacyDictionary` copies blob terms into
  `origin=manual` rows marked `legacyMigrated` (other existing rows and
  tombstones win), reconciles pristine copies left by an earlier partial run,
  writes or reconciles `VOCAB#personal#ko-KR` with the existing vocabulary
  name, then sets `migratedAt` only if the blob still has the `blobRevision`
  and `updatedAt` it copied; every legacy PUT bumps `blobRevision`. Every row
  create, refresh, delete and `VOCAB#` write shares a transaction with a
  ConditionCheck on the legacy item requiring that same revision and no
  `migratedAt` (at most 99 puts plus the check), so a run that copied a
  superseded blob, or lost to a concurrent run that already migrated, writes
  nothing further and re-reads (`ErrLegacyBlobChanged`). Concurrent and repeated
  runs therefore leave pristine copies matching the blob that was marked. Blobs whose
  distinct terms share a `termKey`, or have none, are refused
  with `ErrLegacyDictionaryConflict` and nothing is written; exact duplicates
  collapse. An empty legacy `displayAs` is still returned as empty. Migration
  never removes blob fields. It has no caller yet; term
  CRUD must first move the legacy PUT/DELETE onto rows and resolve refused blobs.
- **Readers.** `GetDictionary` keeps the legacy response: active rows plus blob
  terms (rows and tombstones win by `termKey`; pristine legacy copies are
  ignored, so the blob stays authoritative) before `migratedAt`, rows only
  after. Status sync is a conditional update of the `VOCAB#` row, or of the
  blob's status for the observed name and status; the unconditional whole-item
  `PutItem` is gone and the legacy PUT updates only its own fields.
  `GetVocabularyForTranscription` reads `VOCAB#` first, falls back to the blob
  and never writes; the transcribe Lambda uses both readers.

## Amendment 2026-10-18: term CRUD and shared validator

Migration now has callers; this replaces the previous amendment's "no caller
yet" note. Routes: [API-SPEC](../API-SPEC.md#personal-dictionary).

- **Validator.** `ValidateTermInput` applies to manual terms now, and to
  automatic terms when extraction lands; its blocklist is a seed.
- **User actions.** Delete tombstones any origin. Edit sets `userEditedAt` under
  the caller's `version` and keeps `origin`; keep is `aliasMode=always`;
  accepting a suggestion sets `state=active`. Renaming to another key is a new
  term. Creating a tombstoned term restores it as manual (an explicit user act).
- **Migration callers.** v2 writes migrate first. The shared transcribe reader
  never writes. An unmigratable item blocks v2 writes
  (`LEGACY_DICTIONARY_CONFLICT`). Until the legacy PUT/DELETE move onto rows
  (the follow-up change, deployed together with this one), a legacy write after
  a v2 write updates only the legacy item, which v2 readers ignore once migrated.
- **Caps and keys.** 500 active manual terms per scope, checked before the write
  (concurrent creates may overshoot slightly). Tombstones beyond 2,000 are pruned
  oldest first, bounding the guessable-hash retention noted above. `termKey`
  stays unsalted: a salt in the same table does not stop a table reader, who can
  already read live terms and meetings.
- **Vocabulary handoff.** Recognition-affecting manual changes keep the pre-v2
  in-place rebuild of the derived name, recorded in `VOCAB#personal#ko-KR` under
  its version guard; a row with an inactive slot is left to the A/B builder.
  When that row is missing (no legacy item ever existed), the rebuild creates
  it in a transaction whose ConditionCheck requires the legacy item to be
  absent or already migrated, so only migration creates it for an unmigrated
  item.
  Pending updates are skipped by batch jobs (`GetVocabularyForTranscription`
  requires READY); the response still has no `vocabularyName`, so the live
  client passes none.

## Consequences and risks

Terms persist across meetings and customize both recognition paths. Transcribe
vocabulary builds can be pending or fail, and prompt hints remain probabilistic.
The engines consume different fields; storing a pronunciation does not mean either
path uses it. Resource lifecycle and prompt-length limits remain maintenance
concerns. This feature does not remove the need to review transcripts.

## Evidence

- [dictionary.go](../../backend/internal/model/dictionary.go): stored fields and
  v2 row types.
- [repository dictionary.go](../../backend/internal/repository/dictionary.go) and
  [dictionary_v2.go](../../backend/internal/service/dictionary_v2.go): v2 guards,
  migration, union reader and merge; tests beside each.
- [dictionary_terms.go](../../backend/internal/service/dictionary_terms.go) and
  [dictionary_validate.go](../../backend/internal/service/dictionary_validate.go):
  term CRUD and the shared validator; tests beside each.
- [dictionary.go](../../backend/internal/service/dictionary.go): resource naming,
  status, and phrase-only vocabulary construction.
- [transcribe.go](../../backend/internal/service/transcribe.go),
  [main.go](../../backend/cmd/transcribe/main.go),
  [transcribe.py](../../backend/whisper/transcribe.py): vocabulary consumers.
- [CustomDictionary.tsx](../../frontend/src/components/CustomDictionary.tsx): UI.
- [main.go](../../backend/cmd/api/main.go): registered settings routes.
