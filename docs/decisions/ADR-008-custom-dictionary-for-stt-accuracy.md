# ADR-008: Per-User Dictionaries for Speech Recognition

- Status: Accepted; phonetic TSV uploads and automatic suggestion ingestion remain
  original plans, not current behavior.
- Decision date: Not recorded; original rationale references the 2026-04-23 STT evaluation.
- Implementation checked: 2026-09-13; v2 storage, term CRUD and A/B vocabulary
  builder amendments checked 2026-10-18.

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
- Superseded for migrated users by the A/B builder amendment below (four-column
  table file, ko-KR and en-US). Originally, and still for an unmigratable legacy
  item: `DictionaryService` creates/updates a Korean Transcribe vocabulary using the
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

## Amendment 2026-10-18: term CRUD, shared validator and legacy semantics

Migration now has callers; this replaces the previous amendment's "no caller
yet" note. Routes: [API-SPEC](../API-SPEC.md#personal-dictionary).

- **Validator.** `ValidateTermInput` applies to manual terms and the legacy PUT
  now, and to automatic terms when extraction lands; its blocklist is a seed.
- **User actions.** Delete tombstones any origin. Edit sets `userEditedAt` under
  the caller's `version` and keeps `origin`; keep is `aliasMode=always`;
  accepting a suggestion sets `state=active`. Renaming to another key is a new
  term. Creating a tombstoned term restores it as manual (an explicit user act).
- **Legacy routes.** The whole-list PUT writes the legacy item, migrates it and
  reconciles manual rows only, so a legacy or stale client never tombstones or
  erases automatic terms. Unchanged terms skip revalidation so stored terms that
  predate the validator do not block saving. GET lists active automatic rows
  (possibly phrase-less) in legacy `terms`, so the PUT passes entries keyed to
  automatic or suggested rows through without validation, counts only manual
  entries toward the cap, and warns when it ignores a change to one; a GET
  round-trip always saves. Legacy DELETE removes manual rows.
- **Migration callers.** v2 writes and the legacy PUT migrate first; the API GET
  migrates best effort. The shared transcribe reader never writes. An
  unmigratable item blocks v2 writes (`LEGACY_DICTIONARY_CONFLICT`) and keeps the
  pre-v2 legacy DELETE until a deduplicated legacy PUT resolves it.
- **Caps and keys.** 500 active manual terms per scope, checked before the write
  (concurrent creates may overshoot slightly). Tombstones beyond 2,000 are pruned
  oldest first, bounding the guessable-hash retention noted above. `termKey`
  stays unsalted: a salt in the same table does not stop a table reader, who can
  already read live terms and meetings.
- **Vocabulary handoff** (superseded by the A/B builder amendment below).
  Recognition-affecting manual changes kept the pre-v2
  in-place rebuild of the derived name, recorded in `VOCAB#personal#ko-KR` under
  its version guard; a row with an inactive slot is left to the A/B builder.
  When that row is missing (no legacy item ever existed), the rebuild creates
  it in a transaction whose ConditionCheck requires the legacy item to be
  absent or already migrated, so only migration creates it for an unmigrated
  item.
  Pending updates are skipped by batch jobs (`GetVocabularyForTranscription`
  requires READY); the response still has no `vocabularyName`, so the live
  client passes none.
- **Legacy item copies.** The PUT writes only manual terms to the legacy item, so
  automatic (meeting-derived) terms never gain a second copy there. A manual term
  later tombstoned through the v2 DELETE keeps its text in the legacy item until
  the next legacy PUT or the legacy item's removal; v2 readers ignore it after
  migration. Tombstone content clearing does not cover that copy.
- **Rollback.** Once this version migrates a user, an older API's legacy PUT
  writes only the legacy item, which v2 readers ignore; roll forward instead. An
  older API would also read the legacy item's retained manual copies, including
  terms since deleted through v2.

## Amendment 2026-10-18: A/B vocabulary builder

`EnsureVocabularies` ([vocabulary_build.go](../../backend/internal/service/vocabulary_build.go))
replaces the in-place rebuild. Only the API Lambda enables it; the shared
readers never build. Callers and response: [API-SPEC](../API-SPEC.md#personal-dictionary).

- **Never in place.** `VOCAB#personal#{lang}` has `ActiveName`/`InactiveName`.
  Builds target the inactive slot; a later call flips both by version CAS only
  on READY. Consumers use `ActiveName` only while `activeReady`. FAILED keeps it.
- **Table.** Active phrase terms, manual first, then hits, then oldest, deduped
  by phrase; `vocab/{InactiveName}.tsv` with header
  `Phrase\tSoundsLike\tIPA\tDisplayAs`, four fields per row, at most 1,500 rows
  and 48 KB; invalid rows are dropped individually. The phrase validator is
  minimal (spaces become `-`, AWS table rules); `deriveTranscribePhrase` is not
  in this branch. ko-KR drops dotted acronyms until acceptance item 2. en-US
  needs five Latin-only phrases. The file is deleted after READY, FAILED or quota.
- **Claims** need a changed fingerprint, no build younger than 30 minutes, and
  `VOCAB_REBUILD_MIN_INTERVAL` (default 10m) since the last flip or build start;
  each manual write grants one bypass (kept as `bypassPending` during a build).
  A FAILED table is retried only after a change or a manual write.
- **Errors.** `GetVocabulary` first; NotFound or the unknown-name BadRequest
  creates (a wrong guess fails safely with ConflictException), else update with
  `VocabularyFileUri`. Conflict keeps the claim; LimitExceeded is
  `QUOTA_EXCEEDED`; other BadRequest is FAILED. Classified by error code.
- **Slots.** New names are `ttobak-vocab-{16 random hex}`. The demoted slot stays
  inactive for 24 h after a flip (rebuildable in place, never advertised), then
  is retired and deleted with retries: one vocabulary per language at rest, two
  at peak. The derived legacy name is demoted at its first flip, never rebuilt.
- **Empty language** stops advertising at once and is deleted after 24 h (the
  design deferred both), so removed terms stop biasing recognition.
- **Not covered:** batch en-US `LanguageIdSettings` (batch uses ko-KR only),
  account contexts, live-config, client stream retry, and GatewayStack wiring
  of `VOCAB_REBUILD_MIN_INTERVAL`. An unmigratable legacy item keeps the pre-v2
  `Phrases` rebuild.

Rollout acceptance (record each with a date):

1. Vocabulary quota usage (default 100 per account; two per language per user at peak).
2. A live Create from the four-column file reaches READY for ko-KR and en-US;
   whether ko-KR accepts dotted acronyms and the `DisplayAs` characters used.
3. Whether deleting or updating a vocabulary affects an active stream or job.
4. Whether `StartStreamTranscriptionWebSocket` has a vocabulary resource type
   (unverified). Until then the identity-pool `*` grant lets a user who learns a
   vocabulary name bias their own stream; random names reduce that risk.

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
  term CRUD, legacy semantics and the shared validator; tests beside each.
- [dictionary.go](../../backend/internal/service/dictionary.go): legacy naming,
  status, and the unmigratable-item phrase-only build;
  [vocabulary_build.go](../../backend/internal/service/vocabulary_build.go): A/B
  builder, tests beside it.
- [ai-stack.ts](../../infra/lib/ai-stack.ts),
  [storage-stack.ts](../../infra/lib/storage-stack.ts): vocabulary IAM scope and
  vocab/ lifecycle; assertions in `infra/test/`.
- [transcribe.go](../../backend/internal/service/transcribe.go),
  [main.go](../../backend/cmd/transcribe/main.go),
  [transcribe.py](../../backend/whisper/transcribe.py): vocabulary consumers.
- [CustomDictionary.tsx](../../frontend/src/components/CustomDictionary.tsx): UI.
- [main.go](../../backend/cmd/api/main.go): registered settings routes.
