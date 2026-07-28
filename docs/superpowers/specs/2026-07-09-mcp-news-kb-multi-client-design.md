# MCP 서버 확장: 뉴스/KB 검색 도구 + 다중 AI 클라이언트 지원

> Status: Draft · Date: 2026-07-09 · Author: (brainstormed with Claude)

## 1. Overview (목적)

TTOBAK의 로컬 MCP 서버(`mcp-server/`)는 현재 미팅·계정·문서(vault)만 다룬다(`ttobak_list_meetings`, `ttobak_get_account_insights`, `ttobak_put_document` 등 12개 도구). 사용자는 이 MCP를 (1) 뉴스 크롤러가 모은 인사이트, (2) Knowledge Base 검색까지 다루도록 넓히고, (3) 이 서버를 자신이 쓰는 다른 AI 툴(맥북의 Claude Code, Codex, Amazon Q, Kiro 등)에서도 동일하게 연결해 쓰고 싶어 한다.

기존 MCP 서버는 이미 stdio 전송 + Cognito OAuth PKCE로 동작하고, `TtobakApi`(`api.ts`)가 인증된 HTTP 클라이언트를 감싸고 있어 새 도구 추가는 기존 패턴을 그대로 따르면 된다. "다른 AI 툴 지원"도 별도의 원격 서버가 필요한 게 아니라 — 서버 코드는 그대로 두고 각 클라이언트(맥북별 CLI)의 MCP 설정에 동일하게 등록하는 문제다.

## 2. Goals / Non-Goals

### In
1. MCP에 뉴스/인사이트 검색 도구 2개(`ttobak_search_news`, `ttobak_get_news_detail`) 추가 — 기존 `GET /api/insights`, `GET /api/insights/{sourceId}/{docHash}` 래핑
2. MCP에 KB 검색 도구 1개(`ttobak_search_kb`) 추가 — 신규 저비용 REST 엔드포인트 `POST /api/qa/search-kb`를 신설해 래핑
3. `mcp-server/README.md`에 Codex CLI / Amazon Q Developer CLI / Kiro의 MCP 등록 방법 추가(다른 맥북에서 동일 stdio 서버를 붙이는 절차)

### Out
- 원격 HTTP MCP 엔드포인트 신설(별도 Lambda 호스팅) — stdio + 로컬 빌드 방식 유지
- 여러 맥북 간 토큰/세션 공유 — 각 기기가 독립적으로 `~/.ttobak/tokens.json` 보유, 최초 1회씩 개별 로그인
- SP1(뉴스 크롤링 엔진 자체를 AgentCore Web Search로 교체)과의 통합 — 이 스펙은 크롤러가 이미 모아둔 데이터를 MCP로 노출하는 것뿐, 크롤링 방식과는 독립

## 3. 현재 상태 (재사용 대상)

| 컴포넌트 | 현재 | 이번 스펙에서 역할 |
|---|---|---|
| `mcp-server/src/index.ts` | `ListToolsRequestSchema`/`CallToolRequestSchema` 핸들러의 정적 배열 + switch 디스패치, 12개 도구 | 동일 패턴으로 3개 도구 추가 |
| `mcp-server/src/api.ts` | `TtobakApi` — `node:https` 기반 thin client, private `get/post`, `Authorization: Bearer {idToken}` | 3개 신규 메서드 추가, 기존 에러 파싱(`{error:{code,message}}`) 그대로 사용 |
| `mcp-server/src/auth.ts` | `CognitoAuth.getIdToken()` — 캐시→리프레시→PKCE 로그인 순 | 변경 없음, 신규 메서드가 그대로 호출 |
| `GET /api/insights` (`insightsHandler.ListInsights`) | 크롤러 문서 목록, `type`/`source`/`tags`/`sort`/`page`/`limit` 필터 | `ttobak_search_news`가 그대로 래핑 |
| `GET /api/insights/{sourceId}/{docHash}` (`insightsHandler.GetDocumentContent`) | 본문 포함 상세 | `ttobak_get_news_detail`이 그대로 래핑 |
| `retrieve_from_kb()` (`backend/python/qa/handler.py:374`) | Bedrock `retrieve()` 단일 호출, 캐시(TTL 600s), `user_id` 스코프 필터, score≥0.5, `numberOfResults`≤10 | 신규 엔드포인트가 그대로 호출(로직 변경 없음) |

## 4. 상세 변경 사항

### 4.1 신규 엔드포인트 — `POST /api/qa/search-kb`
Python QA Lambda(`backend/python/qa/handler.py`)에 라우트 추가. 기존 `retrieve_from_kb(question, number_of_results, user_id)`를 그대로 호출 — LLM 생성 없이 단일 Bedrock `retrieve()` 호출이라 `ttobak_ask`(최대 3라운드 에이전틱 루프)보다 훨씬 저렴하고 빠름.
- 요청: `{"question": string, "numberOfResults"?: number}` (기본 5, 최대 10 — 기존 함수 캡 그대로)
- 응답: `{"results": [{"text": string, "score": number, "uri": string}]}`
- 인증: 기존 라우트와 동일한 JWT 검증 경로(Lambda@Edge `/api/*` 통과 후 Lambda가 `user_id` 추출) 재사용

### 4.2 MCP 신규 도구 3개 (`mcp-server/src/index.ts`, `api.ts`)

| Tool | Required | Optional | 래핑 대상 |
|---|---|---|---|
| `ttobak_search_news` | — | `type`, `source`, `tags:string[]`, `sort`, `page`, `limit` | `GET /api/insights` |
| `ttobak_get_news_detail` | `sourceId`, `docHash` | — | `GET /api/insights/{sourceId}/{docHash}` |
| `ttobak_search_kb` | `question` | `numberOfResults` | `POST /api/qa/search-kb` (신규) |

`api.ts`에 `searchNews(params)`, `getNewsDetail(sourceId, docHash)`, `searchKb(question, numberOfResults?)` 3개 메서드 추가 — 기존 메서드와 동일한 `get`/`post` 헬퍼, 동일 에러 처리 패턴.

`index.ts`의 `ListToolsRequestSchema` 배열에 3개 스키마 추가, `CallToolRequestSchema`의 switch에 3개 case 추가(기존 도구들과 동일한 인라인 필수값 검증 + `text(JSON.stringify(...))` 반환 패턴).

### 4.3 README — 다중 클라이언트 등록 가이드
`mcp-server/README.md`에 "다른 AI 툴에서 연결하기" 섹션 추가(영/한 병기, 기존 문서 구조 따름):
- **Codex CLI**: `~/.codex/config.toml`의 `[mcp_servers.ttobak]`에 동일한 `command`/`args`/`env` 4종 등록
- **Amazon Q Developer CLI**: `~/.aws/amazonq/mcp.json`에 동일 등록
- **Kiro**: Kiro MCP 설정(워크스페이스 또는 전역)에 동일 등록
- 공통 전제: 각 기기에 레포 clone + `cd mcp-server && npm install && npm run build` 1회 실행(현재 Claude Code 설정과 동일 절차) — 서버를 원격 호스팅하지 않으므로 기기마다 로컬 빌드가 필요함을 명시
- 최초 사용 시 도구 호출 → 브라우저 Cognito 로그인 → `~/.ttobak/tokens.json` 저장은 기기별로 독립적으로 1회씩 발생함을 명시(토큰 공유 안 됨)

## 5. 에러 처리

- `search-kb` 엔드포인트가 KB 미설정/에러 시: 기존 `retrieve_from_kb` 예외 처리 패턴(빈 배열 반환 + 로그)을 그대로 따름 — 새로운 에러 코드 불필요
- MCP 도구 3개 모두 기존 도구들처럼 백엔드 `{error:{code,message}}` 응답을 `Error(...)`로 던지고, `index.ts` 최상위 `catch`가 `error(message)`로 변환 — 신규 에러 처리 로직 없음

## 6. 테스트/검증

- 백엔드: `search-kb` 라우트에 대해 질문→검색 결과 반환 유닛 테스트(캐시 히트/미스 각 1건)
- MCP: `npm run build` 후 로컬에서 3개 신규 도구를 Claude Code로 수동 호출해 실제 응답 확인(뉴스 검색 1건, 뉴스 상세 1건, KB 검색 1건)
- 다중 클라이언트: 최소 1개 타 클라이언트(예: Codex CLI)에 동일 설정을 등록해 `tools/list`에 15개 도구가 모두 뜨는지, 로그인 플로우가 정상 동작하는지 확인
