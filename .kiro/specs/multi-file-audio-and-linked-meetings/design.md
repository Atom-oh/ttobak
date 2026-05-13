# Design: Multi-File Audio Upload & Linked Meetings

## References
- [ADR-014](../../../docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md)
- [Meeting Model](../../../backend/internal/model/meeting.go)
- [Upload Service](../../../backend/internal/service/upload.go)
- [Transcribe Lambda](../../../backend/cmd/transcribe/main.go)
- [Summarize Lambda](../../../backend/cmd/summarize/main.go)

---

## 1. Data Model Changes

### 1.1 DynamoDB Meeting Entity (Backend Go Model)

**File**: `backend/internal/model/meeting.go`

Add fields to `Meeting` struct:

```go
// Existing (keep for backward compatibility)
AudioKey           string    `dynamodbav:"audioKey,omitempty"`

// New multi-file fields
AudioKeys          []string  `dynamodbav:"audioKeys,omitempty"`          // Ordered list of S3 keys
AudioPartCount     int       `dynamodbav:"audioPartCount,omitempty"`     // Total parts expected
AudioPartsReady    int       `dynamodbav:"audioPartsReady,omitempty"`    // Parts with completed transcription
LinkedMeetingIDs   []string  `dynamodbav:"linkedMeetingIds,omitempty"`   // Chronologically ordered predecessor IDs
```

**Backward compatibility rule**: If `AudioKeys` is empty/nil but `AudioKey` is set, treat it as a single-part meeting. The `GetEffectiveAudioKeys()` helper returns `[]string{m.AudioKey}` in that case.

### 1.2 Frontend TypeScript Model

**File**: `frontend/src/types/meeting.ts`

```typescript
// Existing (keep)
audioKey?: string;

// New
audioKeys?: string[];
audioPartCount?: number;
audioPartsReady?: number;
linkedMeetingIds?: string[];
```

### 1.3 API Response Changes

**File**: `backend/internal/model/request.go`

`MeetingDetailResponse` gains:
```go
AudioKeys        []string `json:"audioKeys,omitempty"`
AudioPartCount   int      `json:"audioPartCount,omitempty"`
AudioPartsReady  int      `json:"audioPartsReady,omitempty"`
LinkedMeetingIDs []string `json:"linkedMeetingIds,omitempty"`
```

`UploadCompleteRequest` gains:
```go
PartIndex  int `json:"partIndex,omitempty"`  // 0-based index of this part
TotalParts int `json:"totalParts,omitempty"` // Total number of parts (set on first call)
```

---

## 2. S3 Key Structure

### 2.1 Multi-Part Audio Keys

```
audio/{userId}/{meetingId}/part_000_{sanitizedFileName}
audio/{userId}/{meetingId}/part_001_{sanitizedFileName}
audio/{userId}/{meetingId}/part_002_{sanitizedFileName}
```

**Single-file (legacy)**: continues to use `audio/{userId}/{meetingId}/{timestamp}_{filename}` without `part_` prefix.

### 2.2 Multi-Part Transcript Keys

```
transcripts/{meetingId}_part_000.json
transcripts/{meetingId}_part_001.json
transcripts/{meetingId}_part_002.json
transcripts/{meetingId}.json              ← merged final (written by summarize Lambda)
```

**Single-file (legacy)**: continues to use `transcripts/{meetingId}.json`.

---

## 3. Upload Flow (Multi-File)

### 3.1 Sequence Diagram

```
Frontend                    API Lambda              S3              EventBridge         Transcribe Lambda
   │                           │                    │                   │                    │
   │─── create meeting ───────►│                    │                   │                    │
   │◄── { meetingId } ────────│                    │                   │                    │
   │                           │                    │                   │                    │
   │─── presigned(part_000) ──►│                    │                   │                    │
   │◄── { uploadUrl, key } ───│                    │                   │                    │
   │─── PUT file[0] ──────────┼───────────────────►│                   │                    │
   │                           │                    │── Object Created ►│                    │
   │                           │                    │                   │── invoke ─────────►│
   │                           │                    │                   │                    │── transcribe part_000
   │                           │                    │                   │                    │
   │─── presigned(part_001) ──►│  (parallel)        │                   │                    │
   │◄── { uploadUrl, key } ───│                    │                   │                    │
   │─── PUT file[1] ──────────┼───────────────────►│                   │                    │
   │                           │                    │── Object Created ►│                    │
   │                           │                    │                   │── invoke ─────────►│
   │                           │                    │                   │                    │── transcribe part_001
   │                           │                    │                   │                    │
   │─── complete(part_000) ───►│                    │                   │                    │
   │    {partIndex:0,total:2}  │── append audioKeys │                   │                    │
   │                           │── set partCount=2  │                   │                    │
   │                           │── status=transcribing                  │                    │
   │                           │                    │                   │                    │
   │─── complete(part_001) ───►│                    │                   │                    │
   │    {partIndex:1,total:2}  │── append audioKeys │                   │                    │
```

### 3.2 Meeting Creation with Pre-Allocated AudioKeys

Pre-allocation is **lazy**: triggered on the first `CompleteUpload` call with `totalParts > 1`,
not at meeting creation time. This is because the user may select files after creating the meeting.

```
On first CompleteUpload with totalParts > 1:
  1. If audioKeys does not exist on the meeting:
     - SET audioKeys = ["", "", ...] (N empty strings)
     - SET audioPartCount = N, audioPartsReady = 0
  2. Use ConditionExpression: attribute_not_exists(audioKeys)
     - If audioKeys already exists (concurrent first call), the condition fails harmlessly
       and the caller falls through to the index-based SET in step 3.3
```

This pre-allocation enables idempotent index-based writes (see 3.3).

### 3.3 CompleteUpload Logic (Modified)

```
IF totalParts > 1 (multi-file):
  1. Build S3 key with part_{NNN} prefix
  2. Atomic DynamoDB update (index-based, idempotent):
     - SET audioKeys[{partIndex}] = :key
     - SET status = "transcribing"
     - ConditionExpression: attribute_exists(PK) AND size(audioKeys) = :totalParts
  3. Retry-safe: re-uploading same part overwrites the same index slot

  Server-side validation (MEDIUM-6 fix):
     - Reject totalParts > 10
     - Reject partIndex >= totalParts
     - Reject if audioKeys[partIndex] is already non-empty (optional, for strict mode)

ELSE (single-file, backward compat):
  1. Existing flow: SET audioKey = key, status = "transcribing"
```

---

## 4. Transcription Pipeline (Multi-Part)

### 4.1 Transcribe Lambda Changes

**File**: `backend/cmd/transcribe/main.go`

**Part detection**: Extract part index from S3 key pattern `part_(\d{3})_`.
- If part prefix found: write output to `transcripts/{meetingId}_part_{NNN}.json`
- If no part prefix: write to `transcripts/{meetingId}.json` (existing behavior)

**Completion tracking**: Handled in the summarize Lambda (not here).
When a `_part_` transcript arrives in S3, EventBridge triggers the summarize Lambda,
which increments the counter and checks if all parts are ready (see Section 5).

### 4.2 Custom EventBridge Event

```json
{
  "source": "ttobak.transcribe",
  "detail-type": "AllPartsTranscribed",
  "detail": {
    "meetingId": "abc-123",
    "partCount": 3,
    "bucket": "ttobak-assets-..."
  }
}
```

### 4.3 CDK: New EventBridge Rule

**File**: `infra/lib/gateway-stack.ts`

```typescript
const allPartsRule = new events.Rule(this, 'AllPartsTranscribedRule', {
  eventPattern: {
    source: ['ttobak.transcribe'],
    detailType: ['AllPartsTranscribed'],
  },
});
allPartsRule.addTarget(new targets.LambdaFunction(summarizeFunction));
```

---

## 5. Summarize Lambda (Merge Logic)

### 5.1 Handler Dispatch

**File**: `backend/cmd/summarize/main.go`

Two-phase unmarshal: first extract `source` from raw JSON, then unmarshal full payload.

**Dispatch table:**

| Event Source | Key Pattern | Action |
|---|---|---|
| `aws.s3` | `transcripts/{id}.json` (no `_part_`) | Existing: single-transcript → full summary generation |
| `aws.s3` | `transcripts/{id}_part_{NNN}.json` | New: save partial transcript, increment counter, emit AllPartsTranscribed if all done |
| `ttobak.transcribe` | N/A (custom event) | New: merge all parts → **directly call summary generation** (no S3 re-trigger) |

**Key routing for S3 events** (MEDIUM-2/MEDIUM-5 fix):
```go
if strings.Contains(key, "_part_") {
    meetingID, partIndex := extractPartInfo(key)  // new function
    return handlePartTranscript(ctx, bucket, key, meetingID, partIndex)
}
// existing flow: extractMeetingIDFromTranscriptKey(key) unchanged
```

**For AllPartsTranscribed event (HIGH-1 fix — no S3 self-re-invocation):**
```
1. Parse meetingId from custom event detail (new struct: AllPartsEvent)
2. List S3 objects matching prefix transcripts/{meetingId}_part_
3. Sort by part index (000, 001, 002...)
4. Download each part transcript
5. For each part N > 0: offset all segment timestamps by cumulative duration of parts 0..N-1
   - Duration source: whisper_metadata.duration field in each part transcript JSON
6. Concatenate all segments into one array
7. Write merged transcript to transcripts/{meetingId}.json (for archival/playback only)
8. Call generateSummary(ctx, meetingID, mergedTranscript) DIRECTLY
   — Do NOT rely on S3 event to re-invoke this Lambda
   — Extract summary generation into a shared function callable from both
     single-transcript path and merge path
```

### 5.2 Timestamp Offset Calculation

```
Part 0: duration 1200s, segments [0.0 .. 1200.0]  → offsets unchanged
Part 1: duration  900s, segments [0.0 ..  900.0]  → add 1200.0 to all timestamps
Part 2: duration  600s, segments [0.0 ..  600.0]  → add 2100.0 to all timestamps
```

Duration is obtained from:
- **Whisper**: `whisper_metadata.duration` field in the JSON output
- **AWS Transcribe**: `results.items[-1].end_time` (last word's end time)

### 5.3 No Infinite Loop Risk

The merged transcript file (`transcripts/{meetingId}.json`) IS written to S3 for archival purposes.
This triggers an S3 Object Created event, which invokes the summarize Lambda again. However:
- The handler checks: if meeting status is already `summarizing` or `done`, skip processing.
- The summary was already generated directly in the AllPartsTranscribed handler (step 8 above).
- The S3-triggered re-invocation becomes a no-op and exits early.

This is defense-in-depth — the primary summary path is the direct call, not the S3 re-trigger.

Use whitelist-based guard: only process if status is `transcribing`. This covers `summarizing`, `done`,
AND `error` states, preventing re-processing in all terminal/in-progress states.

---

## 6. Linked Meetings (Summarize Context)

### 6.1 Bedrock Prompt Augmentation

When `meeting.LinkedMeetingIDs` is non-empty during summary generation:

```
1. For each linkedMeetingId (max 3 predecessors — enforced in both API and prompt builder):
   a. Fetch meeting record from DynamoDB
   b. Extract: title, date, summary, actionItems
   c. Truncate summary to 2000 chars (~500 tokens) to control token budget
   d. Include only action item list text, no surrounding narrative
2. Prepend to Bedrock prompt:
   "## Prior Meeting Context
   ### Meeting: {title} ({date})
   Summary: {summary (truncated)}
   Action Items: {actionItems}
   ---
   Track the status of these prior action items in your summary."
```

**Token budget**: ~700 tokens per predecessor × 3 = ~2100 tokens overhead. Acceptable for
Claude Opus/Sonnet 200K context window. Controlled by character-based truncation (4 chars ≈ 1 token).

### 6.2 Link API Endpoint

**New route**: `POST /api/meetings/{meetingId}/link`

```json
// Request
{ "linkedMeetingIds": ["meeting-id-1", "meeting-id-2"] }

// Response
{ "status": "ok" }
```

Validation:
- All linked meeting IDs must exist and be owned by the same user
- Max 3 linked predecessors (aligned with prompt builder limit)
- No circular links (A→B→A)

---

## 7. Frontend Changes

### 7.1 Upload Mode Multi-File UI

**File**: `frontend/src/app/record/page.tsx`

- Change `<input>` to `multiple` attribute
- New state: `selectedFiles: File[]` with drag-and-drop reorder (simple up/down buttons, no library)
- Show file list with name, size, remove button, drag handle
- On "Upload" button click: create meeting, then upload all files in parallel with per-file progress

### 7.2 AudioUploader (Meeting Detail)

**File**: `frontend/src/components/AudioUploader.tsx`

- Accept `multiple` files
- Show per-file progress bars
- Call `notifyUploadComplete` with `partIndex` and `totalParts` for each file

### 7.3 AudioPlayer (Multi-Part Playlist)

**File**: `frontend/src/components/AudioPlayer.tsx`

- Accept `audioUrls: string[]` prop (alongside existing `audioUrl?: string` for compat)
- Internal state: `currentPartIndex`, track durations array
- On part end: auto-advance to next part
- Show part indicators in the progress bar
- Seek across parts: calculate which part + offset from absolute time

### 7.4 Meeting Detail Client

**File**: `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`

- Fetch all audio URLs when `audioKeys` has multiple entries
- Show per-part transcription progress: "Transcribing 2/3 parts"
- Render linked meeting chain as clickable breadcrumbs
- Pass `audioUrls[]` to AudioPlayer

### 7.5 Meeting Creation (Link Selector)

**File**: `frontend/src/app/record/page.tsx`

- Optional "Follow-up of" dropdown in both record and upload modes
- Populated from recent meetings list (last 10, filtered to status=done)
- On meeting create, pass `linkedMeetingIds` in the create request

---

## 8. Error Handling

| Scenario | Behavior |
|----------|----------|
| 1 of 3 uploads fails | Show error for that file; allow retry for failed file only; use `Promise.allSettled` (not `Promise.all`) to get partial results |
| 1 of 3 transcriptions fails | Status stays `transcribing`; meeting detail shows "Error in Part 2" with retry option; other parts' transcripts are preserved |
| Merge fails in summarize | Status set to `error`; individual part transcripts remain readable; user can trigger re-merge via "Retry" button |
| User uploads 0 files | Upload button disabled; validation message shown |
| User uploads >10 files | **Both client and server** reject: server returns 400 if `totalParts > 10` |
| partIndex >= totalParts | Server returns 400 (prevents out-of-bounds index attacks) |
| Cumulative file size >5GB | Server rejects presigned URL generation (sum of existing parts + new) |
| Mixed codecs (webm + wav) | Each transcribed independently; no issue since merge is at transcript level |
| Auto-expiry timeout | Multi-part meetings: 30 min × audioPartCount (max 120 min) instead of fixed 30 min |

---

## 9. Migration & Compatibility

- **No DynamoDB migration needed**: new fields (`audioKeys`, `audioPartCount`, etc.) are added as optional attributes. Existing items simply don't have them.
- **Backend reads both**: `GetEffectiveAudioKeys()` helper checks `AudioKeys` first, falls back to `[]string{AudioKey}`.
- **Frontend reads both**: TypeScript checks `meeting.audioKeys?.length > 0` first, falls back to `meeting.audioKey`.
- **Existing EventBridge rules unchanged**: audio/ and transcripts/ prefix rules stay. New custom rule added for `AllPartsTranscribed`.
- **ListMeetings projection**: Add `audioPartCount`, `audioPartsReady` to the DynamoDB query projection in `repository/dynamodb.go:551` so meeting list view can show multi-file status badges.
- **Whisper container output key**: Pass `OUTPUT_KEY` env var to ECS task so the container writes to `transcripts/{meetingId}_part_{NNN}.json`. This is an external dependency (Whisper Docker image update).
