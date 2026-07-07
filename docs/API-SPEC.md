# TTOBAK - API Specification

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

> `content` must be **Markdown**, not HTML. The web editor (TipTap) edits in HTML but converts back to Markdown before saving, because the summary is consumed as Markdown downstream (Notion/Obsidian export). Exporters also normalize any stray HTML to Markdown as a safety net for legacy records.

#### Delete Meeting

```
DELETE /api/meetings/{meetingId}

Response: 204 No Content
Error: 403 Forbidden (only owner can delete)
```

---

### Accounts

고객사(Account)는 팀이 공유하는 1급 엔티티다. 생성자는 자동으로 `owner` 멤버가 되며, owner만 멤버를 추가할 수 있다. 멤버십(역할 AM/TAM/SSA/owner)이 곧 접근 권한이다. 모든 엔드포인트는 인증 필요.

#### List Accounts (내 Account 목록)

```
GET /api/accounts

Response: 200 OK
{
  "accounts": [
    {
      "accountId": "uuid",
      "name": "하나은행",
      "role": "owner"            // owner | AM | TAM | SSA
    }
  ]
}
```

내가 멤버인 Account만 반환한다(GSI1 역조회).

#### Create Account

```
POST /api/accounts
Request:
{
  "name": "하나은행",
  "aliases": ["Hana Bank"],     // optional, 태그 별칭 매핑
  "domains": ["hanafn.com"],    // optional
  "industry": "Finance"         // optional
}

Response: 201 Created
{
  "accountId": "uuid",
  "name": "하나은행",
  "aliases": ["Hana Bank"],
  "domains": ["hanafn.com"],
  "industry": "Finance",
  "ownerUserId": "owner-uuid",
  "members": [
    { "userId": "owner-uuid", "email": "owner@example.com", "role": "owner" }
  ],
  "createdAt": "2026-05-30T10:00:00Z"
}

Error: 400 Bad Request (name이 비어있음)
```

생성자는 자동으로 `owner` 역할의 멤버가 된다.

#### Get Account Detail

```
GET /api/accounts/{accountId}

Response: 200 OK
{
  "accountId": "uuid",
  "name": "하나은행",
  "aliases": ["Hana Bank"],
  "domains": ["hanafn.com"],
  "industry": "Finance",
  "ownerUserId": "owner-uuid",
  "members": [
    { "userId": "owner-uuid", "email": "owner@example.com", "role": "owner" },
    { "userId": "tam-uuid", "email": "tam@example.com", "role": "TAM" }
  ],
  "createdAt": "2026-05-30T10:00:00Z"
}

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### Add Member (owner 전용)

```
POST /api/accounts/{accountId}/members
Request:
{
  "email": "tam@example.com",   // 기존 등록 사용자의 이메일
  "role": "TAM"                 // AM | TAM | SSA (owner는 지정 불가)
}

Response: 201 Created
{
  "userId": "tam-uuid",
  "email": "tam@example.com",
  "role": "TAM"
}

Error: 403 Forbidden (owner가 아님)
Error: 404 Not Found (해당 이메일의 사용자 없음)
Error: 400 Bad Request (이미 멤버이거나 잘못된 역할)
```

#### List Account Meetings (공유된 미팅 목록 — 멤버 전용)

```
GET /api/accounts/{accountId}/meetings

Response: 200 OK
{
  "meetings": [
    {
      "meetingId": "uuid",
      "ownerUserId": "owner-uuid",
      "title": "ROSA 리뷰",
      "date": "2026-05-30T10:00:00Z"
    }
  ]
}

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### List Account Insights (인사이트 raw material — 멤버 전용)

미팅에서 추출되어 Account 파티션에 팬아웃된 8유형 인사이트를 조회한다.
`from`/`to`는 선택적 기간 필터(RFC3339), `types`는 선택적 유형 필터(콤마 구분).
기간·유형 필터는 서비스 레이어에서 client-side로 적용된다(spec §6.3).

유형(type) 8종: `trend`, `need`, `competitive`, `risk`, `opportunity`, `tech`, `stakeholder`, `action`

```
GET /api/accounts/{accountId}/insights?from=<RFC3339>&to=<RFC3339>&types=risk,opportunity

Response: 200 OK
{
  "insights": [
    {
      "type": "risk",
      "text": "PoC 일정 2개월 지연 가능",
      "sourceType": "meeting",
      "sourceId": "meeting-uuid",
      "occurredAt": "2026-05-12T09:00:00Z",
      "tsMarker": "[TS:120]",
      "entities": ["PoC"]
    }
  ]
}

Error: 400 Bad Request (잘못된 from/to — RFC3339 아님)
Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### Get Account Brief (묶음 원재료 — 멤버 전용)

Account 한 곳의 원재료(메타 + 유형별 인사이트 + 공유 미팅)를 한 번의 호출로
묶어서 반환한다. 개인 맥북 에이전트가 SFDC/SIFT/2by2/Player Card 준비에 쓰는
"일괄 소비"용. 기존 `GetAccount`+`ListAccountMeetings`+`ListAccountInsights`를
서비스 레이어에서 합성하며, 멤버 게이트를 그대로 상속한다. `from`/`to`/`types`
필터는 insights 엔드포인트와 동일하게 동작한다.

```
GET /api/accounts/{accountId}/brief?from=<RFC3339>&to=<RFC3339>&types=risk,opportunity

Response: 200 OK
{
  "account": { "accountId": "acc-uuid", "name": "하나은행", "members": [ ... ], ... },
  "insightsByType": {
    "risk": [ { "type": "risk", "text": "...", "occurredAt": "2026-05-12T09:00:00Z", ... } ],
    "opportunity": [ ... ]
  },
  "meetings": [ { "meetingId": "meeting-uuid", "title": "ROSA PoC", "ownerUserId": "...", "date": "2026-05-12T09:00:00Z" } ]
}

Error: 400 Bad Request (잘못된 from/to — RFC3339 아님)
Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### Put Account Document (로컬 문서 인제스트 — 멤버 전용)

로컬에서 작성한 문서(이메일/캘린더/prep 노트 등)를 Account에 인라인 마크다운
(≤300KB)으로 저장해 비-Obsidian 팀원도 TTOBAK에서 열람한다. 출처 규칙 루프 차단:
`ttobak_id` frontmatter가 있는 TTOBAK-원본 문서는 거부한다.

```
POST /api/accounts/{accountId}/documents
{ "title": "Email notes", "markdown": "# Prep\n...", "docType": "prep", "path": "Accounts/하나은행/prep.md" }

Response: 201 Created
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "Accounts/하나은행/prep.md", "sourceUserId": "user-uuid", "createdAt": "2026-05-30T09:00:00Z" }

Error: 400 Bad Request (title/markdown 누락 또는 >300KB)
Error: 400 Bad Request (TTOBAK 원본 — loop guard)
Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### List Account Documents (인제스트 문서 목록 — 멤버 전용)

```
GET /api/accounts/{accountId}/documents?docType=prep

Response: 200 OK
{ "documents": [ { "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z" } ] }

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

#### Get Account Document (전체 내용 — 멤버 전용)

```
GET /api/accounts/{accountId}/documents/{docId}

Response: 200 OK
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z", "content": "# Prep\n..." }

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (문서 없음)
```

#### Export Vault (Obsidian 마크다운 내보내기)

본인 소유 미팅을 Obsidian 친화 마크다운(YAML frontmatter)으로 렌더링해
`Accounts/{name}/`(Account 공유) 또는 `_Private/Meetings/`(비공개) 경로의
파일 목록으로 반환한다. MCP 클라이언트가 각 파일을 로컬 vault에 기록한다.

```
GET /api/vault/export

Response: 200 OK
{ "files": [ { "path": "Accounts/하나은행/2026-05-12 ROSA 리뷰.md", "markdown": "---\naccount: \"[[하나은행]]\"\n...\n---\n\n# ROSA 리뷰\n..." } ] }

Error: 403 Forbidden
```

#### Link Meeting to Account (분류만 — owner+멤버 전용)

```
POST /api/meetings/{meetingId}/account
Request:
{
  "accountId": "acc-uuid"
}

Response: 200 OK
{
  "accountId": "acc-uuid"
}

Error: 403 Forbidden (owner가 아니거나 해당 Account 멤버가 아님)
Error: 404 Not Found (미팅 없음)
```

#### Share Meeting to Account (팀 공유 — owner+멤버 전용)

미팅을 Account 팀에 공유한다: `accountId`+`sharedToAccount`를 설정하고,
owner를 제외한 모든 Account 멤버에게 read 권한 Share를 부여하며, Account
파티션에 MeetingRef를 적립한다.

```
POST /api/meetings/{meetingId}/share-account
Request:
{
  "accountId": "acc-uuid"
}

Response: 200 OK
{
  "accountId": "acc-uuid",
  "sharedWith": 2          // read 권한을 부여받은 멤버 수 (owner 제외)
}

Error: 403 Forbidden (owner가 아니거나 해당 Account 멤버가 아님)
Error: 404 Not Found (미팅 없음)
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
    "parentPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
  }
}
```

Note: an empty/absent `parentPageId` on a configured integration means the connection predates the parent-page requirement and must be re-saved with a `parentPage` before exports will work.

#### Configure Notion Integration

Notion internal integrations can no longer create pages at the workspace root — a parent page or database that has been shared with the integration is required.

```
PUT /api/settings/integrations/notion
Request:
{
  "apiKey": "ntn_xxxxxxxxxxxx",
  "parentPage": "https://www.notion.so/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d"
}

Response: 200 OK
{
  "configured": true,
  "maskedKey": "ntn_****xxxx",
  "parentPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
}
```

**Errors:**
- `400 BAD_REQUEST` — `apiKey` missing or invalid format
- `400 BAD_REQUEST` — `"parentPage is required"` (missing)
- `400 BAD_REQUEST` — `"Invalid Notion page URL or ID"` (unparseable `parentPage`)
- `400 BAD_REQUEST` — `"Notion API key is invalid or has been revoked."` (`apiKey` itself rejected by Notion — 401)
- `400 BAD_REQUEST` — `"Notion page not found or not shared with the integration. Share the page with your integration (··· → Connections) and try again."` (page not found, or not shared with the integration — Notion returns 404 for both cases)

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
  2. Bedrock Claude Opus 4.8 호출
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
