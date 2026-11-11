# Shareable-by-default and Bilingual Notes Implementation Plan

Status: Historical implementation plan (2026-11-10); deployment acceptance is recorded separately.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generated meeting notes are shareable (safe provenance) by default, final notes are always Korean, and owners/editors can generate an independent English note shown with a Korean/English toggle.

**Architecture:** Three stacked PRs (95 KB review cap). PR 1 changes provenance rules and generation inputs in Go. PR 2 adds an English-note run (`ANALYSIS#summaryEn`) that reuses the saved-source resummary pipeline with a `target` and publishes only `contentEn*` fields. PR 3 changes the meeting page.

**Tech Stack:** Go 1.27 (chi, DynamoDB expression builder, stdlib tests), Next.js 16/React 19 (node:test scripts), Markdown docs.

**Spec:** `docs/superpowers/specs/2026-11-10-shareable-bilingual-notes-design.md`

## Global Constraints

- English note is generated from the transcript, notes, verified DOCUMENT text and image analyses: never from `Content` (Korean note) or a prior English note.
- English runs never write `content`, `actionItems`, `insights`, `tags`, `sentiment`, `liveSummary`, `summaryPrivacy`, `actionItemContinuity`, simulator or action-analysis state.
- No linked-predecessor content enters any generation prompt.
- `safe` provenance survives owner/editor `content`/`liveSummary` edits; `private`/`unknown` stay sticky.
- Only the owner may run the derivative-clearing sharing-safe reset (`RequestSharingSafe`), unchanged.
- No new unauthenticated route, no infra/EventBridge rule change (reuse detail-type `SummaryRequested`).
- UI copy is Korean (written in code, not in docs); repo docs are concise English. Remove the "sharing-safe notes" wording from the UI.
- Go tests: `(cd backend && /usr/local/go/bin/go test ./... -count=1)` includes `cmd/*`.

## Review Focus

1. An English run racing a Korean resummary or human edit: neither may overwrite the other's fields; the English CAS must not pin `content`.
2. A meeting whose stored `noteLanguage` is `en`: the next Korean generation must still produce Korean.
3. A legacy `unknown` row edited by its owner: must stay `unknown` (no accidental declassification).
4. Item-size budget: a large Korean note plus English note must fail the English publish cleanly (`OUTPUT_TOO_LARGE`), never exceed the item.
5. Meeting deletion while an English run is queued: the `ANALYSIS#summaryEn` row is deleted in the first transaction and the late worker write fails its condition.

---

## PR 1: Shareable by default (backend)

### Task 1: Remove linked-predecessor context from generation

**Files:**
- Modify: `backend/internal/service/bedrock.go` (`summarizeTranscript`), `backend/internal/service/summary_privacy.go` (delete `bindLinkedSummarySources`, `linkedSummaryContext` if unused)
- Modify: `backend/cmd/summarize/main.go` (two `buildLinkedMeetingContext` call sites; delete the function and its owner-mismatch helper if unused), `backend/cmd/summarize/summary_retry.go`
- Modify: `backend/internal/service/resummary.go` (`prepare`: stop dropping image `ProcessedContent`/`Description` in sharing-safe mode; keep clearing `Content`, `LiveSummary`, derivatives, `LinkedMeetingIDs`)
- Test: `backend/internal/service/note_language_test.go` fixture style, new `backend/internal/service/summary_sources_test.go`; `backend/cmd/summarize/main_test.go`

**Interfaces:**
- Produces: `summarizeTranscript` no longer calls `BindLinkedSummarySources`; `priorContext` parameters remain in signatures but every production caller passes `""`.

- [ ] **Step 1: Write failing tests**
  - `TestBatchSummaryIgnoresLinkedMeetings`: meeting with `LinkedMeetingIDs: ["prev"]`, mock repo whose linked row has title `"PREV-SECRET"`; invoke `SummarizeTranscript` via `invokeNoteSourceFixture`; assert `!strings.Contains(request.System+request.User, "PREV-SECRET")` and the mock records zero `BindLinkedSummarySources` calls.
  - `TestSharingSafePrepareKeepsImageAnalysis`: snapshot with `SharingSafe: true` and an image attachment `ProcessedContent: "diagram text"`; assert the prepared attachment keeps `"diagram text"` and `Content == ""`.
  - `TestRetryPassesNoLinkedContext` in `cmd/summarize`: retry path `run` receives `priorContext == ""` for a meeting with links.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service/ ./cmd/summarize/ -run 'LinkedMeetings|KeepsImageAnalysis|NoLinkedContext' -count=1)` — expect FAIL.
- [ ] **Step 3: Implement.** Delete the binding/injection calls; delete now-unused helpers (`go vet` flags leftovers). Keep `LinkedSources` on the snapshot type (still read by `summaryPublicationPrivacy`; it will be empty).
- [ ] **Step 4: Run** the full `go test ./... -count=1` and `go vet ./...` — expect PASS. Fix existing tests that asserted predecessor injection by inverting them to the new rule, not deleting them.
- [ ] **Step 5: Commit** `feat(summary): stop using linked predecessor meetings as note evidence`

### Task 2: Safe provenance survives edits; first live draft is safe

**Files:**
- Modify: `backend/internal/repository/summary_privacy.go` (new pure function), `backend/internal/repository/dynamodb.go` (`updateMeetingFieldsWithCondition` privacy block)
- Test: `backend/internal/repository/summary_privacy_test.go`, `backend/internal/repository/summary_privacy_writers_test.go`

**Interfaces:**
- Produces: `func editedSummaryPrivacyState(parent *model.Meeting) string` in `repository/summary_privacy.go`.

- [ ] **Step 1: Write failing table test** `TestEditedSummaryPrivacyState`:

| parent | want |
|---|---|
| `SummaryPrivacy: nil`, no derivatives | `safe` |
| `SummaryPrivacy: nil`, `ActionItems: "[{...}]"` | `unknown` |
| state `safe`, matching hash | `safe` |
| state `unknown` | `unknown` |
| state `private` | `private` |
| state `safe`, hash mismatch (reads as unknown) | `unknown` |

  Plus writer test `TestLiveSummaryFirstWriteStampsSafe`: `UpdateMeetingFieldsIfMatch` with `{"liveSummary": "draft"}` on a fresh meeting, writers enabled, asserts stored `summaryPrivacy.state == "safe"` and `InheritedState == ""`; and `TestOwnerEditKeepsSafe` / `TestOwnerEditKeepsUnknownSticky`.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/repository/ -run 'EditedSummaryPrivacyState|FirstWriteStampsSafe|EditKeeps' -count=1)` — expect FAIL.
- [ ] **Step 3: Implement.** Replace the `if state == safe { state = unknown }` block with `state := editedSummaryPrivacyState(parent)`; set `InheritedState` to `""` when the result is `safe`, else to the state. Leave the ADR-029 approval clearing on content/live writes unchanged.
- [ ] **Step 4: Run** full repository and service tests — expect PASS; update tests that asserted safe→unknown on edit to the new rule.
- [ ] **Step 5: Commit** `feat(summary): keep safe provenance across human edits`

### Task 3: Korean is the only Korean-note language

**Files:**
- Modify: `backend/internal/service/bedrock.go` (`generateSummarySnapshot` gains `language string`; `summarizeTranscript` passes `model.NoteLanguageKorean`), `backend/internal/service/resummary_generate.go` (`GenerateResummary` passes `model.NoteLanguageKorean`)
- Test: `backend/internal/service/note_language_test.go`

**Interfaces:**
- Produces: `func (s *BedrockService) generateSummarySnapshot(ctx context.Context, meeting *model.Meeting, attachments []model.Attachment, priorContext string, includeSaved bool, language string) (string, error)` — Task 5 calls it with `model.NoteLanguageEnglish`.

- [ ] **Step 1: Rewrite** `TestSummaryPromptsUseNoteLanguageDirective` so that for stored `""`, `"ko"`, `"en"` both batch and resummary prompts contain `noteLanguageDirective("ko")` and never `noteLanguageDirective("en")` or `noteLanguageDirective("")`.
- [ ] **Step 2: Run** `go test ./internal/service/ -run NoteLanguage -count=1` — expect FAIL for `en`/`""`.
- [ ] **Step 3: Implement** the parameter; `noteLanguageDirective` itself is unchanged. `UpdateMeeting` keeps accepting `noteLanguage` (compatibility); keep `noteLanguage` in capture field lists.
- [ ] **Step 4: Run** full Go tests — PASS.
- [ ] **Step 5: Commit** `feat(summary): always write final notes in Korean`

### Task 4: ADR and references for PR 1

**Files:**
- Create: `docs/decisions/ADR-018-mac-caption-overlay.md` (confirm the next free number with `ls docs/decisions`)
- Modify: `docs/decisions/ADR-029-note-provenance-and-sharing-safety.md` (header line "Partially superseded by ADR-018: …"), `docs/API-SPEC.md` (resummary/noteLanguage/provenance paragraphs), `docs/architecture.md` (summary inputs), `docs/README.md` index if ADRs are listed there

- [ ] **Step 1:** ADR-018 records: predecessors removed from all generation; human `content`/`liveSummary` edits keep `safe`; first live draft on empty provenance is `safe`; image analyses are safe evidence (verified: `AnalyzeImage`/`ProcessImageAttachment` read only the image object and attachment row at `dde84cb8`); `noteLanguage` no longer affects Korean generation. Status line and rationale per `docs/CLAUDE.md`.
- [ ] **Step 2: Run** `python3 scripts/docs/check_docs.py` from repo root — PASS.
- [ ] **Step 3: Commit** `docs: record shareable-by-default note provenance (ADR-018)`; open PR 1.

## PR 2: English note backend (stacked on PR 1)

### Task 5: Model, storage and repository writes

**Files:**
- Modify: `backend/internal/model/resummary.go`, `backend/internal/model/meeting.go`, `backend/internal/repository/resummary.go`, `backend/internal/repository/dynamodb.go` (`nonSpillableSizedFields`, `DeleteMeeting`), `backend/internal/repository/stt_recovery.go` (`sttDeletionAllowedChildren`)
- Test: new `backend/internal/repository/english_note_test.go`

**Interfaces:**
- Produces:
  - `const EnglishSummarySK = "ANALYSIS#summaryEn"`; `const SummaryTargetEnglish = "en"`
  - `SummaryRequested` gains `Target string \`json:"target,omitempty"\``
  - `Meeting` gains `ContentEn string \`dynamodbav:"contentEn,omitempty"\``, `ContentEnSourceHash string \`dynamodbav:"contentEnSourceHash,omitempty"\``, `ContentEnGeneratedAt string \`dynamodbav:"contentEnGeneratedAt,omitempty"\``
  - `func (r *DynamoDBRepository) GetEnglishSummary(ctx context.Context, meetingID string) (*model.ResummaryState, error)`
  - `QueueEnglishSummary`, `StartEnglishSummary`, `FailEnglishSummary` with the same parameter lists as their `*Resummary` counterparts, keyed by `EnglishSummarySK`
  - `func (r *DynamoDBRepository) CompleteEnglishSummary(ctx context.Context, snapshot *model.SummarySnapshot, state *model.ResummaryState, content, sourceHash string, now time.Time) error`

- [ ] **Step 1: Write failing tests**
  - `TestCompleteEnglishSummaryWritesOnlyEnglishFields`: captured update expression names are exactly `contentEn`, `contentEnSourceHash`, `contentEnGeneratedAt` plus the state row; no `content`, `summaryPrivacy`, `actionItems`, `status`.
  - `TestCompleteEnglishSummaryPinsSourceNotKoreanContent`: condition includes transcript/notes/attachment checks and excludes `content`.
  - `TestCompleteEnglishSummaryTooLarge`: Korean `content` of 290 KB + English 20 KB returns `ErrSummaryLimit`.
  - `TestDeleteMeetingDeletesEnglishSummaryState`: transaction contains a delete for `MEETING#{id}`/`ANALYSIS#summaryEn`.
- [ ] **Step 2: Run** `go test ./internal/repository/ -run English -count=1` — FAIL.
- [ ] **Step 3: Implement.** Factor the shared queue/start/fail bodies of `resummary.go` over an SK parameter rather than copying them. Add `contentEn` to `nonSpillableSizedFields`. English completion checks use `snapshot.Checks` with the canonical meeting check rebuilt from `summaryMeetingFields` minus `content` (add helper `englishSourceFields()`).
- [ ] **Step 4: Run** full Go tests — PASS.
- [ ] **Step 5: Commit** `feat(notes): store English note run state and result`

### Task 6: English note service and worker dispatch

**Files:**
- Modify: `backend/internal/service/resummary.go` (target-aware request/process), `backend/internal/service/resummary_generate.go`, `backend/cmd/summarize/main.go` (`SummaryRequested` branch passes `Target`)
- Test: `backend/internal/service/resummary_test.go`, `backend/cmd/summarize/main_test.go`

**Interfaces:**
- Consumes: Task 5 repository methods; Task 3 `generateSummarySnapshot(..., language)`.
- Produces:
  - `func (s *ResummaryService) RequestEnglish(ctx context.Context, userID, meetingID string) (*ResummaryResponse, error)` — owner or editor (`authorized(..., edit=true)`), meeting `done`, usable transcript.
  - `func (s *ResummaryService) GetEnglish(ctx context.Context, userID, meetingID string) (*EnglishNoteResponse, error)` — any reader.
  - `type EnglishNoteResponse struct { ResummaryResponse; Content string \`json:"content,omitempty"\`; GeneratedAt string \`json:"generatedAt,omitempty"\`; Stale bool \`json:"stale"\` }`
  - `func englishSourceHash(snapshot *model.SummarySnapshot) string` — hash of checks excluding `content`, plus `"\x00en"`.
  - generator interface gains `GenerateEnglishNote(ctx context.Context, meeting *model.Meeting, attachments []model.Attachment) (string, error)`; `BedrockService` implements it via `generateSummarySnapshot(ctx, meeting, attachments, "", false, model.NoteLanguageEnglish)`.

- [ ] **Step 1: Write failing tests**
  - `TestEnglishRunExcludesKoreanContent`: snapshot `Content: "KOREAN-NOTE"`, fake generator records its meeting; assert `meeting.Content == ""`, `LiveSummary == ""`, image `ProcessedContent` kept.
  - `TestEnglishRunEditorAllowed` / `TestEnglishRunReaderForbidden` (`ErrForbidden`).
  - `TestEnglishRunFailureRetainsPrevious`: generator error → `FailEnglishSummary` called, `CompleteEnglishSummary` not called.
  - `TestEnglishStaleWhenTranscriptChanges`: `GetEnglish` with stored `ContentEnSourceHash` ≠ current `englishSourceHash` → `Stale: true`; equal → `false`.
  - `TestKoreanAndEnglishRunsIndependent`: a pending Korean run does not make `RequestEnglish` busy and vice versa.
  - `cmd/summarize`: `TestSummaryRequestedDispatchesEnglishTarget` routes `{"target":"en"}` to the English process path and `""` to the existing one.
- [ ] **Step 2: Run** `go test ./internal/service/ ./cmd/summarize/ -run 'English|Dispatches' -count=1` — FAIL.
- [ ] **Step 3: Implement.** Thread a private `target string` through `request`/`Process`/`prepare`; for `en`, `prepare` clears `Content`, `LiveSummary`, derivatives and `LinkedMeetingIDs` but keeps image analyses; `Process` calls `GenerateEnglishNote` and `CompleteEnglishSummary(..., englishSourceHash(snapshot), now)`; no terms enqueue. Unknown `Target` values return `ErrInvalidInput`.
- [ ] **Step 4: Run** full Go tests — PASS.
- [ ] **Step 5: Commit** `feat(notes): generate English notes from meeting sources`

### Task 7: HTTP routes, detail exposure, docs

**Files:**
- Modify: `backend/internal/handler/resummary.go`, `backend/cmd/api/main.go` (`GET`/`POST /api/meetings/{meetingId}/notes/en`), `backend/internal/model/request.go` (`MeetingDetailResponse.HasEnglishNote bool \`json:"hasEnglishNote"\``), `backend/internal/service/meeting.go` (set it from `ContentEn != ""`), `docs/API-SPEC.md`, `docs/architecture.md`
- Test: `backend/internal/handler/resummary_test.go` (or new `english_note_test.go`)

**Interfaces:**
- Consumes: Task 6 `RequestEnglish`, `GetEnglish`.
- Produces: `POST` returns `202` with `ResummaryResponse`; `GET` returns `200` `EnglishNoteResponse`; errors map like `writeResummary` (`ErrForbidden`→403, `ErrNotFound`→404, `ErrResummaryBusy`→409). Detail never inlines `contentEn` (keeps detail size unchanged).

- [ ] **Step 1: Write failing handler tests** for 202/200/403/404/409 and that the route is under the authenticated router group (no public prefix).
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement.** **Step 4:** full Go tests, `go vet`, arm64 API build — PASS.
- [ ] **Step 5:** Update `docs/API-SPEC.md` (routes, states, staleness, field list), run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 6: Commit** `feat(api): expose English meeting notes`; open PR 2.

## PR 3: Meeting page (stacked on PR 2)

### Task 8: English note view state and API client

**Files:**
- Create: `frontend/src/lib/englishNote.ts`, `frontend/scripts/english-note.test.mjs`
- Modify: `frontend/src/lib/api.ts` (`getEnglishNote`, `requestEnglishNote`), `frontend/src/types/meeting.ts` (`EnglishNote` type, `MeetingDetail.hasEnglishNote?: boolean`)

**Interfaces:**
- Produces: `export function englishNoteView(note: EnglishNote | null, canEdit: boolean): { showToggle: boolean; action: 'generate' | 'regenerate' | 'retry' | null; badge: 'generating' | 'failed' | 'stale' | null }`

- [ ] **Step 1: Write failing tests** (`node:test`, transpile pattern of `scripts/meeting-status.test.mjs`):
  - no note, editor → `{ showToggle:false, action:'generate', badge:null }`; no note, reader → action `null`.
  - status `running` → badge `generating`, action `null`.
  - status `failed`, editor → badge `failed`, action `retry`.
  - content present, `stale:true`, editor → toggle, badge `stale`, action `regenerate`; reader → toggle, badge `stale`, action `null`.
- [ ] **Step 2: Run** `(cd frontend && node --test scripts/english-note.test.mjs)` — FAIL. **Step 3: Implement.** **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(web): add English note view state`

### Task 9: Meeting page toggle and control cleanup

**Files:**
- Modify: `frontend/src/components/meeting/AISummaryCard.tsx` (Korean/English segmented toggle; badge keys map to Korean labels for generating, failed and source-changed; English view is read-only `MarkdownRenderer` with the same `resolveCitation`), `frontend/src/components/meeting/ResummaryControls.tsx`, `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`, `frontend/src/hooks/useResummary.ts` (poll English status while running, same interval), `docs/DESIGN-SPEC.md`
- Test: extend `frontend/scripts/english-note.test.mjs` with a source-text assertion that `ResummaryControls.tsx` no longer contains the sharing-safe notes label or the `noteLanguages` selector

- [ ] **Step 1: Write failing assertions** (source text): no sharing-safe notes label, no note-language select; `AISummaryCard.tsx` contains the Korean "generate English notes" action label.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
  - Remove the language select and its "regenerate in this language" button.
  - Sharing-safe button becomes a plain Korean "regenerate notes" action for owners only when `summarySharing.requiresRegeneration`, with Korean copy saying: this note was made the old way and cannot be shared individually; regenerating also recreates action items and insights.
  - Toggle shows when `englishNoteView(...).showToggle`; selected tab persists per meeting in component state only.
  - Linked-meeting UI copy that promises carried-over context is changed to plain "linked meetings" navigation wording (grep the Korean word for "context" in `frontend/src/components/meeting/`).
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` — PASS.
- [ ] **Step 5:** Update `docs/DESIGN-SPEC.md` meeting-notes section; `python3 scripts/docs/check_docs.py`.
- [ ] **Step 6: Commit** `feat(web): show Korean/English notes and drop sharing-safe wording`; open PR 3.

## Deployment order (after merges)

Readers before writers: deploy GatewayStack (API + summarize share one stack) with PR 2 before shipping PR 3's frontend; PR 1 changes no reader contracts. Deploy only through the tracked workflows; verify `SUMMARY_*` env values are unchanged afterward.
