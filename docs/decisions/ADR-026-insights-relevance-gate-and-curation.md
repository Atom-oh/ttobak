# ADR-026: Insights Relevance Gate and Manual Curation

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted — implemented. Reviewed by the co-agent panel (Kiro CLI running claude-opus-4.8 and glm-5; both independently returned verdict FAIL on the pre-fix structure).

## Context
Customer-facing insights under `/insights/{sourceId}/{docHash}` were showing articles unrelated to the customer (e.g. an unrelated article surfaced under the `hanabank` crawler source). Tracing the pipeline (`backend/python/crawler/news_crawler.py`) found the cause: `_process_article`'s only filters were missing url/title, non-http(s) scheme, empty snippet, a paywall URL blocklist, and URL-hash dedup — no check anywhere that a search result was actually about the customer. `_generate_search_queries` runs bare `{sourceName}` and `{sourceName} {keyword}` web searches (and, for keyword-only sources, bare topic keywords like "AI" as global searches), which routinely return results that merely mention the name in passing, are about a same-named unrelated entity (Korean company names like "하나"/"우리" are also common words), or are competitor-focused. There was also no way to remove an already-ingested bad doc — `/api/insights` only exposed `GET` routes, and `DELETE /api/crawler/sources/{sourceId}` disables a source's subscription without touching its `DOC#` items or S3 KB objects.

## Decision

### 1. Relevance gate folded into the existing summarize call
`_summarize_and_tag` (called once per article regardless) now also returns a relevance verdict: the prompt asks the model whether the article is actually about the customer/keywords (explicitly rejecting same-name-only mentions, unrelated same-named entities, and competitor-focused coverage), and the response contract gains `relevant: bool` + `relevanceConfidence: 0.0-1.0`. `_process_article` skips the article when `not relevant or confidence < RELEVANCE_THRESHOLD` (env-configurable, default 0.7). This is the single choke point every search-result ingest path already routes through, so no second Bedrock round-trip is needed.

**Fails closed**: a Bedrock exception or an unparseable response returns `relevant=False, confidence=0.0` — an article the gate couldn't score is treated as noise, not accepted on faith. This also fixes a pre-existing bug where a Bedrock failure silently wrote a doc with a blank summary instead of being skipped/retried.

**customUrls bypass the gate** (`require_relevance=False`): a user-supplied URL is an explicit ingest request, not a search result to be judged for relevance.

### 2. Manual curation: `DELETE /api/insights/{sourceId}/{docHash}`
`InsightsService.DeleteDocument` deletes the DynamoDB metadata item and the S3 KB object (trying both the `shared/news/` and `shared/aws-docs/` key shapes when no `S3Key` is stored, mirroring `GetDocumentDetail`'s read-path fallback, so a tech doc's object isn't silently left behind). Authorization is scoped to the source's **owner** (`CrawlerSource.OwnerID`, set to the creator at `AddSource` time) or an admin — NOT any subscriber: `AddSource` lets any authenticated user self-subscribe to an existing source with no invite/approval step, so gating on subscription alone would make this destructive route trivially self-grantable. This mutating route deliberately does not inherit `GetDocumentContent`'s open-read posture (reads are shared substrate by design; a delete is not). A successful delete is logged (`userID`/`sourceID`/`docHash`) as an audit trail for an irreversible action. The Bedrock Knowledge Base's vector index is not purged synchronously — it only reconciles on the next ingestion job (existing `POST /api/kb/sync`, or the daily crawl pipeline's own trigger), so a deleted doc can still surface in Q&A briefly.

### 3. Batch re-score of existing docs: `scripts/insights-rescore.py`
Docs ingested before this gate existed aren't retroactively filtered. The script queries `GSI4PK='DOC#news'`, re-scores each doc's stored title+summary against the same relevance prompt, and (with `--run`) purges below-threshold docs from DynamoDB + S3, then triggers one KB ingestion job to reconcile the deleted vectors. Dry-run by default.

## Consequences

### Positive
- The reported bug's root cause (zero relevance checking) is closed at the one choke point all search-result ingestion already passes through, with no added Bedrock cost or latency.
- Fail-closed behavior means an infra hiccup produces a dropped/retried article, never a silently-accepted blank-summary doc.
- Owners can now remove a bad insight directly instead of it persisting forever with no curation path.

### Negative
- A borderline-relevant article near the threshold may be dropped (false negative) — the LLM's judgment on relevance can be wrong in either direction, same as its judgment on the summary itself.
- Deleting a doc doesn't immediately clear it from Bedrock KB / RAG Q&A results — callers relying on immediate consistency need to know about the ingestion-job lag.
- `scripts/insights-rescore.py` re-scores from the stored summary, not the original snippet (not persisted) — a marginal loss of signal versus re-scoring the original search result, accepted as good enough for a one-time backfill.

## Alternatives Considered
| Option | Pros | Cons |
|--------|------|------|
| Relevance verdict folded into existing summarize call (chosen) | Zero added Bedrock cost/latency; model sees full context (title+snippet) so can distinguish real coverage from passing mentions | Same call both judges and summarizes, so a bad summary and a bad verdict can correlate |
| Separate classifier call | Cleaner separation of concerns | Doubles Bedrock invocations/latency per article for no clear precision gain here |
| Pure lexical name/alias matching | Free, deterministic, zero latency | Korean company names are highly ambiguous ("우리은행"→"우리" = "we/our", "하나은행"→"하나" = "one") — substring matching alone has severe false positives/negatives; rejected as the sole mechanism, though it remains a candidate pre-filter if Bedrock cost ever becomes a concern |
| Cascade-delete docs when a crawler source is unsubscribed | Would also incidentally clean up bad docs | Out of scope — `Unsubscribe`'s current behavior (disable subscription, keep docs) is intentional per existing crawler semantics; conflating it with content curation would change unrelated behavior |

---

<a id="korean"></a>

# 한국어

## 상태
승인됨 — 구현 완료. co-agent 패널(Kiro CLI, claude-opus-4.8 및 glm-5 각각 독립 실행) 리뷰 완료 — 두 모델 모두 수정 전 구조에 대해 독립적으로 FAIL 판정.

## 맥락
`/insights/{sourceId}/{docHash}`의 고객사 인사이트에 무관한 기사가 노출되는 문제가 있었다(예: `hanabank` 크롤러 소스 아래 무관한 기사). 파이프라인(`backend/python/crawler/news_crawler.py`)을 추적한 결과, `_process_article`의 필터는 url/title 누락, non-http(s) 스킴, 빈 snippet, paywall URL 블록리스트, URL 해시 dedup뿐이었고, 검색 결과가 실제로 고객사와 관련 있는지 확인하는 로직이 전혀 없었다. `_generate_search_queries`는 `{고객사명}` 또는 `{고객사명} {키워드}` 단독 검색(키워드만 있는 소스는 "AI" 같은 전역 주제 검색)을 수행하는데, 이는 이름이 우연히 언급되었을 뿐인 기사, 동명이인/동명 기업(한국 기업명 "하나"/"우리"는 흔한 단어이기도 함) 기사, 경쟁사 위주 기사를 자주 반환한다. 또한 이미 수집된 잘못된 문서를 제거할 방법도 없었다 — `/api/insights`는 GET 라우트만 노출했고, `DELETE /api/crawler/sources/{sourceId}`는 구독만 해지할 뿐 `DOC#` 항목이나 S3 KB 객체는 건드리지 않았다.

## 결정

### 1. 기존 요약 호출에 관련성 판단 통합
매 기사마다 이미 호출되는 `_summarize_and_tag`가 이제 관련성 판단도 함께 반환한다: 프롬프트는 모델에게 이 기사가 실제로 고객사/관심 키워드에 관한 것인지 판단하도록 요구하며(단순 이름 언급, 동명 무관 기업, 경쟁사 위주 기사는 명시적으로 관련 없음으로 판단하도록 지시), 응답 계약에 `relevant: bool` + `relevanceConfidence: 0.0-1.0`가 추가되었다. `_process_article`는 `not relevant or confidence < RELEVANCE_THRESHOLD`(환경변수로 조정 가능, 기본 0.7)일 때 기사를 건너뛴다. 모든 검색 결과 수집 경로가 이미 통과하는 단일 지점이므로 추가 Bedrock 호출이 필요 없다.

**Fail-closed**: Bedrock 예외 또는 파싱 불가능한 응답은 `relevant=False, confidence=0.0`을 반환한다 — 판단할 수 없는 기사는 신뢰가 아니라 노이즈로 취급한다. 이는 기존 버그(Bedrock 실패 시 빈 요약으로 그대로 저장되던 문제)도 함께 수정한다.

**customUrls는 게이트 우회** (`require_relevance=False`): 사용자가 직접 넣은 URL은 명시적 수집 요청이며 관련성을 판단할 검색 결과가 아니다.

### 2. 수동 큐레이션: `DELETE /api/insights/{sourceId}/{docHash}`
`InsightsService.DeleteDocument`는 DynamoDB 메타데이터 항목과 S3 KB 객체를 삭제한다(`S3Key`가 저장되어 있지 않으면 `GetDocumentDetail`의 읽기 경로와 동일하게 `shared/news/`와 `shared/aws-docs/` 두 키 형태를 모두 시도해, tech 문서의 객체가 조용히 남는 것을 방지한다). 권한은 해당 소스의 **owner**(`CrawlerSource.OwnerID`, `AddSource` 시점에 생성자로 설정)나 admin으로 한정된다 — 단순 구독자는 안 된다: `AddSource`는 승인 절차 없이 누구나 기존 소스를 자가 구독할 수 있게 하므로, 구독 여부만으로 권한을 주면 이 파괴적인 라우트를 누구나 스스로 획득할 수 있게 된다. 이 mutating 라우트는 `GetDocumentContent`의 열려있는 읽기 권한 정책을 의도적으로 물려받지 않는다(읽기는 설계상 공유 substrate이지만 삭제는 아님). 삭제 성공 시 `userID`/`sourceID`/`docHash`를 로그로 남겨 되돌릴 수 없는 작업의 감사 추적을 남긴다. Bedrock Knowledge Base의 벡터 인덱스는 즉시 정리되지 않으며 다음 ingestion job(기존 `POST /api/kb/sync` 또는 일일 크롤 파이프라인의 자체 트리거)에서만 반영된다 — 삭제된 문서가 잠시 Q&A에 남을 수 있다.

### 3. 기존 데이터 배치 재스코어링: `scripts/insights-rescore.py`
이 게이트가 존재하기 전에 수집된 문서는 소급 필터링되지 않는다. 스크립트는 `GSI4PK='DOC#news'`를 조회해 각 문서의 저장된 title+summary를 동일한 관련성 프롬프트로 재평가하고, `--run` 시 임계값 미달 문서를 DynamoDB+S3에서 삭제한 뒤 삭제된 벡터를 정리하기 위해 KB ingestion job을 1회 트리거한다. 기본은 dry-run.

## 결과

### 긍정적 영향
- 보고된 버그의 근본 원인(관련성 검사 부재)이 모든 검색 결과 수집이 통과하는 단일 지점에서, 추가 Bedrock 비용/지연 없이 해결됨.
- Fail-closed 동작으로 인프라 장애 시 기사가 누락/재시도될 뿐, 빈 요약 문서가 조용히 저장되는 일이 없음.
- 소유자가 이제 잘못된 인사이트를 직접 제거할 수 있어, 큐레이션 경로 없이 영구히 남는 문제가 해결됨.

### 부정적 영향
- 임계값 근처의 경계선 관련 기사가 거짓 음성으로 누락될 수 있음 — LLM의 관련성 판단은 요약 판단과 마찬가지로 양방향으로 틀릴 수 있음.
- 문서 삭제가 Bedrock KB/RAG Q&A 결과에서 즉시 반영되지 않음 — 즉시 일관성이 필요한 호출자는 ingestion job 지연을 인지해야 함.
- `scripts/insights-rescore.py`는 저장된 summary 기준으로 재평가하며 원본 snippet(저장되지 않음) 기준이 아님 — 원본 검색 결과 재평가보다 신호가 약간 떨어지지만 일회성 백필로는 충분하다고 판단.

## 검토한 대안
| 옵션 | 장점 | 단점 |
|--------|------|------|
| 기존 요약 호출에 관련성 판단 통합 (채택) | 추가 Bedrock 비용/지연 없음; 모델이 전체 컨텍스트(title+snippet)를 보므로 실제 보도와 단순 언급을 구분 가능 | 같은 호출이 판단과 요약을 동시에 하므로 나쁜 요약과 나쁜 판단이 상관될 수 있음 |
| 별도 분류기 호출 | 관심사 분리가 깔끔함 | 기사당 Bedrock 호출/지연이 2배가 되는데 여기서는 명확한 정밀도 이득이 없음 |
| 순수 lexical 이름/별칭 매칭 | 무료, 결정적, 지연 없음 | 한국 기업명은 모호성이 심함("우리은행"→"우리", "하나은행"→"하나") — 단순 매칭만으로는 거짓양성/거짓음성이 심각해 단독 메커니즘으로는 기각(다만 Bedrock 비용이 문제가 될 경우 사전 필터 후보로는 남겨둠) |
| 크롤러 소스 구독 해지 시 문서 cascade 삭제 | 부수적으로 잘못된 문서도 정리됨 | 범위 밖 — `Unsubscribe`의 현재 동작(구독만 해지, 문서는 유지)은 기존 크롤러 동작상 의도된 것이며, 콘텐츠 큐레이션과 결합하면 무관한 동작을 변경하게 됨 |
