# ADR-021: Repair the Crawler → KB Ingestion Pipeline and Add AWS Service Auto-Discovery

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
The crawler Step Functions pipeline (`ttobak-crawler-daily` → `ttobak-crawler-workflow`) ran successfully every night and wrote fresh crawled documents to S3, but Bedrock Knowledge Base ingestion had silently stopped since 2026-05-28 (7 weeks). Two independent bugs combined to hide this:

1. **`KB_ID`/`DATA_SOURCE_ID` reset to `'PENDING'`.** `infra/lib/knowledge-stack.ts` hardcodes these as placeholders (the real KB/DataSource were created out-of-band; the CFN `AWS::Bedrock::KnowledgeBase`/`DataSource` resources are commented out as "Phase 2"). A CDK redeploy of `TtobakCrawlerStack`/`TtobakGatewayStack` overwrote the previously-working manual env var values with `'PENDING'` again.
2. **Step Functions payload shape mismatch.** `ingest_trigger.py` expects the aggregated crawl results under a `crawlerResults` key (or a bare list); the actual `ParallelCrawl` state wrote branch outputs under `$.techResult`/`$.newsResults` inside `$.crawlResults`, a shape `ingest_trigger.py` never received correctly — so it always saw "0 crawler(s)" and skipped ingestion regardless of KB_ID.
3. **The failure was invisible.** `ingest_trigger.py` caught every failure (missing config, `start_ingestion_job` exception) and returned `{"status": "ERROR"}` as a normal Lambda result — Step Functions saw this as a successful state transition, so every execution showed SUCCEEDED in the console every single night.

Separately, the tech crawler only searches AWS What's New/Blog RSS for services already listed in a customer's `awsServices` config — it has no way to discover a brand-new AWS service before someone manually adds it. The AgentCore Gateway Web Search connector (already used by the news crawler for company news) was underused: the tech crawler had the `WEB_SEARCH_GATEWAY_URL` env var wired in `infra/lib/crawler-stack.ts`'s `commonEnv` but never called it.

## Decision
1. **Fix the KB IDs**: hardcode the real out-of-band values (`BJJLVLFTOR` / `3AVMMT3RF3`) in `knowledge-stack.ts` and the QA Lambda's fallback, with a comment warning never to redeploy `TtobakKnowledgeStack` for real.
2. **Fix the Step Functions payload**: `CrawlTechDocs` and `MapNewsSources` now use `OutputPath` (not `ResultPath`) so each `ParallelCrawl` branch emits a bare result value; `TriggerIngestion` reads `$.crawlResults` directly. This produces `[techResult, [newsResult, ...]]`, which `ingest_trigger.py`'s existing one-level flatten already handles correctly.
3. **Fail loud**: `ingest_trigger.py` now raises instead of returning an `ERROR` status, so a broken config or API failure surfaces as a FAILED Step Functions execution.
4. **Reuse the AgentCore Gateway Web Search connector in the tech crawler** (`tech_crawler.py` imports `news_crawler` for its SigV4 MCP client rather than duplicating it) for two purposes:
   - An extra search query per known service (`AWS {service} 신기능 발표`), supplementing RSS results with announcements RSS/blog feeds haven't indexed yet.
   - A once-per-run discovery search (`AWS 신규 서비스 GA 출시 발표`) whose results are summarized by Bedrock into candidate service slugs. Slugs not already known are merged into a synthetic `CRAWLER#__auto__/CONFIG` DynamoDB item's `awsServices` list (capped at 30 total, 5 new per run), which `orchestrator.py` picks up on the next scheduled run like any other tech-config source.
5. **`orchestrator.py`** excludes any `sourceId` prefixed `__` (e.g. `__auto__`) from the per-customer news fan-out — these synthetic sources only ever contribute `awsServices`, never a real news config.
6. **Crawl history** (`CRAWLER#{sourceId}/HISTORY#{timestamp}`) is now written after each news-crawler run, and the source's `CONFIG` status/`lastCrawledAt` updated — the Settings UI's crawl history list and status badge previously always showed empty/idle because nothing wrote this data.

## Consequences

### Positive
- KB ingestion runs again every night; a 7-week backlog is backfilled with a one-time manual `start-ingestion-job` call.
- A future config regression (KB_ID reset, payload shape change, API permission loss) fails the Step Functions execution visibly instead of silently succeeding.
- The tech crawler now surfaces AWS service launches it previously couldn't know to look for, without any manual Settings-page edit.
- Settings page crawl history/status becomes accurate.

### Negative
- One extra Bedrock `converse()` call per tech crawler run (discovery) and one extra AgentCore Gateway search per known service — modest added cost/latency, bounded by the existing 14-minute Lambda timeout.
- Auto-discovered services get no human review before their RSS/blog/search results start flowing into the KB; a bad Bedrock extraction could add a garbage slug, but the regex validation (`^[a-z0-9-]{2,30}$`) and hard cap (30 total) bound the damage, and a mistaken slug just yields empty crawl results (SERVICE_BLOG_MAP falls back to a generic AWS blog category on an unrecognized service).
- `TtobakKnowledgeStack` must still never be redeployed for real (unchanged constraint, now documented more prominently).

## References
- Root `CLAUDE.md` — "Known Issues" (KB ID hardcoding, `TtobakKnowledgeStack` non-deployment) and "Speaker diarization" section style this ADR follows.
- `backend/python/crawler/{orchestrator,tech_crawler,news_crawler,ingest_trigger}.py`, `infra/lib/{crawler,knowledge}-stack.ts` — implementation.
- `docs/INFRA-SPEC.md` §6A (CrawlerStack) — pipeline reference.

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
크롤러 Step Functions 파이프라인(`ttobak-crawler-daily` → `ttobak-crawler-workflow`)은 매일 밤 정상적으로 실행되며 S3에 새 크롤 문서를 계속 기록했지만, Bedrock Knowledge Base 인제스천은 2026-05-28 이후 7주간 조용히 멈춰 있었습니다. 두 가지 독립된 버그가 겹쳐 이를 숨겼습니다:

1. **`KB_ID`/`DATA_SOURCE_ID`가 `'PENDING'`으로 리셋됨.** `infra/lib/knowledge-stack.ts`는 이 값들을 플레이스홀더로 하드코딩하고 있었습니다(실제 KB/DataSource는 out-of-band로 생성되었고, `AWS::Bedrock::KnowledgeBase`/`DataSource` CFN 리소스는 "Phase 2"로 주석 처리돼 있음). `TtobakCrawlerStack`/`TtobakGatewayStack`을 CDK로 재배포하면서 이전에 수동으로 맞춰뒀던 값이 다시 `'PENDING'`으로 덮어써졌습니다.
2. **Step Functions 페이로드 형태 불일치.** `ingest_trigger.py`는 집계된 크롤 결과가 `crawlerResults` 키(또는 단순 리스트) 아래에 있길 기대하지만, 실제 `ParallelCrawl` 상태는 브랜치 출력을 `$.crawlResults` 안의 `$.techResult`/`$.newsResults`로 감싸서 기록했습니다 — `ingest_trigger.py`는 이를 제대로 받지 못해 항상 "0 crawler(s)"로 보고 KB_ID와 무관하게 인제스천을 건너뛰었습니다.
3. **장애가 눈에 보이지 않았음.** `ingest_trigger.py`는 모든 실패(설정 누락, `start_ingestion_job` 예외)를 잡아 `{"status": "ERROR"}`를 평범한 Lambda 결과로 반환했습니다 — Step Functions는 이를 정상적인 상태 전이로 인식해, 매일 밤 콘솔에는 SUCCEEDED로만 표시되었습니다.

별개로, tech 크롤러는 고객사의 `awsServices` 설정에 이미 등록된 서비스에 대해서만 AWS What's New/Blog RSS를 검색합니다 — 누군가 Settings 페이지에서 수동으로 추가하기 전에는 완전히 새로운 AWS 서비스를 발견할 방법이 없었습니다. 뉴스 크롤러가 이미 사용 중인 AgentCore Gateway Web Search 커넥터는 tech 크롤러 환경변수(`infra/lib/crawler-stack.ts`의 `commonEnv`)에 `WEB_SEARCH_GATEWAY_URL`이 이미 주입돼 있었음에도 실제로는 한 번도 호출되지 않아 활용도가 낮았습니다.

## 결정
1. **KB ID 수정**: 실제 out-of-band 값(`BJJLVLFTOR` / `3AVMMT3RF3`)을 `knowledge-stack.ts`와 QA Lambda의 fallback에 하드코딩하고, `TtobakKnowledgeStack`을 실제로 재배포하면 안 된다는 경고 주석을 추가.
2. **Step Functions 페이로드 수정**: `CrawlTechDocs`와 `MapNewsSources`가 `ResultPath` 대신 `OutputPath`를 사용해 각 `ParallelCrawl` 브랜치가 래퍼 없는 결과 값을 그대로 출력하도록 함. `TriggerIngestion`은 `$.crawlResults`를 직접 입력받음. 결과 형태는 `[techResult, [newsResult, ...]]`이며, `ingest_trigger.py`에 이미 있던 1단계 flatten 로직이 이를 그대로 처리.
3. **실패를 숨기지 않음**: `ingest_trigger.py`는 이제 `ERROR` 상태를 리턴하는 대신 예외를 raise해, 설정 오류나 API 실패가 Step Functions 실행을 FAILED로 표면화함.
4. **tech 크롤러에서 AgentCore Gateway Web Search 커넥터 재사용** (`tech_crawler.py`가 SigV4 MCP 클라이언트를 새로 만들지 않고 `news_crawler`를 import해 재사용) — 두 가지 용도로:
   - 이미 알고 있는 서비스별로 검색 쿼리 1건 추가(`AWS {service} 신기능 발표`) — RSS/블로그가 아직 색인하지 않은 발표를 보완.
   - 실행당 1회의 발견 검색(`AWS 신규 서비스 GA 출시 발표`) — 결과를 Bedrock으로 요약해 후보 서비스 slug를 추출. 아직 모르는 slug는 합성 DynamoDB 아이템 `CRAWLER#__auto__/CONFIG`의 `awsServices`에 병합(전체 최대 30개, 회당 신규 최대 5개)되며, 다음 예정 실행 때 `orchestrator.py`가 다른 tech 설정 소스와 동일하게 이를 수집.
5. **`orchestrator.py`**는 `sourceId`가 `__`로 시작하는 소스(예: `__auto__`)를 고객사별 뉴스 팬아웃에서 제외 — 이런 합성 소스는 항상 `awsServices`만 기여하며 실제 뉴스 설정을 갖지 않음.
6. **크롤 이력**(`CRAWLER#{sourceId}/HISTORY#{timestamp}`)을 뉴스 크롤러 실행마다 기록하고, 소스의 `CONFIG` 상태/`lastCrawledAt`을 갱신 — Settings UI의 크롤 이력 목록과 상태 배지가 이전엔 아무도 이 데이터를 쓰지 않아 항상 비어있거나 idle로만 표시됐음.

## 결과

### 긍정적
- KB 인제스천이 매일 밤 다시 정상 실행됨. 7주치 밀린 문서는 1회 수동 `start-ingestion-job` 호출로 백필.
- 향후 설정 회귀(KB_ID 리셋, 페이로드 형태 변경, API 권한 손실)가 발생하면 Step Functions 실행이 눈에 보이게 실패하며, 조용히 성공한 것처럼 보이지 않음.
- tech 크롤러가 이전에는 알 방법이 없었던 AWS 서비스 출시를 Settings 페이지 수동 편집 없이도 포착.
- Settings 페이지 크롤 이력/상태가 실제와 일치하게 됨.

### 부정적
- tech 크롤러 실행마다 Bedrock `converse()` 호출 1회(발견용)와 알고 있는 서비스별 AgentCore Gateway 검색 1회가 추가됨 — 기존 14분 Lambda 타임아웃 내에서 감당 가능한 수준의 비용/지연 증가.
- 자동 발견된 서비스는 KB에 반영되기 전 사람의 검토를 거치지 않음. Bedrock 추출 오류로 잘못된 slug가 추가될 수 있으나, 정규식 검증(`^[a-z0-9-]{2,30}$`)과 상한(전체 30개)이 피해를 제한하며, 잘못된 slug는 그저 빈 크롤 결과를 낳을 뿐(SERVICE_BLOG_MAP은 미인식 서비스에 대해 일반 AWS 블로그 카테고리로 폴백).
- `TtobakKnowledgeStack`은 여전히 실제로 재배포하면 안 됨(기존 제약, 이번에 더 명확히 문서화).

## 참고
- 루트 `CLAUDE.md` — "Known Issues"(KB ID 하드코딩, `TtobakKnowledgeStack` 미배포)와 "Speaker diarization" 절의 서술 스타일을 따름.
- `backend/python/crawler/{orchestrator,tech_crawler,news_crawler,ingest_trigger}.py`, `infra/lib/{crawler,knowledge}-stack.ts` — 구현.
- `docs/INFRA-SPEC.md` §6A (CrawlerStack) — 파이프라인 레퍼런스.
