# ADR-008: Custom Dictionary for STT Accuracy Improvement

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

Ttobak is used by AWS Solutions Architects in meetings that involve highly specialized terminology: AWS service names (SageMaker, EKS, LoRA, RAG), customer-specific product names, Korean business jargon, and mixed Korean-English technical terms. Standard STT engines often misrecognize these terms because they fall outside general-purpose language models.

A static Custom Vocabulary (`ttobak-aws-tech-terms`, 30 AWS/tech terms) was added to AWS Transcribe in commit `9f45d3f`, which improved recognition of common AWS terms. However, this vocabulary is hardcoded in the backend and cannot be customized per user or per customer context. Solutions Architects working with different customers need different terminology sets (e.g., banking terms for Hana Bank, insurance terms for Samsung Life).

Additionally, the project is evaluating Whisper large-v3 as an alternative STT engine. Whisper does not natively support custom vocabularies — it requires prompt-based guidance or fine-tuning for domain adaptation.

## Options Considered

### Option 1: Per-User Custom Vocabulary via AWS Transcribe API (Chosen)

Each user manages a personal dictionary through a Settings UI. The backend creates or updates an AWS Transcribe Custom Vocabulary per user (`ttobak-vocab-{userId}`). The vocabulary is passed to `StartTranscriptionJob` at transcription time. A system-level base vocabulary (`ttobak-aws-tech-terms`) is merged with the user's vocabulary.

- **Pros**: Native Transcribe integration; phonetic pronunciation mapping for Korean (e.g., "SageMaker" -> "세이지메이커"); per-user customization; no model retraining; vocabulary changes take effect in ~15 minutes
- **Cons**: AWS Transcribe vocabulary limit is 50KB per vocabulary; `CreateVocabulary` / `UpdateVocabulary` API calls are rate-limited; vocabulary must be in a specific format (4-column TSV: Phrase, IPA, SoundsLike, DisplayAs); each user creates a separate Transcribe resource

### Option 2: Whisper Prompt-Based Guidance

Pass domain-specific terms as the `initial_prompt` parameter to Whisper. The prompt biases the model toward recognizing listed terms without any API or resource management.

- **Pros**: No AWS resource management; works with self-hosted Whisper; instant effect (no build time); simple implementation
- **Cons**: Prompt length limited (~224 tokens); no phonetic mapping; effectiveness varies by term; not available for AWS Transcribe; less reliable than dedicated vocabulary

### Option 3: Shared Organization-Level Dictionary

A single dictionary managed at the organization level (not per-user). All users in an organization share the same vocabulary.

- **Pros**: Simpler management; one vocabulary per org; consistent terminology across team
- **Cons**: Ttobak is currently single-user; over-engineering for current scale; different SAs may work with different customers needing different terms; no personalization

## Decision

Use Option 1: Per-User Custom Vocabulary via AWS Transcribe API, with Option 2 as a complementary approach for Whisper.

### Data Model

```
DynamoDB Item:
  PK: "USER#{userId}"
  SK: "DICTIONARY"
  terms: [
    { "phrase": "SageMaker", "soundsLike": "세이지메이커", "displayAs": "SageMaker" },
    { "phrase": "LoRA", "soundsLike": "로라", "displayAs": "LoRA" },
    { "phrase": "하나은행", "soundsLike": "", "displayAs": "하나은행" }
  ]
  vocabularyName: "ttobak-vocab-{userId}"
  vocabularyStatus: "READY" | "PENDING" | "FAILED"
  updatedAt: "2026-04-23T..."
```

### API Endpoints

```
GET  /api/settings/dictionary        -> { terms: Term[], status: string }
PUT  /api/settings/dictionary        -> { terms: Term[] }
DELETE /api/settings/dictionary/term  -> { phrase: string }
```

### Transcription Flow

```
1. User uploads audio → transcribe Lambda triggered
2. Lambda reads user's dictionary from DynamoDB
3. If vocabularyName exists and status is READY:
   → Pass VocabularyName to StartTranscriptionJob
4. If Whisper engine selected:
   → Build initial_prompt from user's terms
5. Merge base vocabulary (ttobak-aws-tech-terms) with user terms
```

### Auto-Extraction from Insights & Research

Ttobak already crawls AWS documentation, customer news, and generates deep research reports (ADR-004, ResearchAgentStack). These documents contain domain-specific terminology that can seed the custom dictionary automatically:

```
Crawler/Research pipeline
  → New document ingested (S3 + DynamoDB)
    → Term extraction Lambda (Bedrock Haiku)
      → Identify technical terms, acronyms, product names
      → Generate Korean pronunciation (SoundsLike)
      → Suggest additions to user's dictionary
        → User reviews & approves in Settings UI
```

This creates a virtuous cycle: crawled insights improve STT accuracy, which produces better transcripts, which generate better meeting summaries.

### Settings UI

A "Custom Dictionary" section in the Settings page:
- Table of terms with Phrase, Pronunciation (Korean), Display As columns
- Add/edit/delete individual terms
- Bulk import from CSV
- **"Suggested terms" section** — auto-extracted from Insights/Research, one-click approve
- Status indicator (READY / Building... / Failed)
- Pre-populated with common AWS terms that users can extend

## Consequences

### Positive
- SAs can add customer-specific terminology for each engagement
- Korean pronunciation mapping dramatically improves recognition of English technical terms
- Base vocabulary provides sensible defaults; users extend rather than start from scratch
- Whisper prompt guidance provides a complementary path for self-hosted STT
- Terms persist across meetings — configure once, benefit always

### Negative
- Each user creates a Transcribe Custom Vocabulary resource (~15 min build time on first save and each update)
- Transcribe `CreateVocabulary` API is rate-limited (max 10 concurrent builds)
- Vocabulary format is strict (invalid phonetics cause build failure); need client-side validation
- Two vocabulary systems to maintain if both Transcribe and Whisper are used (Transcribe TSV format vs Whisper prompt)
- DynamoDB item size grows with terms (but well within 400KB limit for typical dictionaries of <500 terms)

## References
- Commit `9f45d3f`: Static Custom Vocabulary (`ttobak-aws-tech-terms`, 30 terms)
- `backend/internal/service/transcribe.go:65` -- Current hardcoded VocabularyName
- [AWS Transcribe Custom Vocabularies](https://docs.aws.amazon.com/transcribe/latest/dg/custom-vocabulary.html)
- [Whisper initial_prompt parameter](https://platform.openai.com/docs/guides/speech-to-text/prompting)
- `docs/stt-ab-benchmark-results.md` -- STT A/B benchmark showing terminology recognition gaps
- `docs/decisions/ADR-005-multi-language-auto-detection-for-stt.md` -- Related: multi-language STT

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경

Ttobak은 고도로 전문화된 용어가 포함된 미팅에서 AWS Solutions Architect가 사용합니다: AWS 서비스 이름(SageMaker, EKS, LoRA, RAG), 고객사 고유 제품명, 한국어 비즈니스 용어, 한영 혼합 기술 용어 등. 표준 STT 엔진은 이러한 용어가 범용 언어 모델 범위 밖이기 때문에 자주 오인식합니다.

정적 Custom Vocabulary(`ttobak-aws-tech-terms`, 30개 AWS/기술 용어)가 커밋 `9f45d3f`에서 AWS Transcribe에 추가되어 일반적인 AWS 용어 인식이 개선되었습니다. 하지만 이 어휘집은 백엔드에 하드코딩되어 있어 사용자별 또는 고객 컨텍스트별로 커스터마이징할 수 없습니다. 다른 고객사를 담당하는 SA는 서로 다른 용어 세트가 필요합니다 (예: 하나은행 담당은 금융 용어, 삼성생명 담당은 보험 용어).

또한 프로젝트는 대안 STT 엔진으로 Whisper large-v3를 평가 중입니다. Whisper는 Custom Vocabulary를 기본 지원하지 않아 도메인 적응을 위해 프롬프트 기반 가이드 또는 파인튜닝이 필요합니다.

## 검토한 옵션

### 옵션 1: AWS Transcribe API를 통한 사용자별 Custom Vocabulary (선택됨)

각 사용자가 Settings UI를 통해 개인 사전을 관리합니다. 백엔드가 사용자별 AWS Transcribe Custom Vocabulary(`ttobak-vocab-{userId}`)를 생성 또는 업데이트합니다. 전사 시점에 `StartTranscriptionJob`에 해당 어휘집을 전달합니다. 시스템 레벨 기본 어휘집(`ttobak-aws-tech-terms`)이 사용자 어휘와 병합됩니다.

- **장점**: 네이티브 Transcribe 연동; 한국어 발음 매핑 (예: "SageMaker" -> "세이지메이커"); 사용자별 커스터마이징; 모델 재훈련 불필요; 어휘 변경이 ~15분 내 적용
- **단점**: Transcribe 어휘 크기 제한 50KB; `CreateVocabulary`/`UpdateVocabulary` API 속도 제한; 엄격한 형식 요구 (4열 TSV); 사용자마다 별도 Transcribe 리소스 생성

### 옵션 2: Whisper 프롬프트 기반 가이드

도메인 특화 용어를 Whisper의 `initial_prompt` 파라미터로 전달합니다. 프롬프트가 모델을 해당 용어 인식 쪽으로 편향시킵니다.

- **장점**: AWS 리소스 관리 불필요; 자체 호스팅 Whisper에서 동작; 즉시 효과 (빌드 시간 없음); 간단한 구현
- **단점**: 프롬프트 길이 제한 (~224 토큰); 발음 매핑 없음; 용어별 효과 편차; AWS Transcribe에서 사용 불가; 전용 어휘집보다 덜 안정적

### 옵션 3: 조직 레벨 공유 사전

사용자별이 아닌 조직 레벨에서 관리되는 단일 사전. 조직 내 모든 사용자가 동일한 어휘를 공유합니다.

- **장점**: 관리 단순; 조직당 하나의 어휘; 팀 내 일관된 용어
- **단점**: Ttobak은 현재 단일 사용자; 현재 규모에 과잉 설계; 다른 SA가 다른 고객을 담당하면 다른 용어 필요; 개인화 불가

## 결정

옵션 1을 선택합니다: AWS Transcribe API를 통한 사용자별 Custom Vocabulary. Whisper용으로는 옵션 2를 보완 접근법으로 사용합니다.

### 데이터 모델

```
DynamoDB Item:
  PK: "USER#{userId}"
  SK: "DICTIONARY"
  terms: [
    { "phrase": "SageMaker", "soundsLike": "세이지메이커", "displayAs": "SageMaker" },
    { "phrase": "LoRA", "soundsLike": "로라", "displayAs": "LoRA" },
    { "phrase": "하나은행", "soundsLike": "", "displayAs": "하나은행" }
  ]
  vocabularyName: "ttobak-vocab-{userId}"
  vocabularyStatus: "READY" | "PENDING" | "FAILED"
  updatedAt: "2026-04-23T..."
```

### API 엔드포인트

```
GET  /api/settings/dictionary        -> { terms: Term[], status: string }
PUT  /api/settings/dictionary        -> { terms: Term[] }
DELETE /api/settings/dictionary/term  -> { phrase: string }
```

### 전사 플로우

```
1. 사용자가 오디오 업로드 → transcribe Lambda 트리거
2. Lambda가 DynamoDB에서 사용자 사전 읽기
3. vocabularyName이 존재하고 상태가 READY이면:
   → StartTranscriptionJob에 VocabularyName 전달
4. Whisper 엔진이 선택된 경우:
   → 사용자 용어로 initial_prompt 구성
5. 기본 어휘(ttobak-aws-tech-terms)와 사용자 용어 병합
```

### Insights/Research에서 자동 추출

Ttobak은 이미 AWS 문서, 고객사 뉴스를 크롤링하고 딥 리서치 보고서를 생성합니다 (ADR-004, ResearchAgentStack). 이 문서들에는 사용자 사전을 자동으로 시드할 수 있는 도메인 특화 용어가 포함되어 있습니다:

```
크롤러/리서치 파이프라인
  → 새 문서 수집 (S3 + DynamoDB)
    → 용어 추출 Lambda (Bedrock Haiku)
      → 기술 용어, 약어, 제품명 식별
      → 한국어 발음(SoundsLike) 생성
      → 사용자 사전에 추가 제안
        → 사용자가 Settings UI에서 검토 및 승인
```

이는 선순환을 만듭니다: 크롤링된 인사이트가 STT 정확도를 개선하고, 더 나은 트랜스크립트가 생성되며, 이것이 더 좋은 미팅 요약을 만들어냅니다.

### Settings UI

Settings 페이지의 "사용자 사전" 섹션:
- Phrase, 한국어 발음, 표시명 열이 있는 용어 테이블
- 개별 용어 추가/편집/삭제
- CSV 대량 가져오기
- **"제안된 용어" 섹션** — Insights/Research에서 자동 추출, 원클릭 승인
- 상태 표시기 (준비됨 / 빌드 중... / 실패)
- 일반적인 AWS 용어로 사전 제공, 사용자가 확장

## 영향

### 긍정적
- SA가 각 고객 대응별 고객사 고유 용어를 추가 가능
- 한국어 발음 매핑으로 영어 기술 용어 인식이 크게 향상
- 기본 어휘집이 합리적인 기본값 제공; 사용자는 처음부터 시작하지 않고 확장
- Whisper 프롬프트 가이드가 자체 호스팅 STT를 위한 보완 경로 제공
- 용어가 미팅 간 유지 — 한 번 설정하면 항상 혜택

### 부정적
- 각 사용자가 Transcribe Custom Vocabulary 리소스를 생성 (첫 저장 및 업데이트 시 ~15분 빌드 시간)
- Transcribe `CreateVocabulary` API 속도 제한 (최대 10개 동시 빌드)
- 어휘 형식이 엄격 (잘못된 발음은 빌드 실패 야기); 클라이언트 측 검증 필요
- Transcribe와 Whisper 모두 사용 시 두 어휘 시스템 유지 필요 (Transcribe TSV vs Whisper 프롬프트)
- DynamoDB 항목 크기가 용어에 따라 증가 (하지만 일반적인 500개 미만 사전은 400KB 제한 내)

## 참고 자료
- 커밋 `9f45d3f`: 정적 Custom Vocabulary (`ttobak-aws-tech-terms`, 30개 용어)
- `backend/internal/service/transcribe.go:65` -- 현재 하드코딩된 VocabularyName
- [AWS Transcribe Custom Vocabularies](https://docs.aws.amazon.com/transcribe/latest/dg/custom-vocabulary.html)
- [Whisper initial_prompt 파라미터](https://platform.openai.com/docs/guides/speech-to-text/prompting)
- `docs/stt-ab-benchmark-results.md` -- 용어 인식 갭을 보여주는 STT A/B 벤치마크
- `docs/decisions/ADR-005-multi-language-auto-detection-for-stt.md` -- 관련: 다국어 STT
