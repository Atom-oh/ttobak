# SP1: AgentCore Web Search 기반 뉴스 크롤링

> Status: Draft · Date: 2026-07-09 · Author: (brainstormed with Claude)
> Roadmap 참조: [2026-07-09-work-assistant-roadmap-design.md](2026-07-09-work-assistant-roadmap-design.md) SP1

## 1. Overview (목적)

TTOBAK의 뉴스 크롤러(`backend/python/crawler/news_crawler.py`)는 현재 Google News RSS + 7개 언론사 직접 RSS 피드를 stdlib으로 파싱하고, 매칭된 기사 본문을 최대 30,000자까지 스크래핑해 저장한다. 이 방식은 RSS 피드 구조 변경에 취약하고, 사이트별 유지보수 비용이 들며, 저작권상 전문 스크래핑이 바람직하지 않다.

AWS Bedrock AgentCore Gateway에 신규 **Web Search 커넥터**(`connectorId: "web-search"`)가 출시됐다 — AWS가 직접 운영하는 웹 인덱스 기반 관리형 검색으로, MCP 표준 `tools/call`로 호출하며 쿼리가 AWS 밖으로 나가지 않는다(Private by design). 이 스펙은 RSS 파싱을 이 커넥터 호출로 **완전히 교체**하고, 같은 커넥터를 research-agent의 `web_search()` 도구에도 적용한다.

## 2. Goals / Non-Goals

### In
1. `news_crawler.py`의 RSS 수집 계층(`_search_google_news`, `_fetch_site_rss`, 관련 상수)을 AgentCore Gateway Web Search 호출로 교체
2. 크롤러 소스별(`newsQueries`) 검색 → 기존 Bedrock 요약/태깅(`_summarize_and_tag`) 재사용 → 기존 저장 포맷(`shared/news/{sourceId}/{hash}.md`, `CRAWLER#{sourceId}`/`DOC#{hash}`) 유지 → 기존 KB ingestion 트리거 그대로
3. `research-agent/tools.py`의 `web_search()`도 같은 Gateway로 교체(현재 Google News RSS)
4. us-east-1에 AgentCore Gateway + Web Search 타깃을 신규 CDK 스택으로 배포, ap-northeast-2 Lambda/컨테이너에서 크로스 리전 SigV4 호출
5. Acceptable Use 준수: 응답에 출처 URL·게시일 표시, 기사 전문 대신 snippet(Gateway가 반환하는 `text` 필드)만 저장

### Out
- Web Search가 지원하지 않는 리전으로의 배포(커넥터는 us-east-1 전용, 고정)
- 검색 결과의 캐싱/재색인 시스템(Acceptable Use가 금지 — 매 크롤 사이클마다 재검색)
- `customUrls`(사용자가 직접 지정한 URL) 처리 경로 변경 — 이건 검색이 아니라 직접 페치이므로 그대로 유지
- RSS 방식으로의 폴백(장애 시에도 재시도만 하고 RSS로 돌아가지 않음 — 사용자 확정)

## 3. 현재 상태 (재사용/교체 대상)

| 파일 | 현재 | 이번 스펙 |
|---|---|---|
| `news_crawler.py` `_search_google_news`/`_fetch_site_rss`/`_parse_rss`/`extract_paragraphs` | RSS 페치 + 본문 스크래핑 | **삭제** |
| `news_crawler.py` `_generate_search_queries` | 소스명+키워드로 검색어 생성 | **재사용 그대로** |
| `news_crawler.py` `_summarize_and_tag` (Bedrock `converse`, `global.anthropic.claude-sonnet-5`) | 기사 요약+태깅 | **재사용 그대로** (입력이 전문→snippet으로 바뀔 뿐) |
| `news_crawler.py` `_doc_exists`/`_write_metadata`/`_make_hash` | dedup, DynamoDB 저장, URL 해시 | **재사용 그대로** |
| `news_crawler.py` `_write_to_s3` | 마크다운에 "원문 발췌"(최대 30,000자) 포함 | **수정**: snippet 기반으로 축소, `publishedDate` 필드 추가 |
| `research-agent/tools.py` `web_search()` | Google News RSS 파싱 | **교체**: Gateway 호출 |
| `infra/lib/crawler-stack.ts` Step Functions/Lambda 설정 | `ListActiveSources → Parallel[CrawlTechDocs, Map(CrawlNews)] → TriggerIngestion` | **변경 없음** (환경변수만 추가) |
| `infra/lib/ai-stack.ts` `crawlerRole`/`researchWorkerRole` | Bedrock invoke 권한만 | **추가**: `bedrock-agentcore:InvokeGateway` |

## 4. 아키텍처

```
[Step Functions — 변경 없음]
ListActiveSources → Parallel[ CrawlTechDocs | Map(CrawlNews, concurrency=5) ] → TriggerIngestion
                                          │
                                          ▼
                          news_crawler.py (ap-northeast-2 Lambda, 512MB/14min)
                                          │
                          ① _generate_search_queries() — 그대로
                                          │
                          ② [NEW] _gateway_web_search(query, max_results)
                             SigV4 서명 POST https://{gatewayId}.gateway.bedrock-agentcore
                                  .us-east-1.api.aws/mcp
                             body: {"jsonrpc":"2.0","method":"tools/call",
                                    "params":{"name":"WebSearch",
                                              "arguments":{"query":q[:200],"maxResults":10}}}
                             응답 content[0].text(JSON) → [{text,url,title,publishedDate}]
                                          │
                          ③ 기존 그대로: _doc_exists(dedup) → _summarize_and_tag(Bedrock)
                             → _write_to_s3(snippet 기반) → _write_metadata(DynamoDB)
```

**신규 us-east-1 리소스** — `infra/lib/web-search-gateway-stack.ts` (신규 스택, `EdgeAuthStack`와 동일 패턴: `env: usEast1Env`, `crossRegionReferences: true`):
- `agentcore.Gateway` (CDK L2, `aws-cdk-lib/aws-bedrockagentcore`) — `authorizerConfiguration: agentcore.GatewayAuthorizer.usingAwsIam()` (IAM/SigV4, Cognito 불필요)
- `agentcore.GatewayTarget` — Web Search 커넥터(`source.connectorId: "web-search"`, tool 이름 `WebSearch`). L2가 connector 타깃 타입을 지원하지 않으면 `CfnGatewayTarget`(L1)로 `targetConfiguration.mcp.connector` 직접 지정
- Gateway 서비스 역할(Gateway가 assume, `roleArn` 프로퍼티)에 정책 추가(AWS 공식 Web Search 커넥터 가이드가 명시하는 그대로 — 다른 커넥터(예: Managed KB)는 서비스 역할에 `InvokeGateway`를 넣지 않지만, Web Search 커넥터는 예외적으로 이 두 액션을 서비스 역할에 요구함):
  - `bedrock-agentcore:InvokeGateway` on `arn:aws:bedrock-agentcore:us-east-1:{account}:gateway/*`
  - `bedrock-agentcore:InvokeWebSearch` on `arn:aws:bedrock-agentcore:us-east-1:aws:tool/web-search.v1`
- `CfnOutput`으로 `gatewayId`/`gatewayUrl` export

**Cross-region 참조 (ap-northeast-2 → us-east-1)**: CloudFormation의 export/import는 리전 범위이므로 `WebSearchGatewayStack`(us-east-1)의 `CfnOutput`만으로는 `CrawlerStack`(ap-northeast-2)이 값을 가져올 수 없다. `EdgeAuthStack` 패턴(반대 방향: us-east-1 스택이 ap-northeast-2 값을 소비)과 동일하게, **참조하는 쪽 스택에도 `crossRegionReferences: true`를 켜야 한다** — 이번 경우는 소비자인 `CrawlerStack`(및 research-agent 관련 스택)이 그 대상. CDK가 내부적으로 SSM Parameter Store 기반 커스텀 리소스로 값을 리전 간에 전달하므로, 코드상으로는 `webSearchGatewayStack.gatewayUrl` 같은 프로퍼티를 그대로 참조하면 되지만 **양쪽 스택 모두** `crossRegionReferences: true`가 필요함을 `infra/bin/infra.ts`에서 명시.

**호출자 측 IAM** (`infra/lib/ai-stack.ts`):
- `crawlerRole`(뉴스 크롤러 Lambda, ap-northeast-2)와 research-agent 실행 역할(§5.2에서 확정)에 `bedrock-agentcore:InvokeGateway` on Gateway ARN(us-east-1) 추가 — cross-region 호출이므로 대상 리전을 하드코딩

## 5. 상세 변경 사항

### 5.1 `backend/python/crawler/news_crawler.py`
- **삭제**: `_search_google_news`, `_fetch_site_rss`, `_parse_rss`, `GOOGLE_NEWS_RSS`, `SITE_RSS_FEEDS`, `MAX_ARTICLES_PER_FEED` (검색/RSS 전용, `customUrls` 경로는 안 씀)
- **유지**: `extract_paragraphs`/`_ParagraphExtractor`, `BLOCKED_URL_PATTERNS`, `MAX_CONTENT_LENGTH` — `customUrls`(사용자가 직접 지정한 URL) 처리 경로가 여전히 페이지를 직접 fetch해 본문을 추출하므로 그대로 필요. `MIN_BODY_LENGTH`도 `customUrls` 경로에는 유지하고, Gateway 검색 결과(snippet) 경로에서만 체크를 건너뛴다(§5.1 하단 참조)
- **신규**: `_gateway_web_search(query: str, max_results: int = 10) -> list[dict]`
  - `botocore.auth.SigV4Auth` + `botocore.awsrequest.AWSRequest`로 POST 요청 서명 (리전 `us-east-1`, 서비스명은 AWS 문서 확인 필요 — Gateway invoke의 서명 서비스명을 구현 착수 시 재확인. 통상 `bedrock-agentcore`로 추정되나 최종 값은 `boto3.client('bedrock-agentcore-control').meta.service_model` 또는 실제 호출 테스트로 검증)
  - 요청 바디: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"WebSearch","arguments":{"query": query[:200], "maxResults": max_results}}}`
  - 응답의 `content[0].text`(JSON 문자열)를 파싱해 `results: [{text, url, title, publishedDate}]` 반환. 빈 결과/에러(`isError: true`)는 빈 리스트 반환 + 로그
- **변경**: `handler()`에서 `newsQueries` 순회 시 `_search_google_news`/`_fetch_site_rss` 대신 `_gateway_web_search` 호출. `customUrls` 처리(직접 페치)는 변경 없음
- **변경**: `_process_article`이 이제 `_gateway_web_search`가 준 `text`(snippet, 통상 수백자)를 콘텐츠로 받음 — 기존 `MIN_BODY_LENGTH` 체크는 제거(짧은 snippet도 유효)
- **변경**: `_write_to_s3` — "원문 발췌"(최대 30,000자) 섹션을 제거하고 검색 snippet 전체(짧으므로 길이 제한 불필요)를 유지, `**Published:**` 라인은 Gateway의 `publishedDate` 사용
- **환경변수 추가**: `WEB_SEARCH_GATEWAY_URL`(예: `https://{gatewayId}.gateway.bedrock-agentcore.us-east-1.api.aws/mcp`), `WEB_SEARCH_GATEWAY_REGION=us-east-1`

### 5.2 `backend/python/research-agent/tools.py`
- `web_search()` 내부를 `_gateway_web_search`와 동일한 로직으로 교체(별도 배포 아티팩트라 헬퍼는 각 모듈에 독립 구현 — 공유 모듈화는 배포 복잡도 증가로 이번 스펙에서는 하지 않음)
- Google News RSS 파싱 코드(`urlopen`, `ET.fromstring` 등) 삭제
- `REGION`(ap-northeast-2) 기본 로직은 그대로, Gateway 호출만 `us-east-1` 고정 리전으로 서명

**실행 주체와 IAM/env 배포 경로 (중요 — `researchWorkerRole`과는 별개)**: `research-agent/tools.py`는 `backend/cmd/research-worker`(Lambda, `researchWorkerRole`로 실행)가 아니라 **AgentCore Runtime 컨테이너**(`ttobakResearchContainer`, `backend/python/research-agent/agent.py`가 서빙) 안에서 실행된다. 이 컨테이너의 실행 역할과 ARN은 현재 CDK가 관리하지 않고 `infra/bin/infra.ts`에 하드코딩된 문자열(`agentCoreRuntimeArn`)로만 참조된다 — 즉 IaC 밖에서 배포/관리되는 리소스다. 따라서:
- `bedrock-agentcore:InvokeGateway` 정책은 `researchWorkerRole`(호출자 Lambda)이 아니라 **이 AgentCore Runtime 컨테이너 자신의 실행 역할**에 부여해야 한다. `researchWorkerRole`에 부여해도 컨테이너 내부 코드의 Gateway 호출에는 아무 효과가 없다(그 역할은 `InvokeAgentRuntime`으로 컨테이너를 "부르는" 쪽 권한만 가짐).
- 컨테이너 실행 역할이 CDK 밖에 있으므로, 이 역할에 대한 IAM 정책 추가는 (a) AWS 콘솔/CLI로 직접 편집, 또는 (b) 이 스펙과 별도로 해당 역할을 CDK import(`iam.Role.fromRoleArn`)해서 정책을 붙이는 방식 중 하나로 처리해야 한다 — SP1 구현 착수 시 어느 쪽을 택할지 결정 필요(§8에 열린 사항으로 추가).
- `WEB_SEARCH_GATEWAY_URL`/`WEB_SEARCH_GATEWAY_REGION` 같은 env var도 이 컨테이너의 배포 파이프라인(`agentcore.json`/컨테이너 빌드·배포 스크립트)에 주입해야 하며, `infra/lib/crawler-stack.ts`의 `commonEnv`(Lambda용)와는 다른 경로임을 구분해서 처리한다.

### 5.3 `infra/lib/web-search-gateway-stack.ts` (신규)
전체 신규 파일. `Gateway` + `GatewayTarget`(web-search connector) + 서비스 역할 정책 + output.

### 5.4 `infra/lib/crawler-stack.ts`
`commonEnv`에 `WEB_SEARCH_GATEWAY_URL`, `WEB_SEARCH_GATEWAY_REGION` 추가(신규 props로 전달).

### 5.5 `infra/lib/ai-stack.ts`
`crawlerRole`, `researchWorkerRole`에 `bedrock-agentcore:InvokeGateway` 정책 추가.

### 5.6 `infra/bin/infra.ts`
`WebSearchGatewayStack` 인스턴스화(`env: usEast1Env`), `CrawlerStack`과 research-agent를 쓰는 스택이 `addDependency`.

## 6. 에러 처리 & Acceptable Use

- Gateway 호출 실패(타임아웃/5xx/`isError:true`) 시: 해당 소스의 해당 쿼리만 스킵(빈 결과로 취급), 다른 쿼리·다른 소스는 계속 진행. RSS로 폴백하지 않음(사용자 확정) — 실패는 로그로만 남고 다음 날 크롤에서 재시도.
- 저장되는 마크다운/DynamoDB 아이템에는 항상 `url`(출처)와 `publishedDate`를 포함해 Acceptable Use의 "출처 URL·링크 유지" 요건을 만족.
- 대량 저장/재색인 금지 조항 준수: 매 크롤 사이클마다 신규 검색만 수행하고, 결과를 별도 검색 인덱스로 재구성하지 않음(기존처럼 DynamoDB+S3에 뉴스 브리핑 문서로만 저장 — 이는 "경쟁 인덱스 구축"이 아니라 SA 대상 브리핑 생성이므로 허용 범위).

## 7. 테스트

- `_gateway_web_search` 응답 파싱: 정상 응답, 빈 결과, `isError:true` 각각에 대한 유닛 테스트(us-east-1 실제 호출 없이 `botocore`/HTTP 목킹)
- 기존 `_process_article`/`_summarize_and_tag`/`_make_hash`/`_doc_exists` 테스트가 있다면 입력을 snippet 기준으로 조정
- 배포 후 실사용 검증: 크롤러를 1개 소스에 대해 수동 트리거(Step Functions 콘솔 또는 CLI) → S3 `shared/news/{sourceId}/` 신규 마크다운 생성 확인 → DynamoDB `CRAWLER#{sourceId}` 아이템 확인 → KB ingestion job 정상 트리거 확인

## 8. 열린 사항 (구현 착수 시 확정)

- SigV4 서명의 정확한 서비스명(`bedrock-agentcore` 추정) — AWS SDK/실제 호출로 검증 필요
- CDK `agentcore.Gateway`/`GatewayTarget` L2가 `connector` 타깃 타입을 지원하는지 최신 `aws-cdk-lib` 버전에서 재확인, 미지원 시 L1 `CfnGatewayTarget` 사용
- research-agent AgentCore Runtime 컨테이너(§5.2)의 실행 역할에 `bedrock-agentcore:InvokeGateway`를 부여하는 방식 — 콘솔/CLI 직접 편집 vs CDK `iam.Role.fromRoleArn` import 중 선택
- 로드맵 문서([2026-07-09-work-assistant-roadmap-design.md](2026-07-09-work-assistant-roadmap-design.md) SP1)의 "Gateway를 어느 스택에 둘지" 열린 질문은 이 스펙의 §4/§5.3에서 **신규 `web-search-gateway-stack.ts`로 확정**했으므로 해소됨 — 로드맵 쪽 문서도 동기화 필요
