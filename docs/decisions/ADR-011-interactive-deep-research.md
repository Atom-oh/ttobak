# ADR-011: Interactive Deep Research with Conversational Planning and Sub-pages

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
The current Deep Research feature follows a "fire-and-forget" pattern: user submits a topic, Agent produces a single monolithic report, and the user reads it. This has several limitations:

- No opportunity for the user to guide the research direction before execution
- Agent cannot ask clarifying questions about scope, focus areas, or depth preferences
- Research output is a single flat document with no way to deep-dive into specific sections
- No mechanism to request follow-up research on related sub-topics
- The Notion reference page (SageMaker vs Snowflake comparison) demonstrates the ideal: a main report with 6 linked deep-dive sub-pages, each independently explorable

The Claude Desktop deep research pattern provides a proven UX: topic input, Agent proposes structure and asks questions, user approves, Agent executes.

## Options Considered

### Option 1: WebSocket-based Real-time Chat
Extend existing WebSocket infrastructure for bidirectional Research chat. Agent streams questions and status via WS.

- **Pros**: Real-time interaction, low latency
- **Cons**: AgentCore container runs fire-and-forget (background thread); maintaining WS sessions across Agent invocations is architecturally complex. Significant infrastructure changes needed.

### Option 2: Polling-based Asynchronous Chat (Chosen)
Agent writes chat messages to DynamoDB. Frontend polls for new messages. User responses trigger new Agent invocations via Step Functions.

- **Pros**: Simple implementation, leverages existing polling pattern, messages persisted in DynamoDB, no infrastructure changes, survives page refreshes
- **Cons**: 3-10 second polling delay (acceptable for planning/review conversations)

### Option 3: Hybrid REST Chat + WebSocket Notifications
Chat messages via REST, real-time push notifications via WebSocket.

- **Pros**: Best of both worlds
- **Cons**: Two communication channels to manage, over-engineered for the conversational planning use case

## Decision
Adopt Option 2: Polling-based asynchronous chat. The research planning conversation operates on a minutes-scale cadence (Agent proposes structure, user reviews and responds) where 3-second polling is imperceptible. DynamoDB persistence ensures chat history survives page refreshes and session changes.

Key design decisions:
- **Research status extended**: new `planning` and `approved` states before `running`
- **ChatMessage entity**: stored as `PK=RESEARCH#{id}, SK=MSG#{timestamp}#{msgId}` in DynamoDB
- **Agent modes**: `plan` (propose structure), `respond` (answer questions), `execute` (run research), `subpage` (child research)
- **Sub-pages**: flat hierarchy (1 level only), linked via `parentId` field on Research
- **Polling**: 3s during `planning`, 10s during `running`, none during `done`

## Consequences

### Positive
- Users can guide research direction before execution, resulting in more relevant reports
- Agent asks clarifying questions, reducing irrelevant content
- Sub-page system enables organized deep-dives without bloating the main report
- Notion-like page tree provides familiar navigation pattern
- No new infrastructure dependencies; uses existing DynamoDB + SFN + AgentCore
- Chat history persisted and survives page refreshes

### Negative
- 3-second polling delay during planning conversations (acceptable)
- Each Agent response requires a full SFN execution cycle (~1-2s overhead)
- DynamoDB chat messages accumulate (mitigated by TTL or periodic cleanup)
- Frontend complexity increases (chat panel + page tree + status management)
- Agent must handle multiple modes (plan/respond/execute/subpage) with different prompts

## References
- Design spec: `docs/superpowers/specs/2026-04-25-interactive-deep-research-design.md`
- Notion reference: SageMaker vs Snowflake comparison page with 6 sub-pages
- Claude Desktop deep research UX pattern
- Current implementation: `backend/internal/service/research.go`, `backend/python/research-agent/agent.py`

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
현재 Deep Research 기능은 "fire-and-forget" 패턴을 따릅니다: 유저가 주제를 제출하면 Agent가 단일 보고서를 생성하고, 유저는 이를 읽습니다. 이 방식에는 여러 한계가 있습니다:

- 리서치 실행 전에 유저가 방향을 가이드할 기회가 없습니다
- Agent가 범위, 중점 분야, 깊이에 대해 질문할 수 없습니다
- 결과물이 단일 평면 문서로, 특정 섹션에 대한 deep-dive가 불가합니다
- 관련 하위 주제에 대한 후속 리서치 요청 메커니즘이 없습니다
- Notion 참고 페이지(SageMaker vs Snowflake 비교)가 이상적 구조를 보여줍니다: 메인 보고서 + 6개 하위 deep-dive 페이지

Claude Desktop deep research 패턴이 검증된 UX를 제공합니다: 주제 입력 → Agent 구조 제안 + 질문 → 유저 승인 → Agent 실행.

## 검토한 옵션

### 옵션 1: WebSocket 기반 실시간 채팅
기존 WebSocket 인프라를 확장하여 Research 전용 양방향 채팅 구현.

- **장점**: 실시간 상호작용, 낮은 지연
- **단점**: AgentCore 컨테이너가 fire-and-forget(백그라운드 스레드)으로 동작하여 Agent 호출 간 WS 세션 유지가 아키텍처적으로 복잡합니다. 인프라 변경이 상당합니다.

### 옵션 2: 폴링 기반 비동기 채팅 (선택됨)
Agent가 채팅 메시지를 DynamoDB에 저장합니다. 프론트엔드가 새 메시지를 폴링합니다. 유저 응답이 Step Functions를 통해 새 Agent 호출을 트리거합니다.

- **장점**: 구현 단순, 기존 폴링 패턴 활용, DynamoDB에 메시지 영속, 인프라 변경 없음, 페이지 새로고침에도 유지
- **단점**: 3-10초 폴링 지연 (계획/리뷰 대화에서 허용 가능)

### 옵션 3: REST 채팅 + WebSocket 알림 하이브리드
채팅 메시지는 REST, 실시간 푸시 알림은 WebSocket으로 처리.

- **장점**: 양쪽의 장점
- **단점**: 두 통신 채널 관리, 대화형 계획 사용 사례에 과도한 설계

## 결정
옵션 2: 폴링 기반 비동기 채팅을 채택합니다. 리서치 계획 대화는 분 단위 케이던스로 진행되므로(Agent 구조 제안 → 유저 검토 및 응답) 3초 폴링이 체감되지 않습니다. DynamoDB 영속성으로 채팅 이력이 페이지 새로고침과 세션 변경에도 유지됩니다.

주요 설계 결정:
- **Research 상태 확장**: `running` 전에 새로운 `planning`, `approved` 상태 추가
- **ChatMessage 엔티티**: DynamoDB에 `PK=RESEARCH#{id}, SK=MSG#{timestamp}#{msgId}`로 저장
- **Agent 모드**: `plan`(구조 제안), `respond`(질문 답변), `execute`(리서치 실행), `subpage`(하위 리서치)
- **하위 페이지**: 1단계 flat 구조, Research의 `parentId` 필드로 연결
- **폴링**: `planning` 중 3초, `running` 중 10초, `done`에서는 없음

## 영향

### 긍정적
- 실행 전에 리서치 방향을 가이드하여 더 관련성 높은 보고서 생성
- Agent의 명확화 질문으로 불필요한 내용 감소
- 하위 페이지 시스템으로 메인 보고서 비대화 없이 체계적 deep-dive 가능
- Notion 스타일 페이지 트리로 익숙한 네비게이션 패턴 제공
- 새로운 인프라 의존성 없음; 기존 DynamoDB + SFN + AgentCore 활용
- 채팅 이력이 영속되어 페이지 새로고침에도 유지

### 부정적
- 계획 대화 중 3초 폴링 지연 (허용 가능)
- 각 Agent 응답에 SFN 전체 실행 사이클 필요 (~1-2초 오버헤드)
- DynamoDB 채팅 메시지 누적 (TTL 또는 주기적 정리로 완화)
- 프론트엔드 복잡도 증가 (채팅 패널 + 페이지 트리 + 상태 관리)
- Agent가 여러 모드(plan/respond/execute/subpage)를 다른 프롬프트로 처리해야 함

## 참고 자료
- 디자인 스펙: `docs/superpowers/specs/2026-04-25-interactive-deep-research-design.md`
- Notion 참고: SageMaker vs Snowflake 비교 페이지 (6개 하위 페이지)
- Claude Desktop deep research UX 패턴
- 현재 구현: `backend/internal/service/research.go`, `backend/python/research-agent/agent.py`
