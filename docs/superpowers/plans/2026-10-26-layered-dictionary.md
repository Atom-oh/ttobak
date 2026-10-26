# Layered Dictionary Implementation Plan

> Status: historical implementation record. The current behavior is ADR-008 (amendment 2026-10-26).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Common and Account dictionary layers fed by meetings, KB, deep research and news, use all layers for recognition, and silently correct mis-heard glossary terms in transcripts.

**Architecture:** Terms stay `DICTTERM#` rows; the partition picks the layer (`DICT#COMMON`, `ACCOUNT#{id}`, `USER#{id}`). One extraction/merge pipeline (existing `TermsMerger`) gains a scope and a generic source reference; new sources publish a `TermsSourceRequested` event handled by the summarize Lambda. Each user keeps one Transcribe vocabulary built from a layered plan; the transcript refine step gets a glossary.

**Tech Stack:** Go (backend Lambdas), Python (research agent, crawler), AWS CDK TypeScript (infra), Next.js (frontend), DynamoDB single table `ttobak-main`, EventBridge, Amazon Transcribe, Bedrock.

**Spec:** `docs/superpowers/specs/2026-10-26-layered-dictionary-design.md` (decisions L1–L4 in §6). ADR-008 is the parent decision record.

**Base:** start from `main` after PR #478 is merged (it adds `joinWhisperPrompt`, `TermsAnalysisService.SetVocabularyRefresher`, the summarize vocabulary builder). Rebase this branch onto it before Task 1.

## Global Constraints

- Partitions: Common `DICT#COMMON`; Account `ACCOUNT#{accountId}`; Personal `USER#{userId}`. Term SK stays `DICTTERM#{termKey}`; `termKey` rules unchanged (ADR-008).
- Caps: 300 auto terms per scope (`MaxAutoTermsPerScope`), 500 manual per scope (`MaxManualTermsPerScope`), 10 observations per term; Common audit keeps the last 20 changes per term.
- Vocabulary: one personal ko-KR vocabulary per user; table ≤ `VocabMaxRows` (1500) and `VocabMaxBytes` (48 KiB), cut at whole terms in layer order: Personal manual → Account → Personal auto → Common.
- Whisper prompt ≤ `whisperPromptMaxRunes` (300), order: Personal manual → meeting's Account → Common → Personal auto.
- Account writes require a membership ConditionCheck on `ACCOUNT#{id}` / `MEMBER#{userId}` in the same transaction.
- L1: Account terms from a member's untainted meeting are `active`; tainted → `suggested`.
- L2: any signed-in user may edit/delete Common terms; every Common write records `updatedBy`/`updatedAt` and an audit row.
- L3: transcript corrections are applied silently; records stored on the meeting, never shown in the UI.
- L4: KB upload asks for an Account (or none = Personal); the choice is stored as S3 object metadata `account-id`.
- Every new flag defaults off, and every skipped run because of a flag logs `outcome=DISABLED`. Flags: `ttobak:accountTermsMode`, `ttobak:transcriptCorrection`, `ttobak:kbTermsMode`, `ttobak:researchTermsMode`, `ttobak:newsTermsMode` (values `off|on`).
- No customer names in code, tests or docs. Tests use fictional terms (e.g. `AlphaGrid`, `K-culture`).
- Deploy per stack with `--exclusively` in dependency order; never `--all`.

## Review Focus

1. A user removed from an Account must stop contributing to and receiving that Account's terms — Task 2 test `TestAccountTermWriteRequiresMembership`, Task 6 test `TestLayeredPlanSkipsAccountsUserLeft`.
2. A Common vandal edit (rename to an offensive term, mass delete) must be traceable and restorable — Task 3 test `TestCommonEditKeepsAuditAndRestores`.
3. The correction step must not invent terms: a transcript with no sound-alike span stays byte-identical — Task 8 test `TestRefineGlossaryNoSpanNoChange`.
4. An Account glossary must never be used on another Account's meeting — Task 8 test `TestGlossaryScopedToMeetingAccount`.
5. A KB upload with an Account the user does not belong to is rejected, not silently stored as Personal — Task 9 test `TestKBUploadRejectsForeignAccount`.

---

## Phase 1 — Storage and read-only layers

### Task 1: Model — Common scope, source refs, audit rows

**Files:**
- Modify: `backend/internal/model/dictionary.go`
- Test: `backend/internal/model/dictionary_test.go`

**Interfaces:**
- Produces: `PrefixDictCommon = "DICT#COMMON"`; `TermScopeCommon = "common"`; `PrefixDictLog = "DICTLOG#"`;
  `type DictSourceRef struct { Kind string \`dynamodbav:"kind" json:"kind"\`; Ref string \`dynamodbav:"ref" json:"ref"\`; At string \`dynamodbav:"at" json:"at"\` }`;
  `DictTerm.Sources []DictSourceRef \`dynamodbav:"sources,omitempty"\`` (≤ 10, newest first);
  `DictObservation.SourceKind`, `DictObservation.SourceRef string` (meeting rows keep `meetingId`);
  `type DictTermLog struct { PK, SK, TermKey, Actor, Action, Before, After, At string }` with SK `DICTLOG#{termKey}#{at}`;
  `func TermScopeOf(pk string) (scope, accountID string, ok bool)`;
  source kinds `DictSourceMeeting="meeting"`, `DictSourceKB="kb"`, `DictSourceResearch="research"`, `DictSourceNews="news"`.

- [ ] **Step 1: Write failing tests** `TestTermScopeOf` — `USER#u1`→(`user`,"",true); `ACCOUNT#a1`→(`account`,"a1",true); `DICT#COMMON`→(`common`,"",true); `MEETING#x`→ok=false. `TestDictSourceRefsCapped` — `AppendDictSource(refs, ref)` keeps newest first, dedupes by Kind+Ref, max 10.
- [ ] **Step 2: Run** `cd backend && go test ./internal/model -run 'TermScopeOf|DictSourceRefs'` → FAIL (undefined).
- [ ] **Step 3: Implement** the constants, fields, `TermScopeOf`, and `func AppendDictSource(refs []DictSourceRef, ref DictSourceRef) []DictSourceRef`.
- [ ] **Step 4: Run** same command → PASS; `go test ./internal/...` stays green.
- [ ] **Step 5: Commit** `feat(dict): common scope, source refs and audit row model`.

### Task 2: Repository — membership-checked Account writes

**Files:**
- Modify: `backend/internal/repository/dictionary.go` (`CreateAutoTerm` :523, `MergeAutoObservation` :566, `PromoteSuggestedTerm` :575, `CreateDictTerm` :741, `UpdateDictTerm` :662, `TombstoneDictTerm` :685)
- Test: `backend/internal/repository/dictionary_account_test.go` (DynamoDB Local pattern used by existing repository tests)

**Interfaces:**
- Produces: `type MembershipCheck struct { AccountID, UserID string }`; field `DictMergeInput.Member *MembershipCheck`; `func (r *DictionaryRepository) CreateAutoTermChecked(ctx context.Context, term *model.DictTerm, obs *model.DictObservation, member *MembershipCheck) error`; `CreateDictTerm`, `UpdateDictTerm`, `TombstoneDictTerm` gain a trailing `member *MembershipCheck` parameter (nil for `USER#`/`DICT#COMMON`).
- Rule: when `TermScopeOf(pk)` is `account`, a nil `member` or `member.AccountID` ≠ the PK's account returns `ErrInvalidInput`; otherwise the transaction adds a ConditionCheck `attribute_exists(PK)` on `ACCOUNT#{id}`/`MEMBER#{userId}` (copy the shape of `hierarchyCheck`, `repository/account_hierarchy.go:45`). A failed check maps to `ErrNotAccountMember`.

- [ ] **Step 1: Write failing tests** `TestAccountTermWriteRequiresMembership` (member writes succeed; non-member → `ErrNotAccountMember`, nothing written; nil member on `ACCOUNT#` → `ErrInvalidInput`) and `TestPersonalAndCommonWritesNeedNoMember`.
- [ ] **Step 2: Run** `go test ./internal/repository -run 'AccountTermWrite|NeedNoMember'` → FAIL.
- [ ] **Step 3: Implement**; update every caller to pass `nil` (compile-driven).
- [ ] **Step 4: Run** `go test ./internal/repository/... ./internal/service/... ./cmd/...` → PASS.
- [ ] **Step 5: Commit** `feat(dict): membership-checked account term writes`.

### Task 3: Service + API — layered reads, Common audit

**Files:**
- Modify: `backend/internal/service/dictionary_terms.go`, `backend/internal/handler/dictionary.go`, `backend/cmd/api/main.go` (routes near :405)
- Create: `backend/internal/service/dictionary_layers.go`
- Test: `backend/internal/service/dictionary_layers_test.go`, `backend/internal/handler/dictionary_layers_test.go`

**Interfaces:**
- Produces: `func (s *DictionaryService) ListLayer(ctx context.Context, userID, layer string) ([]model.DictTermView, error)` where `layer` is `personal`, `common` or `account:{id}` (account requires `requireMember`, else `ErrForbidden`); `func (s *DictionaryService) UpdateCommonTerm(ctx context.Context, userID, termID string, change repository.DictTermChange, expectedVersion int64) error` and `DeleteCommonTerm(...)`, `RestoreCommonTerm(ctx, userID, termID string, logSK string) error` — each writes a `DictTermLog` row and trims to 20 per term; `ListTermLog(ctx, layer, termID string) ([]model.DictTermLog, error)`.
- Routes: `GET /api/settings/dictionary/layers/{layer}/terms`, `PATCH|DELETE /api/settings/dictionary/layers/common/terms/{termId}`, `GET /api/settings/dictionary/layers/{layer}/terms/{termId}/log`, `POST .../log/{logId}/restore`. Account write routes follow the same shape with membership. `DictTermView` gains `layer` and `sources`.
- Uses `DictionaryService` needing an account-membership reader: add `SetMembershipReader(func(ctx, accountID, userID string) (bool, error))`, wired in `cmd/api/main.go` to `repo.GetMember`.

- [ ] **Step 1: Write failing tests** `TestListLayerAccountRequiresMember`, `TestCommonEditKeepsAuditAndRestores` (edit → log row with Before/After/Actor; delete → tombstone + log; restore from log brings back displayAs; 25 edits keep 20 log rows), handler test for 403 on a foreign `account:` layer.
- [ ] **Step 2: Run** `go test ./internal/service -run 'ListLayer|CommonEdit' && go test ./internal/handler -run Layers` → FAIL.
- [ ] **Step 3: Implement.** Update `docs/API-SPEC.md` for the new routes.
- [ ] **Step 4: Run** the same → PASS.
- [ ] **Step 5: Commit** `feat(dict): layered dictionary reads and audited common edits`.

### Task 4: Dictionary layer tabs (frontend)

**Files:**
- Modify: `frontend/src/components/CustomDictionary.tsx`, `frontend/src/lib/dictionary.ts`, `frontend/src/lib/api.ts` (`dictionaryApi` :689)
- Create: `frontend/src/components/DictionaryLayerTabs.tsx`, `frontend/src/components/TermHistory.tsx`
- Test: `frontend/src/lib/dictionary.test.ts`, `frontend/src/components/DictionaryLayerTabs.test.tsx`

**Interfaces:**
- Consumes: Task 3 routes.
- Produces: `type DictionaryLayer = 'personal' | 'common' | \`account:${string}\``; `dictionaryApi.listLayer(layer)`, `updateCommon(termId, patch, version)`, `deleteCommon(termId, version)`, `termLog(layer, termId)`, `restore(layer, termId, logId)`; `termSourceBadges(item): {kind, label}[]` (Korean labels for meeting/KB/research/news; source of truth `frontend/src/lib/dictionary.ts`).
- UI: Korean-labelled tabs for Personal, Account (select of the user's Accounts) and Common (source of truth `frontend/src/lib/dictionary.ts`); existing `DictionaryTable` reused per tab; Common rows show Korean-labelled edit and delete actions for everyone and a history button opening `TermHistory` with a restore action (labels in `frontend/src/components/TermHistory.tsx`).

- [ ] **Step 1: Write failing tests** for `termSourceBadges` and for tab switching calling `listLayer` with `account:a1`.
- [ ] **Step 2: Run** `cd frontend && npx vitest run src/lib/dictionary.test.ts src/components/DictionaryLayerTabs.test.tsx` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** same → PASS; `npm run lint` clean.
- [ ] **Step 5: Commit** `feat(dict-ui): layer tabs, source badges, common history`.

## Phase 2 — Meetings feed Accounts; layered recognition

### Task 5: Scoped merge and Account terms from meetings

**Files:**
- Modify: `backend/internal/service/terms_merge.go` (`MergeExtractedTerms` :119), `backend/internal/service/terms_analysis.go` (`Process`, merge call :352), `backend/internal/service/terms_config.go` (or wherever `TermsConfigFromEnv` lives), `infra/lib/gateway-stack.ts` (`autoTermsEnvironment` :124)
- Test: `backend/internal/service/terms_merge_scope_test.go`, `backend/internal/service/terms_analysis_test.go`

**Interfaces:**
- Produces: `type TermsScope struct { PK string; Member *repository.MembershipCheck }`; `func (m *TermsMerger) MergeScoped(ctx context.Context, scope TermsScope, candidates []ExtractedTerm, ref TermsSourceRef) (*TermsMergeResult, error)`; `type TermsSourceRef struct { Kind, Ref, OwnerID string; NotesSafe, Tainted bool; Accepted []model.TermsAcceptedTerm }`. `MergeExtractedTerms(ctx, scopePK, candidates, ref TermsMeetingRef)` stays as a wrapper for the personal scope.
- `Process`: after the personal merge, when `ACCOUNT_TERMS_MODE=on` and the meeting has `AccountID` and the owner is a member, run `MergeScoped` into `ACCOUNT#{id}` with the same filtered terms (L1: `active` unless tainted). Counters logged separately (`layer=account`). The vocabulary refresher runs for every member of the Account (cap 50 members per run; more → log `refresh=truncated`).
- Env: `ACCOUNT_TERMS_MODE` from context `ttobak:accountTermsMode` (default `off`).

- [ ] **Step 1: Write failing tests** `TestMergeScopedAccountRequiresMember`, `TestProcessWritesAccountLayerWhenLinked` (linked meeting → term in both `USER#owner` and `ACCOUNT#a1`, source ref kind `meeting`), `TestProcessAccountLayerDisabledLogs` (flag off → no account rows, log line contains `layer=account outcome=DISABLED`), `TestProcessRefreshesAllMembersCapped` (60 members → 50 refreshes + truncated log).
- [ ] **Step 2: Run** `go test ./internal/service -run 'MergeScoped|AccountLayer|AllMembers'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/service/... ./cmd/summarize/...` → PASS; infra `npx jest test/gateway-stack.test.ts -t autoTerms` covers the new env var.
- [ ] **Step 5: Commit** `feat(terms): account layer from linked meetings`.

### Task 6: Layered vocabulary plan

**Files:**
- Modify: `backend/internal/service/vocabulary_table.go` (`planVocabulary` :144), `backend/internal/service/vocabulary_build.go` (`ensureVocabulary` :191 reads rows)
- Test: `backend/internal/service/vocabulary_layers_test.go`

**Interfaces:**
- Produces: `type vocabLayer struct { name string; rows []model.DictTerm }`; `func planVocabularyLayers(layers []vocabLayer, lang string) vocabPlan` (dedupe by `termKey`, first layer wins; cut at whole rows under `VocabMaxRows`/`VocabMaxBytes`; fingerprint covers the merged table); `func (s *DictionaryService) vocabularyLayers(ctx context.Context, userID string) ([]vocabLayer, error)` returning Personal manual, Accounts (ordered by the user's most recent meeting per Account — newest 200 of the user's meetings via the existing user-meeting list method, then remaining accounts in `ListAccountsForUser` order), Personal auto, Common. `ensureVocabulary` uses it instead of `ListDictTerms(USER#…)`.

- [ ] **Step 1: Write failing tests** `TestLayeredPlanOrderAndDedupe`, `TestLayeredPlanCutsAtLimits` (1,600 rows → 1,500, Personal manual all kept), `TestLayeredPlanSkipsAccountsUserLeft` (membership gone → that Account's rows absent).
- [ ] **Step 2: Run** `go test ./internal/service -run LayeredPlan` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/service/...` → PASS (existing builder tests must still pass: a user with no Accounts/Common gets the same fingerprint as before).
- [ ] **Step 5: Commit** `feat(vocab): layered vocabulary plan`.

### Task 7: Per-meeting Whisper prompt

**Files:**
- Modify: `backend/cmd/transcribe/main.go` (`whisperInputs` :319, `loadDictionaryTerms` :342)
- Test: `backend/cmd/transcribe/dictionary_test.go`

**Interfaces:**
- Produces: `var loadDictionaryTerms = func(ctx context.Context, userID, accountID string) ([]string, error)` returning display forms in order Personal manual → Account(`accountID`, only if the user is a member) → Common → Personal auto; `whisperInputs` passes `meeting.AccountID`; joined by `joinWhisperPrompt`.

- [ ] **Step 1: Write failing test** `TestWhisperPromptLayersForMeetingAccount` (account term appears before common; a non-member's account terms are absent).
- [ ] **Step 2–4:** run `go test ./cmd/transcribe -run WhisperPromptLayers` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(whisper): per-meeting layered prompt`.

## Phase 3 — Silent transcript correction

### Task 8: Glossary in RefineTranscript

**Files:**
- Modify: `backend/internal/service/bedrock.go` (`RefineTranscriptWithQuality` :1098, `buildRefineSystemPrompt` :1319, `refineChunk` :1361), `backend/cmd/summarize/transcript_quality.go` (`refineParsedTranscript` :30), `backend/internal/model/meeting.go`, `infra/lib/gateway-stack.ts`
- Create: `backend/internal/service/refine_glossary.go`
- Test: `backend/internal/service/refine_glossary_test.go`, `backend/cmd/summarize/transcript_quality_test.go`

**Interfaces:**
- Produces: `type GlossaryTerm struct { DisplayAs string; HeardAs []string }`; `func (s *BedrockService) RefineTranscriptWithGlossary(ctx context.Context, segments []WhisperSegment, glossary []GlossaryTerm) (string, []RefinedSegment, *model.TranscriptQuality, []model.TranscriptCorrection, error)`; `type TranscriptCorrection struct { From, To, TermKey string; Segment int }` stored as `Meeting.TranscriptCorrections` (`dynamodbav:"transcriptCorrections,omitempty"`, cap 50, never returned by meeting API responses); `func (s *DictionaryService) GlossaryForMeeting(ctx context.Context, ownerID, accountID string) ([]GlossaryTerm, error)` (Personal + that Account + Common, max 200 terms).
- Prompt rule (exact copy for the system prompt addition): "Glossary terms may have been mis-heard as similar-sounding words. Replace a span only when it is a clear mis-hearing of a glossary term in context. Never add a glossary term where no similar-sounding span exists. Never paraphrase. Return each replacement in `corrections`."
- Post-check in Go: drop any correction whose `From` does not occur in the original segment text or whose `To` is not a glossary display form; cap 50.
- Feedback: after a run, for each `From` corrected to the same term in ≥ 2 distinct meetings (tracked on the term's observations), append `From` to that term's `heardAs` (owner layer: the meeting's Account if the term came from it, else Personal).
- Flag: `TRANSCRIPT_CORRECTION_MODE` from `ttobak:transcriptCorrection` (default `off`).

- [ ] **Step 1: Write failing tests** `TestRefineGlossaryFixesMishearing` (fake model returns "gay culture"→"K-culture"; output text corrected; correction recorded), `TestRefineGlossaryNoSpanNoChange` (model claims a correction whose From is absent → dropped; text unchanged byte-for-byte), `TestGlossaryScopedToMeetingAccount` (account b1 terms absent for a meeting on a1), `TestCorrectionsNotInMeetingAPI` (handler JSON has no `transcriptCorrections`), `TestRepeatedCorrectionAddsHeardAs`.
- [ ] **Step 2: Run** `go test ./internal/service -run 'RefineGlossary|GlossaryScoped|RepeatedCorrection' && go test ./internal/handler -run CorrectionsNot` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/... ./cmd/summarize/...` → PASS.
- [ ] **Step 5: Commit** `feat(refine): silent glossary-based transcript correction`.

## Phase 4 — KB and research sources

### Task 9: KB upload asks for an Account

**Files:**
- Modify: `backend/internal/model/request.go` (`KBUploadRequest` :462), `backend/internal/service/kb.go` (`GetPresignedURL` :62), `backend/internal/handler/kb.go` (:28), `frontend/src/lib/api.ts` (:357), `frontend/src/components/KBFileList.tsx`
- Test: `backend/internal/service/kb_account_test.go`, `frontend/src/components/KBFileList.test.tsx`

**Interfaces:**
- Produces: `KBUploadRequest.AccountID string \`json:"accountId,omitempty"\``; `GetPresignedURL(ctx, userID, fileName, fileType, accountID string)`; non-empty `accountID` requires membership (else `ErrForbidden`), and the presigned PUT sets `Metadata{"account-id": accountID}` (the client must send header `x-amz-meta-account-id`). UI: upload dialog with a required choice — the user's Accounts plus a personal (no Account) option, labelled in Korean in `frontend/src/components/KBFileList.tsx`.

- [ ] **Step 1: Write failing tests** `TestKBUploadRejectsForeignAccount`, `TestKBUploadSignsAccountMetadata`, UI test that upload is blocked until a choice is made.
- [ ] **Step 2–4:** `go test ./internal/service -run KBUpload` and `npx vitest run src/components/KBFileList.test.tsx` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(kb): upload asks for the account`.

### Task 10: Generic source extraction (KB, research)

**Files:**
- Create: `backend/internal/service/terms_sources.go`
- Modify: `backend/cmd/summarize/main.go` (event dispatch), `infra/lib/gateway-stack.ts` (rule near :1107; KB bucket EventBridge notifications), `python/research-agent/tools.py` (`save_report` :362)
- Test: `backend/internal/service/terms_sources_test.go`, `python/research-agent/test_tools.py` (existing test module for tools, or create it)

**Interfaces:**
- Produces: event `ttobak.analysis` / `TermsSourceRequested` with `model.TermsSourceRequested{ Kind, Ref, OwnerID string; AccountIDs []string }`; `func (s *TermsAnalysisService) ProcessSource(ctx context.Context, ev model.TermsSourceRequested) error` — loads text (KB: S3 object via the existing attachment text extraction; research: report at `Research.S3Key`), runs the existing extraction calls and `FilterCandidates` with `TermSources{Documents: …}`, merges into each Account in `AccountIDs` where `OwnerID` is a member, else into `USER#{OwnerID}`; source ref kind `kb`/`research`. Idempotent per `Kind+Ref` (a `TERMS_SOURCE#{kind}#{ref}` run row with the content hash).
- KB trigger: EventBridge rule on the KB bucket `Object Created` for `kb/` → a small mapper in summarize that reads `account-id` metadata and publishes `TermsSourceRequested{Kind:"kb", Ref: key}`.
- Research trigger: `save_report` publishes `TermsSourceRequested{Kind:"research", Ref: research_id, OwnerID, AccountIDs}` after status `done` (best effort; failure logged, never fails the report).
- Flags: `KB_TERMS_MODE`, `RESEARCH_TERMS_MODE` (`ttobak:kbTermsMode`, `ttobak:researchTermsMode`).

- [ ] **Step 1: Write failing tests** `TestProcessSourceResearchToAccounts`, `TestProcessSourceKBPersonalWhenNoAccount`, `TestProcessSourceIdempotent`, `TestProcessSourceDisabledLogs`; Python `test_save_report_publishes_terms_event` (boto3 events client stubbed) and `test_save_report_survives_publish_error`.
- [ ] **Step 2: Run** `go test ./internal/service -run ProcessSource` and `cd python/research-agent && python -m pytest test_tools.py -k terms` → FAIL.
- [ ] **Step 3: Implement**; grant the research agent role `events:PutEvents` on the default bus only if it lacks it (check `research-agent-stack.ts`).
- [ ] **Step 4: Run** the same + `npx jest test/gateway-stack.test.ts` (new rule targets summarize) → PASS (the 6 known `gateway-stack` failures on main are unrelated; compare the failure list before/after).
- [ ] **Step 5: Commit** `feat(terms): KB and research sources`.

## Phase 5 — News feeds Common

### Task 11: Daily news terms with repeat threshold

**Files:**
- Modify: `backend/internal/service/terms_sources.go`, `backend/cmd/summarize/main.go`, `infra/lib/gateway-stack.ts` (schedule)
- Test: `backend/internal/service/terms_news_test.go`

**Interfaces:**
- Produces: `func (s *TermsAnalysisService) ProcessNewsDaily(ctx context.Context, day string) error` — reads the day's crawled documents (title + summary) through the crawler repository's existing document listing (add `ListDocumentsCrawledBetween(ctx, from, to int64)` if none exists), extracts candidates, keeps a term only if it appears in ≥ 2 documents that day or was a candidate on ≥ 2 distinct days in the last 7 (candidate history row `TERMS_NEWS#{termKey}`, TTL 14 days), merges into `DICT#COMMON` with source ref kind `news`. Schedule: EventBridge cron `0 21 * * ? *` (UTC, after the 19:00 crawl) → summarize with `{"kind":"news-daily"}`.
- Flag: `NEWS_TERMS_MODE` (`ttobak:newsTermsMode`).

- [ ] **Step 1: Write failing tests** `TestNewsSingleMentionNotAdded`, `TestNewsTwoDocsAdded`, `TestNewsTwoDaysAdded`, `TestNewsCommonCapEvictsOldestAuto` (cap 300).
- [ ] **Step 2–4:** `go test ./internal/service -run News` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(terms): daily news terms into the common layer`.

### Task 12: Observability and docs

**Files:**
- Modify: `infra/lib/pipeline-failures.ts`, `docs/decisions/ADR-008-custom-dictionary-for-stt-accuracy.md` (new amendment), `docs/INFRA-SPEC.md`, `docs/API-SPEC.md`
- Test: `infra/test/pipeline-failures.test.ts`

**Interfaces:**
- Produces: metric filters on summarize logs for `terms run: .* layer=(personal|account|common)` and `terms source: .* outcome=`; alarm `TermsSourcesSilent` when a source's trigger ran but produced 0 runs for 7 days; ADR-008 amendment "2026-10-26: layered dictionary" summarising L1–L4 and caps.

- [ ] **Step 1: Write failing infra test** for the alarm and metric filters.
- [ ] **Step 2–4:** `npx jest test/pipeline-failures.test.ts` FAIL → implement → PASS; `python3 scripts/docs/check_docs.py` clean.
- [ ] **Step 5: Commit** `feat(obs): dictionary layer metrics; docs: ADR-008 amendment`.

## Rollout (after all tasks, each a separate deploy with user approval)

1. Deploy Phase 1 (flags off). Verify layer tabs, Common audit.
2. `ttobak:accountTermsMode=on`; watch `layer=account` logs and vocabulary builds for one day.
3. `ttobak:transcriptCorrection=on`; sample 10 meetings' correction records.
4. `ttobak:kbTermsMode=on`, `ttobak:researchTermsMode=on`.
5. `ttobak:newsTermsMode=on`.
