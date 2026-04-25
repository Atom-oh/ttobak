# Ttobak - API Specification

> Backend REST API 상세 명세

## Base URL

```
Production: https://{cloudfront-domain}/api
Local Dev:  http://localhost:8080/api
```

## Authentication

모든 API 요청은 Cognito JWT를 필요로 합니다.

- Lambda@Edge가 CloudFront Viewer Request에서 JWT 검증
- API Gateway HTTP API: Lambda@Edge 통과 후 Lambda 직접 호출
- API Gateway WebSocket API: Cognito Authorizer로 $connect 시 인증
- Backend Lambda는 요청 컨텍스트에서 `sub` (userId)를 추출하여 사용
- 프론트엔드에서 직접 호출 시: `Authorization: Bearer {idToken}` 헤더 사용

## Endpoints

### Health Check

```
GET /api/health
Response: 200 OK
{
  "status": "ok",
  "timestamp": "2026-03-05T12:00:00Z"
}
```

---

### Meetings

#### List Meetings

```
GET /api/meetings?tab={all|shared}&cursor={lastKey}&limit={20}

Response: 200 OK
{
  "meetings": [
    {
      "meetingId": "uuid",
      "title": "Product Strategy Sync",
      "date": "2026-03-05T10:00:00Z",
      "status": "done",           // recording | transcribing | summarizing | done | error
      "summary": "AI 요약 미리보기 (첫 200자)...",
      "participants": ["Alice", "Bob"],
      "tags": ["Internal"],
      "isShared": false,          // true if this is a shared meeting
      "sharedBy": null,           // owner email if shared
      "permission": null,         // "read" | "edit" if shared
      "createdAt": "2026-03-05T10:00:00Z",
      "updatedAt": "2026-03-05T11:30:00Z"
    }
  ],
  "nextCursor": "base64-encoded-lastEvaluatedKey or null"
}
```

#### Create Meeting

```
POST /api/meetings
Request:
{
  "title": "New Meeting",
  "date": "2026-03-05T10:00:00Z",
  "participants": ["Alice", "Bob"]
}

Response: 201 Created
{
  "meetingId": "uuid",
  "title": "New Meeting",
  "date": "2026-03-05T10:00:00Z",
  "status": "recording",
  "participants": ["Alice", "Bob"],
  "content": "",
  "createdAt": "2026-03-05T10:00:00Z"
}
```

#### Get Meeting Detail

```
GET /api/meetings/{meetingId}

Response: 200 OK
{
  "meetingId": "uuid",
  "userId": "owner-uuid",
  "title": "Product Strategy Sync",
  "date": "2026-03-05T10:00:00Z",
  "status": "done",
  "participants": ["Alice", "Bob", "Charlie"],
  "content": "# 회의록\n\n## 안건\n...",     // Markdown
  "transcriptA": "Transcribe 결과 전체 텍스트...",
  "transcriptB": "Nova 2 Sonic 결과 전체 텍스트...",
  "selectedTranscript": "A",                    // "A" | "B" | null
  "audioKey": "audio/user-uuid/meeting-uuid.webm",
  "attachments": [
    {
      "attachmentId": "uuid",
      "originalKey": "images/user-uuid/photo1.jpg",
      "processedKey": "processed/user-uuid/photo1-mermaid.md",
      "type": "diagram",                        // photo | screenshot | diagram | whiteboard
      "status": "done",                         // uploaded | processing | done
      "description": "시스템 아키텍처 다이어그램",
      "processedContent": "```mermaid\ngraph TD\n...\n```"
    }
  ],
  "shares": [                                   // Only visible to owner
    {
      "userId": "shared-user-uuid",
      "email": "bob@example.com",
      "permission": "read"
    }
  ],
  "createdAt": "2026-03-05T10:00:00Z",
  "updatedAt": "2026-03-05T11:30:00Z"
}

Error: 403 Forbidden (if not owner and not shared)
Error: 404 Not Found
```

#### Update Meeting

```
PUT /api/meetings/{meetingId}
Request:
{
  "title": "Updated Title",                     // optional
  "content": "# Updated markdown...",           // optional
  "selectedTranscript": "B",                    // optional
  "participants": ["Alice", "Bob", "David"],    // optional
  "status": "done"                              // optional
}

Response: 200 OK
{ "meetingId": "uuid", "updatedAt": "..." }

Error: 403 Forbidden (shared users with "read" permission cannot edit)
```

#### Delete Meeting

```
DELETE /api/meetings/{meetingId}

Response: 204 No Content
Error: 403 Forbidden (only owner can delete)
```

---

### Sharing

#### Share Meeting

```
POST /api/meetings/{meetingId}/share
Request:
{
  "email": "bob@example.com",
  "permission": "read"          // "read" | "edit"
}

Response: 200 OK
{
  "sharedWith": {
    "userId": "uuid",
    "email": "bob@example.com",
    "permission": "read"
  }
}

Error: 403 Forbidden (only owner can share)
Error: 404 User not found
```

#### Revoke Share

```
DELETE /api/meetings/{meetingId}/share/{userId}

Response: 204 No Content
Error: 403 Forbidden (only owner can revoke)
```

#### Search Users (for sharing)

```
GET /api/users/search?q={email-prefix}

Response: 200 OK
{
  "users": [
    {
      "userId": "uuid",
      "email": "bob@example.com",
      "name": "Bob Kim"
    }
  ]
}
```

---

### Upload

#### Get Presigned URL

```
POST /api/upload/presigned
Request:
{
  "fileName": "recording.webm",
  "fileType": "audio/webm",         // audio/webm | audio/mp4 | audio/x-m4a | image/jpeg | image/png
  "category": "audio"               // "audio" | "image"
}

Response: 200 OK
{
  "uploadUrl": "https://s3.amazonaws.com/bucket/...",
  "key": "audio/user-uuid/meeting-uuid/recording.webm",
  "expiresIn": 3600
}
```

#### Notify Upload Complete

```
POST /api/upload/complete
Request:
{
  "meetingId": "uuid",
  "key": "audio/user-uuid/meeting-uuid/recording.webm",
  "category": "audio"               // "audio" | "image"
}

Response: 200 OK
{
  "status": "processing"
}
```

---

### Real-time Translation (REST)

> 현재 구현: WebSocket 대신 Browser Speech API + REST 호출 방식으로 실시간 전사/번역 구현

#### Translate Text

```
POST /api/translate
Request:
{
  "text": "번역할 텍스트",
  "sourceLang": "ko",
  "targetLang": "en"
}

Response: 200 OK
{
  "translatedText": "Text to translate",
  "sourceLang": "ko",
  "targetLang": "en"
}
```

#### Live Summary (200단어마다 호출)

```
POST /api/summarize-live
Request:
{
  "meetingId": "client-meeting-id",
  "text": "전체 전사 텍스트...",
  "previousSummary": "이전 요약 (optional)"
}

Response: 200 OK
{
  "summary": "현재까지 요약된 내용..."
}
```

---

### STT Results

#### Select Transcript

```
PUT /api/meetings/{meetingId}/transcript
Request:
{
  "selected": "A"                   // "A" | "B"
}

Response: 200 OK
```

---

### WebSocket (API Gateway) — 미구현

> **현재 상태**: 실시간 전사는 Browser Web Speech API (`BrowserSpeechRecognition`)로 클라이언트에서 처리하고, 번역/요약은 REST API 호출. WebSocket 기반 Nova Sonic 스트리밍은 v2 목표.

실시간 전사 및 번역을 위한 WebSocket API입니다.

```
Endpoint: wss://{apigw-domain}/realtime

Connection: $connect with Authorization header (Cognito JWT)

Client → Server Messages:

1. Start Session
{
  "action": "start",
  "meetingId": "uuid",
  "language": "ko-KR",              // source language
  "targetLangs": ["en-US", "ja-JP"] // optional translation targets
}

2. Audio Chunk
{
  "action": "audio",
  "data": "base64-encoded-audio-chunk"
}

3. Stop Session
{
  "action": "stop"
}

Server → Client Messages:

1. Transcript Result
{
  "type": "transcript",
  "text": "전사된 텍스트",
  "isFinal": true,                  // false for interim results
  "timestamp": "2026-03-05T10:00:00Z",
  "speaker": "Speaker 1"            // optional speaker diarization
}

2. Translation Result
{
  "type": "translation",
  "text": "Translated text",
  "targetLang": "en-US",
  "timestamp": "2026-03-05T10:00:00Z"
}

3. Error
{
  "type": "error",
  "code": "STREAMING_ERROR",
  "message": "Nova Sonic connection failed"
}
```

---

### Q&A (Knowledge Base RAG)

#### Ask Question

```
POST /api/meetings/{meetingId}/ask
Request:
{
  "question": "이 회의에서 결정된 마감일은 언제인가요?",
  "includeKB": true                 // true: global KB 포함, false: 현재 회의만
}

Response: 200 OK
{
  "answer": "마감일은 3월 15일로 결정되었습니다.",
  "sources": [
    {
      "type": "meeting",            // "meeting" | "kb"
      "meetingId": "uuid",
      "title": "Product Strategy Sync",
      "excerpt": "...마감일을 3월 15일로 확정...",
      "relevanceScore": 0.95
    },
    {
      "type": "kb",
      "fileId": "uuid",
      "fileName": "project-timeline.pdf",
      "excerpt": "...Phase 2 deadline: March 15...",
      "relevanceScore": 0.82
    }
  ],
  "questionId": "uuid"
}
```

---

### Knowledge Base

#### Upload KB File (Get Presigned URL)

```
POST /api/kb/upload
Request:
{
  "fileName": "project-spec.pdf",
  "fileType": "application/pdf",    // pdf | md | pptx | docx
  "fileSize": 1048576               // bytes
}

Response: 200 OK
{
  "uploadUrl": "https://s3.amazonaws.com/...",
  "fileId": "uuid",
  "key": "kb/{userId}/{fileId}/project-spec.pdf",
  "expiresIn": 3600
}
```

#### Sync KB Index

```
POST /api/kb/sync
Request:
{
  "fileId": "uuid"                  // optional: specific file, omit for full sync
}

Response: 202 Accepted
{
  "syncJobId": "uuid",
  "status": "indexing"
}
```

#### List KB Files

```
GET /api/kb/files?cursor={lastKey}&limit={20}

Response: 200 OK
{
  "files": [
    {
      "fileId": "uuid",
      "fileName": "project-spec.pdf",
      "fileType": "application/pdf",
      "fileSize": 1048576,
      "status": "indexed",          // uploading | indexing | indexed | error
      "createdAt": "2026-03-05T10:00:00Z",
      "updatedAt": "2026-03-05T10:05:00Z"
    }
  ],
  "nextCursor": "base64-encoded-lastEvaluatedKey or null"
}
```

#### Delete KB File

```
DELETE /api/kb/files/{fileId}

Response: 204 No Content
```

---

### Export

#### Export Meeting

```
POST /api/meetings/{meetingId}/export
Request:
{
  "format": "pdf"                   // "pdf" | "markdown" | "notion" | "obsidian"
}

Response (PDF/Markdown/Obsidian): 200 OK
{
  "url": "https://s3.presigned-url...",
  "fileName": "meeting-2026-03-05.pdf",
  "expiresIn": 3600
}

Response (Notion): 200 OK
{
  "notionPageId": "abc123",
  "notionUrl": "https://notion.so/abc123"
}

Error: 400 Bad Request (if Notion API key not configured)
{
  "error": {
    "code": "INTEGRATION_NOT_CONFIGURED",
    "message": "Notion API key not configured. Please add it in Settings."
  }
}
```

#### Get Obsidian Export (Direct Download)

```
GET /api/meetings/{meetingId}/export/obsidian

Response: 200 OK
{
  "filename": "Product-Strategy-Sync-2026-03-05.md",
  "content": "---\ntitle: Product Strategy Sync\ndate: 2026-03-05\nparticipants:\n  - Alice\n  - Bob\ntags:\n  - internal\n  - strategy\nstatus: done\nrelated:\n  - \"[[Weekly Team Standup 2026-03-04]]\"\n  - \"[[Q1 Planning 2026-02-28]]\"\n---\n\n# Product Strategy Sync\n\n## Summary\n...\n\n## Action Items\n- [ ] Task 1\n- [ ] Task 2\n\n## Backlinks\n- [[Weekly Team Standup 2026-03-04]]\n- [[Q1 Planning 2026-02-28]]\n"
}
```

**Obsidian Export Format:**
- YAML frontmatter: title, date, participants, tags, status, related
- `[[wikilinks]]` to other meetings by title for cross-referencing
- Backlinks section at the end for building knowledge graph in Obsidian vaults

---

### Integration Settings

#### Get Integration Settings

```
GET /api/settings/integrations

Response: 200 OK
{
  "notion": {
    "configured": true,
    "maskedKey": "ntn_****abcd",    // last 4 chars visible
    "connectedAt": "2026-03-05T10:00:00Z"
  },
  "obsidian": {
    "configured": false
  }
}
```

#### Configure Notion Integration

```
PUT /api/settings/integrations/notion
Request:
{
  "apiKey": "ntn_xxxxxxxxxxxx"
}

Response: 200 OK
{
  "configured": true,
  "maskedKey": "ntn_****xxxx",
  "connectedAt": "2026-03-05T10:00:00Z"
}
```

#### Remove Notion Integration

```
DELETE /api/settings/integrations/notion

Response: 204 No Content
```

---

## Error Response Format

```json
{
  "error": {
    "code": "UNAUTHORIZED",
    "message": "Authentication required"
  }
}
```

| HTTP Status | Code | Description |
|-------------|------|-------------|
| 400 | BAD_REQUEST | 잘못된 요청 파라미터 |
| 401 | UNAUTHORIZED | 인증 필요 |
| 403 | FORBIDDEN | 권한 없음 (소유권/공유 권한) |
| 404 | NOT_FOUND | 리소스 없음 |
| 500 | INTERNAL_ERROR | 서버 내부 오류 |

---

## Lambda Functions

### 1. API Lambda (cmd/api)
- **트리거**: API Gateway HTTP API
- **역할**: 모든 REST API 처리
- **라우팅**: Chi Router
- **환경변수**: TABLE_NAME, BUCKET_NAME, COGNITO_USER_POOL_ID, KB_ID

### 2. Transcribe Lambda (cmd/transcribe)
- **트리거**: S3 Event (audio/ prefix) via EventBridge
- **역할**: STT A/B 파이프라인 시작 (오프라인 녹음용)
- **처리**:
  1. S3 이벤트에서 오디오 키 추출
  2. Transcribe StartTranscriptionJob 호출 (결과 A)
  3. Nova 2 Sonic Bidirectional Streaming API 호출 (결과 B)
  4. 결과를 DynamoDB에 저장
  5. 회의 상태를 "transcribing" → "summarizing"으로 업데이트
- **환경변수**: TABLE_NAME, BUCKET_NAME, OUTPUT_BUCKET

### 3. Summarize Lambda (cmd/summarize)
- **트리거**: DynamoDB Stream (status가 "summarizing"으로 변경 시) 또는 직접 호출
- **역할**: Bedrock Claude로 회의록 요약
- **처리**:
  1. 선택된 전사 텍스트 로드
  2. Bedrock Claude Opus 4.6 호출
  3. 구조화된 마크다운 회의록 생성
  4. DynamoDB에 content 저장
  5. 상태를 "done"으로 업데이트
- **환경변수**: TABLE_NAME, BEDROCK_MODEL_ID

### 4. Process Image Lambda (cmd/process-image)
- **트리거**: S3 Event (images/ prefix) via EventBridge
- **역할**: 이미지 분석 + 다이어그램 재생성
- **처리**:
  1. S3에서 이미지 다운로드
  2. Bedrock Claude Vision으로 분류 (architecture/table/whiteboard/photo)
  3. 분류별 처리:
     - architecture → Mermaid 다이어그램 코드
     - table → 마크다운 테이블
     - whiteboard → 텍스트 추출 + 구조화
     - photo → 설명 텍스트
  4. 결과를 S3 (processed/) + DynamoDB에 저장
- **환경변수**: TABLE_NAME, BUCKET_NAME, BEDROCK_MODEL_ID

### 5. WebSocket Lambda (cmd/realtime)
- **트리거**: API Gateway WebSocket API ($connect, $disconnect, $default)
- **역할**: 실시간 전사 + 번역 스트리밍
- **처리**:
  1. $connect: Cognito JWT 검증, 연결 정보 DynamoDB 저장
  2. start: Nova Sonic v2 스트리밍 세션 시작
  3. audio: 오디오 청크를 Nova Sonic으로 전달
  4. Nova Sonic 결과 수신 → 클라이언트로 transcript 전송
  5. 번역 요청 시 Bedrock Claude로 실시간 번역 → translation 전송
  6. stop/$disconnect: 세션 종료, 전체 전사본 저장
- **환경변수**: TABLE_NAME, CONNECTIONS_TABLE_NAME, NOVA_SONIC_MODEL_ID, BEDROCK_MODEL_ID

### 6. KB Lambda (cmd/kb)
- **트리거**: S3 Event (kb/ prefix) via EventBridge + API Gateway (sync 요청)
- **역할**: Knowledge Base 파일 인덱싱
- **처리**:
  1. S3에서 파일 다운로드 (pdf/md/pptx/docx)
  2. Bedrock Knowledge Base에 문서 추가/업데이트
  3. OpenSearch Serverless 인덱스 업데이트
  4. DynamoDB에 인덱싱 상태 저장
- **환경변수**: TABLE_NAME, BUCKET_NAME, KB_ID, AOSS_ENDPOINT

### 7. Lambda@Edge (cmd/edge-auth, us-east-1)
- **트리거**: CloudFront Viewer Request
- **역할**: Cognito JWT 검증
- **처리**:
  1. Authorization 헤더에서 JWT 추출
  2. Cognito JWKS로 서명 검증
  3. 유효하면 요청 통과, userId를 헤더에 추가
  4. 무효하면 401 응답 또는 로그인 리다이렉트
- **환경변수**: COGNITO_USER_POOL_ID, COGNITO_REGION (us-east-1 배포)

---

## DynamoDB Access Patterns

| 접근 패턴 | Key Condition | Filter |
|-----------|---------------|--------|
| 내 회의 목록 | PK=USER#{userId}, SK begins_with MEETING# | entityType=MEETING |
| 내 회의 날짜순 | GSI1: PK=MEETING#{meetingId}, SK=USER#{userId} | - |
| 회의 상세 | PK=USER#{userId}, SK=MEETING#{meetingId} | - |
| 공유받은 목록 | PK=USER#{userId}, SK begins_with SHARED# | - |
| 첨부파일 목록 | PK=MEETING#{meetingId}, SK begins_with ATTACH# | - |
| 공유 대상 목록 | GSI1: PK=MEETING#{meetingId}, SK begins_with SHARED# | - |
| 사용자 이메일 검색 | GSI2: PK begins_with EMAIL#{emailPrefix} | - |
| 사용자 프로필 | PK=USER#{userId}, SK=PROFILE | - |

### 공유 확인 로직 (meeting detail 접근 시)
```
1. PK=USER#{userId}, SK=MEETING#{meetingId} 조회 → 소유자인 경우 OK
2. 실패 시 PK=USER#{userId}, SK=SHARED#{meetingId} 조회 → 공유 받은 경우 permission 확인
3. 둘 다 실패 → 403 Forbidden
```
