# ADR-008: Per-User Dictionaries for Speech Recognition

- Status: Accepted; phonetic TSV uploads remain an original plan, not current
  behavior. Automatic term extraction ships off by default (`AUTO_TERMS_MODE=off`).
- Decision date: Not recorded; original rationale references the 2026-04-23 STT evaluation.
- Implementation checked: 2026-09-13; v2 storage, term CRUD, filter, terms
  run-state/merge, extraction job and A/B vocabulary builder amendments checked
  2026-10-18; layered dictionary amendment 2026-10-26.

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
  table file, ko-KR builds). Originally, and still for an unmigratable legacy
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
- **Vocabulary handoff** (historical: superseded by the A/B builder amendment
  below, which removed this in-place rebuild). Recognition-affecting manual changes keep the pre-v2
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

## Amendment 2026-10-18: A/B vocabulary builder

`EnsureVocabularies` ([vocabulary_build.go](../../backend/internal/service/vocabulary_build.go))
replaces the in-place rebuild. Only the API Lambda enables it; the shared
readers never build. Callers and response: [API-SPEC](../API-SPEC.md#personal-dictionary).

- **Never in place.** `VOCAB#personal#{lang}` has `ActiveName`/`InactiveName`.
  Builds target the inactive slot; a later call flips both by version and
  fingerprint CAS only on READY. FAILED keeps the active slot.
- **Languages.** Only ko-KR is built. en-US builds are deferred until a consumer
  exists (batch uses ko-KR only; no response names a vocabulary for live
  streams); the en-US table rules below stay, unused, and `vocabularies.en`
  reports `NONE`.
- **Table.** Active phrase terms, manual first, then hits, then oldest, deduped
  by phrase; `vocab/{InactiveName}.{fingerprint}.tsv` (a stale submitter
  writes its own key, never a different table; unlike ADR-037 keys it is not
  attempt-unique, so retries of one table share it) with header
  `Phrase\tSoundsLike\tIPA\tDisplayAs`, four fields per row, at most 1,500 rows
  and 48 KB; invalid rows are dropped individually. The phrase validator is
  minimal (spaces become `-`, AWS table rules), separate from `terms_filter.go`.
  ko-KR drops dotted acronyms until acceptance item 2. en-US
  needs five Latin-only phrases. The file is deleted after READY, FAILED or quota.
- **Plan.** Every state-machine step reads the VOCAB# row and only then lists
  the terms, so a step never acts on a listing older than the row it read; a
  term deleted by a concurrent write is never built back in.
- **Claims** need a changed fingerprint, no claimed build, and
  `VOCAB_REBUILD_MIN_INTERVAL` (default 10m) since the last flip or build start;
  each manual write grants one bypass (kept as `bypassPending` during a build).
  A FAILED table is retried only after a change or a manual write. A claimed
  build is resumed or polled whatever its age; it is never reclaimed or deleted
  without its provider state.
- **Submit.** After writing the table file, a submit claim CAS (version,
  `buildingFingerprint`) sets `buildSubmitted` and `submittedAt`; only the
  winner calls Transcribe. A poll accepts READY or FAILED only when the
  provider's `LastModifiedTime` is at or after `submittedAt` (5 s tolerance);
  an older result (a reused slot whose submission never ran) or a missing name
  resubmits.
- **Errors.** `GetVocabulary` first; NotFound or the unknown-name BadRequest
  creates (a wrong guess fails safely with ConflictException), else update with
  `VocabularyFileUri`. Conflict releases the submit claim for a later retry;
  LimitExceeded is `QUOTA_EXCEEDED`; other BadRequest is FAILED; an ambiguous
  error keeps the claim for the poll to verify. Classified by error code.
- **Slots.** New names are `ttobak-vocab-{16 random hex}`. The demoted slot stays
  inactive for 24 h after a flip (rebuildable in place, never advertised).
  Retirement and deletion run only inside `EnsureVocabularies`, on the user's
  next dictionary GET or write after the grace window (the GET each recording
  start makes counts); there is no scheduled reclamation. Resting count per
  user is therefore one ko-KR name after a first build, two after any later
  flip until such a request, and up to three for a migrated legacy user (the
  retired derived name waits for 24 h after the latest flip). Base used one.
  A failed poll is a `warnings` entry.
- **Flip opportunities.** Legacy `status` is `PENDING` while a build is in
  flight, even over a READY active slot, so the shipped settings UI keeps
  polling the GET, which flips. Each recording start also calls the GET.
- **Batch** reads only: a submitted build verified READY (as in Submit) is used
  before any flip, else `ActiveName` while `activeReady`; a pre-A/B in-place
  build (no fingerprint) is still confirmed with Transcribe, as before, and no
  A/B build is claimed until its result is recorded. A migration retry leaves
  a row with A/B fields to the builder. Live streams receive no vocabulary
  name (below).
- **No vocabulary name in responses:** the live client would send it on a
  ko-KR/en-US multi-language stream, which may fail; it waits for item 5.
- **Empty language** stops advertising at once (`activeReady` false,
  `emptySince` set), also while a build is claimed or submitted: the claim is
  dropped and its provider result is never flipped or served. Both slots are
  deleted after 24 h as in Slots.
- **Not covered:** en-US builds, batch en-US `LanguageIdSettings`, scheduled
  reclamation of inactive or retired names,
  account contexts, live-config, client stream retry, and GatewayStack wiring
  of `VOCAB_REBUILD_MIN_INTERVAL`. An unmigratable legacy item keeps the pre-v2
  `Phrases` rebuild.

## Amendment 2026-10-18: settings UI v2 and live vocabulary client

- **UI** uses only the per-term routes; the legacy PUT stays for older clients.
  Duplicates use the `termKey` normalization (the old lowercase-phrase check let
  `Dynamo-DB` and `DynamoDB` coexist), checked against shared Go vectors (the JS
  half runs in `npm run test:unit`, in CI only for `frontend/**` changes). An unmigratable legacy
  item's terms cannot be edited; the UI marks the conflicting ones and deletes
  them through the legacy DELETE-by-phrase, which retries migration.
- **No live name.** The A/B amendment's wait for item 5 stands: there is no
  live-config route and no response names a vocabulary, so the recording page
  only calls the dictionary GET (a flip opportunity) and live streams send none.
- **Client plumbing.** `liveVocabulary.ts` sends one name per `LanguageOptions`
  language when given names; with one, a vocabulary BadRequest before the first
  event retries once without it and any vocabulary rejection drops it for the
  recording. Without names it adds no request fields and never retries.
- Not covered: live-config, source meeting titles, account dictionaries, live
  aliases, backfill.

Rollout acceptance (record each with a date):

1. Vocabulary quota usage (default 100 per account; up to three ko-KR names per
   user at rest, as in Slots).
2. A live Create from the four-column file reaches READY for ko-KR;
   whether ko-KR accepts dotted acronyms and the `DisplayAs` characters used.
3. Whether deleting or updating a vocabulary affects an active stream or job.
4. Whether `StartStreamTranscriptionWebSocket` has a vocabulary resource type
   (unverified). Until then the identity-pool `*` grant lets a user who learns a
   vocabulary name bias their own stream; random names reduce that risk.
5. Whether a multi-language stream with ko-KR-only or ko-KR plus en-US
   `VocabularyNames` survives en-US speech.
6. That `GetVocabulary` `LastModifiedTime` advances on each accepted Create
   and Update, which the submit verification relies on.

## Amendment 2026-10-26: layered dictionary

This partly supersedes the rejection of a shared dictionary above: shared layers
are added next to the personal one, which stays the only layer a user owns.

- **Layers and partitions.** Common `DICT#COMMON`, Account `ACCOUNT#{accountId}`
  and Personal `USER#{userId}`. Term rows stay `DICTTERM#{termKey}` with the
  unchanged `termKey` rules. Caps per scope: 300 automatic terms
  (`MaxAutoTermsPerScope`), 500 manual terms (`MaxManualTermsPerScope`), 10
  observations per term; Common keeps the last 20 audit rows per term.
- **Consumers.** One personal ko-KR vocabulary per user, at most 1500 rows and
  48 KiB, cut at whole terms in the order Personal manual, Account, Personal
  auto, Common. The Whisper prompt is at most 300 runes in the order Personal
  manual, the meeting's Account, Common, Personal auto. A personal row that is
  inactive or switched off does not shadow the same key from Account or Common.
  A failing Account-order lookup skips the vocabulary step with a warning rather
  than rebuilding without Account terms.
- **L1 (Account).** Terms from a member's meeting run are written to the
  meeting's Account: `active` from an untainted meeting, `suggested` from a
  tainted one. Every Account write carries a membership ConditionCheck on
  `ACCOUNT#{id}` / `MEMBER#{userId}` in the same transaction; the early member
  check only saves writes. Legacy migration writes reject `ACCOUNT#` keys. A
  failed Account merge is best effort and not retried (the personal run is the
  unit of retry); it logs `layer=account outcome=<code>` and is counted.
- **L2 (Common).** Any signed-in user may edit or delete Common terms; every
  write records `updatedBy`/`updatedAt` and an audit row, and system writes
  (news creates and evictions, actor `system:news`) are audited too. Deletes
  are tombstones that can be restored from the audit snapshot.
- **L3 (corrections).** Glossary corrections to the transcript are applied
  silently; the records are stored on the meeting and never shown in the UI. A
  refined segment in which a glossary form appears more often than in its source
  lines without a kept record explaining it is re-run on the plain prompt, then
  falls back to the raw text, so a correction pass cannot invent terms. Automatic
  `heardAs` appends do not set `userEditedAt`; Common counters are not written.
- **L4 (sources).** A KB upload asks for an Account (none means Personal); the
  choice is stored as S3 object metadata `account-id`. KB documents (plain text
  only: `.md`, `.markdown`, `.txt`) and research reports feed the owner's
  Account or Personal layer. Daily news feeds Common only, and only from crawler
  sources an operator marked global and that carry no sticky `userTiedAt`; the
  repeat threshold counts distinct sources, and Common sources are public news
  references, never meeting or KB references.
- **Flags.** `ttobak:accountTermsMode`, `ttobak:transcriptCorrection`,
  `ttobak:kbTermsMode`, `ttobak:researchTermsMode` and `ttobak:newsTermsMode`
  (`off|on`) all default off; a run skipped by a flag logs `outcome=DISABLED`.
  Rollout is one flag per deployment, each after a deployment with it off.
- **Observability.** Metric filters on the summarize log group and the
  `TermsSourcesSilent` alarm are described in
  [INFRA-SPEC](../INFRA-SPEC.md#dictionary-layer-metrics).
- **Follow-ups (not in this change).** FU1: route KB PDF and Office files
  through the document-extraction Lambda and use the extracted text, since only
  plain text yields terms today. FU2: sweep `DICTOBS#...#{kind}:{digest}` rows
  on KB file delete and research trash/delete, as the meeting sweep does. FU3:
  one-off backfill of `userTiedAt` on every existing non-reserved crawler CONFIG
  before an operator marks any source global; `fix-crawler-newsqueries.py` has no
  marker guard.

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
- [CustomDictionary.tsx](../../frontend/src/components/CustomDictionary.tsx) and
  [liveVocabulary.ts](../../frontend/src/lib/liveVocabulary.ts): UI and live
  vocabulary plumbing; tests in `frontend/scripts/`.
- [main.go](../../backend/cmd/api/main.go): registered settings routes.
- [pipeline-failures.ts](../../infra/lib/pipeline-failures.ts) and
  [pipeline-failures.test.ts](../../infra/test/pipeline-failures.test.ts): layer
  metric filters and the `TermsSourcesSilent` alarm.
