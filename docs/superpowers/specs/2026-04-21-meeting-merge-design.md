# Meeting Merge Feature Design

**Date:** 2026-04-21
**Status:** Approved
**Branch:** TBD

## Problem

녹음이 끊기거나, 같은 회의를 여러 기기(MacBook, iPhone)에서 따로 녹음한 경우, 하나의 회의가 2~3개 미팅으로 분리되어 저장된다. 이를 하나의 미팅으로 합치는 기능이 필요하다.

## Key Decisions

- 오디오 파일은 합치지 않음 — 각 소스의 오디오/전사를 독립 유지
- 각 소스별 transcript를 모두 Bedrock에 전달하여 통합 요약 재생성
- 프론트엔드에서 소스별 탭으로 transcript/오디오 표시, 통합 요약은 탭 밖에 항상 표시
- Lazy migration으로 기존 데이터 하위호환

## Data Model Changes

### New: `AudioSource` struct

```go
type AudioSource struct {
    ID                 string            `dynamodbav:"id"`
    Label              string            `dynamodbav:"label"`
    AudioKey           string            `dynamodbav:"audioKey"`
    TranscriptA        string            `dynamodbav:"transcriptA"`
    TranscriptB        string            `dynamodbav:"transcriptB"`
    SelectedTranscript string            `dynamodbav:"selectedTranscript"`
    TranscriptSegments string            `dynamodbav:"transcriptSegments"`
    SpeakerMap         map[string]string `dynamodbav:"speakerMap"`
    SttProvider        string            `dynamodbav:"sttProvider"`
    Duration           float64           `dynamodbav:"duration"`
    CreatedAt          time.Time         `dynamodbav:"createdAt"`
}
```

### Meeting model changes

**Removed fields** (moved into AudioSource):
- `AudioKey`, `TranscriptA`, `TranscriptB`, `SelectedTranscript`, `TranscriptSegments`, `SpeakerMap`, `SttProvider`

**Added fields:**
- `AudioSources []AudioSource` — 1 or more audio sources
- `MergedFrom []string` — source meeting IDs for tracking

**Retained at meeting level:**
- `Content` (unified summary from all sources)
- `ActionItems` (unified)
- `Notes`, `Participants`, `Tags`, `Status`, etc.

### Backward Compatibility — Lazy Migration

No DynamoDB batch migration. On read, if `AudioSources` is empty and `AudioKey` exists, auto-convert legacy flat fields to `AudioSources[0]`. On next write, saved in new format only.

During transition, API response includes both legacy flat fields (from first source) and `audioSources` array.

## API Changes

### New endpoint

```
POST /api/meetings/{meetingId}/merge
Body: { "sourceMeetingIds": ["uuid1", "uuid2"] }
Response: 200 { meeting: Meeting }
```

**Preconditions:**
- All meetings must belong to the same user
- All meetings must have `status: "done"`
- Source and target must be different meetings

**Processing order:**
1. Validate preconditions
2. Fetch source meetings
3. Append source AudioSources to target (auto-label: source meeting title or "소스 N")
4. Re-key source attachments to target meetingId
5. Union shares (more permissive permission wins on conflict)
6. Union Notes (separator), Participants, Tags
7. Delete source meetings (DynamoDB records + share records)
8. Set target `status = "summarizing"`, `mergedFrom = [sourceIds]`
9. Trigger re-summarization (internal call to summarize flow)
10. On summary completion: `status = "done"`, regenerate KB document

### Modified endpoints

| Endpoint | Change |
|---|---|
| `GET /api/meetings/{meetingId}` | Returns `audioSources` array. Legacy flat fields also included (first source) for backward compat |
| `GET /api/meetings/{meetingId}/audio` | Add `?sourceId={id}` query param. Default: first source |
| `PUT /api/meetings/{meetingId}/transcript` | Add `?sourceId={id}`. Selects transcript for specific source |
| `PUT /api/meetings/{meetingId}/speakers` | Add `?sourceId={id}`. Updates speaker map for specific source |
| `POST /api/meetings` | Auto-creates `AudioSources[0]` from provided fields |

## Frontend Changes

### Meeting Detail Page — Source Tabs

```
┌─────────────────────────────────────┐
│  [MacBook]  [iPhone]  [+ 합치기]    │  ← Source tab bar
├─────────────────────────────────────┤
│  ▶ Audio player (selected source)   │
│  Speaker map editor (selected)      │
│  Transcript segments (selected)     │
└─────────────────────────────────────┘

── Unified area (always visible) ──────
│  AI Summary (unified)               │
│  Action Items (unified)             │
│  Notes (unified)                    │
│  Attachments (all)                  │
```

- **Single source**: Tab bar hidden, identical to current UX
- **Multiple sources**: Tab bar visible, each tab shows its own audio + transcript
- **Labels**: Editable by user, default from source meeting title

### Merge Flow UI

1. Click `[+ 합치기]` tab/button
2. **Meeting selection modal** — list of user's `done` meetings, with search/date filter, multi-select
3. **Confirmation dialog** — "선택한 N개 미팅을 이 미팅에 합칩니다. 원본 미팅은 삭제됩니다. 계속하시겠습니까?"
4. **Processing state** — status becomes `summarizing`, banner shows "통합 요약 생성 중..."
5. **Completion** — new source tabs appear, unified summary updates

### Mobile (<768px)

- Source tab bar: horizontal scroll when 3+ tabs
- Merge modal: fullscreen bottom sheet
- `[+ 합치기]`: icon-only button (`call_merge` Material Symbol) at tab bar end

## Summarization for Merged Meetings

### Prompt structure

```
다음은 같은 회의를 여러 기기에서 녹음한 전사 결과입니다.

[소스: {label1}]
{transcript1}

[소스: {label2}]
{transcript2}

이 전사 결과들을 종합하여 하나의 통합 회의록을 작성하세요.
중복되는 내용은 한 번만, 각 소스에서만 들린 내용도 포함하세요.
```

### Token limit handling

If combined transcripts exceed Bedrock token limit: summarize each source independently first, then generate a unified summary from the per-source summaries (two-stage summarization).

### Trigger mechanism

Merge API does not call Bedrock directly. Sets `status = "summarizing"` and calls the existing summarize handler internally. Summarize handler detects `AudioSources` and switches to multi-source prompt.

## Error Handling

| Scenario | Response |
|---|---|
| Source meeting not `done` | 400 — "모든 미팅이 완료 상태여야 합니다" |
| Source owned by different user | 403 |
| Source = target | 400 |
| Source already deleted | 404 |
| Bedrock summarization fails | AudioSource merge completes, status = `error`. User can retry via existing re-summarize button |
| Combined transcript exceeds token limit | Two-stage summarization (per-source → unified) |
| DynamoDB transaction exceeds 100 items | Batch transactions in groups of 100 (reuse existing DeleteMeeting pattern) |
| Network error mid-merge | Order is: read sources → update target → delete sources. Failure before source deletion = data duplication only (no loss). Safe to retry. |

## Scope Exclusions

- Audio file concatenation (ffmpeg) — not needed, sources stay independent
- Cross-user meeting merge — only same-user meetings
- Undo merge — not in scope (source meetings are deleted)
- Auto-suggest merge candidates — future enhancement
