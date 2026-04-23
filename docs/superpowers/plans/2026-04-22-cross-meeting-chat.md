# Cross-Meeting Chat Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/chat` page where users can ask questions across all their meetings (owned + shared) using the existing KB + QA infrastructure.

**Architecture:** Extend the existing `ttobak-qa` Lambda with shared-meeting KB filters and a `list_meetings` tool. Add Go handler for chat session CRUD. Create a fullscreen `/chat` page reusing `QAChatMessage` and `RealtimeWebSocket`.

**Tech Stack:** Python (QA Lambda), Go (API Lambda), React/Next.js (frontend), DynamoDB, Bedrock KB

---

### Task 1: Backend — KB Filter Expansion for Shared Meetings

**Files:**
- Modify: `backend/python/qa/handler.py`

- [ ] **Step 1: Add `_list_shared_meetings` helper**

Add after the existing `_kb_cache_put` function (around line 160):

```python
_shared_meetings_cache = {}

def _list_shared_meetings(user_id):
    """List meetings shared with this user. Cached per-invocation."""
    if user_id in _shared_meetings_cache:
        return _shared_meetings_cache[user_id]
    try:
        result = table.query(
            KeyConditionExpression='PK = :pk AND begins_with(SK, :sk)',
            ExpressionAttributeValues={
                ':pk': f'USER#{user_id}',
                ':sk': 'SHARED#',
            },
            ProjectionExpression='meetingId, ownerId',
        )
        items = [{'meetingId': i['meetingId'], 'ownerId': i.get('ownerId', '')} for i in result.get('Items', [])]
        _shared_meetings_cache[user_id] = items
        return items
    except Exception as e:
        logger.warning(f"Failed to list shared meetings: {e}")
        return []
```

- [ ] **Step 2: Update `retrieve_from_kb` to include shared meeting paths**

Replace the filter block in `retrieve_from_kb` (lines 179-185):

```python
        if user_id:
            filters = [
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'kb/{user_id}/'}},
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'meetings/{user_id}/'}},
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': 'shared/'}},
            ]
            for s in _list_shared_meetings(user_id):
                if s.get('ownerId') and s.get('meetingId'):
                    filters.append({
                        'stringContains': {
                            'key': 'x-amz-bedrock-kb-source-uri',
                            'value': f"meetings/{s['ownerId']}/{s['meetingId']}"
                        }
                    })
            retrieval_config['vectorSearchConfiguration']['filter'] = {'orAll': filters}
```

- [ ] **Step 3: Commit**

```bash
cd /home/ec2-user/ttobak
git add backend/python/qa/handler.py
git commit -m "feat(qa): expand KB filter to include shared meetings"
```

---

### Task 2: Backend — `list_meetings` Tool

**Files:**
- Modify: `backend/python/qa/tools.py`
- Modify: `backend/python/qa/handler.py`

- [ ] **Step 1: Add tool definition to `tools.py`**

Add to the `TOOL_DEFINITIONS` list in `tools.py` after the `get_aws_recommendation` entry:

```python
    {
        "toolSpec": {
            "name": "list_meetings",
            "description": "사용자의 미팅 목록을 검색합니다. 본인 미팅과 공유받은 미팅 모두 포함. 날짜, 태그, 키워드로 필터링 가능합니다.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "dateFrom": {"type": "string", "description": "시작 날짜 (ISO 8601, 예: 2026-04-01)"},
                        "dateTo": {"type": "string", "description": "종료 날짜 (ISO 8601, 예: 2026-04-22)"},
                        "tag": {"type": "string", "description": "태그 필터 (예: eks, database)"},
                        "keyword": {"type": "string", "description": "제목 키워드 검색"},
                        "limit": {"type": "integer", "description": "최대 결과 수 (기본 20)"}
                    }
                }
            }
        }
    }
```

- [ ] **Step 2: Add `list_meetings_for_user` function to `handler.py`**

Add after `_list_shared_meetings`:

```python
def list_meetings_for_user(user_id, date_from=None, date_to=None, tag=None, keyword=None, limit=20):
    """List meetings (owned + shared) with optional filters."""
    meetings = []

    # 1. Own meetings
    try:
        result = table.query(
            KeyConditionExpression='PK = :pk AND begins_with(SK, :sk)',
            ExpressionAttributeValues={
                ':pk': f'USER#{user_id}',
                ':sk': 'MEETING#',
            },
            ProjectionExpression='meetingId, title, createdAt, tags, #s',
            ExpressionAttributeNames={'#s': 'status'},
        )
        for item in result.get('Items', []):
            meetings.append({
                'meetingId': item.get('meetingId', ''),
                'title': item.get('title', ''),
                'date': item.get('createdAt', '')[:10],
                'tags': item.get('tags', []) if isinstance(item.get('tags'), list) else [],
                'status': item.get('status', ''),
                'isShared': False,
            })
    except Exception as e:
        logger.warning(f"Failed to list own meetings: {e}")

    # 2. Shared meetings
    for s in _list_shared_meetings(user_id):
        try:
            mid = s['meetingId']
            owner = s['ownerId']
            if not owner:
                continue
            res = table.get_item(Key={'PK': f'USER#{owner}', 'SK': f'MEETING#{mid}'}, ConsistentRead=True)
            item = res.get('Item')
            if item:
                meetings.append({
                    'meetingId': mid,
                    'title': item.get('title', ''),
                    'date': item.get('createdAt', '')[:10],
                    'tags': item.get('tags', []) if isinstance(item.get('tags'), list) else [],
                    'status': item.get('status', ''),
                    'isShared': True,
                    'sharedBy': item.get('email', owner),
                })
        except Exception as e:
            logger.warning(f"Failed to get shared meeting {s}: {e}")

    # Apply filters
    if date_from:
        meetings = [m for m in meetings if m['date'] >= date_from]
    if date_to:
        meetings = [m for m in meetings if m['date'] <= date_to]
    if tag:
        tag_lower = tag.lower()
        meetings = [m for m in meetings if any(tag_lower in t.lower() for t in m.get('tags', []))]
    if keyword:
        kw_lower = keyword.lower()
        meetings = [m for m in meetings if kw_lower in m.get('title', '').lower()]

    meetings.sort(key=lambda m: m.get('date', ''), reverse=True)
    return meetings[:limit]
```

- [ ] **Step 3: Register `list_meetings` in `execute_tool`**

Add a new `elif` branch in `execute_tool` in `tools.py`:

```python
        elif tool_name == "list_meetings":
            user_id = context.get("user_id")
            if not user_id:
                return "사용자 인증 정보가 없습니다.", []
            results = context["list_meetings"](
                user_id,
                date_from=tool_input.get("dateFrom"),
                date_to=tool_input.get("dateTo"),
                tag=tool_input.get("tag"),
                keyword=tool_input.get("keyword"),
                limit=tool_input.get("limit", 20),
            )
            if not results:
                return "조건에 맞는 미팅을 찾지 못했습니다.", []
            lines = []
            for m in results:
                shared_tag = " [공유]" if m.get("isShared") else ""
                tags_str = ", ".join(m.get("tags", []))
                lines.append(f"- **{m['title']}** ({m['date']}){shared_tag} [tags: {tags_str}] [ID: {m['meetingId']}]")
            return f"총 {len(results)}건의 미팅:\n" + "\n".join(lines), []
```

- [ ] **Step 4: Pass `list_meetings` and `user_id` in context**

In `handler.py`, update the `context` dict in `agentic_converse` (around line 198) and `agentic_converse_stream` (around line 550):

```python
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": lambda q, n=5: retrieve_from_kb(q, n, user_id=user_id),
        "list_meetings": list_meetings_for_user,
        "user_id": user_id,
    }
```

Do the same in `agentic_converse_stream`.

- [ ] **Step 5: Commit**

```bash
git add backend/python/qa/handler.py backend/python/qa/tools.py
git commit -m "feat(qa): add list_meetings tool for cross-meeting search"
```

---

### Task 3: Backend — Chat Session CRUD (Go API)

**Files:**
- Create: `backend/internal/handler/chat.go`
- Modify: `backend/internal/repository/dynamodb.go`
- Modify: `backend/cmd/api/main.go`

- [ ] **Step 1: Add chat session repository methods**

Add to `backend/internal/repository/dynamodb.go`:

```go
// ChatSession represents a chat session record
type ChatSession struct {
    SessionID     string `dynamodbav:"sessionId" json:"sessionId"`
    Title         string `dynamodbav:"title" json:"title"`
    CreatedAt     string `dynamodbav:"createdAt" json:"createdAt"`
    LastMessageAt string `dynamodbav:"lastMessageAt" json:"lastMessageAt"`
    MessageCount  int    `dynamodbav:"messageCount" json:"messageCount"`
}

// ListChatSessions lists chat sessions for a user
func (r *DynamoDBRepository) ListChatSessions(ctx context.Context, userID string) ([]ChatSession, error) {
    keyEx := expression.Key("PK").Equal(expression.Value(model.PrefixUser + userID)).
        And(expression.Key("SK").BeginsWith("CHAT_SESSION#"))
    expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
    if err != nil {
        return nil, fmt.Errorf("failed to build expression: %w", err)
    }

    result, err := r.client.Query(ctx, &dynamodb.QueryInput{
        TableName:                 aws.String(r.tableName),
        KeyConditionExpression:    expr.KeyCondition(),
        ExpressionAttributeNames:  expr.Names(),
        ExpressionAttributeValues: expr.Values(),
        ScanIndexForward:          aws.Bool(false),
    })
    if err != nil {
        return nil, fmt.Errorf("failed to list chat sessions: %w", err)
    }

    var sessions []ChatSession
    for _, item := range result.Items {
        var s ChatSession
        if err := attributevalue.UnmarshalMap(item, &s); err == nil {
            sessions = append(sessions, s)
        }
    }
    return sessions, nil
}

// DeleteChatSession deletes a chat session and its messages
func (r *DynamoDBRepository) DeleteChatSession(ctx context.Context, userID, sessionID string) error {
    _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
        TableName: aws.String(r.tableName),
        Key: map[string]types.AttributeValue{
            "PK": &types.AttributeValueMemberS{Value: model.PrefixUser + userID},
            "SK": &types.AttributeValueMemberS{Value: "CHAT_SESSION#" + sessionID},
        },
    })
    if err != nil {
        return fmt.Errorf("failed to delete chat session: %w", err)
    }
    // Also delete messages
    r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
        TableName: aws.String(r.tableName),
        Key: map[string]types.AttributeValue{
            "PK": &types.AttributeValueMemberS{Value: "SESSION#" + userID + "#" + sessionID},
            "SK": &types.AttributeValueMemberS{Value: "MESSAGES"},
        },
    })
    return nil
}
```

- [ ] **Step 2: Create chat handler**

Create `backend/internal/handler/chat.go`:

```go
package handler

import (
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/ttobak/backend/internal/middleware"
    "github.com/ttobak/backend/internal/repository"
)

type ChatHandler struct {
    repo *repository.DynamoDBRepository
}

func NewChatHandler(repo *repository.DynamoDBRepository) *ChatHandler {
    return &ChatHandler{repo: repo}
}

func (h *ChatHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    userID := middleware.GetUserID(ctx)

    sessions, err := h.repo.ListChatSessions(ctx, userID)
    if err != nil {
        writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
        return
    }
    if sessions == nil {
        sessions = []repository.ChatSession{}
    }
    writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

func (h *ChatHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    userID := middleware.GetUserID(ctx)
    sessionID := chi.URLParam(r, "sessionId")

    if sessionID == "" {
        writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Session ID required")
        return
    }

    if err := h.repo.DeleteChatSession(ctx, userID, sessionID); err != nil {
        writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
        return
    }
    w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 3: Register routes in `main.go`**

Add after the crawler handler initialization (around line 88):

```go
chatHandler := handler.NewChatHandler(repo)
```

Add routes inside the authenticated group (after research routes, around line 179):

```go
        // Chat session routes
        r.Get("/api/chat/sessions", chatHandler.ListSessions)
        r.Delete("/api/chat/sessions/{sessionId}", chatHandler.DeleteSession)
```

- [ ] **Step 4: Build and verify**

```bash
cd /home/ec2-user/ttobak/backend
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

Expected: build succeeds with no errors.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/handler/chat.go backend/internal/repository/dynamodb.go backend/cmd/api/main.go
git commit -m "feat(api): add chat session list/delete endpoints"
```

---

### Task 4: Backend — Session Title Auto-Generation

**Files:**
- Modify: `backend/python/qa/handler.py`

- [ ] **Step 1: Add session title generation in `save_session`**

Update `save_session` in `handler.py` to also create/update the `CHAT_SESSION#` metadata record when the session prefix is `chat-`:

```python
def save_session(session_id, messages, user_id=None):
    """Save conversation history to DynamoDB with 7-day TTL."""
    if not session_id:
        return
    pk = f"SESSION#{user_id}#{session_id}" if user_id else f"SESSION#{session_id}"
    msg_count = len([m for m in messages if m.get('role') == 'user'])
    try:
        table.put_item(Item={
            "PK": pk,
            "SK": "MESSAGES",
            "messages": json.dumps(messages, ensure_ascii=False),
            "TTL": int(time.time()) + 604800,  # 7 days
        })
        # Auto-create/update CHAT_SESSION metadata for chat-* sessions
        if user_id and session_id.startswith('chat-'):
            first_question = ''
            for m in messages:
                if m.get('role') == 'user':
                    content = m.get('content', [])
                    if isinstance(content, list) and content:
                        first_question = content[0].get('text', '')[:50]
                    break
            title = first_question or '새 대화'
            now = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            table.put_item(Item={
                "PK": f"USER#{user_id}",
                "SK": f"CHAT_SESSION#{session_id}",
                "sessionId": session_id,
                "title": title,
                "createdAt": now,
                "lastMessageAt": now,
                "messageCount": msg_count,
                "entityType": "CHAT_SESSION",
                "TTL": int(time.time()) + 2592000,  # 30 days
            })
    except Exception as e:
        logger.warning(f"Failed to save session {session_id}: {e}")
```

- [ ] **Step 2: Commit**

```bash
git add backend/python/qa/handler.py
git commit -m "feat(qa): auto-generate chat session metadata on save"
```

---

### Task 5: Frontend — API Client + Types

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/types/meeting.ts`

- [ ] **Step 1: Add ChatSession type**

Add to `frontend/src/types/meeting.ts`:

```typescript
export interface ChatSession {
  sessionId: string;
  title: string;
  createdAt: string;
  lastMessageAt: string;
  messageCount: number;
}
```

- [ ] **Step 2: Add chatApi to `api.ts`**

Add after the existing `summaryApi` export:

```typescript
export const chatApi = {
  listSessions: () =>
    api.get<{ sessions: import('@/types/meeting').ChatSession[] }>('/api/chat/sessions'),

  deleteSession: (sessionId: string) =>
    api.delete(`/api/chat/sessions/${sessionId}`),
};
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/types/meeting.ts
git commit -m "feat(api): add chatApi client for session management"
```

---

### Task 6: Frontend — `/chat` Page

**Files:**
- Create: `frontend/src/app/chat/page.tsx`
- Create: `frontend/src/app/chat/ChatClient.tsx`

- [ ] **Step 1: Create page wrapper**

Create `frontend/src/app/chat/page.tsx`:

```tsx
'use client';

import { Suspense } from 'react';
import { ChatClient } from './ChatClient';

export default function ChatPage() {
  return (
    <Suspense fallback={
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    }>
      <ChatClient />
    </Suspense>
  );
}
```

- [ ] **Step 2: Create ChatClient component**

Create `frontend/src/app/chat/ChatClient.tsx`:

```tsx
'use client';

import { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { qaApi, chatApi } from '@/lib/api';
import { RealtimeWebSocket, type WebSocketMessage } from '@/lib/websocket';
import { QAChatMessage } from '@/components/qa';
import type { ChatSession } from '@/types/meeting';

interface ChatEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  isStreaming?: boolean;
}

const suggestedQuestions = [
  '이번 주 미팅 요약해줘',
  '미완료 액션아이템 모아줘',
  '최근 공유받은 미팅 정리해줘',
  'EKS 관련 논의 요약해줘',
];

const WS_URL = process.env.NEXT_PUBLIC_WEBSOCKET_URL || '';

export function ChatClient() {
  const router = useRouter();
  const { user, isAuthenticated, isLoading } = useAuth();
  const [question, setQuestion] = useState('');
  const [chatHistory, setChatHistory] = useState<ChatEntry[]>([]);
  const [isAsking, setIsAsking] = useState(false);
  const [sessions, setSessions] = useState<ChatSession[]>([]);
  const [showSessions, setShowSessions] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const wsRef = useRef<RealtimeWebSocket | null>(null);
  const activeEntryIdRef = useRef<string | null>(null);

  const sessionId = useMemo(() => {
    if (!user) return '';
    return `chat-${user.userId}-${Date.now()}`;
  }, [user]);

  const [currentSessionId, setCurrentSessionId] = useState('');

  useEffect(() => {
    if (sessionId) setCurrentSessionId(sessionId);
  }, [sessionId]);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [chatHistory]);

  useEffect(() => {
    if (isAuthenticated) {
      chatApi.listSessions().then(r => setSessions(r.sessions)).catch(() => {});
    }
  }, [isAuthenticated]);

  const handleStreamMessage = useCallback((msg: WebSocketMessage) => {
    const entryId = activeEntryIdRef.current;
    if (!entryId) return;

    switch (msg.type) {
      case 'answer_delta':
        setChatHistory(prev =>
          prev.map(e => e.id === entryId ? { ...e, answer: e.answer + (msg.text || '') } : e)
        );
        break;
      case 'answer_complete':
        setChatHistory(prev =>
          prev.map(e => e.id === entryId ? {
            ...e,
            answer: msg.answer || e.answer,
            sources: msg.sources,
            usedKB: msg.usedKB,
            usedDocs: msg.usedDocs,
            toolsUsed: msg.toolsUsed,
            isStreaming: false,
          } : e)
        );
        setIsAsking(false);
        activeEntryIdRef.current = null;
        inputRef.current?.focus();
        break;
      case 'answer_error':
        setChatHistory(prev =>
          prev.map(e => e.id === entryId ? { ...e, answer: msg.error || '오류가 발생했습니다.', isStreaming: false } : e)
        );
        setIsAsking(false);
        activeEntryIdRef.current = null;
        break;
    }
  }, []);

  const ensureWebSocket = useCallback(async (): Promise<RealtimeWebSocket | null> => {
    if (!WS_URL) return null;
    if (wsRef.current?.isConnected) return wsRef.current;
    const ws = new RealtimeWebSocket(WS_URL, handleStreamMessage, () => { wsRef.current = null; });
    try {
      await ws.connect();
      wsRef.current = ws;
      return ws;
    } catch {
      return null;
    }
  }, [handleStreamMessage]);

  useEffect(() => {
    return () => { wsRef.current?.disconnect(); };
  }, []);

  const handleAsk = async (q: string) => {
    if (!q.trim() || isAsking) return;
    setQuestion('');
    setIsAsking(true);

    const entryId = Date.now().toString();
    setChatHistory(prev => [...prev, { id: entryId, question: q.trim(), answer: '', isStreaming: true }]);
    activeEntryIdRef.current = entryId;

    const ws = await ensureWebSocket();
    if (ws) {
      ws.askLive(q.trim(), undefined, undefined, currentSessionId);
      return;
    }

    // HTTP fallback
    setChatHistory(prev => prev.map(e => e.id === entryId ? { ...e, isStreaming: false } : e));
    try {
      const response = await qaApi.ask(q.trim(), undefined, currentSessionId);
      setChatHistory(prev =>
        prev.map(e => e.id === entryId ? {
          ...e, answer: response.answer, sources: response.sources,
          usedKB: response.usedKB, usedDocs: response.usedDocs, toolsUsed: response.toolsUsed,
        } : e)
      );
    } catch {
      setChatHistory(prev =>
        prev.map(e => e.id === entryId ? { ...e, answer: '답변을 가져오지 못했습니다.' } : e)
      );
    } finally {
      setIsAsking(false);
      activeEntryIdRef.current = null;
      inputRef.current?.focus();
    }
  };

  const handleNewChat = () => {
    setChatHistory([]);
    setCurrentSessionId(`chat-${user?.userId}-${Date.now()}`);
    inputRef.current?.focus();
  };

  const handleDeleteSession = async (sid: string) => {
    try {
      await chatApi.deleteSession(sid);
      setSessions(prev => prev.filter(s => s.sessionId !== sid));
    } catch {}
  };

  if (isLoading) {
    return <div className="min-h-screen flex items-center justify-center"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>;
  }
  if (!isAuthenticated) { router.push('/'); return null; }

  return (
    <AppLayout activePath="/chat">
      <div className="flex flex-col h-[calc(100vh-4rem)] lg:h-screen">
        {/* Header */}
        <div className="flex items-center justify-between px-4 lg:px-6 py-3 border-b border-slate-100 dark:border-white/10 bg-white dark:bg-[#0e0e13]">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 rounded-lg bg-primary flex items-center justify-center">
              <span className="material-symbols-outlined text-white text-lg">smart_toy</span>
            </div>
            <div>
              <h1 className="text-sm font-bold text-slate-900 dark:text-white">Ttobak Assistant</h1>
              <p className="text-[11px] text-slate-500 dark:text-slate-400">미팅 데이터 기반 AI 어시스턴트</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <div className="relative">
              <button
                onClick={() => setShowSessions(!showSessions)}
                className="px-3 py-1.5 text-xs font-medium bg-slate-100 dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-200 dark:hover:bg-white/10 transition-colors"
              >
                이전 대화 {sessions.length > 0 && `(${sessions.length})`}
              </button>
              {showSessions && sessions.length > 0 && (
                <>
                  <div className="fixed inset-0 z-10" onClick={() => setShowSessions(false)} />
                  <div className="absolute right-0 top-full mt-1 w-72 bg-white dark:bg-slate-800 border border-slate-200 dark:border-white/10 rounded-xl shadow-xl z-20 max-h-80 overflow-y-auto">
                    {sessions.map(s => (
                      <div key={s.sessionId} className="flex items-center gap-2 px-4 py-3 hover:bg-slate-50 dark:hover:bg-white/5 transition-colors border-b border-slate-100 dark:border-white/5 last:border-0">
                        <div className="flex-1 min-w-0">
                          <p className="text-sm font-medium text-slate-900 dark:text-white truncate">{s.title}</p>
                          <p className="text-[10px] text-slate-500">{new Date(s.lastMessageAt).toLocaleDateString('ko-KR')}</p>
                        </div>
                        <button onClick={(e) => { e.stopPropagation(); handleDeleteSession(s.sessionId); }} className="text-slate-400 hover:text-red-500 transition-colors">
                          <span className="material-symbols-outlined text-sm">close</span>
                        </button>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
            <button
              onClick={handleNewChat}
              className="px-3 py-1.5 text-xs font-semibold bg-primary text-white rounded-lg hover:bg-primary/90 transition-colors"
            >
              새 대화
            </button>
          </div>
        </div>

        {/* Chat Area */}
        <div ref={containerRef} className="flex-1 overflow-y-auto px-4 lg:px-6 py-6">
          {chatHistory.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-full gap-6">
              <div className="text-center">
                <span className="material-symbols-outlined text-5xl text-slate-300 dark:text-slate-600 mb-3 block">forum</span>
                <h2 className="text-lg font-bold text-slate-900 dark:text-white">미팅 데이터에 대해 질문하세요</h2>
                <p className="text-sm text-slate-500 dark:text-slate-400 mt-1">본인 미팅 + 공유받은 미팅에서 검색합니다</p>
              </div>
              <div className="flex flex-wrap gap-2 justify-center max-w-md">
                {suggestedQuestions.map((q) => (
                  <button
                    key={q}
                    onClick={() => handleAsk(q)}
                    disabled={isAsking}
                    className="px-4 py-2 bg-white dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-lg text-sm text-primary dark:text-[#00E5FF] hover:border-primary/30 dark:hover:border-[#00E5FF]/30 transition-colors disabled:opacity-50"
                  >
                    {q}
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div className="max-w-3xl mx-auto space-y-4">
              {chatHistory.map(entry => (
                <QAChatMessage
                  key={entry.id}
                  question={entry.question}
                  answer={entry.answer}
                  sources={entry.sources}
                  usedKB={entry.usedKB}
                  usedDocs={entry.usedDocs}
                  toolsUsed={entry.toolsUsed}
                  isStreaming={entry.isStreaming}
                />
              ))}
            </div>
          )}
        </div>

        {/* Input */}
        <div className="px-4 lg:px-6 py-4 border-t border-slate-100 dark:border-white/10 bg-white dark:bg-[#0e0e13]">
          <form onSubmit={(e) => { e.preventDefault(); handleAsk(question); }} className="max-w-3xl mx-auto flex items-center gap-2">
            <input
              ref={inputRef}
              type="text"
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              placeholder="미팅 데이터에 대해 질문하세요..."
              className="flex-1 px-4 py-3 text-sm bg-slate-100 dark:bg-white/5 border border-slate-200 dark:border-white/10 rounded-xl focus:ring-2 focus:ring-primary/20 placeholder:text-slate-400"
              disabled={isAsking}
            />
            <button
              type="submit"
              disabled={!question.trim() || isAsking}
              className="flex items-center justify-center w-11 h-11 rounded-xl bg-primary text-white hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              <span className="material-symbols-outlined text-xl">send</span>
            </button>
          </form>
        </div>
      </div>
    </AppLayout>
  );
}
```

- [ ] **Step 3: Build and verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build
```

Expected: 11 pages generated including `/chat`.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/app/chat/
git commit -m "feat: add /chat page — fullscreen cross-meeting AI assistant"
```

---

### Task 7: Frontend — Navigation Updates

**Files:**
- Modify: `frontend/src/components/layout/Sidebar.tsx`
- Modify: `frontend/src/components/layout/MobileNav.tsx`

- [ ] **Step 1: Add Assistant to sidebar nav**

In `Sidebar.tsx`, add to the `mainNav` array:

```typescript
const mainNav = [
  { href: '/', icon: 'video_camera_front', label: 'Meetings' },
  { href: '/chat', icon: 'smart_toy', label: 'Assistant' },
  { href: '/files', icon: 'description', label: 'Files' },
  { href: '/kb', icon: 'library_books', label: 'Knowledge Base' },
  { href: '/insights', icon: 'insights', label: 'Insights' },
  { href: '/settings', icon: 'settings', label: 'Settings' },
];
```

- [ ] **Step 2: Add Assistant to mobile nav**

In `MobileNav.tsx`, add to the `navItems` array:

```typescript
const navItems = [
  { href: '/', icon: 'home', label: 'Home' },
  { href: '/chat', icon: 'smart_toy', label: 'AI' },
  { href: '/record', icon: 'mic', label: 'Record' },
  { href: '/insights', icon: 'insights', label: 'Insights' },
  { href: '/profile', icon: 'person', label: 'Profile' },
];
```

- [ ] **Step 3: Commit**

```bash
git add frontend/src/components/layout/Sidebar.tsx frontend/src/components/layout/MobileNav.tsx
git commit -m "feat: add Assistant to sidebar and mobile navigation"
```

---

### Task 8: Deploy and Verify

**Files:** None (deployment only)

- [ ] **Step 1: Build all Go binaries**

```bash
cd /home/ec2-user/ttobak/backend
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

- [ ] **Step 2: CDK deploy**

```bash
cd /home/ec2-user/ttobak/infra && npx cdk deploy TtobakGatewayStack --require-approval never
```

- [ ] **Step 3: Frontend build and deploy**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build
aws s3 sync out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

- [ ] **Step 4: End-to-end verification**

1. Navigate to `/chat` → empty state with suggested question chips
2. Click "이번 주 미팅 요약해줘" → AI searches KB, returns cross-meeting summary
3. Verify shared meeting data appears in results (if any meetings are shared)
4. Refresh page → "이전 대화" dropdown shows the session
5. Start new chat → "새 대화" creates fresh session
6. Verify WebSocket streaming works (token-by-token display)

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "feat: cross-meeting chat assistant — complete implementation"
git push origin feat/recording-equalizer-live-transcription
```
