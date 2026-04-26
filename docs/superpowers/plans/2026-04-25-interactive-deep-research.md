# Interactive Deep Research Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transform Research from fire-and-forget to conversational: planning chat before execution, post-completion modifications, and Notion-style sub-page hierarchy.

**Architecture:** Polling-based async chat via DynamoDB ChatMessage entity. Research gains `planning`/`approved` states. Agent operates in 4 modes (plan/respond/execute/subpage). Frontend adds chat panel + page tree.

**Tech Stack:** Go (Lambda), DynamoDB, Step Functions, AgentCore (FastAPI/Python), Next.js 16, TypeScript

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `backend/internal/model/chat.go` | ChatMessage model + request/response types |
| `backend/internal/repository/chat.go` | ChatMessage DynamoDB CRUD |
| `backend/internal/handler/chat_research.go` | GET/POST /api/research/{id}/chat + GET subpages |
| `frontend/src/components/ResearchChat.tsx` | Chat panel component |
| `frontend/src/components/ResearchPageTree.tsx` | Sub-page tree navigation |

### Modified Files
| File | Change |
|------|--------|
| `backend/internal/model/meeting.go` | Add `parentId` to Research |
| `backend/internal/service/research.go` | Planning flow, sub-page creation, chat trigger |
| `backend/internal/repository/research.go` | ListSubPages query |
| `backend/cmd/api/main.go` | Register chat routes |
| `backend/cmd/research-worker/main.go` | Mode routing (plan/respond/execute/subpage) |
| `backend/python/research-agent/agent.py` | Plan/respond mode prompts |
| `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx` | Integrate chat + page tree |
| `frontend/src/lib/api.ts` | Chat API methods |
| `frontend/src/types/meeting.ts` | ChatMessage type, Research parentId |

---

### Task 1: ChatMessage Model + Repository

**Files:**
- Create: `backend/internal/model/chat.go`
- Create: `backend/internal/repository/chat.go`

- [ ] **Step 1: Create chat.go model**

ChatMessage struct with PK/SK, msgId, role, content, action, metadata, createdAt. Request/response types for API.

- [ ] **Step 2: Create chat repository**

Methods: `SaveMessage(ctx, researchId, msg)`, `ListMessages(ctx, researchId)` (query PK=RESEARCH#{id}, SK begins_with MSG#, ScanIndexForward=true).

- [ ] **Step 3: Build + test**
- [ ] **Step 4: Commit**: `feat(research): ChatMessage model + DynamoDB repository`

---

### Task 2: Research Model Extension

**Files:**
- Modify: `backend/internal/model/meeting.go`
- Modify: `backend/internal/repository/research.go`

- [ ] **Step 1: Add parentId to Research struct**

Add `ParentID string dynamodbav:"parentId,omitempty" json:"parentId,omitempty"` field.

- [ ] **Step 2: Add ListSubPages to repository**

Query user's research list, filter by parentId == given researchId.

- [ ] **Step 3: Update CreateResearch to accept parentId**
- [ ] **Step 4: Build + test**
- [ ] **Step 5: Commit**: `feat(research): add parentId for sub-page hierarchy`

---

### Task 3: Chat API Handlers

**Files:**
- Create: `backend/internal/handler/chat_research.go`
- Modify: `backend/cmd/api/main.go`

- [ ] **Step 1: Create handler with 3 endpoints**

`GET /api/research/{researchId}/chat` — list messages
`POST /api/research/{researchId}/chat` — send message + trigger agent
`GET /api/research/{researchId}/subpages` — list sub-pages

- [ ] **Step 2: POST handler logic**

Based on `action`:
- none: save user message, trigger SFN with mode "respond"
- "approve": save message, update Research status to "approved", trigger SFN with mode "execute"
- "request_subpage": create child Research with parentId, trigger SFN with mode "subpage"

- [ ] **Step 3: Register routes in main.go**
- [ ] **Step 4: Build + test**
- [ ] **Step 5: Commit**: `feat(research): chat API endpoints — list, send, subpages`

---

### Task 4: Research Service Planning Flow

**Files:**
- Modify: `backend/internal/service/research.go`

- [ ] **Step 1: Modify CreateResearch**

Change initial status from "running" to "planning". Trigger SFN with `mode: "plan"` instead of direct execution.

- [ ] **Step 2: Add SendChatMessage method**

Save message to DynamoDB, trigger appropriate SFN mode based on action.

- [ ] **Step 3: Add CreateSubPage method**

Create child Research with parentId, status "running", trigger SFN with mode "subpage" + parent context.

- [ ] **Step 4: Build + test**
- [ ] **Step 5: Commit**: `feat(research): planning flow + chat message + sub-page creation`

---

### Task 5: Research Worker Mode Routing

**Files:**
- Modify: `backend/cmd/research-worker/main.go`

- [ ] **Step 1: Add mode field to ResearchEvent**

`Mode string json:"mode"` — values: "plan", "respond", "execute", "subpage"

- [ ] **Step 2: Route by mode in handler**

```go
switch event.Mode {
case "plan":    return handlePlan(ctx, event)
case "respond": return handleRespond(ctx, event)
case "execute": return handleExecute(ctx, event)  // existing behavior
case "subpage": return handleSubpage(ctx, event)
default:        return handleExecute(ctx, event)   // backward compat
}
```

- [ ] **Step 3: Implement handlePlan**

Invoke AgentCore with `{ mode: "plan", topic, researchId }`. Agent proposes structure, saves ChatMessages to DDB.

- [ ] **Step 4: Implement handleRespond**

Invoke AgentCore with `{ mode: "respond", researchId, chatHistory }`. Agent reads history, responds.

- [ ] **Step 5: Implement handleSubpage**

Invoke AgentCore with `{ mode: "subpage", topic, researchId, parentContent }` (parent report for context).

- [ ] **Step 6: Build + test**
- [ ] **Step 7: Commit**: `feat(research-worker): mode routing — plan/respond/execute/subpage`

---

### Task 6: AgentCore Agent — Plan/Respond Modes

**Files:**
- Modify: `backend/python/research-agent/agent.py`

- [ ] **Step 1: Add PLAN_PROMPT**

System prompt for planning mode: analyze topic, propose 4-6 section structure, ask 2-3 clarifying questions. Save as ChatMessages via DynamoDB boto3.

- [ ] **Step 2: Add RESPOND_PROMPT**

System prompt for responding: read chat history, answer user question or revise structure. Save response as ChatMessage.

- [ ] **Step 3: Update /invocations to route by mode**

```python
@app.post("/invocations")
async def invoke(request):
    mode = request.mode or "execute"
    if mode == "plan": return handle_plan(request)
    elif mode == "respond": return handle_respond(request)
    elif mode == "subpage": return handle_subpage(request)
    else: return handle_execute(request)  # existing
```

- [ ] **Step 4: Implement handle_plan and handle_respond**

Both use strands Agent with appropriate prompts, save ChatMessages to DynamoDB.

- [ ] **Step 5: Implement handle_subpage**

Read parent report from S3, use as context for focused sub-topic research.

- [ ] **Step 6: Test locally if possible**
- [ ] **Step 7: Commit**: `feat(research-agent): plan/respond/subpage modes`

---

### Task 7: Frontend Types + API

**Files:**
- Modify: `frontend/src/types/meeting.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add ChatMessage type**

```typescript
export interface ChatMessage {
  msgId: string;
  role: 'user' | 'agent';
  content: string;
  action?: 'propose_structure' | 'ask_question' | 'approve' | 'request_subpage';
  metadata?: Record<string, any>;
  createdAt: string;
}
```

- [ ] **Step 2: Add parentId to Research type**

- [ ] **Step 3: Add chat API methods**

```typescript
export const researchChatApi = {
  listMessages: (researchId: string) => api.get<{ messages: ChatMessage[] }>(`/api/research/${id}/chat`),
  sendMessage: (researchId: string, data: { content: string; action?: string }) => api.post(`/api/research/${id}/chat`, data),
  listSubPages: (researchId: string) => api.get<{ subpages: Research[] }>(`/api/research/${id}/subpages`),
};
```

- [ ] **Step 4: Commit**: `feat(frontend): ChatMessage type + research chat API`

---

### Task 8: ResearchChat Component

**Files:**
- Create: `frontend/src/components/ResearchChat.tsx`

- [ ] **Step 1: Create chat panel component**

Props: `researchId: string`, `status: string`, `onApprove: () => void`, `onSubPageCreated: () => void`

Features:
- Poll for messages (3s for planning, stop for done)
- Render messages: agent messages with markdown, user messages as bubbles
- `propose_structure` messages get "Approve" button
- Input field with send button
- Collapsible panel (open during planning, collapsed during done)

- [ ] **Step 2: Commit**: `feat(frontend): ResearchChat component`

---

### Task 9: ResearchPageTree Component

**Files:**
- Create: `frontend/src/components/ResearchPageTree.tsx`

- [ ] **Step 1: Create page tree component**

Props: `researchId: string`, `subpages: Research[]`, `activePageId: string`, `onPageSelect: (id: string) => void`

Features:
- Show main research as root
- List sub-pages with icon + title
- Highlight active page
- "+ Add sub-page" button at bottom
- Only visible when sub-pages exist or status is done

- [ ] **Step 2: Commit**: `feat(frontend): ResearchPageTree component`

---

### Task 10: Integrate into ResearchDetailClient

**Files:**
- Modify: `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx`

- [ ] **Step 1: Add chat panel + page tree**

- Import ResearchChat and ResearchPageTree
- Add state: `activePageId`, `subpages`
- Fetch sub-pages when status is done
- Layout: content area + chat panel (right side, 360px)
- Page tree: shown above content when sub-pages exist
- Status-based rendering: planning shows chat only, done shows content + chat toggle

- [ ] **Step 2: Modify create flow in InsightsList**

When creating research, navigate to detail page immediately (status: planning).

- [ ] **Step 3: Build + test**
- [ ] **Step 4: Commit**: `feat(research): integrate chat panel + page tree into detail page`

---

### Task 11: Final Build + Verification

- [ ] **Step 1: TypeScript check**: `npx tsc --noEmit`
- [ ] **Step 2: Go tests**: `go test ./internal/... -count=1`
- [ ] **Step 3: Production build**: `npm run build`
- [ ] **Step 4: Final commit if needed**
