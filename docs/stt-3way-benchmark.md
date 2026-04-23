# STT 3-Way Benchmark Results

Date: 2026-04-23 02:22

| Engine | Mode | Performance |
|--------|------|-------------|
| **A** | AWS Transcribe | ko-KR 고정 |
| **B** | AWS Transcribe | 다국어 감지 (ko-KR + en-US) |
| **C** | Whisper large-v3 | CPU int8, 70-80min/meeting |

Evaluator: Claude Sonnet 4.6

## Whisper Processing Times

| Meeting | Whisper Time | Whisper Chars |
|---------|-------------|---------------|
| 하나은행 연구개발망 | 4,261s (71min) | 34,282 |
| 하나금융기술연구소 | 3,737s (62min) | 36,457 |
| 하나금융기술연구소(Mobile) | (not completed) | - |

---

## 하나금융기술연구소

# STT 비교 분석: 하나금융기술연구소 미팅

## 📊 평가 점수표

| 평가 항목 | A: Transcribe 표준 | B: Transcribe 다국어 | 비고 |
|-----...(see previous run for meeting 1 evaluation)

---

## 하나금융기술연구소(Mobile)

# STT 비교 분석: 하나금융기술연구소 미팅

## 📊 평가 점수표

| 평가 항목 | A: Transcribe 표준 | B: Transcribe 다국어 | 비고 |
|-----------|:-----------------:|:-------------------:|------|
| **AWS 서비스명 인식** | 5 | 5 | 둘 다 동일하게 부정확 |
| **한국어 자연어 처리** | 7 | 7 | 거의 동일 |
| **기술 용어 정확도** | 5 | 5 | 둘 다 동일하게 오류 |
| **영어 혼용 처리** | 5 | 5 | 차이 없음 |
| **문장 연속성** | 7 | 7 | 동일 |
| **전체 가독성** | 6 | 6 | 동일 |
| **종합 점수** | **5.8** | **5.8** | **사실상 동일** |

---

## 🔍 상세 분석

### 1. 두 결과물의 핵심 발견

> **⚠️ 두 transcript는 단어 하나도 다르지 않습니다.**
> 완전히 동일한 텍스트입니다.

제공된 A와 B 텍스트를 전체 비교한 결과, **차이점이 전혀 없습니다.** 따라서 아래 분석은 **공통 품질 평가**로 진행합니다.

---

### 2. 기술 용어 오인식 목록 (공통)

#### 🔴 심각한 오류 (의미 왜곡)

| 원문 발화 (추정) | STT 결과 | 올바른 표기 | 심각도 |
|----------------|---------|-----------|--------|
| SageMaker Unified Studio | 세일즈 메이크 유니파이 스튜디오 | SageMaker Unified Studio | 🔴 높음 |
| from scratch | 프랑스 크래치나 | from scratch | 🔴 높음 |
| adoption | aution | adoption | 🔴 높음 |
| LoRA | 로라 | LoRA | 🟡 중간 |
| RAG aware routing | dra 어웨어 라우팅 | RAG-aware routing | 🔴 높음 |
| KV cache aware routing | kb 캐쉬 어웨어 라우팅 | KV cache-aware routing | 🟡 중간 |
| HyperPod | 하이퍼 파드 | HyperPod | 🟢 낮음 |
| EKS | 이케이에스 / 이케s | EKS | 🟡 중간 |
| SageMaker | 세이지 메이커 | SageMaker | 🟢 낮음 |
| Upstage | 업스테이치 | Upstage | 🟡 중간 |
| 12Labs | 투웰브 앱스 | 12Labs | 🟡 중간 |
| AI Studio | 에이아이 스튜디오 | AI Studio | 🟢 낮음 |
| on-premise | 온 프레임 | on-premise | 🔴 높음 |
| painful | 페인플 | painful | 🟢 낮음 |
| Kubernetes | 쿠버네티스 | Kubernetes | 🟢 낮음 |
| POC | poc | PoC | 🟢 낮음 |
| GPU | 지피유 | GPU | 🟢 낮음 |
| AWS | 에이더블유에스 | AWS | 🟢 낮음 |
| TI (클라우드) | 티아이 | TI | 🟢 낮음 |
| Advanced AI Lab | advanced ai 랩 | Advanced AI Lab | 🟢 낮음 |

#### 🟡 한국어 자연어 오류

| STT 결과 | 올바른 표기 | 유형 |
|---------|-----------|------|
| 집 짧은 미팅 | 짧은 미팅 | 삽입 오류 |
| 그래프레을 | 그래프를 / LoRA를 | 오인식 |
| 유지 케이스 | 유즈케이스(use case) | 오인식 |
| 에이아온 이케이에스 | Amazon EKS | 오인식 |
| 메인테스 | 메인테인 / 관리 | 오인식 |
| 클라우티아 | 클라우디아(?) | 오인식 |
| 아이부자 | IBUJA(?) / 특정 서비스명 | 불명확 |
| 연애 계획 | 연간 계획 | 오인식 |
| 멈추어해지는 | 안정화되는 | 오인식 |

---

### 3. 항목별 상세 분석

#### ✅ 잘 된 부분
```
- 전반적인 한국어 문장 흐름 유지
- 화자 발화 패턴(어, 근데, 그래서 등) 자연스럽게 반영
- 문맥상 이해 가능한 수준의 기본 한국어 처리
- 숫자/수량 표현 (스물 몇 대, 일 년에서 이 년) 처리 양호
```

#### ❌ 문제점
```
- 영어 기술 용어를 한국어 발음으로 변환 후 재오인식하는 이중 오류
- 고유명사(서비스명, 회사명) 처리 매우 취약
- 문맥 기반 교정 부재 (on-premise → 온 프레임)
- 영어 단어 직접 출력 거의 없음 (다국어 모드임에도)
```

---

## 💡 종합 추천

### 현재 상황 진단

```
A (표준) = B (다국어) : 품질 차이 없음
```

두 모드 모두 **이 도메인(AWS 기술 + 금융 한국어 혼용)에서 단독 사용 부적합**합니다.

### 개선 권고사항

#### 즉시 적용 가능
| 방법 | 효과 | 난이도 |
|------|------|--------|
| **Custom Vocabulary 등록** | AWS 서비스명, 기술 용어 정확도 대폭 향상 | 🟢 쉬움 |
| **Custom Language Model** | 도메인 특화 언어모델 학습 | 🔴 어려움 |
| **후처리 사전 적용** | 오인식 패턴 규칙 기반 교정 | 🟡 중간 |

#### Custom Vocabulary 우선 등록 추천 목록
```
SageMaker, EKS, HyperPod, LoRA, RAG, KV-cache,
GPU, on-premise, from-scratch, fine-tuning,
Kubernetes, Upstage, 12Labs, AI-Studio,
foundation-model, POC, adoption
```

### 최종 권고

> **현재 두 모드 간 유의미한 차이 없음 → Custom Vocabulary 적용이 최우선**
>
> 다국어 모드의 이점(영어 직접 출력)이 실제로 작동하지 않고 있으므로,
> **표준 모드 + Custom Vocabulary** 조합을 먼저 시도하고,
> 효과 미흡 시 **Custom Language Model** 구축을 권장합니다.

---
