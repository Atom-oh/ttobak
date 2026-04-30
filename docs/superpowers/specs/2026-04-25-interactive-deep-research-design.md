# Interactive Deep Research — Design Spec

**Date:** 2026-04-25
**Status:** Approved

## Overview

Transform Research from "1 question → 1 report" to an interactive, multi-turn conversational research experience with Notion-style sub-page hierarchy. Modeled after Claude Desktop deep research: topic input → Agent proposes structure + asks questions → user approves → Agent executes → user can request modifications and sub-pages via chat after completion.

## Data Model

### Research (extended)
Existing fields preserved. New fields:
- `parentId` (string) — parent researchId for sub-pages. Empty string for main research.
- `structure` (string) — Agent-proposed report structure (markdown)

### ChatMessage (new DynamoDB entity)
```
PK: RESEARCH#{researchId}
SK: MSG#{timestamp}#{msgId}

Fields:
  msgId: string (random hex)
  role: "user" | "agent"
  content: string
  action?: "propose_structure" | "ask_question" | "approve" | "request_subpage"
  metadata?: JSON string (suggestedTopics, proposedStructure, subpageId)
  createdAt: string (ISO 8601)
```

### Research Status Flow
```
planning → (Agent questions / user answers) → approved → running → done
                                                                    ↓
                                                          chat: modify / request sub-page
                                                                    ↓
                                                          child Research (parentId linked)
```

- `planning`: Agent proposing structure, asking questions. User input awaited.
- `approved`: User approved structure. Waiting for execution.
- `running`: Agent executing research (existing behavior).
- `done`: Complete. Chat available for modifications and sub-page requests.

### Sub-page Relationship
- Flat (1 level only): main research → sub-pages. No sub-sub-pages.
- Query: filter `parentId = {researchId}` from user's research list.
- No separate GSI needed (volume is small, tens of items max).

## API Design

### Modified Endpoints

```
POST /api/research
  Body: { topic, mode }
  Change: creates with status "planning" (not "running")
          triggers SFN with mode "plan"
  Response: { researchId, status: "planning" }
```

### New Endpoints

```
GET /api/research/{researchId}/chat
  → Chat message history (chronological)
  Response: { messages: ChatMessage[] }

POST /api/research/{researchId}/chat
  Body: { content, action? }
  Actions:
    - (none): general chat message → triggers Agent respond
    - "approve": approve structure → status becomes "approved" → triggers execution
    - "request_subpage": { content: "topic for sub-page" } → creates child Research
  Response: { messageId }

GET /api/research/{researchId}/subpages
  → List child research pages
  Response: { subpages: Research[] }
```

### Agent Modes (AgentCore)

**Plan mode** (`mode: "plan"`):
1. Agent analyzes topic, proposes structure
2. Saves ChatMessage (role: "agent", action: "propose_structure")
3. Asks clarifying questions via ChatMessage
4. Exits. Status remains "planning".

**Respond mode** (`mode: "respond"`):
1. Agent reads chat history from DynamoDB
2. Responds to user's message (answer question, revise structure)
3. Saves response as ChatMessage
4. Exits. Status remains "planning".

**Execute mode** (`mode: "execute"`):
1. Standard research execution (existing behavior)
2. Uses approved structure from chat history
3. Saves report to S3, updates DynamoDB status to "done"

**Sub-page mode** (`mode: "subpage"`):
1. Receives parent researchId + sub-topic
2. References parent report for context
3. Executes focused research on sub-topic
4. Saves as child Research with parentId

## Frontend Design

### Create Research Flow
1. "New Research" → topic + mode input (existing modal)
2. Submit → navigates to `/insights/research/{id}`
3. Research detail page opens with chat panel (status: planning)

### Layout (Desktop)
```
┌──────────┬─────────────────────────────┬──────────────┐
│ Nav      │     Content Area            │  Chat Panel  │
│ Sidebar  │                             │  (360px)     │
│          │  [Page Tree] + [Content]    │              │
│          │                             │  Messages    │
│          │  planning: waiting          │  Input       │
│          │  running: progress          │              │
│          │  done: markdown + TOC       │              │
└──────────┴─────────────────────────────┴──────────────┘
```

### Page Tree
Shown in content area header when sub-pages exist:
```
📊 Main Report  ← current
├── 📋 PoC Checklist
├── 🟦 SageMaker Deep Dive
└── + Add sub-page
```
Click switches content (state-based, no route change).

### Chat Panel States
| Status | Panel | Behavior |
|--------|-------|----------|
| planning | Open (required) | Agent messages + user input + "Approve" button |
| approved | Open | "Research starting..." |
| running | Open (collapsible) | Progress. Input disabled. |
| done | Collapsed (expandable) | Post-completion chat for modifications/sub-pages |
| error | Open | Error + retry |

### Polling
- 3-second poll for `planning` status (waiting for Agent messages)
- 10-second poll for `running` status (existing)
- No polling for `done`

## Implementation Scope

### Phase 1 (this spec)
- Backend: Research model extension (parentId), ChatMessage entity, new API endpoints
- Backend: research-worker Lambda modes (plan/respond/execute/subpage)
- AgentCore: Agent prompt updates for plan/respond modes
- Frontend: Chat panel component, page tree, modified create flow

### Out of Scope
- Real-time WebSocket streaming for chat (future optimization)
- Sub-sub-pages (intentionally flat)
- Chat message editing/deletion
- Collaborative research (multi-user on same research)

## Dependencies
No new packages. Uses existing:
- DynamoDB single-table design
- Step Functions workflow
- AgentCore container (FastAPI)
- Frontend polling pattern (existing research status polling)

## Files to Create/Modify

### New Files
- `backend/internal/model/chat.go` — ChatMessage model
- `backend/internal/repository/chat.go` — ChatMessage CRUD
- `backend/internal/service/chat.go` — Chat service (save, list, trigger agent)
- `backend/internal/handler/chat_research.go` — Chat API handlers
- `frontend/src/components/ResearchChat.tsx` — Chat panel component
- `frontend/src/components/ResearchPageTree.tsx` — Page tree component

### Modified Files
- `backend/internal/model/meeting.go` — add parentId to Research
- `backend/internal/model/request.go` — add chat request types
- `backend/internal/service/research.go` — planning flow, sub-page creation
- `backend/cmd/api/main.go` — register new routes
- `backend/cmd/research-worker/main.go` — add mode routing (plan/respond/execute/subpage)
- `backend/python/research-agent/agent.py` — plan/respond prompts
- `frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx` — integrate chat + page tree
- `frontend/src/lib/api.ts` — add chat API methods
- `frontend/src/types/meeting.ts` — add ChatMessage type
