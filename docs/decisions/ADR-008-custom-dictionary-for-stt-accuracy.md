# ADR-008: Per-User Dictionaries for Speech Recognition

- Status: Accepted; phonetic TSV uploads remain an original plan, not current
  behavior. Automatic term extraction ships off by default (`AUTO_TERMS_MODE=off`).
- Decision date: Not recorded; original rationale references the 2026-04-23 STT evaluation.
- Implementation checked: 2026-09-13; v2 storage, term CRUD, filter, terms
  run-state/merge and extraction job amendments checked 2026-10-18.

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

## Amendment 2026-10-18: term CRUD, shared validator and legacy semantics

Migration now has callers; this replaces the previous amendment's "no caller
yet" note. Routes: [API-SPEC](../API-SPEC.md#personal-dictionary).

- **Validator.** `ValidateTermInput` applies to manual terms and the legacy PUT
  now, and to automatic terms when extraction lands; its blocklist is a seed
  (grown by the automatic-term filters amendment below).
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
  round-trip always saves. A legacy-empty `displayAs` is shown only while the
  phrase still derives the term's key, so a v2 phrase edit does not make the
  round trip replace the row. Legacy DELETE removes manual rows.
- **Legacy PUT limits.** Reconciliation is per row, not one transaction; row
  writes are version-conditioned and a retry converges. The whole list is last
  writer wins: manual rows created after the client's GET, including by an
  overlapping PUT, are deleted without a conflict. Fencing the legacy item
  write on its observed `blobRevision` would serialize overlapping PUTs; it is
  not implemented.
- **Migration callers.** v2 writes and the legacy PUT migrate first; the API GET
  migrates best effort. The shared transcribe reader never writes. An
  unmigratable item blocks v2 writes (`LEGACY_DICTIONARY_CONFLICT`) and keeps the
  pre-v2 legacy DELETE until a deduplicated legacy PUT resolves it.
- **Caps and keys.** 500 active manual terms per scope, checked before the write
  (concurrent creates may overshoot slightly). Tombstones beyond 2,000 are pruned
  oldest first, bounding the guessable-hash retention noted above. `termKey`
  stays unsalted: a salt in the same table does not stop a table reader, who can
  already read live terms and meetings.
- **Vocabulary handoff.** Recognition-affecting manual changes keep the pre-v2
  in-place rebuild of the derived name, recorded in `VOCAB#personal#ko-KR`.
  Each attempt reads that row before listing terms and writes only if its
  version is unchanged, so any rebuild recorded since that read rejects the
  stale write; a conflict recomputes from a fresh row and term listing (three
  attempts, then a warning without a write). The Transcribe call precedes the
  guarded write, so an exhausted or failed attempt can leave the provider ahead
  of the record until the next change. While Transcribe reports a pending
  build, the update is skipped with a warning and the next change rebuilds.
  A row with an inactive slot is left to the A/B builder.
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

## Amendment 2026-10-18: automatic-term filters

`FilterCandidates` in `service/terms_filter.go` is pure; the extraction job
below calls it. A candidate failing a rule is dropped alone,
and the per-rule drop counts (`TermFilterCounts`) hold numbers only, never text.
Rules, in order:

- **Confidence** at least the configured minimum (default 0.85; values outside
  (0, 1] use the default).
- **Blocklist.** The embedded `terms_blocklist.txt` (common Korean and English
  meeting and function words plus fillers, about 550 entries) is matched on an
  automatic candidate's term key and on every heardAs. For manual terms,
  `ValidateTermInput` rejects a blocklisted heardAs, but the manual display form
  is not blocklisted, because typing it is an explicit user act.
- **Shape.** `ValidateTermInput`, then: no digits-only or punctuation-only
  terms, dates or times (month names count only as whole names or
  abbreviations, so `JUnit 5` is kept and `Oct 18` is not), emails, URLs with a
  scheme or `www.`, all-lowercase bare domains (`ttobak.com`; `ASP.NET` and
  `Socket.IO` are kept), runs of 7 or more digits (phone- or card-like), or more
  than 6 words. Hangul-only terms need 2 syllables; Latin-only terms need 2
  letters, and 3 unless all capitals.
- **Grounding** after NFKC, lowercase and removal of the term-key separators,
  with word boundaries for term matches: a match may not extend a Latin/digit
  run (so `EKS` does not match `weeks`), and a Hangul match may not continue a
  preceding Hangul syllable but may carry particles. At least one quote; every
  quote is a substring of its named source (transcript, notes or an identified
  document) and contains the term or a heardAs at boundaries judged in that
  source, so a quote cut from inside a longer word does not count; every heardAs
  occurs in the transcript; the display form occurs in notes or a document, or
  twice in the transcript; notes-only terms must also occur in the transcript.
  Source types come from where the term occurs, not from the model's labels.
- **Transcribe phrase.** A model phrase is used only when it has the same term
  key and passes the AWS phrase validator; otherwise `deriveTranscribePhrase`
  joins words and camelCase or script boundaries with `-` and dots every 2-6
  capital segment, whether a camelCase run or a standalone word (`DynamoDB` ->
  `Dynamo-D.B.`, `EKS` -> `E.K.S.`, `Amazon EKS` -> `Amazon-E.K.S.`; an
  all-caps word such as `NASA` is also spelled out). If the dotted form fails
  validation, the undotted join is tried. The validator enforces ko-KR `[A-Za-z\-.]` plus Hangul syllables
  (U+AC00-U+D7A3), en-US `[A-Za-z'\-.]`, no digits, at most 256 characters, no leading `.`/`'`/`-`, no trailing `'`/`-`, no
  `--`/`..`, and periods only after single letters. Hangul-containing terms are
  ko only; Latin-only terms are ko and en; other scripts get no phrase. Dotted
  acronyms keep `TranscribeKo=false` until a live ko-KR build records that
  Transcribe accepts them. A term without a phrase is still kept.
- **Duplicates** by term key keep the best-ranked candidate.
- **Cap.** At most 15 terms per run, ranked by confidence, then source-type count.

## Amendment 2026-10-18: terms run state and merge rules

Storage and merge code for automatic terms; the extraction job amendment below
is its caller.

- **Run state** `MEETING#{mid}/ANALYSIS#terms` (`model.TermsAnalysis`) follows the
  action-items CAS: `runId`, `status` queued|running|succeeded|failed,
  `sourceHash`, `summaryGeneration`, `leaseUntil` (ms), an enum `errorCode`
  (shared analysis codes plus `DISABLED` and `TAINTED_SUGGEST_ONLY`), counters
  and `acceptedTerms`. Queue requires the observed prior run/status/lease (an
  expired lease when pending) and a `sourceHash` different from
  `succeededSourceHash`, which only Complete sets. Queue, Start and RenewLease
  carry a ConditionCheck that the owner's meeting exists and is done; Complete
  matches run and lease and checks that the meeting exists; Fail is one
  conditional update of the observed run, run by the caller with a detached
  3-second context. The row stores hashes, enums and counts, never term text.
- **acceptedTerms** holds at most 40 `{termKey, sourceTypes}` entries (32-hex
  keys, `transcript|notes|document`). It lists every term this meeting has an
  observation for and only grows, because the planned deletion cleanup
  (design A8) can find a meeting's `DICTOBS#` rows only through it. The merger
  writes a new key only while the list has room, checked per candidate so
  skipped candidates take no slot, and returns the list even on failure so
  Fail can record it. Deletion cleanup is in the extraction job amendment.
- **Merge** (`TermsMerger.MergeExtractedTerms`, owner's personal scope only):
  no row creates an auto term with hits 1 (`suggested` when the meeting is
  tainted); tombstones, manual rows, rows with `userEditedAt` and unknown
  states are skipped; an untainted run promotes a suggestion to `active` with
  a version-CAS transaction that also records the observation
  (`PromoteSuggestedTerm`) and merges into an active auto term
  (`MergeAutoObservation`, which now also unions `sourceTypes`). Promotion
  replaces the suggestion's heardAs, sourceTypes, displayAs, phrase, type,
  confidence and recognition flags with the untainted run's values and sets
  `hits` to 1, so nothing a non-owner planted becomes active or counts toward
  `hits >= 2`. Tainted observations carry `tainted: true`. A tainted run never
  reinforces an existing term. An alias whose key equals its own
  term or another active or candidate term is dropped; heardAs stays at most
  five. A version conflict or a `TransactionConflict` cancellation
  (`ErrDictTransactionConflict`) re-reads and retries, three attempts in
  total; an existing observation is a no-op, and a meeting whose
  acceptedTerms already lists the key (its observation was pruned) records the
  observation again without a hit, so re-running a meeting keeps `hits`. A
  meeting whose earlier write failed ambiguously is also listed and so may be
  undercounted, never double-counted. A
  leftover observation without a term (failed eviction cleanup) is deleted
  before the create is retried.
- **Caps.** Unedited automatic terms (active or suggested) are capped at 300 per
  scope; `EvictAutoTerm` (now also accepting `suggested`) removes the lowest
  `hits*2 + (lastSeen within 30 days ? 1 : 0)` first, oldest `lastSeenAt` on
  ties. When its version and `userEditedAt` guards fail, the row is re-read: a
  row now gone, edited or tombstoned reduces the excess (a concurrent merge
  may already have evicted it), and only a still-evictable newer row moves on
  to the next candidate. Each term keeps its ten newest observations; `hits` is never
  decremented. Cleanup failures are surfaced. Account scope is not implemented; the
  manual 500-term cap and tombstone pruning belong to the term CRUD amendment.

## Amendment 2026-10-18: extraction job (off by default)

- **Provenance.** A non-owner write that changes content, transcriptA/B, the
  selected transcript, saved notes (including the `expectedNotes`
  compare-and-set path, marked before its write) or liveSummary sets sticky
  `sourceEditedByNonOwnerAt`; notes and liveSummary feed the AI notes the job
  reads (`TestSourceWriterAllowlist` classifies every writer). Attachments need
  no marker: upload completion is owner-only. Tainted runs only suggest
  (`TAINTED_SUGGEST_ONLY`); only owner-uploaded documents are direct evidence.
- **Job.** `TermsAnalysisService.Enqueue` is `DISABLED` unless
  `AUTO_TERMS_MODE=personal` and `AUTO_TERMS_USERS` is empty or lists the owner,
  skips an unchanged `sourceHash` and publishes `TermsRequested` (triggers: end
  of summarize analyses, preserved human content, re-summary; non-fatal). The
  summarize dispatch runs it within min(300 s, deadline-30 s): at most three
  90 s calls (Sonnet default, 3000 tokens, thinking off, strict JSON), then the
  filters, `MigrateLegacyDictionary` for the owner and the merger. As for manual
  v2 writes, migration runs first and a failure fails the run before any key is
  reserved or row written, so an automatic row never claims an observed
  unmigrated blob term's key; a blob first created by a legacy PUT during the
  run is the same window manual writes have. Keys are reserved in
  `acceptedTerms` before observation writes, which check that the meeting exists. An Enqueue during a live run is
  `PENDING` and records nothing, so a run failing `SOURCE_CHANGED`, and every
  successful run, calls Enqueue again for the current source.
- **Deletion.** `SweepMeetingTerms` follows `DeleteMeeting`: it deletes the
  meeting's `DICTOBS#` rows, unedited auto terms left without observations
  (the term version is read before the observation count, so a concurrent
  observation fails the version-guarded delete) and then the run state, which
  records `ownerId`; only that owner is swept. A failed sweep keeps the state
  and surfaces the error; the owner's retried DELETE of the gone meeting re-runs
  it before returning 404, and a failed owner lookup is returned for retry.
  There is no scheduled reconciliation, so an un-retried failure leaves the rows.
  STT-failed deletions skip the sweep (they never reach `done` or queue a run).
- **Infrastructure.** GatewayStack routes `TermsRequested` to summarize with a
  DLQ and alarm, and renders `AUTO_TERMS_*`/`BEDROCK_TERMS_MODEL_ID` from
  validated CDK context, `AUTO_TERMS_MODE=off` by default
  ([INFRA-SPEC](../INFRA-SPEC.md#automatic-term-extraction-delivery)). Enabling
  `personal` is a separate, explicit context change made after a deployment with
  `off`; synth is not deployment.

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
- [terms_analysis.go](../../backend/internal/service/terms_analysis.go) and
  [terms_extract.go](../../backend/internal/service/terms_extract.go): extraction job;
  [meeting_source_taint_test.go](../../backend/internal/service/meeting_source_taint_test.go):
  `TestSourceWriterAllowlist`.
- [terms_analysis.go](../../backend/internal/model/terms_analysis.go),
  [repository terms_analysis.go](../../backend/internal/repository/terms_analysis.go)
  and [terms_merge.go](../../backend/internal/service/terms_merge.go): run state,
  CAS transitions and merge rules; tests beside each.
- [dictionary.go](../../backend/internal/service/dictionary.go): resource naming,
  status, and phrase-only vocabulary construction.
- [transcribe.go](../../backend/internal/service/transcribe.go),
  [main.go](../../backend/cmd/transcribe/main.go),
  [transcribe.py](../../backend/whisper/transcribe.py): vocabulary consumers.
- [CustomDictionary.tsx](../../frontend/src/components/CustomDictionary.tsx): UI.
- [main.go](../../backend/cmd/api/main.go): registered settings routes.
