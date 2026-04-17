# ADR-004: Automated Crawler & Insights for SA Knowledge Base

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

Ttobak serves AWS Solutions Architects who prepare for and follow up on customer meetings. The current Knowledge Base contains only meeting-generated documents (auto-exported summaries) and user-uploaded files. The QA Lambda has a real-time `search_aws_docs` tool, but it performs keyword-based search against the public AWS docs API without semantic understanding.

SAs need richer context for effective customer engagement:
1. **AWS documentation** specific to the customer's tech stack (e.g., EKS best practices for a customer running Kubernetes) should be pre-embedded in the KB for semantic RAG retrieval
2. **Customer news and articles** (press releases, tech media coverage, IR announcements) provide business context that helps SAs tailor their recommendations
3. Multiple SAs may work with the same customer, so crawled data should be shared without duplicating crawling effort

The existing architecture has no automated data ingestion from external sources. All KB content arrives through manual upload or the post-meeting summarization pipeline.

## Options Considered

### Option 1: Lambda + Step Functions Crawler with Shared KB (Chosen)

A new `CrawlerStack` adds a Step Functions workflow triggered by EventBridge daily schedule. The workflow fans out per customer source, running TechCrawler and NewsCrawler Lambdas in parallel. Crawled documents are stored in `shared/` prefix in the KB S3 bucket, accessible to all SAs. Customer sources are managed at the system level in DynamoDB with per-user subscriptions, preventing duplicate crawls.

- **Pros**: Fits existing serverless architecture; Step Functions provide failure isolation, retries, and parallel execution per source; system-level source management prevents duplicate crawling; shared KB enriches all SAs; cost-efficient (~$12-27/month)
- **Cons**: New CDK stack and 4 Lambdas to maintain; Step Functions add orchestration complexity; daily schedule may miss breaking news; Lambda 5-minute timeout limits per-source crawl volume

### Option 2: ECS Fargate Scheduled Tasks

Run crawlers as Fargate containers triggered by EventBridge. No time limits, can process large document sets.

- **Pros**: No timeout constraints; can handle arbitrarily large crawl jobs; single container per source simplifies orchestration
- **Cons**: Requires Docker image management and ECR; higher baseline cost (Fargate pricing vs Lambda pay-per-use); breaks the fully serverless pattern; needs VPC configuration for some use cases; more operational overhead

### Option 3: Extend Existing API Lambda

Add crawling endpoints to the existing `ttobak-api` Lambda. EventBridge triggers the API Gateway endpoint to initiate crawling.

- **Pros**: No new infrastructure; reuses existing Lambda and API Gateway
- **Cons**: API Lambda has 30-second timeout (insufficient for crawling); couples long-running batch work with request-response API; cold starts from increased package size affect API latency; single Lambda responsibility violated

## Decision

Use Option 1: Lambda + Step Functions with a dedicated CrawlerStack.

The architecture introduces four focused Lambdas orchestrated by Step Functions:
- `ttobak-crawler-orchestrator`: Lists active crawler sources from DynamoDB
- `ttobak-crawler-tech`: Crawls AWS documentation per service (What Is, Best Practices, FAQ pages)
- `ttobak-crawler-news`: Crawls customer news from Naver News API, Google News RSS, tech media, and custom URLs
- `ttobak-crawler-ingest`: Triggers Bedrock KB ingestion for the `shared/` S3 prefix

Customer sources are stored as `CRAWLER#<sourceId>` items in DynamoDB with a `subscribers` list. When multiple SAs register the same customer, they subscribe to the existing source and their AWS service selections are merged as a union. Crawling runs once per source regardless of subscriber count.

Crawled documents go to `shared/aws-docs/` and `shared/news/` prefixes in the KB S3 bucket. The QA Lambda's KB retrieval filter is updated to include `shared/` alongside user-specific prefixes.

A new Insights page (`/insights`) with News and Tech tabs provides a browsable interface for crawled content. Settings gains a Crawler Sources section for managing customer subscriptions.

## Consequences

### Positive
- QA responses gain deep AWS service knowledge via semantic KB retrieval (vs current keyword-only docs search)
- SAs get customer business context (news, press) alongside technical meeting notes
- Shared KB eliminates redundant crawling when multiple SAs serve the same customer
- Step Functions provide built-in retry, error handling, and parallel execution
- Insights UI gives SAs a dedicated view of crawled intelligence without relying on QA queries
- Estimated cost is low (~$12-27/month) due to serverless architecture and efficient Haiku summarization

### Negative
- Adds a new CDK stack with 4 Lambdas, Step Functions, and EventBridge rules to maintain
- Daily schedule introduces up to 24-hour latency for new content (no real-time crawling)
- Lambda 5-minute timeout per crawler may limit volume for customers with extensive news coverage
- News crawling depends on external APIs (Naver, Google RSS) which may change or rate-limit
- Shared KB means all SAs see all crawled content, which may include irrelevant sources from other SAs' customers
- BeautifulSoup/newspaper3k HTML parsing is fragile against site layout changes

## References
- `docs/superpowers/specs/2026-04-17-crawler-insights-design.md` -- Full design specification
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- Related: MCP server for external access
- `infra/lib/knowledge-stack.ts` -- Existing KB infrastructure (OpenSearch Serverless + Bedrock KB)
- `backend/python/qa/handler.py` -- QA Lambda with current `search_aws_docs` tool
- `backend/python/qa/tools.py` -- Tool definitions including KB retrieval filter logic

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경

Ttobak은 고객 미팅을 준비하고 후속 조치를 하는 AWS Solutions Architect를 지원합니다. 현재 Knowledge Base에는 미팅에서 생성된 문서(자동 내보낸 요약)와 사용자가 업로드한 파일만 포함되어 있습니다. QA Lambda에 실시간 `search_aws_docs` 도구가 있지만, 시맨틱 이해 없이 공개 AWS 문서 API에 대한 키워드 기반 검색을 수행합니다.

SA는 효과적인 고객 대응을 위해 더 풍부한 컨텍스트가 필요합니다:
1. **고객사 기술 스택에 맞는 AWS 문서** (예: Kubernetes를 운영하는 고객을 위한 EKS 모범 사례)를 시맨틱 RAG 검색을 위해 KB에 사전 임베딩해야 합니다
2. **고객사 뉴스 및 기사** (보도자료, 기술 미디어 보도, IR 공시)는 SA가 권장 사항을 맞춤화하는 데 도움이 되는 비즈니스 컨텍스트를 제공합니다
3. 여러 SA가 동일한 고객사를 담당할 수 있으므로, 크롤링된 데이터는 크롤링 노력을 중복하지 않고 공유되어야 합니다

기존 아키텍처에는 외부 소스로부터의 자동화된 데이터 수집이 없습니다. 모든 KB 콘텐츠는 수동 업로드 또는 미팅 후 요약 파이프라인을 통해 들어옵니다.

## 검토한 옵션

### 옵션 1: Lambda + Step Functions 크롤러와 공유 KB (선택됨)

새로운 `CrawlerStack`이 EventBridge 일일 스케줄로 트리거되는 Step Functions 워크플로를 추가합니다. 워크플로는 고객사 소스별로 팬아웃하여 TechCrawler와 NewsCrawler Lambda를 병렬로 실행합니다. 크롤링된 문서는 KB S3 버킷의 `shared/` 접두사에 저장되어 모든 SA가 접근할 수 있습니다. 고객사 소스는 DynamoDB에서 시스템 레벨로 관리되며 사용자별 구독으로 중복 크롤링을 방지합니다.

- **장점**: 기존 서버리스 아키텍처에 부합; Step Functions로 실패 격리, 재시도, 소스별 병렬 처리 제공; 시스템 레벨 소스 관리로 중복 크롤링 방지; 공유 KB로 모든 SA 지원; 비용 효율적 (월 ~$12-27)
- **단점**: 새 CDK 스택과 4개 Lambda 유지보수 필요; Step Functions 오케스트레이션 복잡도 추가; 일일 스케줄로 속보 누락 가능; Lambda 5분 타임아웃으로 소스당 크롤링 볼륨 제한

### 옵션 2: ECS Fargate 스케줄 태스크

크롤러를 EventBridge로 트리거되는 Fargate 컨테이너로 실행합니다. 시간 제한 없이 대규모 문서 세트를 처리할 수 있습니다.

- **장점**: 타임아웃 제약 없음; 임의의 대규모 크롤링 작업 처리 가능; 소스당 단일 컨테이너로 오케스트레이션 단순화
- **단점**: Docker 이미지 관리 및 ECR 필요; Fargate 가격 책정으로 더 높은 기본 비용; 완전 서버리스 패턴 파괴; 일부 사용 사례에서 VPC 구성 필요; 운영 오버헤드 증가

### 옵션 3: 기존 API Lambda 확장

기존 `ttobak-api` Lambda에 크롤링 엔드포인트를 추가합니다. EventBridge가 API Gateway 엔드포인트를 트리거하여 크롤링을 시작합니다.

- **장점**: 새 인프라 불필요; 기존 Lambda 및 API Gateway 재사용
- **단점**: API Lambda 30초 타임아웃으로 크롤링에 부적합; 장시간 배치 작업이 요청-응답 API와 결합; 증가된 패키지 크기로 인한 콜드 스타트가 API 레이턴시에 영향; 단일 Lambda 책임 원칙 위반

## 결정

옵션 1을 선택합니다: 전용 CrawlerStack을 갖춘 Lambda + Step Functions.

아키텍처는 Step Functions가 오케스트레이션하는 4개의 집중된 Lambda를 도입합니다:
- `ttobak-crawler-orchestrator`: DynamoDB에서 활성 크롤러 소스 목록 조회
- `ttobak-crawler-tech`: 서비스별 AWS 문서 크롤링 (What Is, Best Practices, FAQ 페이지)
- `ttobak-crawler-news`: Naver News API, Google News RSS, 기술 미디어, 사용자 지정 URL에서 고객사 뉴스 크롤링
- `ttobak-crawler-ingest`: `shared/` S3 접두사에 대한 Bedrock KB 인제스션 트리거

고객사 소스는 DynamoDB에 `CRAWLER#<sourceId>` 항목으로 `subscribers` 목록과 함께 저장됩니다. 여러 SA가 동일한 고객사를 등록하면 기존 소스를 구독하고 AWS 서비스 선택은 합집합으로 병합됩니다. 크롤링은 구독자 수에 관계없이 소스당 1회 실행됩니다.

크롤링된 문서는 KB S3 버킷의 `shared/aws-docs/`와 `shared/news/` 접두사에 저장됩니다. QA Lambda의 KB 검색 필터는 사용자별 접두사와 함께 `shared/`를 포함하도록 업데이트됩니다.

새로운 Insights 페이지(`/insights`)의 News와 Tech 탭이 크롤링된 콘텐츠를 브라우징할 수 있는 인터페이스를 제공합니다. Settings에는 고객사 구독 관리를 위한 Crawler Sources 섹션이 추가됩니다.

## 영향

### 긍정적
- QA 응답이 시맨틱 KB 검색을 통해 심층적인 AWS 서비스 지식을 획득합니다 (현재 키워드 전용 문서 검색 대비)
- SA가 기술적 미팅 노트와 함께 고객사 비즈니스 컨텍스트(뉴스, 보도)를 얻습니다
- 공유 KB로 여러 SA가 동일 고객사를 담당할 때 중복 크롤링을 제거합니다
- Step Functions가 내장된 재시도, 에러 처리, 병렬 실행을 제공합니다
- Insights UI가 QA 쿼리에 의존하지 않고 크롤링된 인텔리전스를 위한 전용 뷰를 SA에게 제공합니다
- 서버리스 아키텍처와 효율적인 Haiku 요약으로 비용이 낮습니다 (월 ~$12-27)

### 부정적
- 4개 Lambda, Step Functions, EventBridge 규칙을 포함한 새 CDK 스택 유지보수 필요
- 일일 스케줄로 새 콘텐츠에 최대 24시간 지연 발생 (실시간 크롤링 없음)
- 크롤러당 Lambda 5분 타임아웃으로 뉴스 보도가 많은 고객사의 볼륨이 제한될 수 있음
- 뉴스 크롤링이 외부 API(Naver, Google RSS)에 의존하여 변경 또는 속도 제한 가능
- 공유 KB는 모든 SA가 모든 크롤링 콘텐츠를 볼 수 있어 다른 SA의 고객사에서 온 무관한 소스가 포함될 수 있음
- BeautifulSoup/newspaper3k HTML 파싱은 사이트 레이아웃 변경에 취약

## 참고 자료
- `docs/superpowers/specs/2026-04-17-crawler-insights-design.md` -- 전체 설계 명세
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- 관련: 외부 접근을 위한 MCP 서버
- `infra/lib/knowledge-stack.ts` -- 기존 KB 인프라 (OpenSearch Serverless + Bedrock KB)
- `backend/python/qa/handler.py` -- 현재 `search_aws_docs` 도구가 포함된 QA Lambda
- `backend/python/qa/tools.py` -- KB 검색 필터 로직을 포함한 도구 정의
