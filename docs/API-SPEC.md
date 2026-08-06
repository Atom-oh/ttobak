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

## Download URL 형태 (ADR-027)

API가 반환하는 모든 다운로드 URL(`downloadUrl`, `previewUrl`, `audioUrl`/
`audioUrls[]`, 첨부 `url`, 공개 공유 302 Location)은 CloudFront 서명 URL이다:

```
https://{domain}/media/{s3Key}?Expires=...&Signature=...&Key-Pair-Id=...
```

TTL 시맨틱은 이전 S3 presign과 동일(기본 1시간, 공개 공유 5분). 업로드
(`uploadUrl`, PUT)는 여전히 raw S3 presigned URL이다. 백엔드가 CloudFront
서명 키를 못 읽으면(로컬 개발 등) S3 presigned GET URL로 폴백한다.

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
      "sentiment": "positive",    // positive | neutral | negative, omitted until analyzed
      "duration": 1830,           // total audio length in seconds, omitted when unknown
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
  "liveSummary": "## 실시간 요약\n...",       // Markdown incl. mermaid, built during recording (omitted if never saved)
  "transcriptA": "Transcribe 결과 전체 텍스트...",
  "transcriptB": "Nova 2 Sonic 결과 전체 텍스트...",
  "selectedTranscript": "A",                    // "A" | "B" | null
  "audioKey": "audio/user-uuid/meeting-uuid.webm",
  "notionPageId": "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d", // owner only, omitted for shared users; set once exported to Notion, re-export updates this page in place
  "permission": "owner",                        // "owner" | "read" | "edit"
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
  "notes": "In-meeting notes...",               // optional, see semantics below
  "liveSummary": "## 실시간 요약\n...",          // optional, same omit-vs-empty semantics as notes
  "selectedTranscript": "B",                    // optional
  "participants": ["Alice", "Bob", "David"],    // optional
  "status": "done"                              // optional
}

Response: 200 OK
{ "meetingId": "uuid", "updatedAt": "..." }

Error: 403 Forbidden (shared users with "read" permission cannot edit)
```

> `content` must be **Markdown**, not HTML. The web editor (TipTap) edits in HTML but converts back to Markdown before saving, because the summary is consumed as Markdown downstream (Notion/Obsidian export). Exporters also normalize any stray HTML to Markdown as a safety net for legacy records.

> `notes` and `liveSummary` are the only fields with omit-vs-explicit-empty semantics: omitting the key entirely leaves the stored value untouched, while sending an explicit `""` clears it. Every other field in this request follows the older "empty/omitted string means don't touch this field" convention (a plain `string`, not a pointer) — so e.g. sending `"title": ""` does NOT clear the title, it's treated the same as omitting it. `liveSummary` is the markdown (incl. mermaid) summary built incrementally during recording — the frontend sends it at save time (both the normal and retry update paths, when present), and the summarize pipeline feeds it into final-summary generation as prior context. Capped server-side at 32,000 characters (`400 BAD_REQUEST` beyond that).

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
      "role": "owner"            // owner | AM | TAM | SSA | SA | SA Manager | AM Manager
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
  "role": "TAM"                 // AM | TAM | SSA | SA | SA Manager | AM Manager (owner는 지정 불가)
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

#### Update Member Role (owner 전용)

```
PUT /api/accounts/{accountId}/members/{userId}
Request:
{
  "role": "AM"                  // AM | TAM | SSA | SA | SA Manager | AM Manager (owner로는 변경 불가)
}

Response: 200 OK
{
  "userId": "tam-uuid",
  "email": "tam@example.com",
  "role": "AM"
}

Error: 403 Forbidden (owner가 아님)
Error: 404 Not Found (해당 멤버 없음)
Error: 400 Bad Request (잘못된 역할이거나 대상이 owner)
```

#### Remove Member (owner 전용)

```
DELETE /api/accounts/{accountId}/members/{userId}[?force=true]

Response: 204 No Content (모든 미팅의 Share cleanup까지 완전히 성공한 경우)
Response: 200 OK (force=true였고, 멤버십 삭제는 성공했으나 일부 미팅의 Share cleanup이 실패했거나 origin 태그가 없는 모호한 Share가 발견된 경우)
{
  "removed": true,
  "cleanupFailedForMeetings": ["meeting-id-1", "meeting-id-2"],
  "ambiguousUntaggedMeetingIDs": ["meeting-id-3"]
}

Error: 403 Forbidden (owner가 아님)
Error: 404 Not Found (해당 멤버 없음)
Error: 400 Bad Request (owner는 제거 불가)
Error: 400 Bad Request (force=true 없이 호출했는데 대상이 origin=="account"가 아닌 — 즉 모호한 — Share를 이 account와 연결된 미팅에 하나라도 보유. 멤버십은 삭제되지 않고 그대로 유지됨. ?force=true로 재시도하면 진행되며, 그 경우 응답은 위 200 분기와 동일한 바디를 가짐)
Error: 500 Internal Server Error (cleanup 대상 미팅 목록 조회 자체가 실패 — 멤버십은 삭제되지 않고 그대로 유지되므로 안전하게 재시도 가능)
```

> 멤버십 삭제는 per-user Share 레코드가 없는 미팅에 대한 새 접근을 즉시 차단합니다. 기존 Share 레코드가 있는 미팅은 같은 `RemoveMember` 요청 안에서 account의 전체 MeetingRef 목록을 순회하는 best-effort cleanup이 account-origin Share만 회수합니다. 이 처리는 N개 미팅 전체에 대해 즉시 완료되는 작업이 아니며 멤버십 삭제와 트랜잭션으로 묶이지 않습니다. 소유자가 별도로 부여한 direct Share는 삭제하지 않습니다.
>
> **`force` 파라미터 (fail-closed 기본값)**: `Origin != "account"`인 Share(실질적으로 `Origin==""`)는 owner가 명시적으로 부여한 direct grant일 수도, `Origin` 필드 도입 이전에 쓰인 legacy account-share일 수도 있으며 이 시스템은 둘을 구분할 수 없습니다(자세한 내용은 [ADR-023](decisions/ADR-023-share-origin-provenance-and-legacy-migration.md)). `force`가 없으면 `RemoveMember`는 대상이 이런 모호한 Share를 하나라도 보유한 순간 **멤버십 삭제 자체를 거부**합니다(400, 멤버십은 그대로 유지) — 이전 라운드처럼 멤버십을 삭제하고 나서야 모호성을 응답에 보고하는 fail-open 방식이 아닙니다. `?force=true`를 넘기면 이 precheck를 건너뛰고 멤버십을 삭제하며, 모호한 Share는 그대로 두고 `ambiguousUntaggedMeetingIDs`에 보고합니다. precheck 도중 발생하는 조회 오류(일시적 DynamoDB 오류 포함)는 **500으로 응답하고 멤버십을 그대로 유지**합니다(재시도 가능) — 이 precheck 자체가 접근-잔존 갭을 닫는 보안 게이트이므로 일시 오류를 관대하게 넘기면 그 갭이 다시 열리기 때문입니다. 이 판정은 `SharedToAccount`가 true인 미팅만 대상으로 합니다 — `AccountID`만 설정되고 `SharedToAccount`가 false인 Link-only 미팅의 Share는 team-share grant와 무관하므로 차단 대상이 아니며 그대로 둡니다.
>
> **미팅 목록 조회 자체의 실패**: cleanup 대상을 정하기 위한 `ListMeetingRefsForAccount` 호출은 멤버십 삭제 **이전**에 실행됩니다 — 이 호출이 실패하면 멤버십은 삭제되지 않은 채 500으로 응답하므로, 호출자는 동일 요청을 안전하게 재시도할 수 있습니다(멤버십이 이미 지워진 뒤라면 재시도가 404가 되어버려 재시도할 방법이 없었던 이전 동작을 수정).
>
> **Cleanup 실패가 접근을 잔존시키지 않음**: 특정 미팅의 cleanup(Share 조회/삭제)이 실패하는 경우는 로그뿐 아니라 응답 바디의 `cleanupFailedForMeetings`로도 노출됩니다 — 하지만 이 cleanup은 접근 통제의 유일한 수단이 아닙니다. `origin=="account"` Share row를 무조건 신뢰하지 않고 **현재 account membership을 읽기 시점에 즉시 재검증**하는 로직이 이 row를 보는 모든 read path에 적용됩니다: 미팅 상세(`checkAccess`), 미팅 목록(`ListMeetings` — 제목/요약 같은 메타데이터도 상세 뷰와 동일하게 차단), KB Q&A(`KnowledgeService.Ask` — 현재 어떤 Lambda 라우트에도 연결되지 않은 미사용 코드지만 향후 재사용 대비 동일 로직 적용), Python Q&A Lambda(`backend/python/qa/handler.py`의 `_list_shared_meetings`). Python 경로에서 `SHARED_MEETINGS_CACHE_TTL_SECONDS`(기본 300초) 동안 캐싱되는 것은 어느 미팅이 공유돼 있는지의 **불변 식별자(meetingId/ownerId)뿐**이며, 각 share가 여전히 존재하는지·현재 origin이 무엇인지·미팅의 `sharedToAccount` 상태·account membership은 모두 매 호출마다 캐시 없이 재조회합니다 — 제거는 그 TTL과 무관하게 다음 QA 요청부터 즉시 반영됩니다. KB 검색결과 캐시(`KB_CACHE_TTL_SECONDS`, 기본 600초)도 조회 시점에 이 live 재검증으로 만든 접근 서명(access signature)을 함께 저장·대조하므로, 캐시된 이후 접근이 바뀌면(제거, un-share 등) 같은 질문을 다시 물어도 stale 결과가 나가지 않고 캐시 미스로 처리돼 새로 조회합니다. 따라서 cleanup 삭제 자체가 실패해 stale Share row가 남아 있어도 제거된 멤버는 다음 읽기부터 즉시 접근을 잃습니다. `cleanupFailedForMeetings`는 여전히 유용한 시그널이지만(운영자가 stale row를 정리하고 싶을 때), 접근을 즉시 차단하기 위해 필요한 것은 아닙니다.
>
> **이 보장이 적용되지 않는 경우**: `Origin` 필드 도입 이전에 쓰인 legacy share(`origin==""`)는 direct grant와 구분 불가능하므로 이 재검증 대상이 아니며 무조건 신뢰됩니다 — backfill CLI로 `origin=account` 태그를 소급 부여하기 전까지는 멤버 제거로 회수되지 않습니다. 아래 Known limitation 참고.
>
> **Known limitation & remediation**: 이 수정 배포 전에 `share-account`가 생성한 Share 레코드는 origin 태그가 없어 direct grant로 취급되므로, `RemoveMember`의 cleanup이 자동으로 회수하지 못합니다. `force`를 넘기지 않으면 이제 이 경우 제거 자체가 차단되므로(위 참고), 조용히 접근이 잔존하는 케이스는 owner가 `force=true`를 명시적으로 선택한 경우로 좁혀집니다 — 그 경우 응답의 `ambiguousUntaggedMeetingIDs`로 어떤 미팅이 영향받는지 확인 가능합니다. 이 목록은 정밀한 legacy 판정이 아니라 **거친(coarse) 시그널**입니다: 제거된 멤버가 account와 연결된 미팅에 별도의 direct Share도 보유한 경우에는 legacy account-share의 실제 존재 여부와 무관하게 그 미팅도 목록에 포함되므로, 항목이 있다는 사실만으로 반드시 backfill이 필요한 legacy share라고 판단해서는 안 됩니다. `backend/cmd/backfill-share-origin` CLI(운영자가 `--account-id` 단위로 직접 실행, 기본 dry-run·`--apply`로 확정)가 이런 과거 레코드에 `origin=account`(및 `accountId`) 태그를 소급 부여해 이후 `RemoveMember` cleanup 대상이 되도록(그리고 force 없이도 제거가 차단되지 않도록) 만드는 remediation 경로다. **주의**: 이 CLI는 태깅 여부가 모호한 후보(같은 미팅이 account와 direct 양쪽으로 공유된 경우 두 origin이 구분되지 않음)를 자동으로 구별하지 못한다 — `--apply` 실행 시 dry-run에서 출력된 CANDIDATE 전부가 예외 없이 태깅되므로, 신뢰할 수 없는 후보는 `--apply` 전에 반드시 `--exclude userId1:meetingId1,userId2:meetingId2,...`로 명시적으로 제외해야 한다(그렇지 않으면 direct grant가 `origin=account`로 오태깅되고, 이후 `RemoveMember`가 owner가 명시적으로 부여한 공유를 자동 회수할 수 있다). 이 CLI는 미팅 기준으로 Share row를 직접 열거하므로(현재 멤버십을 거치지 않음) 이미 계정에서 제거된 사용자의 legacy share도 후보로 찾아 태깅할 수 있습니다 — 다만 backfill을 멤버 제거보다 먼저 실행하는 쪽이 여전히 마찰 없는 경로입니다. **한 가지는 이 CLI로도 태깅할 수 없습니다**: 미팅이 이후 이 account에서 un-share되거나 다른 account로 재공유된 경우, 그 미팅의 legacy share는 `ORPHANED`로만 보고되고 절대 태깅되지 않습니다(어느 account 소속으로 태깅해야 할지 안전하게 추론할 수 없기 때문) — 이 경우는 미팅 owner에게 확인 후 owner가 직접 `RevokeShare`로 정리해야 하는, 의도적으로 수동인 remediation 경로입니다. 전체 설계 배경과 검토한 대안은 [ADR-023](decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) 참고.

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
      "implication": "Q3 갱신 협상 전 PoC 결과가 나오지 않을 위험",
      "nextAction": "인프라 승인 상태를 TAM이 이번 주 확인",
      "sourceType": "meeting",
      "sourceId": "meeting-uuid",
      "occurredAt": "2026-05-12T09:00:00Z",
      "tsMarker": "[TS:120]",
      "entities": ["PoC"]
    }
  ]
}
```

`implication`(함의)/`nextAction`(권장 조치)은 모두 선택 필드로,
`ExtractInsights`(Bedrock Haiku)가 구조화된 근거를 함께 생성할 때 채워진다 —
이전에는 `type`/`text`만 있었다. `ExtractInsights`는 `evidence`(발언
준-verbatim 인용)도 함께 생성하지만, account/project 파티션으로 팬아웃되는
이 응답에는 **의도적으로 포함되지 않는다** — 미팅 접근권 없는 account/project
멤버가 원본 발언 인용을 읽게 되는 노출을 막기 위함(`BuildAccountInsights`,
`meeting.go`). `evidence`는 미팅 자체의 `Insights` JSON을 통해 미팅 접근권이
있는 사용자에게만 노출된다. `Project.Insights`
(`GET /api/projects/{projectId}/insights`, `GET /api/projects/{projectId}/brief`)도
같은 스키마(`FieldInsight`)와 같은 팬아웃 정책을 공유한다.

```
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

#### List Account Research (연동된 리서치 목록 — 멤버 전용)

리서치를 Account에 연동(`POST /api/research/{researchId}/accounts`,
`DELETE /api/research/{researchId}/accounts/{accountId}` — 리서치 CRUD
자체의 나머지 엔드포인트는 이 문서에 아직 반영되지 않은 기존 기능)한 뒤
Account 쪽에서 조회하는 read 경로. 연동 시 `accountIds`는 DynamoDB String
Set에 원자적 `ADD`/`DELETE`로 갱신되어 동시 연동 요청 간 write race가 없다.
조회 시 각 항목의 `accountIds`가 실제로 대상 accountId를 포함하는지
재검증한다(fail-closed) — 연동 해제 후 역참조(`ACCOUNT#{id}/RESEARCH_REF#`)
정리가 실패해도 목록에는 노출되지 않는다.

```
GET /api/accounts/{accountId}/research

Response: 200 OK
{ "research": [ { "researchId": "r-uuid", "topic": "...", "summary": "...", "status": "done", "ownerUserId": "...", "createdAt": "..." } ] }

Error: 403 Forbidden (멤버가 아님)
```

#### Put Account Document (로컬 문서 인제스트 — 멤버 전용)

로컬에서 작성한 문서(이메일/캘린더/prep 노트 등)를 Account에 인라인 마크다운
(≤300KB)으로 저장해 비-Obsidian 팀원도 TTOBAK에서 열람한다. 출처 규칙 루프 차단:
`ttobak_id` frontmatter가 있는 TTOBAK-원본 문서는 거부한다.

`docType`은 자유 문자열이다 (`"prep"`/`"reference"` 등 기존 값과 문서 허브 v2가
UI에 노출하는 `"note"`/`"blog"`/`"slide"`가 모두 동일 필드를 공유하며, 서버는
enum 검증을 하지 않는다). `markdown`에 포함된 `[[문서명]]`, `[[문서명|별칭]]`,
`[[문서명#절]]` 형태의 위키링크는 저장 시 서버가 파싱해 정규화된 제목 목록을
`links`에 저장한다 (그래프 뷰 등 향후 기능의 데이터 소스). 슬라이드(PPTX/PDF)는
`markdown` 대신 `fileKey`(사전 발급된 presigned PUT로 업로드한 S3 키, 반드시
`docs/{내 userId}/` 접두어)를 전달한다 — 이 put 호출 자체가 업로드 완료 기록이며
별도의 `/api/upload/complete` 단계는 없다.

```
POST /api/accounts/{accountId}/documents
{ "title": "Email notes", "markdown": "# Prep\n\n[[하나은행]] 미팅 준비...", "docType": "prep", "path": "Accounts/하나은행/prep.md" }
{ "title": "발표자료", "docType": "slide", "fileKey": "docs/user-uuid/1234567890_deck.pdf", "fileName": "deck.pdf", "mimeType": "application/pdf", "fileSize": 123456 }

Response: 201 Created
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "Accounts/하나은행/prep.md", "links": ["하나은행"], "sourceUserId": "user-uuid", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z" }

Error: 400 Bad Request (title 누락, markdown/fileKey 둘 다 없음, 또는 markdown >300KB)
Error: 400 Bad Request (TTOBAK 원본 — loop guard)
Error: 403 Forbidden (멤버가 아님, 또는 fileKey가 내 userId 접두어가 아님)
Error: 404 Not Found (Account 없음)
```

#### List Account Documents (인제스트 문서 목록 — 멤버 전용)

```
GET /api/accounts/{accountId}/documents?docType=prep

Response: 200 OK
{ "documents": [ { "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z" } ] }

`links`/`fileName`은 값이 있을 때만 나타난다(`omitempty`) — 위키링크나
파일이 없는 문서는 필드 자체가 응답에서 생략된다(빈 배열/`null`이 아님).

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (Account 없음)
```

목록은 `content`를 포함하지 않는다 (본문은 Get으로 개별 조회).

#### Get Account Document (전체 내용 — 멤버 전용)

```
GET /api/accounts/{accountId}/documents/{docId}

Response: 200 OK
{ "docId": "doc-uuid", "title": "Email notes", "docType": "prep", "path": "...", "links": ["하나은행"], "sourceUserId": "...", "createdAt": "2026-05-30T09:00:00Z", "updatedAt": "2026-05-30T09:00:00Z", "content": "# Prep\n\n[[하나은행]] 미팅 준비..." }

슬라이드(`fileName` 있는 문서)는 `content`가 빈 문자열이고 `downloadUrl`(원본
파일, 1시간 유효 GET URL)이 채워진다. PPTX/PPT는 추가로 `previewUrl`
(PDF 사이드카, 변환이 끝난 뒤에만 존재 — ADR-022)이 함께 채워질 수 있다;
`downloadUrl`은 항상 원본을 가리키고 사이드카로 바뀌지 않는다. 둘 다 값이
없으면 필드 자체가 응답에서 생략된다.

Error: 403 Forbidden (멤버가 아님)
Error: 404 Not Found (문서 없음)
```

#### Update / Delete Account Document (멤버 전용)

Update는 필드별 "생략 시 기존 값 보존" 방식이다 — `docId`/`sourceUserId`/
`createdAt`은 항상 보존된다. `title`만 필수(빈 문자열이면 거부); `docType`/
`path`는 생략(빈 문자열)하면 기존 값 유지, 값을 보내면 그 값으로 교체된다.
`markdown`은 JSON 키 자체를 생략하면 본문이 보존되고, 명시적으로 빈 문자열을
보내면 본문이 지워진다(`*string` — nil=생략, non-nil pointer to ""=명시적
삭제). `markdown`과 `fileKey`를 둘 다 생략하면 기존 본문/파일이 그대로
보존된다(더 이상 오류가 아님); 둘 다 값이 있으면 거부된다(노트/블로그이거나
슬라이드이거나, 둘 다일 수 없음). 값이 있는 `markdown`을 보내면 링크 재파싱과
loop guard가 적용된다; 값이 있는 `fileKey`가 기존과 다르면 소유권(내 userId
접두어)이 재검증된다. 슬라이드→노트 전환은 `markdown`만 보내면 되고(기존
파일 필드가 함께 지워진다); 노트→슬라이드 전환은 `fileKey`만 보내면 된다
(기존 본문이 함께 지워진다).

```
PUT /api/accounts/{accountId}/documents/{docId}
{ "title": "Email notes v2", "markdown": "# Prep v2\n..." }

Response: 200 OK
{ "docId": "doc-uuid", "title": "Email notes v2", ... }

DELETE /api/accounts/{accountId}/documents/{docId}
Response: 204 No Content

Error: 400 Bad Request (title 누락, markdown과 fileKey가 둘 다 값이 있음, markdown >300KB, 또는 TTOBAK 원본)
Error: 403 Forbidden (멤버가 아님, 또는 fileKey가 내 userId 접두어가 아님)
Error: 404 Not Found (문서 없음)
```

#### Personal Documents (Account 미소속 — 소유자 전용)

`ttobak_ask`/문서 허브 v2의 개인 노트/블로그/슬라이드. `PK: USER#{내 userId}`로
저장되므로 소유권이 키에 내재해 있고 Account 멤버십 확인이 필요 없다. 요청/응답
스키마는 Account 문서와 동일 (accountId 경로 세그먼트만 없음).

```
POST   /api/documents                 { "title": "...", "markdown": "...", "docType": "note" }
GET    /api/documents?docType=note    → { "documents": [ AccountDocumentDTO, ... ] }
GET    /api/documents/{docId}         → AccountDocumentDetail
PUT    /api/documents/{docId}         { "title": "...", "markdown": "..." }
DELETE /api/documents/{docId}         → 204 No Content

Error: 400 Bad Request / 404 Not Found — 위 Account 문서 엔드포인트와 동일한 의미
(멤버십 확인 자체가 없어 "멤버가 아님" 403은 없지만, foreign fileKey는 여전히
403 Forbidden — PK가 소유권 증명일 뿐, fileKey 접두어 검증은 별도로 적용됨)
```

#### Slide Upload (문서용 presigned URL)

기존 presigned 업로드 엔드포인트(`POST /api/upload/presigned`)의 `category`에
`"doc"`가 추가되었다. `fileType`은 `application/pdf` 또는 PowerPoint MIME(
`application/vnd.openxmlformats-officedocument.presentationml.presentation`,
`application/vnd.ms-powerpoint`)만 허용된다. `meetingId`는 필요 없다 (문서는
미팅에 종속되지 않음). S3 키는 `docs/{내 userId}/{타임스탬프}_{파일명}` 형태이며, 위
Put Document 호출의 `fileKey`로 그대로 전달한다.

```
POST /api/upload/presigned
{ "fileName": "deck.pdf", "fileType": "application/pdf", "category": "doc" }

Response: 200 OK
{ "uploadUrl": "https://...presigned-put-url...", "key": "docs/user-uuid/1234567890_deck.pdf", "expiresIn": 3600 }

Error: 400 Bad Request (fileType이 pdf/PowerPoint MIME이 아님)
```

이후 `uploadUrl`로 파일을 직접 PUT하고, 응답의 `key`를 Put Document의
`fileKey`로 전달한다 — `/api/upload/complete` 호출은 없다.

#### Slide Preview (PPTX → PDF 변환, ADR-022)

`docs/` 접두어에 업로드된 PPTX/PPT는 별도 컨테이너 Lambda(`cmd/convert-doc`)가
EventBridge S3 이벤트로 트리거되어 headless LibreOffice로 PDF 사이드카를
생성한다(결정론적 키, DynamoDB 쓰기 없음 — 문서 레코드가 아직 없을 수 있기
때문). Get Account/Personal Document 응답에서 `downloadUrl`은 **항상 원본**
파일을 가리키고, PPTX/PPT면 변환이 끝난 뒤 `previewUrl`(PDF 사이드카)이
별도 필드로 함께 채워진다 — `downloadUrl`이 사이드카로 바뀌는 일은 없다.
변환이 아직 끝나지 않았으면 `previewUrl`이 생략된다(폴링해서 재조회). 별도의
공개 REST 엔드포인트는 없다. 이 "`downloadUrl`은 절대 사이드카가 아님" 규칙은
**JSON 응답의 필드 이름에 대한 규칙**이다 — 아래 Public Share Link의
`GET /api/public/docs/{token}`은 필드가 아니라 302 리다이렉트 자체이고, 그
리다이렉트 타겟은 (PPTX/PPT면) 의도적으로 사이드카를 향한다: 무인증
방문자에게 미리보기가 목적이므로, 다른 곳과 반대로 여기서는 사이드카가
있으면 사이드카로, 없으면 원본으로 리다이렉트한다.

#### Share Document to Account (개인 문서 → 팀 복제)

개인 문서를 Account 팀에 공유한다. 슬라이드/노트 모두 가능하다(코드에
슬라이드 전용 검증은 없음 — 마크다운 문서는 본문을 그대로 복제). 슬라이드는
참조가 아니라 **복제** — S3 `CopyObject`로 별도 키
(`docs/{내 userId}/{ms}_{랜덤ID}_{파일명}`)에 복사하고 새 `AccountDocumentDTO`를
만든다(원본을 덮어쓰지 않으므로 이후 원본을 바꿔도 공유본은 그대로). 위 Slide
Upload 절의 `docs/{내 userId}/{타임스탬프}_{파일명}`과 레이아웃이 다른 것처럼
보이지만 같은 규칙이다 — 업로드는 `{타임스탬프}_{파일명}`, 공유 복제는
충돌 방지를 위해 `generateID()`가 끼어든 `{ms}_{랜덤ID}_{파일명}`일 뿐, 둘
다 `docs/{userId}/` 접두어와 파일명 보존 규칙은 동일하다.

```
POST /api/documents/{docId}/share-account
{ "accountId": "acc-uuid" }

Response: 201 Created
{ "docId": "new-doc-uuid", "title": "발표자료", "docType": "slide", "fileName": "deck.pdf", ... }  (AccountDocumentDTO)

Error: 400 Bad Request (accountId 누락)
Error: 403 Forbidden (문서 소유자가 아님, 또는 accountId 멤버가 아님)
Error: 404 Not Found (문서 없음)
```

#### Share Document with a Specific User (개인 문서 → 1인 공유, 참조 방식)

개인 문서를 이메일로 지정한 한 사용자와 공유한다. 위 Share-to-Account와 달리
**복제가 아니라 참조** — 소유자의 문서 한 부만 존재하고, `ListUserDocuments`/
`GetUserDocument`는 소유자 파티션을 그대로 읽으므로 수신자는 항상 최신
내용을 본다. 항상 읽기 전용(요청에 `permission` 필드 자체가 없음 —
`ShareMeeting`과 달리 편집 공유는 지원하지 않음). 소유자만 공유/조회/철회
가능 — 소유자가 아니면 `getDoc`이 404를 반환해(권한 여부를 드러내지 않는
fail-closed) 자동으로 막힌다.

```
POST /api/documents/{docId}/share
{ "email": "target@example.com" }

Response: 201 Created
{ "sharedWith": { "userId": "user-uuid", "email": "target@example.com", "permission": "read" } }

GET /api/documents/{docId}/shares
Response: 200 OK
{ "shares": [ { "userId": "...", "email": "...", "permission": "read", "sharedAt": "..." }, ... ] }

DELETE /api/documents/{docId}/share/{userId}
Response: 204 No Content

Error: 400 Bad Request (email 누락)
Error: 404 Not Found (문서 없음, 또는 호출자가 소유자가 아님 — 403 대신 404로 존재 자체를 숨김)
```

수신자 쪽 `GetUserDocument`/`ListUserDocuments` 응답에는 `sharedBy`(소유자
이메일)가 채워지고 `publicShareToken`은 생략된다 — 프론트엔드는 `sharedBy`
유무로 읽기 전용 뱃지를 표시한다(`ShareButton`의 `readOnly` prop).

#### Public Share Link (개인 파일 문서 무인증 공개 링크)

128비트 랜덤 토큰(`crypto/rand`)을 발급해 인증 없이 접근 가능한 링크를
만든다. `fileKey`가 있는 문서(슬라이드/PDF 등 — 마크다운 노트는 제외)에만
허용된다. 발급/철회는 인증된 소유자만 가능하지만, 링크 자체
(`GET /api/public/docs/{token}`)는 CloudFront `/api/public/*` behavior에
등록되어 있어 API Gateway JWT authorizer와 Lambda@Edge JWT 체크를 둘 다
건너뛴다 — `/api/*`의 다른 모든 라우트가 이 두 계층을 모두 통과해야 하는 것과
다르다(자세한 내용은
[ADR-022](decisions/ADR-022-slide-preview-conversion-and-public-share-links.md)).
핸들러는 문서 내용을 직접 반환하지 않고 항상 302로 서명된 GET URL
(`https://{domain}/media/...`, ADR-027 — 또는 PDF 사이드카가 있으면
그쪽)로 리다이렉트한다. 발급은 동시 요청 간 원자적
(`SetPublicShareTokenIfAbsent` 조건부 쓰기)이라 더블클릭으로 토큰 두 개가
발급돼 한쪽이 깨지는 경우가 없다.

```
POST /api/documents/{docId}/public-share
Response: 200 OK
{ "token": "8f2c...랜덤128비트..." }

DELETE /api/documents/{docId}/public-share
Response: 204 No Content

GET /api/public/docs/{token}   (인증 헤더 없음)
Response: 302 Found → Location: <5분 유효 서명 GET URL (https://{domain}/media/...)>

Error: 400 Bad Request (대상 문서에 fileKey가 없음 — 마크다운 노트는 공개 공유 불가)
Error: 403 Forbidden (문서 소유자가 아님 — public-share 발급/철회 시에만)
Error: 404 Not Found (문서 없음, 또는 토큰이 철회/만료됨)
```

이 라우트가 발급하는 서명 URL은 5분 TTL(`PublicShareURLTTL`)로, 다른
모든 곳(1시간)보다 짧다 — 철회 후 이미 발급된 URL이 살아있는 창을 좁히기
위한 의도적 단축(ADR-022). 5분도 0은 아니므로 철회 즉시 완전히 막히는 것은
아니라는 점은 여전히 알려진 한계다.

#### Export Vault (Obsidian 마크다운 내보내기)

본인 소유 미팅과 문서를 Obsidian 친화 마크다운(YAML frontmatter)으로 렌더링해
파일 목록으로 반환한다. MCP 클라이언트가 각 파일을 로컬 vault에 기록한다.

- 미팅: `Accounts/{name}/`(Account 공유) 또는 `_Private/Meetings/`(비공개)
- 문서(마크다운 본문이 있는 것만 — 슬라이드는 제외): `Accounts/{name}/Docs/`
  (계정 멤버십별) 또는 `_Private/Docs/`(개인 문서). frontmatter에 `doc_type`,
  `links`, `ttobak_id`를 포함(ADR-020 참조) — 재인제스트 시 ADR-017과 동일한
  loop guard가 적용된다.

```
GET /api/vault/export

Response: 200 OK
{ "files": [
  { "path": "Accounts/하나은행/2026-05-12 ROSA 리뷰.md", "markdown": "---\naccount: \"[[하나은행]]\"\n...\n---\n\n# ROSA 리뷰\n..." },
  { "path": "Accounts/하나은행/Docs/회의 준비.md", "markdown": "---\ndoc_type: note\nttobak_id: doc-uuid\n---\n\n준비 내용..." }
] }

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

### Projects

Project(SFDC Opportunity)는 미팅노트·리서치·인사이트를 영업 기회 단위로 묶는
1급 엔티티다. Account와 달리 **다대다 그래프**로 여러 Account에 동시에 연결될
수 있다(파트너사+엔드고객 등). Research↔Account 연동과 동일한 그래프 레퍼런스
패턴(문자열 집합 + 역참조 아이템 + fail-closed 재검증)을 재사용한다 — 자세한
내부 데이터 모델은 ADR-025 참고.

**접근 권한**은 하이브리드: project owner, 직접 초대된 멤버(`POST .../members`),
또는 **연결된 Account 중 하나의 멤버**면 통과한다. 즉 Account를 프로젝트에
연동하면 그 Account의 팀 전체가 자동으로 프로젝트 열람 권한을 얻는다(추가
멤버 초대 없이). SFDC 연동은 메타데이터 필드(`sfdcOpptyId`/`sfdcUrl`)만
저장하며, 실제 SFDC 값을 읽어오는 것은 외부 MCP 클라이언트(SFDC MCP →
`ttobak_create_project`)가 담당한다 — 서버 쪽 SFDC API 연동은 없다.

#### List My Projects

```
GET /api/projects

Response: 200 OK
{ "projects": [ { "projectId": "uuid", "name": "...", "stage": "...", "sfdcOpptyId": "..." } ] }
```

내가 owner이거나 직접 멤버이거나, 내가 속한 Account에 연결된 프로젝트를
모두 반환한다(owner 인덱스 + GSI1 멤버 역조회 + 내 Account 멤버십을 거쳐
연결된 프로젝트 목록, 셋 다 canonical 상태로 재검증 — `requireProjectAccess`의
하이브리드 접근 권한과 정확히 같은 3가지 경로). Account 쪽에서도 동일한
프로젝트를 `GET /api/accounts/{accountId}/projects`로 볼 수 있다 — 이 둘은
서로를 대체하는 두 개의 발견 경로다.

#### Create Project

```
POST /api/projects
Request:
{
  "name": "하나은행 클라우드 마이그레이션",
  "description": "...",          // optional
  "sfdcOpptyId": "006XX...",      // optional
  "sfdcUrl": "https://...",       // optional
  "stage": "Negotiation"          // optional
}

Response: 201 Created
{
  "projectId": "uuid", "name": "...", "description": "...",
  "sfdcOpptyId": "006XX...", "sfdcUrl": "https://...", "stage": "Negotiation",
  "ownerUserId": "owner-uuid", "accountIds": [], "members": [],
  "createdAt": "2026-07-21T00:00:00Z", "updatedAt": "2026-07-21T00:00:00Z"
}

Error: 400 Bad Request (name이 비어있음)
```

#### Get / Update / Delete Project

```
GET /api/projects/{projectId}
PUT /api/projects/{projectId}      (owner 전용, Create와 동일한 필드)
DELETE /api/projects/{projectId}   (owner 전용)

Error: 403 Forbidden — GET: owner/직접 멤버/연결된 Account 멤버 아님
Error: 403 Forbidden — PUT/DELETE: owner 아님 (직접 멤버·연결된 Account 멤버여도 거부)
Error: 404 Not Found
Error: 400 Bad Request (DELETE: 연결된 Account/미팅/리서치/멤버가 하나라도
       남아있으면 거부 — 고아 관계 데이터를 남기지 않기 위해 전부 해제 후
       삭제해야 한다)
```

#### Members (owner 전용)

```
POST   /api/projects/{projectId}/members
Request: { "email": "user@example.com" }
Response: 201 Created — { "userId": "uuid", "email": "user@example.com" }
Error: 400 Bad Request (이미 멤버) · 404 Not Found (해당 email 유저 없음)

DELETE /api/projects/{projectId}/members/{userId}
Response: 204 No Content
```

멤버는 Account처럼 역할(owner/AM/TAM/SSA) 구분이 없다 — 있으면(owner) 있고
없으면(member) 없는 이진 상태.

#### Link / Unlink Account

```
POST   /api/projects/{projectId}/accounts        (owner 전용, 대상 Account 멤버여야 함)
Request: { "accountId": "uuid" }
Response: 200 OK — { "accountIds": ["uuid", ...] }
Error: 403 Forbidden (owner 아님 또는 대상 Account 멤버 아님)

DELETE /api/projects/{projectId}/accounts/{accountId}   (owner 전용)
Response: 204 No Content
```

연동/해제 모두 `Project.accountIds`(String Set)와 역참조
(`ACCOUNT#{accountId}/PROJECTREF#{projectId}`)를 **단일 `TransactWriteItems`
로 원자적으로** 갱신한다(ADR-025) — 해제 시 owner가 그 Account의 현재
멤버가 아니어도 된다(해제는 project ownership만으로 충분 — 그렇지 않으면
owner가 Account에서 제거된 뒤 영구히 연동을 못 푸는 상황이 생긴다).

#### Link / Unlink Meeting, Research

```
POST   /api/projects/{projectId}/meetings          Request: { "meetingId": "uuid" }
DELETE /api/projects/{projectId}/meetings/{meetingId}
POST   /api/projects/{projectId}/research          Request: { "researchId": "uuid" }
DELETE /api/projects/{projectId}/research/{researchId}

Error: 404 Not Found — Link(POST) Meeting: 대상 미팅이 호출자 소유가 아님(호출자
       자신의 파티션에서만 조회하므로 타인 미팅은 "없음"과 구분되지 않음) 또는
       미팅/프로젝트 자체가 없음
Error: 403 Forbidden — Link(POST) Research: 대상 리서치가 호출자 소유가 아님
       (Research는 조회 자체는 owner 무관하게 되므로 소유 여부를 직접 검사)
Error: 403 Forbidden — Link(POST) 공통: 위 소유권 검사를 통과해도 프로젝트
       접근 권한(owner/직접 멤버/연결된 Account 멤버) 없으면 거부
Error: 403 Forbidden — Unlink(DELETE): 대상의 owner도 아니고 프로젝트 owner도 아님 (아래 비대칭 설명 참고)
Error: 404 Not Found (리서치/프로젝트 없음)
```

`Meeting.projectIds`/`Research.projectIds`도 동일하게 String Set + 원자적
`TransactWriteItems`로 연동한다. **링크**는 대상 미팅/리서치의 owner일 것을
요구하지만, **언링크**는 그 owner이거나 **프로젝트 owner**면 충분하다 —
링크한 멤버가 이후 `RemoveMember`로 제거되면 본인도(프로젝트 접근권 상실),
프로젝트 owner도(그 미팅/리서치의 owner가 아님) 언링크를 못 하게 되는
데드락을 막기 위한 비대칭이다(ADR-025). `SharedToAccount`(계정 공유 게이트)
와는 별개다 — 프로젝트에 링크된 미팅의 제목/인사이트는 프로젝트 접근 권한이
있는 사람 모두에게 노출된다(ADR-025 참고, `SharedToAccount`를 의도적으로
우회하는 별도 공유 채널).

#### List Project Meetings / Research

```
GET /api/projects/{projectId}/meetings
Response: 200 OK — { "meetings": [ { "meetingId", "ownerUserId", "title", "date" } ] }

GET /api/projects/{projectId}/research
Response: 200 OK — { "research": [ { "researchId", "topic", "summary", "status", "ownerUserId", "createdAt" } ] }

Error: 403 Forbidden (프로젝트 접근 권한 없음)
```

두 목록 모두 역참조 아이템을 후보로 삼고 canonical `projectIds` 집합에
여전히 포함되는지 재검증한다(fail-closed) — 링크된 미팅이 이 API가 모르는
경로(기존 미팅 삭제 등)로 지워져 역참조만 고아로 남는 경우처럼, 트랜잭션
경계 밖의 다른 실패 모드로 생긴 stale ref도 조회 결과에는 절대 노출되지
않는다. `meetingId` 기준으로도 중복 제거한다(ADR-025의 mutable-Date
레퍼런스 SK 이슈에 대한 방어).

#### Get Project Insights

```
GET /api/projects/{projectId}/insights?from=RFC3339&to=RFC3339&types=risk,tech

Response: 200 OK
{ "insights": [ { "type": "risk", "text": "...", "sourceId": "meeting-uuid", "occurredAt": "...", "tsMarker": "[TS:120]", "entities": [] } ] }

Error: 400 Bad Request (from/to가 RFC3339 아님, 또는 유효하지 않은 insight type)
Error: 403 Forbidden (프로젝트 접근 권한 없음)
```

**저장하지 않고 읽기 시점에 집계한다** — 링크된 미팅들의 `Insights` JSON을
매번 파싱해 반환하므로, 미팅이 재요약되어 인사이트가 갱신되면 다음 조회에
자동 반영된다(Account 인사이트처럼 공유 시점에 스냅샷을 떠서 저장하지
않음 — 동기화 드리프트 자체가 존재하지 않는다).

#### Get Project Brief

```
GET /api/projects/{projectId}/brief?from=RFC3339&to=RFC3339&types=risk,tech

Response: 200 OK
{
  "project": { ... ProjectResponse ... },
  "insightsByType": { "risk": [...], "tech": [...] },
  "meetings": [...],
  "research": [...]
}
```

Get Project + List Meetings + List Research + Get Insights를 한 번에
묶은 편의 엔드포인트.

#### List Account's Projects

```
GET /api/accounts/{accountId}/projects   (해당 Account 멤버 전용)

Response: 200 OK
{ "projects": [ { "projectId", "name", "stage", "sfdcOpptyId" } ] }
```

Account 쪽에서 "이 Account에 연동된 프로젝트" 역참조를 조회하는 read 경로
(Research의 `GET /api/accounts/{accountId}/research`와 동일한 패턴).

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

#### Agentic Q&A (Python QA Lambda)

`POST /api/qa/ask`, `POST /api/qa/meeting/{meetingId}`, WebSocket `ask_live` — Bedrock Converse 에이전틱 루프.
사용 가능 도구: `search_knowledge_base`, `search_aws_docs`, `search_transcript`, `get_aws_recommendation`,
`search_web`, `list_meetings`, `get_meeting_detail`, `start_research`, account 도구들.
스트리밍(`ask_live`) 경로도 비스트리밍과 동일하게 실시간 트랜스크립트 tail(2000자)을 시스템 프롬프트에 포함한다.
대화 연속성: `sessionId`별 히스토리를 DynamoDB에 저장(7일 TTL), 같은 세션의 후속 질문은 이전 문답 맥락을 이어받는다.

**`search_web` 데이터 전송 고지**: 이 도구는 us-east-1 AgentCore Web Search Gateway를 SigV4 크로스리전으로
호출하며, 모델이 만든 검색 쿼리(최대 200자 — 회의 대화에서 파생된 키워드가 포함될 수 있음)가 **외부 웹 검색
제공자로 전송**된다. 이 전송은 **사용자가 직접 입력한 질문 경로에서도 일어난다** — 아래 선제 검색 opt-in
토글이 제어하는 것은 '자동 발화'뿐이다. 수동 경로의 완화책은 시스템 프롬프트/도구 설명의 쿼리 구성 제약
(고객사·참석자 실명, 내부 코드명, 회의 수치 금지 — 일반화 키워드만)이며, 트랜스크립트 속 문장을 지시로
취급하지 않는 인젝션 가드가 함께 적용된다. 쿼리 원문은 CloudWatch에 로깅하지 않는다 — `web_search.py` 자체
로그와 에이전틱 루프의 tool-call 로그(`redact_tool_input_for_log`) 모두 해시+길이만 남긴다.
`WEB_SEARCH_GATEWAY_URL` 미설정 시 도구는 계속 노출되되 호출하면 "web search not configured" 실패 사유가
모델에 전달된다(도구 라운드 1회 소비 — 완전 비활성이 아님). 서버측 per-user rate limit은 아직 없다
(CLAUDE.md Known Issues / ADR-028 follow-up).

#### Detect Questions (실시간 질문 감지 + 선제 검색 플래그)

```
POST /api/qa/detect-questions
Request:
{
  "transcript": "최근 대화 내용...",
  "summary": "현재 미팅 요약 (선택)",
  "previousQuestions": ["이미 제안된 질문"]
}

Response: 200 OK
{
  "questions": ["EKS 1.31 지원 종료일은?", "어느 팀이 마이그레이션을 맡을까요?"],
  "proactive": ["EKS 1.31 지원 종료일은?"]   // questions의 부분집합 — 검색으로 즉시 사실 확인
                                              // 가능한 질문. 프론트(LiveQAPanel)가 배치당 1건을
                                              // 자동 발화해 답을 미리 띄운다 (선제 검색)
}
```

선제 검색 자동 발화는 **기본 꺼짐(opt-in)**: 회의 대화에서 파생된 질문이 사용자 조작 없이 외부 웹 검색까지
이어질 수 있으므로, LiveQAPanel 헤더의 "선제 검색" 토글(localStorage `ttobak.proactiveSearchEnabled`)을 켠
사용자에게만 동작한다. 꺼져 있으면 `proactive` 질문도 일반 추천 칩으로만 표시된다.

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
Request: (no body — always a full-data-source sync)

Response: 200 OK
{
  "status": "started",
  "jobId": "bedrock-ingestion-job-id",
  "message": "Knowledge Base sync started"
}

// When the API Lambda lacks KB_ID/KB_DATASOURCE_ID (env not deployed):
Response: 200 OK
{
  "status": "skipped",
  "message": "Knowledge Base not configured"
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

#### Invite User (admin-only)

Creates a Cognito user with a system-generated temporary password. Cognito emails the invite directly (default template: username + temp password — no login link, since no `userInvitation` template is configured in `infra/lib/auth-stack.ts`) — no SES/templating on our side. The admin who invited the user is responsible for sharing the sign-in URL separately. The invitee's first sign-in returns a `NEW_PASSWORD_REQUIRED` challenge, handled client-side by `completeNewPassword()` in `frontend/src/lib/auth.ts`.

Requires the caller's JWT `cognito:groups` claim to contain `admins` (enforced by `middleware.RequireAdmin`, backed by JWKS-verified signature checking in `middleware.ParseVerifiedJWT`).

```
POST /api/settings/invite-user
Request:
{
  "email": "new.hire@amazon.com",
  "name": "New Hire",       // optional
  "admin": false             // optional, adds to "admins" group if true
}

Response: 201 Created
{
  "email": "new.hire@amazon.com",
  "invited": true,
  "addedToAdmins": false
}
```

**Errors:**
- `400 BAD_REQUEST` — email missing or invalid format
- `403 FORBIDDEN` — caller is not in the `admins` group
- `409 BAD_REQUEST` — a user with this email already exists

**Partial success:** if `admin: true` but adding the user to the `admins` group fails after the account was already created and invited, the response is still `201 Created` with `addedToAdmins: false` rather than an error — the invite itself succeeded.

---

## Insights (Crawler)

Crawled news/tech documents (`CRAWLER#{sourceId}/DOC#{docHash}` items, distinct from the meeting-derived `ACCOUNT#{accountId}/INSIGHT#...` items under Accounts above). `GET /api/insights` lists/filters; `GET /api/insights/{sourceId}/{docHash}` returns full content.

```
DELETE /api/insights/{sourceId}/{docHash}

Response: 204 No Content
```

Manually curates a single crawled **news** document — e.g. a search result the relevance gate (`backend/python/crawler/news_crawler.py`) let through anyway, or one ingested before the gate existed. Deletes the S3 KB markdown object first, then the DynamoDB metadata item (order matters — see below), trying both the `shared/news/` and `shared/aws-docs/` key shapes when no `S3Key` is stored, mirroring the read path in `GetDocumentDetail`. Tech docs (`type === 'tech'`, stored under the synthetic `__tech__` source, which has no `CONFIG`/owner row) are NOT deletable through this route — `GetSource` 404s for them, and the frontend hides the delete button accordingly for now.

**Authorization:** caller must be the source's owner (`CrawlerSource.OwnerID`, the user who first created the source via `AddSource`) or an admin (`cognito:groups` contains `admins`) — NOT merely a subscriber. `AddSource` lets any authenticated user self-subscribe to an existing source with no invite/approval step, so gating on subscription alone would make this destructive route trivially self-grantable by anyone. A source created before this field existed has `OwnerID == ""` and stays denied to every non-admin explicitly (not merely by an accidental string mismatch), indefinitely — there is no automated backfill (see `scripts/insights-backfill-owner.py`, report-only, and ADR-026 for why); an admin can set `ownerId` by hand if the real creator is known out of band. An empty caller ID is also explicitly rejected (401) so it can never accidentally match an unbackfilled `OwnerID == ""`. This route does NOT inherit `GetDocumentContent`'s open-read posture (insights are shared substrate by design for reads; a mutating route is not). A successful delete is logged with `userID`/`sourceID`/`docHash` as an audit trail.

**Delete order:** S3 object(s) are deleted before the DynamoDB row. If S3 delete fails, metadata is untouched and the request is safely retryable; deleting DynamoDB first would risk the opposite outcome — metadata gone, `GetDocument` returns nil on retry, and the S3 object + KB vector become permanently unreachable via any API path.

**Errors:**
- `400 BAD_REQUEST` — missing/invalid `sourceId` or `docHash`
- `401 UNAUTHORIZED` — no authenticated caller
- `403 FORBIDDEN` — caller is not the source's owner and not an admin
- `404 NOT_FOUND` — source or document doesn't exist (includes every tech doc, and any source without a `CONFIG` row)
- `500 INTERNAL_ERROR` — S3 or DynamoDB delete failed; per the ordering above, a failure here always means DynamoDB metadata is still intact and the request can be retried

**KB vector caveat:** deleting the S3 object does not immediately evict it from the Bedrock Knowledge Base's vector index — that only reconciles on an ingestion job. `InsightsHandler.DeleteDocument` triggers one itself, best-effort, right after a successful delete (same `KBService.SyncKB` as `POST /api/kb/sync`) — a failure there is logged but does not turn the delete response into an error, so a deleted doc can still surface in Q&A RAG results until that job completes, or if it failed, until the next daily crawl/manual sync. `scripts/insights-rescore.py` (batch re-score + purge for existing docs ingested before the relevance gate) triggers one ingestion job itself after a purge run.

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
  2. 첨부 컨텍스트 구성: 이미지 분석 결과(다이어그램 첨부는 `첨부 다이어그램`으로 라벨링되어 mermaid 코드가 신뢰 소스로 전달) + 문서 첨부(PPTX/PDF 등, 본문 미추출) 파일명 목록
  3. Bedrock Claude Opus 4.8 호출 — 회의록에 조건부 `## 아키텍처 다이어그램` 섹션 포함 (첨부 다이어그램 mermaid가 있거나 아키텍처 논의가 구체적일 때만; 아니면 섹션 생략)
  4. 구조화된 마크다운 회의록 생성 (+ 하단에 `## 첨부 이미지`/`## 첨부 문서` — `attachment://{id}` 링크, 프론트엔드가 presigned URL로 해석)
  5. DynamoDB에 content 저장
  6. 상태를 "done"으로 업데이트
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

### 8. Convert-Doc Lambda (cmd/convert-doc, 컨테이너 이미지, ADR-022)
- **트리거**: S3 Event (docs/ prefix, .ppt/.pptx만) via EventBridge
- **역할**: 업로드된 PPTX/PPT를 headless LibreOffice로 PDF 사이드카로 변환
  (in-browser 미리보기용, 기존 PDF `<iframe>` 뷰어 재사용)
- **처리**:
  1. S3에서 PPTX/PPT 다운로드
  2. `soffice --headless --convert-to pdf` 실행 (AWS_* 환경변수 제거 후
     exec — 신뢰 불가 입력 파싱 시 자격증명 유출 방지)
  3. 결정론적 사이드카 키로 PDF 업로드 (DynamoDB 쓰기 없음)
- **IAM**: `docs/*` 읽기 + `docs-pdf/*` 쓰기로 스코프(다른 업로드 카테고리의
  버킷 전체 `grantReadWrite`보다 좁음)
- **환경변수**: BUCKET_NAME (미설정 시 콜드스타트에서 즉시 `log.Fatal`)

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
