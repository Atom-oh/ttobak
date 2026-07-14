# TTOBAK 업무 도우미 확장 — 마스터 로드맵

> Status: Draft · Date: 2026-07-09 · Author: (brainstormed with Claude)

## 1. Overview (목적)

TTOBAK은 현재 "미팅 녹음 → STT → 요약 → Q&A" 중심의 미팅록 관리 도구이며, [account-insight-substrate 설계](2026-05-30-account-insight-substrate-design.md)를 통해 Account를 1급 엔티티로 승격하고 Insight Substrate, MCP back-data 도구, Obsidian Vault export/inbound ingest([ADR-017](../decisions/ADR-017-vault-export-and-inbound-ingest.md))까지 갖췄다.

이번 로드맵은 그 위에 TTOBAK을 **개인 업무 도우미 워크스페이스**로 확장한다: 실시간 웹 검색 기반 자료조사, 노트/블로그/슬라이드를 아우르는 문서 허브, 위키링크 그래프 뷰, 고객에게 미인증으로 공개하는 페이지, Salesforce Opportunity 연동, 그리고 이 모든 것의 KB 통합. Notion과 유사한 형태지만 뉴스 자료조사·협업·내부 문서·영업 정보를 SA 업무 흐름에 맞춰 통합한다는 점이 다르다.

이 문서는 **마스터 로드맵**이다 — 요청 범위가 6개의 독립적인 서브시스템에 걸쳐 있어 하나의 스펙으로 담을 수 없다. 각 서브프로젝트(SP1~SP6)는 이후 별도 세션에서 `brainstorming → spec → writing-plans → 구현` 사이클을 거친다. 이 문서는 비전, 분해, 의존 관계, 각 서브프로젝트의 범위/비범위를 확정해 다음 세션이 바로 상세 설계에 들어갈 수 있게 한다.

## 2. 확정된 결정 사항 (사용자 확인)

- **뉴스 검색 엔진**: AgentCore Gateway의 신규 **Web Search 커넥터** (`connectorId: "web-search"`, MCP 표준, us-east-1 전용, 쿼리 ≤200자, 결과 1~25건, 출처 URL 표시 의무·대량 저장/재색인 금지가 Acceptable Use에 명시됨). 기존 Google News RSS 크롤러를 대체.
- **Public 공개 방식**: 문서/미팅노트 단위 **비밀 토큰 URL** (`/p/{token}`). 목록 페이지나 검색 노출은 없음 — 링크를 아는 사람만 접근.
- **슬라이드 범위**: PPTX/PDF **업로드 + 브라우저 뷰어 + 다운로드**. TTOBAK 내 슬라이드 제작 기능은 없음. 퍼블릭 공유 시 PDF export본만 공유.
- **그래프 노드/엣지**: **위키링크(`[[문서명]]`) 기반**. Obsidian과 동일한 멘탈모델 — 문서 본문에 링크를 써야 그래프에 엣지가 생긴다 (엔티티 자동 연결 아님).
- **협업 범위**: 기존 Account 멤버 공유 구조를 새 문서 타입(노트/블로그/슬라이드)에도 동일 적용. 실시간 공동편집은 범위 밖.
- **Salesforce Opportunity 연동**: 사용자의 로컬 macOS 앱("ttobak mac map")이 사내 internal MCP를 통해 Salesforce에서 Opportunity를 읽어와 **TTOBAK 서버 API로 push**한다. TTOBAK은 Salesforce에 직접 접근하지 않는다 — 수신 전용 API만 제공.

## 3. 현재 기반 자산 (재사용 대상)

| 영역 | 기존 자산 | 이번 로드맵에서의 역할 |
|---|---|---|
| Account/공유 | `Account`, `Share`, `MEMBER#{userId}` (account-insight-substrate 설계, ADR-015/016) | 새 문서 타입의 공유 단위로 그대로 재사용 |
| 문서 모델 | `AccountDocument` (`backend/internal/model/account.go`), `PutDocument`/`ListDocuments`/`GetDocument` | 문서 타입 필드 확장의 베이스 |
| Vault export | `VaultService.ExportVault`, `ttobak_id` 루프가드 (ADR-017) | 신규 문서 타입도 동일 export 파이프라인에 편입 |
| 뉴스 크롤러 | `backend/python/crawler/news_crawler.py`, Step Functions (`infra/lib/crawler-stack.ts`) | RSS 수집부만 Web Search 커넥터 호출로 교체, 저장/요약/KB ingestion은 재사용 |
| KB export | `internal/service/kb_export.go`, KB 버킷 `shared/*` 구조 | 신규 콘텐츠(문서 v2, oppty)의 KB 반영 경로 |
| QA 에이전트 | `backend/python/qa/tools.py` (`search_knowledge_base` 등) | 신규 데이터 소스에 대한 조회 툴 추가 지점 |
| MCP 서버 | `mcp-server/src/` (stdio, 15 tools, Cognito PKCE) | oppty 등록/조회 툴 추가 지점 |
| Lambda@Edge | `infra/lib/edge-auth-stack.ts` (`/api/*` 전역 JWT) | public 경로 예외 처리 지점 |
| Editor/렌더링 | `MeetingEditor.tsx` (TipTap), `MarkdownRenderer.tsx` | 문서 허브 v2 에디터/렌더러의 베이스 |

## 4. 로드맵: 6개 서브프로젝트

```
SP1 (독립) ──────────────────┐
SP2 → SP3 (공개할 문서 필요)   ├─→ SP6 (콘텐츠 KB 통합)
SP2 → SP4 (위키링크 인덱스 필요) │
SP5 (독립) ──────────────────┘
```

권장 순서: **SP1 → SP2 → SP3 → SP4 → SP5 → SP6**. SP1과 SP5는 SP2~4와 독립이라 병렬 진행 가능.

### SP1. AgentCore Web Search 뉴스 크롤링 — **Shipped** (2026-07-13, PR #111)

**In**: `news_crawler.py`의 RSS 파싱을 AgentCore Gateway Web Search 커넥터 호출로 교체(크롤러 소스별 검색 쿼리 → 결과 필터 → 기존 Bedrock 요약/태깅 재사용 → 기존 저장 포맷 `shared/news/{sourceId}/{hash}.md` + `CRAWLER#{sourceId}`/`DOC#{hash}` 유지). us-east-1 Gateway 리소스를 CDK로 신설(서울 리전 크롤러 Lambda에서 크로스 리전 MCP 호출). research-agent(`backend/python/research-agent/tools.py`)의 `web_search()`도 같은 Gateway로 교체. 결과에 출처 URL·게시일 표시(Acceptable Use 준수), 기사 전문 대신 snippet+요약만 저장(기존보다 오히려 저장량 감소).

**Out**: 커넥터가 지원하지 않는 리전에 배포하는 것, 검색 결과 캐싱/재색인 시스템 구축(Acceptable Use가 금지).

**해소됨**: us-east-1 Gateway는 신규 `web-search-gateway-stack.ts`로 확정 — 상세는 [SP1 설계](2026-07-09-sp1-agentcore-web-search-news-crawling-design.md) §4/§5.3 참조.

### SP2. 문서 허브 v2 (노트/블로그/슬라이드/위키링크)

**In**: `AccountDocument`에 `docType: note|blog|slide` 추가, Account 미소속 개인 문서 허용. 노트/블로그는 TipTap 에디터(`MeetingEditor.tsx` 재사용) + 위키링크 자동완성(`[[문서명]]` 입력 시 후보 검색) + 저장 시 링크 파싱·인덱싱(그래프의 데이터 소스). 슬라이드는 PPTX/PDF를 기존 presigned-upload 패턴(신규 `doc` 카테고리)으로 S3 업로드 + 다운로드 버튼. `VaultService.ExportVault`에 신규 타입 포함.

**Out**: 슬라이드 제작/편집 기능, 실시간 공동편집, PPTX→PDF 자동 변환(사용자가 직접 PDF로 export해서 올리는 것을 기본 흐름으로 함 — 자동 변환은 후속 검토).

**해소됨**: PDF 뷰어는 PDF.js 대신 presigned GET URL의 브라우저 네이티브 `<iframe>`으로 구현(의존성/번들 크기 절감, iOS Safari 1페이지 한계는 상시 노출되는 다운로드 버튼이 커버) — 상세는 [ADR-020](../decisions/ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) 참조. 위키링크 broken link 표시는 이번 범위에서 최소 처리(비클릭 muted 텍스트)로 마무리, 정교한 표시는 SP4(그래프 뷰)와 함께 재검토.

### SP3. Public 페이지 (비밀 토큰 URL)

**In**: 문서/미팅노트에 "공개" 토글 → 랜덤 토큰(충돌 방지 가능한 길이) 발급/철회, `GET /api/public/{token}` (미인증) + 프론트 정적 라우트 `/p/[token]`. Lambda@Edge를 `/api/public/*`만 예외로 통과시키도록 수정(그 외 `/api/*`는 기존대로 JWT 필수 — CLAUDE.md 보안 정책상 CloudFront 경유 유지, 신규 public 엔드포인트 없음). 공개 렌더링은 읽기 전용(위키링크는 클릭 불가 텍스트로 렌더링, 비공개 문서로의 링크가 노출되지 않도록). 슬라이드 공개는 PDF export본만.

**Out**: 공개 문서 목록/검색 페이지(비밀 URL 모델과 상충), 공개 페이지에서의 댓글/상호작용.

**열린 질문**: 토큰 만료(TTL) 여부 — 기본은 명시적 철회까지 무기한으로 시작하고 필요 시 TTL 추가.

### SP4. 그래프 뷰

**In**: SP2의 위키링크 인덱스(저장 시 파싱된 `[[link]]` 목록을 DynamoDB에 엣지로 저장)를 소비하는 `GET /api/graph` (문서/미팅/Account를 노드로, 위키링크를 엣지로). 프론트에 force-directed 그래프 시각화(경량 라이브러리 1개 신규 도입, 후보는 SP4 상세 설계에서 비교) — 노드 클릭 시 해당 문서로 이동.

**Out**: 3D 그래프, 실시간 협업 커서, 그래프 기반 편집(엣지를 그래프 뷰에서 직접 만드는 것 — 위키링크는 본문에서만 생성).

### SP5. Salesforce Opportunity 연동 (mac map app)

**In**: `SK=OPPTY#{opptyId}` 아이템(Account 파티션 하위 — 금액/단계/예상 마감일/최종 수정 시각). `PUT /api/accounts/{id}/opportunities`(upsert, mac map 앱이 호출하는 수신 전용 API). MCP 서버에 `ttobak_put_opportunity`/`ttobak_list_opportunities` 추가. `GetAccountBrief`/Account 상세 페이지에 oppty 요약 노출(미팅 준비 컨텍스트 강화).

**Out**: Salesforce로의 역방향 쓰기, TTOBAK 서버가 Salesforce API를 직접 호출하는 것 — 항상 mac map 앱이 중개.

**열린 질문**: mac map 앱의 인증 방식(사용자 Cognito 토큰 재사용 여부) — SP5 상세 설계에서 확정.

### SP6. KB 고도화 (통합 마무리)

**In**: 문서 허브 v2(SP2)와 oppty(SP5) 콘텐츠를 KB 버킷으로 export + ingestion(`kb_export.go` 패턴 확장). QA 에이전트(`qa/tools.py`)에 문서/oppty 검색 툴 추가 — "이 고객 미팅 준비해줘" 질의가 뉴스(SP1)+문서(SP2)+과거 미팅+oppty(SP5)를 종합해 응답. 죽은 스텁 `backend/cmd/kb` 정리(삭제).

**Out**: KB 아키텍처 전면 재설계(OpenSearch Serverless 교체 등) — 이번 로드맵은 콘텐츠 소스 추가에 한정.

## 5. 검증

이 문서 자체는 산출물이 코드가 아니므로, 완료 기준은: (1) 위 self-review 통과, (2) 사용자가 로드맵과 분해 순서에 동의. 이후 각 SPn은 개별 세션에서 자체 스펙 + 구현 + 검증(코드 실행/테스트)을 갖는다.
