# Cross-Meeting Chat Assistant Design Spec

## Overview

팀 내 공유된 미팅 데이터를 KB로 활용하여 자유 질문 챗봇으로 크로스-미팅 검색 및 분석을 제공하는 `/chat` 페이지.

**사용자**: SA 팀 (본인 미팅 + 공유받은 미팅에 접근)
**형태**: 자유 질문 챗봇 (풀스크린, 세션 이력 지원)
**접근 방식**: 기존 QA 인프라(KB 필터 확장 + 도구 추가)

## Architecture

```
/chat 페이지 (풀스크린 챗 UI)
    │ qaApi.ask() — WebSocket streaming or HTTP fallback
    ▼
ttobak-qa Lambda (기존, 수정)
    ├─ search_knowledge_base (수정: 공유 미팅 필터 추가)
    ├─ list_meetings (NEW: 날짜/태그/키워드 메타데이터 검색)
    ├─ search_transcript (기존)
    ├─ search_aws_docs (기존)
    └─ get_aws_recommendation (기존)
    │
    ▼
Bedrock KB (OpenSearch Serverless)
    meetings/{userA}/*.md + meetings/{userB}/*.md
    kb/{user}/*.pdf + shared/crawled/*.md
```

## Backend Changes

### 1. KB Filter Expansion (`handler.py` — `retrieve_from_kb`)

현재 필터: `kb/{userId}/` + `meetings/{userId}/` + `shared/`

변경: 공유받은 미팅의 owner 경로도 포함.

```python
def _get_accessible_meeting_filters(user_id):
    """Build KB URI filters including shared meetings."""
    filters = [
        {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'kb/{user_id}/'}},
        {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'meetings/{user_id}/'}},
        {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': 'shared/'}},
    ]
    # Add shared meeting paths
    shared = _list_shared_meetings(user_id)
    for s in shared:
        filters.append({
            'stringContains': {
                'key': 'x-amz-bedrock-kb-source-uri',
                'value': f"meetings/{s['ownerId']}/{s['meetingId']}"
            }
        })
    return {'orAll': filters}
```

`_list_shared_meetings`: DynamoDB query `PK=USER#{userId}, SK begins_with SHARED#` — 캐시 가능 (세션 내 1회 조회).

### 2. New Tool: `list_meetings` (`tools.py`)

```python
TOOL_DEFINITION = {
    "name": "list_meetings",
    "description": "사용자의 미팅 목록을 검색합니다. 본인 미팅과 공유받은 미팅 모두 포함. 날짜, 태그, 키워드로 필터링 가능.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "dateFrom": {"type": "string", "description": "시작 날짜 (ISO 8601, 예: 2026-04-01)"},
            "dateTo": {"type": "string", "description": "종료 날짜 (ISO 8601)"},
            "tag": {"type": "string", "description": "태그 필터 (예: eks, database)"},
            "keyword": {"type": "string", "description": "제목/내용 키워드 검색"},
            "limit": {"type": "integer", "description": "최대 결과 수 (기본 20)"}
        }
    }
}
```

구현: DynamoDB에서 `PK=USER#{userId}, SK begins_with MEETING#` + `SK begins_with SHARED#` 조회. 날짜/태그/키워드는 클라이언트 사이드 필터링 (DynamoDB scan 없이 두 쿼리 결과를 합산 후 필터).

반환 형식:
```json
[
  {"meetingId": "...", "title": "...", "date": "2026-04-15", "tags": ["eks", "gpu"], "status": "done", "isShared": false},
  {"meetingId": "...", "title": "...", "date": "2026-04-20", "tags": ["backend"], "status": "done", "isShared": true, "sharedBy": "admin@ttobak.local"}
]
```

### 3. Session Management

**DynamoDB 스키마:**

세션 목록:
```
PK: USER#{userId}
SK: CHAT_SESSION#{sessionId}
Attributes: title, createdAt, lastMessageAt, messageCount, entityType=CHAT_SESSION
```

세션 메시지 (기존 구조 재사용):
```
PK: SESSION#{userId}#{sessionId}
SK: MESSAGES
Attributes: messages (JSON), TTL (7일)
```

**세션 라이프사이클:**
1. 새 대화 시작 → 프론트엔드가 `sessionId = chat-{userId}-{timestamp}` 생성
2. 첫 번째 응답 후 → 백엔드가 세션 제목 자동 생성 (질문에서 추출)
3. 세션 목록 API: `GET /api/chat/sessions` → DynamoDB query `PK=USER#{userId}, SK begins_with CHAT_SESSION#`
4. TTL: 메시지는 7일, 세션 메타는 30일

**새 API 엔드포인트 (Go API Lambda에 추가):**
- `GET /api/chat/sessions` — 세션 목록
- `DELETE /api/chat/sessions/{sessionId}` — 세션 삭제

## Frontend: `/chat` Page

### Layout (풀스크린 챗)

```
┌──────────────────────────────────────────────┐
│ [AI icon] Ttobak Assistant    [이전 대화 ▼] [새 대화] │
├──────────────────────────────────────────────┤
│                                              │
│  (빈 상태: 추천 질문 칩)                       │
│  [이번 주 미팅 요약] [미완료 액션아이템]        │
│  [EKS 관련 논의] [최근 공유받은 미팅]           │
│                                              │
│  ── 또는 대화 진행 시 ──                       │
│                                              │
│  사용자: 최근 EKS 관련 고객 미팅 요약해줘      │
│                                              │
│  AI: 최근 2주간 3건의 EKS 미팅이 있었습니다... │
│      [KB 검색] [3건 미팅]                      │
│      출처: 하나금융 미팅 (4/15), ...           │
│                                              │
├──────────────────────────────────────────────┤
│ [질문 입력]                            [전송] │
└──────────────────────────────────────────────┘
```

### Components

**새 파일:**
- `frontend/src/app/chat/page.tsx` — 챗 페이지 (Suspense wrapper)
- `frontend/src/app/chat/ChatClient.tsx` — 클라이언트 컴포넌트

**재사용:**
- `QAChatMessage` — 메시지 버블 (isStreaming 지원)
- `RealtimeWebSocket` — WebSocket 스트리밍
- `qaApi.ask()` — 기존 QA 엔드포인트 (meetingId 없이 호출)

**새 API 메서드 (`api.ts`):**
```typescript
export const chatApi = {
  listSessions: () => api.get<{ sessions: ChatSession[] }>('/api/chat/sessions'),
  deleteSession: (sessionId: string) => api.delete(`/api/chat/sessions/${sessionId}`),
};
```

### 세션 이력 드롭다운

상단 "이전 대화" 버튼 클릭 시 드롭다운:
- 최근 세션 목록 (제목 + 날짜)
- 클릭 시 해당 세션의 메시지 로드
- 삭제 버튼 (스와이프 or hover)

### 추천 질문 칩

빈 대화 상태에서 표시:
- "이번 주 미팅 요약"
- "미완료 액션아이템 모아줘"
- "최근 공유받은 미팅 정리"
- "EKS 관련 논의 요약"

### Navigation

사이드바에 새 메뉴 추가:
```typescript
{ href: '/chat', icon: 'smart_toy', label: 'Assistant' }
```

모바일 하단 네비게이션에도 추가.

## Data Flow

1. 사용자가 `/chat`에서 질문 입력
2. 프론트엔드 → `qaApi.ask(question, undefined, sessionId)` (context 없음, sessionId는 `chat-{userId}-{ts}`)
3. QA Lambda: `streamMode == 'ask_live'` → WebSocket 스트리밍 or HTTP 동기
4. 에이전틱 루프:
   - AI가 `list_meetings` 호출 → 관련 미팅 목록 반환
   - AI가 `search_knowledge_base` 호출 → 공유 미팅 포함 벡터 검색
   - AI가 종합 답변 생성
5. 답변 + 소스(미팅 링크) 반환
6. 프론트엔드: 소스의 meetingId를 `/meeting/{id}` 링크로 변환

## Security

- KB 필터는 항상 `user_id` 기반: 본인 미팅 + 공유받은 미팅만 검색
- `list_meetings` 도구도 DynamoDB에서 본인 + SHARED# 레코드만 조회
- 타인의 미팅에는 접근 불가 (SHARED 레코드가 없으면 필터에 포함 안 됨)
- 세션 데이터는 `SESSION#{userId}#` 스코프로 격리

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `frontend/src/app/chat/page.tsx` | NEW | 챗 페이지 wrapper |
| `frontend/src/app/chat/ChatClient.tsx` | NEW | 풀스크린 챗 클라이언트 |
| `frontend/src/lib/api.ts` | MODIFY | `chatApi` 추가 |
| `frontend/src/components/layout/Sidebar.tsx` | MODIFY | Assistant 메뉴 추가 |
| `frontend/src/components/layout/MobileNav.tsx` | MODIFY | 모바일 네비에 추가 |
| `backend/python/qa/handler.py` | MODIFY | KB 필터 확장, 세션 제목 자동생성 |
| `backend/python/qa/tools.py` | MODIFY | `list_meetings` 도구 추가 |
| `backend/internal/handler/chat.go` | NEW | 세션 목록/삭제 핸들러 |
| `backend/internal/repository/dynamodb.go` | MODIFY | 챗 세션 CRUD |
| `backend/cmd/api/main.go` | MODIFY | `/api/chat/*` 라우트 추가 |

## Verification

1. `/chat` 페이지 접속 → 빈 상태에서 추천 질문 표시
2. "이번 주 미팅 요약해줘" 질문 → KB에서 미팅 검색 + 종합 답변
3. 다른 사용자가 공유한 미팅도 검색 결과에 포함 확인
4. 페이지 이탈 후 재접속 → "이전 대화" 드롭다운에 세션 표시
5. 세션 클릭 → 이전 대화 내용 로드
6. WebSocket 스트리밍 → 토큰 단위 표시
7. 소스 링크 클릭 → 해당 미팅 상세 페이지로 이동
