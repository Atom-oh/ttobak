# Tasks: Multi-File Audio Upload & Linked Meetings

## Phase 1: Backend Model + Upload API

### Task 1.1: Extend Meeting Model
- [ ] Add multi-file fields to `Meeting` struct
  - **File**: `backend/internal/model/meeting.go`
  - Add `AudioKeys []string` with `dynamodbav:"audioKeys,omitempty"`
  - Add `AudioPartCount int` with `dynamodbav:"audioPartCount,omitempty"`
  - Add `AudioPartsReady int` with `dynamodbav:"audioPartsReady,omitempty"`
  - Add `LinkedMeetingIDs []string` with `dynamodbav:"linkedMeetingIds,omitempty"`
  - Add helper method `GetEffectiveAudioKeys() []string` that returns `AudioKeys` if non-empty, else `[]string{AudioKey}` if AudioKey is set, else `nil`

### Task 1.2: Extend API Request/Response Models
- [ ] Add multi-file fields to request/response structs
  - **File**: `backend/internal/model/request.go`
  - `UploadCompleteRequest`: add `PartIndex int` (`json:"partIndex,omitempty"`) and `TotalParts int` (`json:"totalParts,omitempty"`)
  - `MeetingDetailResponse`: add `AudioKeys []string`, `AudioPartCount int`, `AudioPartsReady int`, `LinkedMeetingIDs []string`
  - `CreateMeetingRequest`: add `LinkedMeetingIDs []string` (`json:"linkedMeetingIds,omitempty"`)
  - Update meeting-to-response mapping in handler to populate new fields

### Task 1.3: Add DynamoDB Index-Based Set + Atomic Increment Operations
- [ ] Implement `SetAudioKeyAtIndex` repository method (HIGH-2 fix: idempotent index-based write)
  - **File**: `backend/internal/repository/dynamodb.go`
  - New method: `SetAudioKeyAtIndex(ctx, userID, meetingID, key string, partIndex, totalParts int) error`
  - Uses `SET audioKeys[{partIndex}] = :key, #st = :transcribing`
  - ConditionExpression: `attribute_exists(PK) AND size(audioKeys) = :totalParts`
  - Retry-safe: re-uploading same part overwrites the same index slot
- [ ] Implement `PreAllocateAudioKeys` repository method
  - **File**: `backend/internal/repository/dynamodb.go`
  - New method: `PreAllocateAudioKeys(ctx, userID, meetingID string, totalParts int) error`
  - Sets `audioKeys = ["", "", ...]` (N empty strings), `audioPartCount = N`, `audioPartsReady = 0`
  - Called during meeting creation when totalParts > 1
- [ ] Implement `IncrementAudioPartsReady` repository method (MEDIUM-1 fix: use ReturnValues)
  - **File**: `backend/internal/repository/dynamodb.go`
  - New method: `IncrementAudioPartsReady(ctx, meetingID string) (newPartsReady, partCount int, error)`
  - Uses `ADD audioPartsReady :one` expression with `ReturnValues: ALL_NEW`
  - MUST use the returned values directly — do NOT do a separate GetItem after increment
  - Needs GSI3 lookup for PK/SK from meetingID (use existing `GetMeetingByID` pattern)

### Task 1.4: Modify Upload Service for Multi-File
- [ ] Update `GeneratePresignedUploadURL` to support part prefix
  - **File**: `backend/internal/service/upload.go`
  - When `req.PartIndex >= 0 && req.TotalParts > 1`: generate key as `audio/{userId}/{meetingId}/part_{NNN}_{sanitizedFileName}` where NNN = zero-padded partIndex
  - When single file (totalParts <= 1 or 0): existing key format unchanged
- [ ] Update `CompleteUpload` for multi-file index-based set
  - **File**: `backend/internal/service/upload.go`
  - When `req.TotalParts > 1`: call `repo.SetAudioKeyAtIndex(...)` instead of `repo.UpdateMeetingFields(audioKey=...)`
  - When single file: existing `UpdateMeetingFields` flow unchanged
- [ ] Add server-side validation (MEDIUM-6 fix)
  - **File**: `backend/internal/service/upload.go`
  - Reject `totalParts > 10`
  - Reject `partIndex >= totalParts`
  - Reject `partIndex < 0`
- [ ] Add `PartIndex` and `TotalParts` to `PresignedURLRequest`
  - **File**: `backend/internal/model/request.go`
  - Add `PartIndex int` and `TotalParts int` fields

### Task 1.5: Update Meeting Detail Handler (Audio URLs)
- [ ] Return multiple audio URLs for multi-part meetings
  - **File**: `backend/internal/handler/meeting.go`
  - Modify `GetAudioURL` handler: if `meeting.AudioKeys` is non-empty, return `{ "audioUrls": [...] }` with presigned URLs for each key
  - Fall back to existing `{ "audioUrl": "..." }` for single-key meetings
  - Add new response shape to `model/request.go`: `AudioURLResponse` with both `AudioUrl string` and `AudioUrls []string`

### Task 1.6: Add Link Meetings Endpoint
- [ ] Create `POST /api/meetings/{meetingId}/link` handler
  - **File**: `backend/internal/handler/meeting.go`
  - Validate: all linkedMeetingIds exist and owned by same user
  - Validate: max 3 predecessors (aligned with prompt builder), no circular links
  - Call `repo.UpdateMeetingFields(ctx, userID, meetingID, {"linkedMeetingIds": ids})`
- [ ] Register route in API router
  - **File**: `backend/cmd/api/main.go`
  - Add `r.Post("/api/meetings/{meetingId}/link", meetingHandler.LinkMeetings)`
- [ ] Pass linkedMeetingIds on meeting creation
  - **File**: `backend/internal/service/meeting.go` (or handler)
  - When `CreateMeetingRequest.LinkedMeetingIDs` is non-empty, set the field on the new meeting

---

## Phase 2: Transcribe Lambda (Multi-Part Output)

### Task 2.1: Part-Aware Transcript Output
- [x] Detect part index from S3 key and adjust output path
  - **File**: `backend/cmd/transcribe/main.go`
  - Add `extractPartIndex(key string) (int, bool)` — regex match `part_(\d{3})_` in key
  - If part detected: set Whisper/Transcribe output key to `transcripts/{meetingId}_part_{NNN}.json`
  - If no part: existing `transcripts/{meetingId}.json` output unchanged
- [x] Pass part index + output key to Whisper ECS task (Observation B)
  - **File**: `backend/cmd/transcribe/main.go` in `startWhisperTask`
  - Add `OUTPUT_KEY` environment override to ECS RunTask (e.g., `transcripts/{meetingId}_part_{NNN}.json`)
  - Whisper container reads `OUTPUT_KEY` to determine output path
  - **External dependency**: Whisper Docker image must be updated to honor `OUTPUT_KEY`

### Task 2.2: Completion Tracking (in Summarize Lambda, not here)
Completion tracking is handled in Phase 3 (summarize Lambda). When a `_part_` transcript
lands in S3, EventBridge triggers summarize Lambda, which increments `audioPartsReady` and
checks if all parts are done. This avoids adding EventBridge client to the transcribe Lambda.

---

## Phase 3: Summarize Lambda (Merge + Linked Context)

### Task 3.1: Two-Phase Event Dispatch (MEDIUM-2 fix)
- [x] Implement two-phase unmarshal for event type detection
  - **File**: `backend/cmd/summarize/main.go`
  - First: unmarshal raw JSON to extract only `source` and `detail-type` fields
  - Then dispatch:
    - `"aws.s3"` + key contains `_part_`: → `handlePartTranscript()` (new)
    - `"aws.s3"` + key has no `_part_`: → existing flow (unchanged)
    - `"ttobak.transcribe"`: → `handleAllPartsTranscribed()` (new)
  - **Key routing** (MEDIUM-5 fix): add explicit `_part_` check BEFORE `extractMeetingIDFromTranscriptKey`
- [x] Add new event struct for custom AllPartsTranscribed event
  - **File**: `backend/internal/model/events.go`
  - New struct `AllPartsTranscribedEvent` with `MeetingID`, `PartCount`, `Bucket` fields
- [x] Add `handlePartTranscript` function
  - **File**: `backend/cmd/summarize/main.go`
  - Steps:
    1. Leave partial transcript in S3 (no DynamoDB write for individual parts)
    2. Call `repo.IncrementAudioPartsReady(meetingID)` with `ReturnValues: ALL_NEW`
    3. If returned `partsReady == partCount`: emit `AllPartsTranscribed` EventBridge event
    4. Do NOT run summary generation on individual parts
- [x] Add EventBridge client to summarize Lambda init()
  - Import EventBridge SDK, add `emitAllPartsTranscribedEvent` helper

### Task 3.2: Implement Transcript Merge + Direct Summary (HIGH-1 fix)
- [x] Add merge function for multi-part transcripts
  - **File**: `backend/cmd/summarize/merge.go` (new file)
  - `mergePartTranscripts(ctx, bucket, meetingID string, partCount int) (string, []TranscriptSegmentOut, error)`
  - Steps:
    1. List S3 objects `transcripts/{meetingId}_part_*.json`, sort by part index
    2. Download and parse each part, refine whisper segments
    3. For part N>0: add cumulative offset to all segment timestamps
    4. Concatenate all segments into merged text + segments
- [x] Extract summary generation into shared function (HIGH-1 fix)
  - **File**: `backend/cmd/summarize/main.go`
  - `generateSummary(ctx, meeting, priorContext)` handles: summary, action items, tags, sentiment, KB export
  - Both single-transcript and merge paths call this function
  - `handleAllPartsTranscribed` calls `mergePartTranscripts()` then `generateSummary()` directly
  - Does NOT rely on S3 write to re-trigger itself
- [x] Add guard for S3 re-trigger after merge write
  - `handleSingleTranscript` checks if meeting status is `summarizing` or `done` → skip

### Task 3.3: Add Linked Meeting Context to Bedrock Prompt
- [x] Fetch predecessor summaries during summary generation
  - **File**: `backend/cmd/summarize/main.go`
  - `buildLinkedMeetingContext(ctx, meeting)` fetches up to 3 linked meetings
  - Truncates each summary to 2000 chars, action items to 500 chars
  - Returns formatted Korean context prefix string
- [x] Modify `SummarizeTranscript` to accept explicit `priorContext string` parameter
  - **File**: `backend/internal/service/bedrock.go`
  - Changed signature from variadic `userID ...string` to explicit `userID, priorContext string`
  - Prepends prior context to user prompt when non-empty

---

## Phase 4: CDK Infrastructure

### Task 4.1: Add AllPartsTranscribed EventBridge Rule
- [x] Define new rule targeting summarize Lambda
  - **File**: `infra/lib/gateway-stack.ts`
  - Added `AllPartsTranscribedRule` with pattern `source: ['ttobak.transcribe'], detailType: ['AllPartsTranscribed']`
  - Target: `summarizeFunction` (EventBridge auto-grants invoke permission via addTarget)

### Task 4.2: Grant EventBridge PutEvents to Summarize Lambda
- [x] Add IAM permission for summarize Lambda to emit events
  - **File**: `infra/lib/ai-stack.ts` (where summarizeRole is defined)
  - Added `events:PutEvents` policy scoped to `default` event bus ARN

---

## Phase 5: Frontend Multi-File Upload

### Task 5.1: Multi-File Selection in Upload Mode
- [ ] Update record page upload UI for multiple files
  - **File**: `frontend/src/app/record/page.tsx`
  - Add `multiple` attribute to `<input type="file">`
  - New state: `selectedFiles: File[]` (replaces single-file flow)
  - On file select: append to `selectedFiles`, show list with reorder controls
  - Reorder: simple "move up" / "move down" buttons per file
  - Remove: "X" button per file
  - Validate: max 10 files, each ≤500MB, audio MIME types only
  - "Start Upload" button: creates meeting, then calls `uploadMultipleFiles(files, meetingId)`

### Task 5.2: Multi-File Upload Logic
- [ ] Add `uploadMultipleFiles` function (MEDIUM-4 fix: Promise.allSettled)
  - **File**: `frontend/src/lib/upload.ts`
  - Signature: `uploadMultipleFiles(files: File[], category, progressCallback, meetingId) => Promise<{results: PromiseSettledResult[]}>`
  - For each file (parallel via `Promise.allSettled`, NOT `Promise.all`):
    1. Get presigned URL with `partIndex` and `totalParts`
    2. Upload via existing XHR logic
    3. Call `notifyUploadComplete` with `partIndex` and `totalParts`
  - `Promise.allSettled` returns status for every file, enabling per-file retry on failure
  - Per-file progress tracked separately; overall progress = avg of all files
- [ ] Update `uploadsApi.getPresignedUrl` to include partIndex/totalParts
  - **File**: `frontend/src/lib/api.ts`
  - Extend data type to include optional `partIndex?: number` and `totalParts?: number`

### Task 5.3: Multi-File Upload Progress UI
- [ ] Create file list with per-file progress
  - **File**: `frontend/src/app/record/page.tsx`
  - When uploading: show each file with individual progress bar
  - Show overall progress summary: "Uploading 2/3 files..."
  - On completion: redirect to `/meeting/{meetingId}` as before

### Task 5.4: Update AudioUploader for Multi-File
- [ ] Enable multi-file in AudioUploader component
  - **File**: `frontend/src/components/AudioUploader.tsx`
  - Add `multiple` to both drag-and-drop and file input
  - Process all dropped/selected files, not just `files[0]`
  - Show per-file progress bars stacked vertically
  - Calculate `partIndex` as: existing meeting audioKeys length + file index
  - Calculate `totalParts` as: existing meeting audioKeys length + new files count

---

## Phase 6: Frontend Audio Playback

### Task 6.1: Multi-Part AudioPlayer
- [ ] Extend AudioPlayer to support playlist
  - **File**: `frontend/src/components/AudioPlayer.tsx`
  - Add prop: `audioUrls?: string[]` (alongside existing `audioUrl?: string`)
  - Internal state: `currentPartIndex: number`, `partDurations: number[]`, `totalDuration: number`
  - On part end (`onEnded` event): advance to next part, start playback
  - Progress bar: show total progress across all parts with part dividers
  - Time display: show absolute time (cumulative across parts)
  - Seek: click on progress bar calculates which part + offset

### Task 6.2: Meeting Detail Audio URL Fetching
- [ ] Fetch multiple audio URLs when audioKeys is present
  - **File**: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
  - If `meeting.audioKeys?.length > 1`: fetch all audio URLs (batch or parallel individual calls)
  - Pass `audioUrls={urls}` to AudioPlayer
  - If single audio (legacy or audioKeys.length === 1): pass `audioUrl={url}` as before
- [ ] Add API method for fetching multiple audio URLs
  - **File**: `frontend/src/lib/api.ts`
  - `meetingsApi.audioUrls(meetingId)` → returns `{ audioUrls: string[] }`

### Task 6.3: Multi-Part Transcription Progress
- [ ] Show per-part progress in meeting detail
  - **File**: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
  - When `meeting.audioPartCount > 1 && meeting.status === 'transcribing'`:
    - Show "Transcribing: {audioPartsReady}/{audioPartCount} parts"
    - Progress bar: `audioPartsReady / audioPartCount * 100`%

---

## Phase 7: Frontend Linked Meetings

### Task 7.1: Follow-Up Selector on Meeting Creation
- [ ] Add "Follow-up of" dropdown
  - **File**: `frontend/src/app/record/page.tsx`
  - Fetch recent meetings (status=done, last 10) on mount
  - Optional combobox/dropdown: "Follow-up of: [select meeting]"
  - On meeting create: include `linkedMeetingIds: [selectedMeetingId]` in request body
  - Show in both record mode and upload mode

### Task 7.2: Linked Meeting Chain in Meeting Detail
- [ ] Display linked meeting breadcrumbs
  - **File**: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
  - If `meeting.linkedMeetingIds` is non-empty:
    - Show horizontal breadcrumb chain: "Previous: [Meeting Title] → Current Meeting"
    - Each predecessor is a clickable link to `/meeting/{id}`
  - Styled as subtle breadcrumbs above the meeting header

### Task 7.3: Action Item Cross-Session Tracking Display
- [ ] Distinguish carried-over vs new action items
  - **File**: `frontend/src/components/meeting/ActionItemsCard.tsx`
  - If action item has a `fromMeeting` field: show with "(from {date})" label and status badge
  - Status badges: "completed" (green), "in progress" (yellow), "carried over" (gray)

---

## Phase 8: Testing & Documentation

### Task 8.0: Fix Auto-Expiry Timeout for Multi-Part (Observation C)
- [ ] Adjust 30-minute auto-expiry for multi-part meetings
  - **File**: `backend/internal/handler/meeting.go` (GetMeeting handler)
  - Current: marks stuck `transcribing`/`summarizing` as `error` after 30 minutes
  - New: for multi-part meetings, timeout = 30 min × audioPartCount (max 120 min)
  - Check `meeting.AudioPartCount` and adjust threshold accordingly

### Task 8.0b: Add New Fields to ListMeetings Projection (Observation D)
- [ ] Update DynamoDB query projection for meeting list
  - **File**: `backend/internal/repository/dynamodb.go` (around line 551)
  - Add `audioPartCount`, `audioPartsReady` to projection expression
  - Enables meeting list view to show multi-file status badges

### Task 8.1: Backend Unit Tests
- [ ] Test `SetAudioKeyAtIndex` repository method (idempotent index write)
- [ ] Test `PreAllocateAudioKeys` repository method
- [ ] Test `IncrementAudioPartsReady` repository method (verify ReturnValues: ALL_NEW)
- [ ] Test `CompleteUpload` with multi-file parameters
- [ ] Test `extractPartIndex` key parsing
- [ ] Test `mergePartTranscripts` with 2-3 parts
- [ ] Test linked meeting context building
- [ ] Test backward compatibility: single-file upload still works

### Task 8.2: Update API Spec Documentation
- [ ] Document new fields in upload endpoints
  - **File**: `docs/API-SPEC.md`
  - Update `POST /api/upload/presigned` with `partIndex`, `totalParts`
  - Update `POST /api/upload/complete` with `partIndex`, `totalParts`
  - Add `POST /api/meetings/{meetingId}/link`
  - Update `GET /api/meetings/{meetingId}` response with new fields
  - Update `GET /api/meetings/{meetingId}/audio` response with `audioUrls`

### Task 8.3: Update Architecture Documentation
- [ ] Add multi-file pipeline diagram
  - **File**: `docs/architecture.md`
  - Add section describing multi-part transcription flow
  - Document AllPartsTranscribed custom event

### Task 8.4: Build & Verify
- [ ] Build all Go Lambda binaries
  - `cd backend && for dir in cmd/api cmd/transcribe cmd/summarize; do GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir; done`
- [ ] Build frontend
  - `cd frontend && npm run build && npm run lint`
- [ ] CDK synth
  - `cd infra && npx cdk synth`
