# Requirements: Multi-File Audio Upload & Linked Meetings

## Feature Overview
Enable users to upload multiple audio files for a single meeting (split recordings) and link follow-up meetings for cross-session context continuity. Based on [ADR-014](../../../docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md).

---

## User Stories

### Story 1: Multi-File Audio Upload (Upload Mode)
**As a** user with a meeting recording split across multiple files,
**I want to** upload all files at once for a single meeting,
**So that** I get one unified transcript and summary without manually merging audio files.

#### Acceptance Criteria
1. **Given** I am on the record page in upload mode (`/record?mode=upload`), **when** I click the file selector or drag-and-drop, **then** I can select multiple audio files at once.
2. **Given** I have selected multiple files, **when** I view the upload queue, **then** I see each file listed with its name, size, and a drag handle to reorder them.
3. **Given** I have reordered the files, **when** I start the upload, **then** each file uploads independently with its own progress bar, and the overall progress is shown.
4. **Given** all files have uploaded successfully, **when** the transcription pipeline runs, **then** each file is transcribed independently in parallel.
5. **Given** all file transcriptions are complete, **when** the summarize step runs, **then** the transcripts are merged chronologically (by part index) and a single unified summary and action items are generated.
6. **Given** an existing meeting with status `done`, **when** I use the AudioUploader to add more audio files, **then** the new files are appended as additional parts and re-transcribed without losing the existing transcript.
7. **Given** a meeting with multiple audio parts, **when** I view the meeting detail page, **then** I can see all audio parts and play them sequentially.

---

### Story 2: Multi-File Audio Upload (Post-Recording Add)
**As a** user viewing a completed meeting,
**I want to** add additional audio recordings to an existing meeting,
**So that** supplementary recordings (e.g., a follow-up call the same day) are included in the same transcript.

#### Acceptance Criteria
1. **Given** a meeting with status `done` or `error`, **when** I open the meeting detail, **then** I see an option to "Add audio files" alongside or instead of the single-file uploader.
2. **Given** I upload one or more additional files, **when** upload completes, **then** the files are appended as new parts (`part_N`) without overwriting existing audio.
3. **Given** new parts are uploaded, **when** transcription completes for all new parts, **then** the meeting's transcript is re-merged (existing + new parts) and summary is regenerated.

---

### Story 3: Backward-Compatible Single-File Upload
**As a** user uploading a single audio file (the existing flow),
**I want** the upload experience to remain unchanged,
**So that** the multi-file feature doesn't add unnecessary complexity to the simple case.

#### Acceptance Criteria
1. **Given** I select a single file in upload mode, **when** I upload it, **then** the flow behaves exactly as before: one presigned URL, one upload, one `audioKey`, one transcription.
2. **Given** an existing meeting was created before this feature (has `audioKey` string, not `audioKeys` list), **when** I view the meeting, **then** the audio plays correctly using the legacy `audioKey` field.
3. **Given** the transcribe Lambda receives an audio file without `part_` prefix in the S3 key, **when** it processes the file, **then** it writes the transcript to `transcripts/{meetingId}.json` (the existing path).

---

### Story 4: Multi-Part Audio Playback
**As a** user viewing a meeting with multiple audio parts,
**I want to** listen to all parts in sequence,
**So that** I can review the full meeting audio without switching files manually.

#### Acceptance Criteria
1. **Given** a meeting has 3 audio parts, **when** I open the audio player, **then** I see a playlist with part labels (Part 1, Part 2, Part 3) and total duration.
2. **Given** Part 1 finishes playing, **when** playback continues, **then** Part 2 starts automatically (gapless or with minimal gap).
3. **Given** the player is playing Part 2, **when** I click on a transcript segment from Part 3, **then** the player jumps to Part 3 at the correct timestamp.
4. **Given** a single-file meeting (legacy), **when** I open the audio player, **then** it behaves exactly as the current single-URL player.

---

### Story 5: Linked Follow-Up Meetings
**As a** user running weekly syncs on the same project,
**I want to** link follow-up meetings to their predecessors,
**So that** the AI summary tracks action items across sessions (e.g., "from last week: migrate auth v2 -- status: completed").

#### Acceptance Criteria
1. **Given** I am creating a new meeting, **when** I choose "Follow-up of..." from a dropdown of recent meetings, **then** the new meeting is linked to the selected predecessor.
2. **Given** a meeting is linked to 2 predecessors, **when** the summarize Lambda runs, **then** it fetches the predecessors' summaries and action items and includes them as context in the Bedrock prompt.
3. **Given** the AI receives prior context, **when** it generates the summary, **then** it produces an "Action Item Tracking" section that marks each prior action item as "completed", "in progress", or "carried over".
4. **Given** I view a linked meeting's detail page, **when** I look at the header area, **then** I see a breadcrumb-style chain of linked meetings with clickable navigation.
5. **Given** meeting A is linked to B which is linked to C, **when** I view meeting C, **then** the context chain only includes B's summary and action items (direct predecessors, not the full chain) to limit prompt size.

---

### Story 6: Transcription Progress for Multi-Part
**As a** user who uploaded 3 audio files,
**I want to** see per-part transcription progress,
**So that** I know which parts are done and how long the remaining parts will take.

#### Acceptance Criteria
1. **Given** a meeting has 3 audio parts with `audioPartCount=3`, **when** I view the meeting detail, **then** I see "Transcribing: 1/3 parts complete" (or similar).
2. **Given** 2 of 3 parts are transcribed, **when** I check the status, **then** I see "Transcribing: 2/3 parts complete" with partial transcript visible for completed parts.
3. **Given** all 3 parts are transcribed and merged, **when** the status transitions to `summarizing`, **then** the progress indicator updates accordingly.

---

## Out of Scope (for this iteration)
- Meeting merge (combining two fully-processed meetings retroactively)
- Audio file trimming/editing in-browser
- Real-time recording producing multi-part files (live recording always produces one file)
- Cross-user meeting linking (only the owner can link their own meetings)
