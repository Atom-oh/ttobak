# ADR-014: Multi-File Audio Upload, Linked Meetings, and Transcript Merge

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — Phases 1, 2, 4, 5 implemented (2026-05-26). Phase 3 (summarize merge) and Phase 6 (linked meetings) pending.

## Context

Users sometimes end up with meeting recordings split across multiple audio files. This happens when:

- Recording software crashes or restarts mid-meeting
- System audio capture (Mac app) drops the stream due to permission resets
- Users deliberately pause/resume, creating separate file segments
- Mobile or external recorders produce chunked output (e.g., 30-minute WAV segments)

Currently, TTOBAK supports exactly **one audio file per meeting**:
- `Meeting.AudioKey` is a single `string` field in DynamoDB
- `CompleteUpload` overwrites `audioKey` on each call, so only the last uploaded file persists
- The transcribe Lambda processes one S3 key per invocation
- The summarize Lambda expects one transcript file at `transcripts/{meetingId}.json`
- The frontend `AudioUploader` accepts a single file and the audio player streams a single URL

This limitation forces users to either manually merge files externally (using ffmpeg or similar) before uploading, or create separate meetings and lose the continuity of a single conversation thread. Neither is acceptable UX.

This ADR also subsumes the previously discussed "meeting merge" concept. Rather than merging two fully-processed meetings (which involves reconciling separate speaker maps, summaries, and action items), the cleaner solution is to support multiple audio inputs for a single meeting at the upload stage, before any processing occurs.

A closely related scenario is **linked follow-up meetings**: a series of meetings on the same topic (e.g., weekly project syncs) where users want continuity of context. Action items from meeting N should be tracked into meeting N+1 ("last time we agreed to do X -- this time we confirmed it's done"). Each linked meeting keeps its own transcript and summary, but the summarize step receives prior meetings' summaries and action items as context, enabling Bedrock Claude to produce cross-session tracking.

## Options Considered

### Option 1: Multi-AudioKey Model (file-level parallelism, transcript-level merge)

Extend the Meeting model to store multiple audio keys. Each file is transcribed independently. Transcripts are merged chronologically at the summarize stage.

**Data model changes:**
- Add `AudioKeys []string` (DynamoDB `audioKeys: L`) alongside existing `AudioKey`
- Add `AudioPartCount int` and `AudioPartsCompleted int` for tracking
- Backward compatible: existing single-file meetings keep `AudioKey`; new multi-file meetings use `AudioKeys`

**Upload flow:**
1. Frontend allows selecting/dragging multiple files (sorted by name or user reorder)
2. Each file gets its own presigned URL and uploads independently to `audio/{userId}/{meetingId}/part_{N}_{filename}`
3. `CompleteUpload` appends to `AudioKeys` list and increments `AudioPartsCompleted`
4. Status stays `transcribing` until all parts are complete

**Transcription:**
- Each S3 put triggers the existing EventBridge rule
- Transcribe Lambda runs independently per file
- Output goes to `transcripts/{meetingId}_part_{N}.json`

**Merge at summarize stage:**
- A new EventBridge rule or DynamoDB stream detects when `AudioPartsCompleted == AudioPartCount`
- Summarize Lambda loads all part transcripts, sorts by part index, concatenates segments with adjusted timestamps (part N offsets = sum of durations of parts 0..N-1)
- Proceeds with normal summary generation on the merged transcript

**Audio playback:**
- Frontend audio player plays files sequentially (gapless queue) or offers a per-file selector

- **Pros**: Each file gets optimal Whisper context window; parallel transcription is fast; existing EventBridge pipeline reused; per-file progress tracking; naturally supports "add more audio later"
- **Cons**: Coordination logic for "all parts done" adds complexity; timestamp merge requires duration detection; DynamoDB list append needs conditional expressions for concurrency

### Option 2: Server-Side File Concatenation (ffmpeg merge, single pipeline)

Upload all files to S3, then a Lambda or ECS task concatenates them into one file using ffmpeg before the existing single-file pipeline runs.

**Flow:**
1. Frontend uploads multiple files to `audio/{userId}/{meetingId}/parts/`
2. After all uploads complete, frontend calls a new `POST /api/upload/merge` endpoint
3. Backend launches an ECS task (or Lambda with ffmpeg layer) that runs `ffmpeg -i "concat:part1|part2|..." -c copy merged.webm`
4. The merged file is written to the standard `audio/{userId}/{meetingId}/merged_{timestamp}.ext`
5. EventBridge picks it up and the existing pipeline runs unmodified

- **Pros**: Zero changes to transcribe/summarize pipeline; single transcript, single audio player URL; conceptually simple
- **Cons**: ffmpeg Lambda layer is ~50MB and has 15-min/10GB limits; ECS adds cold-start latency for a simple concat; re-encoding may be needed if formats differ; doubles S3 storage (parts + merged); blocks on the slowest upload before any processing can start; fragile for mixed codecs (webm + m4a + wav)

### Option 3: Frontend-Side Merge (Web Audio API concat before upload)

Use the Web Audio API to decode and concatenate all audio files in the browser, then upload a single merged blob.

- **Pros**: No backend changes at all; existing pipeline untouched
- **Cons**: Browser memory limits (~2GB for Web Audio buffers) make this impractical for long meetings; decoding + re-encoding is slow on mobile; lossy re-encoding degrades quality; blocks the UI during processing; crashes on files >30 minutes total

## Decision

**Option 1: Multi-AudioKey Model** is chosen.

Rationale:
1. **Whisper performs better on bounded segments.** Whisper's context window is ~30 seconds; feeding it a 2-hour concatenated file doesn't improve accuracy over processing shorter segments independently. Split files are actually closer to optimal input size.
2. **Parallel transcription reduces wall-clock time.** Three 20-minute files can be transcribed simultaneously on three ECS tasks instead of sequentially on one 60-minute file.
3. **No codec/format constraints.** Users can upload a mix of .webm, .m4a, and .wav; each is transcribed independently without needing format-compatible concatenation.
4. **Incremental progress.** The frontend can show per-file upload progress and per-part transcription status, giving users immediate feedback.
5. **Foundation for meeting merge.** If users later want to merge two separate meetings, the multi-key model already supports appending audio keys from a source meeting to a target meeting.

The transcript merge logic at the summarize stage is straightforward: concatenate segments sorted by part index, with timestamp offsets computed from audio duration metadata stored alongside each part.

For linked meetings, the `Meeting` model gains a `LinkedMeetingIDs []string` field (ordered chronologically). When the summarize Lambda processes a meeting with linked predecessors, it fetches their summaries and action items and includes them in the Bedrock prompt as prior context. This enables outputs like "Action item from 2026-05-06 meeting: 'migrate auth to v2' -- status: completed (confirmed in this meeting)."

## Implementation Plan

### Phase 1: Backend Model + Upload API ✅ (Done)
- `AudioKeys`, `AudioPartCount`, `AudioPartsReady` added to `Meeting` model (`model/meeting.go`)
- `GetEffectiveAudioKeys()` backward-compat helper returns `AudioKeys` or falls back to `[AudioKey]`
- `CompleteUpload` supports `partIndex`/`totalParts` for multi-file list-append semantics
- `PreAllocateAudioKeys`, `SetAudioKeyAtIndex`, `IncrementAudioPartsReady` repo methods with DynamoDB conditional expressions
- `GetAudioURL` handler returns `audioUrls []string` for multi-file meetings
- Presigned URL endpoint accepts `partIndex`/`totalParts` and generates `part_{NNN}_{filename}` S3 keys

### Phase 2: Transcribe Lambda ✅ (Done)
- Detects part index from S3 key pattern `part_{NNN}_{filename}`
- Writes output to `transcripts/{meetingId}_part_{NNN}.json`
- Unicode NFC normalization for Korean filenames in ECS env vars (fix: 2026-05-26)
- **Gap**: Does not yet increment `AudioPartsReady` or emit "all parts done" event

### Phase 3: Summarize Lambda (Merge Logic) — Not Started
- New handler for "all parts ready" event
- Load all `transcripts/{meetingId}_part_*.json`, sort by part index
- Compute timestamp offsets from audio duration (stored in part transcript metadata or S3 object metadata)
- Concatenate segments, write merged `transcripts/{meetingId}.json`
- Proceed with existing summary pipeline

### Phase 4: Frontend Multi-File Upload ✅ (Done — 2026-05-26)
- `AudioUploader` accepts `multiple` files with drag-and-drop reordering
- Per-file progress bars with parallel upload
- File list with remove/reorder before upload start
- `uploadsApi` passes `partIndex`/`totalParts` to presigned URL and complete endpoints
- `MeetingDetail` TypeScript type includes `audioKeys`, `audioPartCount`, `audioPartsReady`
- Max 10 files, 500MB each

### Phase 5: Audio Playback ✅ (Done — 2026-05-26)
- `AudioPlayer` accepts `audioUrls?: string[]` for sequential multi-track playback
- Auto-advances to next track on ended; part selector buttons for multi-track
- Skip previous/next track controls shown in multi-track mode
- Backward compatible: single `audioUrl` prop still works

### Phase 6: Linked Follow-Up Meetings
- Add `LinkedMeetingIDs []string` to `Meeting` model (DynamoDB `linkedMeetingIds: L`)
- Add `POST /api/meetings/{meetingId}/link` endpoint to link a meeting to predecessors
- Frontend: meeting creation can select "follow-up of" from recent meetings; meeting detail shows linked chain
- Summarize Lambda: when `LinkedMeetingIDs` is non-empty, fetch predecessor summaries and action items, include as context in Bedrock prompt
- Action item tracking: Bedrock marks prior action items as "completed", "in progress", or "carried over" based on transcript content
- Meeting detail UI: show linked meeting chain with navigation between sessions

## Consequences

### Positive
- Users can upload split recordings without external tooling
- Parallel transcription reduces total processing time for multi-file uploads
- Architecture naturally extends to "meeting merge" (append audio keys from another meeting)
- Backward compatible: existing single-file meetings work without changes
- Linked meetings enable cross-session action item tracking and continuity
- Bedrock Claude receives richer context, producing more actionable summaries that reference prior decisions

### Negative
- Coordination complexity for "all parts done" detection (mitigated by DynamoDB atomic counters)
- Transcript timestamp merge requires audio duration metadata (requires Whisper/Transcribe to emit duration in output)
- Frontend audio player complexity increases (sequential playback, part switching)
- DynamoDB list-append operations need conditional expressions to prevent concurrent overwrites
- Linked meeting context increases Bedrock prompt size and cost (mitigated by passing only summaries and action items, not full transcripts)

## References
- Current upload flow: `backend/internal/service/upload.go`
- Meeting model: `backend/internal/model/meeting.go`
- Transcribe Lambda: `backend/cmd/transcribe/main.go`
- Summarize Lambda: `backend/cmd/summarize/main.go`
- Frontend uploader: `frontend/src/components/AudioUploader.tsx`
- Related: ADR-008 (custom dictionary for STT) - vocabulary applies per-user across all parts
- Related: ADR-009 (Whisper GPU ECS Spot) - each part can be a separate ECS task

---

<a id="korean"></a>

# 한국어

## 상태
채택됨 — Phase 1, 2, 4, 5 구현 완료 (2026-05-26). Phase 3 (요약 병합)과 Phase 6 (후속 미팅 링크)은 미구현.

## 배경

사용자가 미팅 녹음 파일이 여러 개로 나뉘는 경우가 있습니다:

- 녹음 소프트웨어가 미팅 중 크래시되거나 재시작하는 경우
- 시스템 오디오 캡처(Mac 앱)가 권한 리셋으로 스트림이 끊기는 경우
- 사용자가 의도적으로 일시정지/재개하여 별도 파일이 생성되는 경우
- 모바일이나 외부 녹음기가 청크 단위(예: 30분 WAV 세그먼트)로 출력하는 경우

현재 TTOBAK은 **미팅당 오디오 파일 1개**만 지원합니다:
- `Meeting.AudioKey`는 DynamoDB에서 단일 `string` 필드입니다
- `CompleteUpload`는 호출할 때마다 `audioKey`를 덮어써서 마지막 업로드 파일만 유지됩니다
- Transcribe Lambda는 호출당 S3 키 1개만 처리합니다
- Summarize Lambda는 `transcripts/{meetingId}.json` 하나의 트랜스크립트 파일을 기대합니다
- 프런트엔드 `AudioUploader`는 단일 파일만 받고 오디오 플레이어는 단일 URL만 스트리밍합니다

이 제한으로 사용자는 업로드 전에 외부 도구(ffmpeg 등)로 파일을 수동 병합하거나, 별도 미팅을 만들어 하나의 대화 흐름의 연속성을 잃게 됩니다. 두 경우 모두 좋은 UX가 아닙니다.

이 ADR은 이전에 논의된 "미팅 합치기" 개념도 포함합니다. 완전히 처리된 두 미팅을 병합하는 것(별도의 화자 맵, 요약, 액션 아이템을 조정해야 함)보다, 처리 전 업로드 단계에서 단일 미팅에 여러 오디오 입력을 지원하는 것이 더 깔끔한 해결책입니다.

밀접하게 관련된 시나리오로 **후속 미팅 링크**가 있습니다: 같은 주제의 연속 미팅(예: 주간 프로젝트 동기화)에서 사용자가 맥락의 연속성을 원하는 경우입니다. 미팅 N의 액션 아이템이 미팅 N+1에서 추적되어야 합니다 ("지난번에 X를 하기로 했는데, 이번에 완료를 확인함"). 각 링크된 미팅은 자체 트랜스크립트와 요약을 유지하지만, 요약 단계에서 이전 미팅의 요약과 액션 아이템을 컨텍스트로 받아 Bedrock Claude가 세션 간 추적을 생성할 수 있습니다.

## 검토한 옵션

### 옵션 1: Multi-AudioKey 모델 (파일 단위 병렬 처리, 트랜스크립트 단위 병합)

Meeting 모델을 확장하여 여러 오디오 키를 저장합니다. 각 파일은 독립적으로 트랜스크립션됩니다. 트랜스크립트는 Summarize 단계에서 시간순으로 병합됩니다.

**데이터 모델 변경:**
- 기존 `AudioKey` 옆에 `AudioKeys []string` (DynamoDB `audioKeys: L`) 추가
- 추적을 위해 `AudioPartCount int`와 `AudioPartsCompleted int` 추가
- 하위 호환: 기존 단일 파일 미팅은 `AudioKey` 유지; 새 멀티파일 미팅은 `AudioKeys` 사용

**업로드 흐름:**
1. 프런트엔드에서 여러 파일 선택/드래그 허용 (이름순 정렬 또는 사용자 재정렬)
2. 각 파일은 개별 presigned URL을 받아 `audio/{userId}/{meetingId}/part_{N}_{filename}`에 독립 업로드
3. `CompleteUpload`가 `AudioKeys` 리스트에 추가하고 `AudioPartsCompleted` 증가
4. 모든 파트가 완료될 때까지 상태는 `transcribing` 유지

**트랜스크립션:**
- 각 S3 put이 기존 EventBridge 규칙을 트리거
- Transcribe Lambda가 파일별 독립 실행
- 출력은 `transcripts/{meetingId}_part_{N}.json`으로 저장

**Summarize 단계 병합:**
- 새 EventBridge 규칙 또는 DynamoDB 스트림이 `AudioPartsCompleted == AudioPartCount`를 감지
- Summarize Lambda가 모든 파트 트랜스크립트를 로드하고, 파트 인덱스로 정렬하고, 조정된 타임스탬프(파트 N 오프셋 = 파트 0..N-1 길이의 합)로 세그먼트를 연결
- 병합된 트랜스크립트로 기존 요약 생성 진행

**오디오 재생:**
- 프런트엔드 오디오 플레이어가 파일을 순차 재생(갭 없는 큐) 또는 파일별 선택기 제공

- **장점**: 각 파일이 최적의 Whisper 컨텍스트 윈도우를 받음; 병렬 트랜스크립션으로 빠름; 기존 EventBridge 파이프라인 재사용; 파일별 진행 상태 추적; "나중에 오디오 추가"를 자연스럽게 지원
- **단점**: "모든 파트 완료" 조율 로직이 복잡성 추가; 타임스탬프 병합에 길이 감지 필요; DynamoDB 리스트 추가에 동시성을 위한 조건부 표현식 필요

### 옵션 2: 서버 측 파일 결합 (ffmpeg 병합, 단일 파이프라인)

모든 파일을 S3에 업로드한 후 Lambda 또는 ECS 태스크가 ffmpeg로 파일을 결합하고 기존 단일 파일 파이프라인을 실행합니다.

- **장점**: 트랜스크립/서마라이즈 파이프라인 변경 없음; 단일 트랜스크립트, 단일 오디오 플레이어 URL; 개념적으로 단순
- **단점**: ffmpeg Lambda 레이어 약 50MB에 15분/10GB 제한; ECS는 단순 결합에 콜드스타트 지연 추가; 포맷이 다르면 재인코딩 필요; S3 스토리지 2배(파트 + 병합본); 처리 시작 전 가장 느린 업로드를 기다려야 함; 혼합 코덱(webm + m4a + wav)에 취약

### 옵션 3: 프런트엔드 측 병합 (Web Audio API로 업로드 전 결합)

Web Audio API로 브라우저에서 모든 오디오 파일을 디코딩하고 결합한 후 단일 blob으로 업로드합니다.

- **장점**: 백엔드 변경 없음; 기존 파이프라인 그대로
- **단점**: 브라우저 메모리 제한(Web Audio 버퍼 약 2GB)으로 긴 미팅에 비실용적; 모바일에서 디코딩+재인코딩이 느림; 손실 재인코딩으로 품질 저하; 처리 중 UI 블로킹; 총 30분 이상 파일에서 크래시

## 결정

**옵션 1: Multi-AudioKey 모델**을 선택합니다.

근거:
1. **Whisper는 제한된 세그먼트에서 더 좋은 성능을 보입니다.** Whisper의 컨텍스트 윈도우는 약 30초이므로, 2시간 결합 파일을 제공해도 짧은 세그먼트를 독립 처리하는 것보다 정확도가 향상되지 않습니다. 분할된 파일이 오히려 최적 입력 크기에 가깝습니다.
2. **병렬 트랜스크립션으로 실제 처리 시간이 단축됩니다.** 세 개의 20분 파일을 세 개의 ECS 태스크에서 동시에 처리하면, 하나의 60분 파일을 순차 처리하는 것보다 빠릅니다.
3. **코덱/포맷 제약이 없습니다.** 사용자가 .webm, .m4a, .wav를 혼합 업로드할 수 있으며, 각각 포맷 호환 결합 없이 독립 트랜스크립션됩니다.
4. **점진적 진행 상태.** 프런트엔드에서 파일별 업로드 진행률과 파트별 트랜스크립션 상태를 보여줄 수 있어 사용자에게 즉각적인 피드백을 제공합니다.
5. **미팅 합치기의 기반.** 나중에 사용자가 두 개의 별도 미팅을 합치고 싶을 때, 멀티키 모델은 이미 소스 미팅의 오디오 키를 타겟 미팅에 추가하는 것을 지원합니다.

Summarize 단계의 트랜스크립트 병합 로직은 간단합니다: 파트 인덱스로 정렬된 세그먼트를 연결하되, 각 파트와 함께 저장된 오디오 길이 메타데이터에서 계산된 타임스탬프 오프셋을 적용합니다.

후속 미팅 링크의 경우, `Meeting` 모델에 `LinkedMeetingIDs []string` 필드가 추가됩니다(시간순 정렬). Summarize Lambda가 링크된 이전 미팅이 있는 미팅을 처리할 때, 이전 미팅의 요약과 액션 아이템을 가져와 Bedrock 프롬프트에 사전 컨텍스트로 포함합니다. 이를 통해 "2026-05-06 미팅 액션 아이템: 'auth를 v2로 마이그레이션' -- 상태: 완료 (이번 미팅에서 확인)"과 같은 출력이 가능합니다.

## 구현 계획

### Phase 1: 백엔드 모델 + 업로드 API ✅ (완료)
- `Meeting` 모델에 `AudioKeys`, `AudioPartCount`, `AudioPartsReady` 추가 (`model/meeting.go`)
- `GetEffectiveAudioKeys()` 하위 호환 헬퍼가 `AudioKeys` 또는 `[AudioKey]` 반환
- `CompleteUpload`가 `partIndex`/`totalParts` 지원하여 리스트 추가 의미론 구현
- `PreAllocateAudioKeys`, `SetAudioKeyAtIndex`, `IncrementAudioPartsReady` 리포지토리 메서드 (DynamoDB 조건부 표현식 포함)
- `GetAudioURL` 핸들러가 멀티파일 미팅에 `audioUrls []string` 반환
- Presigned URL 엔드포인트가 `partIndex`/`totalParts` 수신하여 `part_{NNN}_{filename}` S3 키 생성

### Phase 2: Transcribe Lambda ✅ (완료)
- S3 키 패턴 `part_{NNN}_{filename}`에서 파트 인덱스 감지
- 출력을 `transcripts/{meetingId}_part_{NNN}.json`으로 작성
- 한국어 파일명에 대한 Unicode NFC 정규화 (수정: 2026-05-26)
- **미완**: `AudioPartsReady` 증가 및 "모든 파트 완료" 이벤트 발행 미구현

### Phase 3: Summarize Lambda (병합 로직) — 미시작
- "모든 파트 준비 완료" 이벤트를 위한 새 핸들러
- 모든 `transcripts/{meetingId}_part_*.json` 로드, 파트 인덱스로 정렬
- 오디오 길이로 타임스탬프 오프셋 계산
- 세그먼트 연결, 병합된 `transcripts/{meetingId}.json` 작성
- 기존 요약 파이프라인 실행

### Phase 4: 프런트엔드 멀티파일 업로드 ✅ (완료 — 2026-05-26)
- `AudioUploader`가 드래그앤드롭 재정렬로 `multiple` 파일 허용
- 파일별 프로그레스 바, 병렬 업로드
- 업로드 전 파일 목록에서 제거/재정렬 가능
- `uploadsApi`가 `partIndex`/`totalParts`를 presigned URL 및 complete 엔드포인트에 전달
- `MeetingDetail` TypeScript 타입에 `audioKeys`, `audioPartCount`, `audioPartsReady` 추가
- 최대 10개 파일, 각 500MB

### Phase 5: 오디오 재생 ✅ (완료 — 2026-05-26)
- `AudioPlayer`가 `audioUrls?: string[]`로 순차 멀티트랙 재생 지원
- 트랙 종료 시 자동 다음 트랙 재생; 멀티트랙용 파트 선택 버튼
- 멀티트랙 모드에서 이전/다음 트랙 건너뛰기 컨트롤 표시
- 하위 호환: 단일 `audioUrl` prop도 동작

### Phase 6: 후속 미팅 링크
- `Meeting` 모델에 `LinkedMeetingIDs []string` 추가 (DynamoDB `linkedMeetingIds: L`)
- `POST /api/meetings/{meetingId}/link` 엔드포인트 추가하여 이전 미팅에 링크
- 프런트엔드: 미팅 생성 시 최근 미팅에서 "후속 미팅" 선택 가능; 미팅 상세에서 링크된 체인 표시
- Summarize Lambda: `LinkedMeetingIDs`가 비어있지 않으면 이전 미팅의 요약과 액션 아이템을 가져와 Bedrock 프롬프트에 컨텍스트로 포함
- 액션 아이템 추적: Bedrock이 트랜스크립트 내용에 기반하여 이전 액션 아이템을 "완료", "진행 중", "이월"로 표시
- 미팅 상세 UI: 세션 간 탐색이 가능한 링크된 미팅 체인 표시

## 영향

### 긍정적
- 사용자가 외부 도구 없이 분할된 녹음을 업로드할 수 있습니다
- 병렬 트랜스크립션으로 멀티파일 업로드의 총 처리 시간이 단축됩니다
- 아키텍처가 "미팅 합치기"로 자연스럽게 확장됩니다 (다른 미팅의 오디오 키를 추가)
- 하위 호환: 기존 단일 파일 미팅이 변경 없이 작동합니다
- 링크된 미팅이 세션 간 액션 아이템 추적과 연속성을 가능하게 합니다
- Bedrock Claude가 더 풍부한 컨텍스트를 받아 이전 결정을 참조하는 더 실행 가능한 요약을 생성합니다

### 부정적
- "모든 파트 완료" 감지를 위한 조율 복잡성 (DynamoDB 원자적 카운터로 완화)
- 트랜스크립트 타임스탬프 병합에 오디오 길이 메타데이터 필요 (Whisper/Transcribe가 출력에 길이를 포함해야 함)
- 프런트엔드 오디오 플레이어 복잡성 증가 (순차 재생, 파트 전환)
- DynamoDB 리스트 추가 연산에 동시 덮어쓰기 방지를 위한 조건부 표현식 필요
- 링크된 미팅 컨텍스트가 Bedrock 프롬프트 크기와 비용을 증가시킵니다 (요약과 액션 아이템만 전달하고 전체 트랜스크립트는 제외하여 완화)

## 참고 자료
- 현재 업로드 흐름: `backend/internal/service/upload.go`
- Meeting 모델: `backend/internal/model/meeting.go`
- Transcribe Lambda: `backend/cmd/transcribe/main.go`
- Summarize Lambda: `backend/cmd/summarize/main.go`
- 프런트엔드 업로더: `frontend/src/components/AudioUploader.tsx`
- 관련: ADR-008 (STT 정확도를 위한 사용자 사전) - 어휘가 모든 파트에 사용자별로 적용
- 관련: ADR-009 (Whisper GPU ECS Spot) - 각 파트가 별도 ECS 태스크로 실행 가능
