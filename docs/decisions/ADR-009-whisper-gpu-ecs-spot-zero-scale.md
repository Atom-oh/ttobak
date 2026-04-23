# ADR-009: Adopt Whisper GPU on ECS Spot with Zero-Scale for STT

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
Ttobak uses speech-to-text (STT) to convert meeting recordings into transcripts. The current engine is AWS Transcribe, which scored 3.5/10 in a 3-way benchmark on Korean-English mixed technical meetings (AWS SA meetings with Korean banks). Key problems:

- AWS service names misrecognized: `SageMaker` becomes `세일즈 메이커`, `EKS` becomes `ks`/`ets`, `GPU` becomes `pu`/`gpo`
- English abbreviations rendered as broken Korean phonetic transcriptions
- Custom Vocabulary (ADR-008) improves known terms but cannot fix the fundamental code-switching weakness
- Cost: $1.44 per 1-hour meeting

A 3-way benchmark (2026-04-23) compared AWS Transcribe, Whisper large-v3 on CPU (int8), and Whisper large-v3 on GPU (A10G, float16):

| Engine | Quality | Time/Meeting | Cost/Meeting |
|--------|---------|-------------|-------------|
| AWS Transcribe | 3.5/10 | ~3 min | $1.44 |
| Whisper CPU (int8) | 7.5/10 | ~70 min | N/A (too slow) |
| Whisper GPU (A10G fp16) | 7.5/10 | ~7 min | $0.04 (Spot) |

## Options Considered

### Option 1: AWS Transcribe with Custom Vocabulary
- **Pros**: Fully managed, fast (~3 min), no infrastructure to maintain, streaming support
- **Cons**: 3.5/10 quality on Korean-English mixed content, $1.44/meeting, fundamental limitation in code-switching between Korean and English

### Option 2: Whisper GPU on ECS with Spot Instances (Zero-Scale)
- **Pros**: 7.5/10 quality (2x better), $0.04/meeting (36x cheaper on Spot), accurate English abbreviation output (NLP, LLM, GPU, EKS in uppercase), natural Korean sentence flow, zero cost at idle (ASG min=0)
- **Cons**: Cold start ~3-5 min when scaling from zero, requires GPU infrastructure (ECS + EC2 + ECR), Docker image ~8GB with model baked in, Spot instance interruption risk

### Option 3: Whisper on AWS Lambda with CPU
- **Pros**: Serverless, no infrastructure
- **Cons**: 70+ min per meeting (unacceptable), Lambda 15-min timeout insufficient for long meetings, high memory cost

### Option 4: Amazon Bedrock Nova Sonic
- **Pros**: Managed service, real-time streaming
- **Cons**: Benchmark showed identical output to standard Transcribe for this domain, no quality improvement

## Decision
Adopt **Option 2: Whisper GPU on ECS with Spot Instances (Zero-Scale)** as the primary post-upload STT engine.

Architecture:

```text
Audio Upload -> S3 -> EventBridge -> transcribe Lambda -> ECS RunTask
                                                           |
                                                    g5.xlarge Spot
                                                    Whisper large-v3
                                                    GPU float16
                                                           |
                                                    transcript -> S3
                                                           |
                                              EventBridge -> summarize Lambda
```

Key design decisions:
- **ECS on EC2** (not Fargate): Fargate does not support GPU instances
- **g5.xlarge Spot**: A10G GPU with 24GB VRAM, ~$0.36/hr Spot vs $1.006/hr On-Demand
- **ASG min=0**: Zero cost when idle, Capacity Provider auto-scales 0 to 1 on task placement
- **Model baked in ECR image**: Eliminates ~3min model download on every cold start
- **Lambda trigger**: Existing transcribe Lambda calls `ecs:RunTask` instead of starting a Transcribe job when `sttProvider=whisper`
- **No ALB**: Batch processing only, no HTTP API needed
- **Hybrid strategy**: Live STT uses browser-side Transcribe Streaming for real-time subtitles; Whisper GPU handles final high-quality post-upload transcription

## Consequences

### Positive
- STT quality improves from 3.5/10 to 7.5/10 for Korean-English mixed technical meetings
- Cost per meeting drops from $1.44 to $0.04 (Spot), a 36x reduction
- English technical terms (GPU, EKS, NLP, LLM, DCGM) are correctly recognized and output in uppercase
- Zero infrastructure cost when no meetings are being processed (ASG min=0)
- Existing EventBridge pipeline (transcript upload triggers summarize) works unchanged

### Negative
- Cold start latency of 3-5 minutes when ASG scales from 0 to 1 (acceptable for async post-upload processing)
- New infrastructure to maintain: ECS cluster, ASG, ECR repository, GPU AMI updates
- Spot instance interruption can fail a transcription task (mitigated by retry logic and the fact that meetings are retryable)
- ECR image is ~8GB due to CUDA runtime + Whisper model, requiring larger storage and longer build times
- VPC with NAT Gateway required for ECS private subnet (adds ~$30/month fixed cost if not shared with existing VPC)

## References
- [STT Benchmark Final Results](../stt-benchmark-final.md) -- Full 3-way benchmark with quality scores and error analysis
- [ADR-008: Custom Dictionary for STT Accuracy](ADR-008-custom-dictionary-for-stt-accuracy.md) -- Custom Vocabulary for AWS Transcribe (still applies for live STT)
- [faster-whisper](https://github.com/SYSTRAN/faster-whisper) -- CTranslate2-based Whisper inference engine
- ECS Capacity Provider managed scaling: AWS documentation on zero-scale patterns

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
또박(Ttobak)은 회의 녹음을 텍스트로 변환하기 위해 음성인식(STT)을 사용합니다. 현재 엔진은 AWS Transcribe이며, 한영 혼용 기술 회의(AWS SA의 한국 은행 미팅)에서 3-Way 벤치마크 결과 3.5/10점을 받았습니다. 주요 문제점:

- AWS 서비스명 오인식: `SageMaker`가 `세일즈 메이커`로, `EKS`가 `ks`/`ets`로, `GPU`가 `pu`/`gpo`로 변환
- 영어 약어가 깨진 한국어 음차로 출력
- Custom Vocabulary(ADR-008)는 알려진 용어를 개선하지만 근본적인 코드스위칭 취약점은 해결 불가
- 비용: 1시간 회의당 $1.44

2026-04-23 3-Way 벤치마크에서 AWS Transcribe, Whisper large-v3 CPU(int8), Whisper large-v3 GPU(A10G, float16)를 비교했습니다:

| 엔진 | 품질 | 시간/회의 | 비용/회의 |
|------|------|----------|----------|
| AWS Transcribe | 3.5/10 | ~3분 | $1.44 |
| Whisper CPU (int8) | 7.5/10 | ~70분 | 해당 없음 (너무 느림) |
| Whisper GPU (A10G fp16) | 7.5/10 | ~7분 | $0.04 (Spot) |

## 검토한 옵션

### 옵션 1: AWS Transcribe + Custom Vocabulary
- **장점**: 완전 관리형, 빠른 처리 (~3분), 인프라 관리 불필요, 스트리밍 지원
- **단점**: 한영 혼용 콘텐츠에서 3.5/10 품질, 회의당 $1.44, 한국어-영어 코드스위칭의 근본적 한계

### 옵션 2: ECS Spot 인스턴스에서 Whisper GPU (Zero-Scale)
- **장점**: 7.5/10 품질 (2배 향상), 회의당 $0.04 (36배 저렴, Spot 기준), 영어 약어 정확한 출력 (NLP, LLM, GPU, EKS 대문자), 자연스러운 한국어 문장 흐름, 유휴 시 비용 $0 (ASG min=0)
- **단점**: 제로에서 스케일업 시 콜드스타트 ~3-5분, GPU 인프라 관리 필요 (ECS + EC2 + ECR), 모델 포함 Docker 이미지 ~8GB, Spot 인스턴스 중단 위험

### 옵션 3: AWS Lambda에서 Whisper CPU
- **장점**: 서버리스, 인프라 불필요
- **단점**: 회의당 70분 이상 (허용 불가), Lambda 15분 제한 초과, 높은 메모리 비용

### 옵션 4: Amazon Bedrock Nova Sonic
- **장점**: 관리형 서비스, 실시간 스트리밍
- **단점**: 벤치마크 결과 표준 Transcribe와 동일한 출력, 품질 개선 없음

## 결정
**옵션 2: ECS Spot 인스턴스에서 Whisper GPU (Zero-Scale)**를 업로드 후 STT의 주 엔진으로 채택합니다.

아키텍처:

```text
오디오 업로드 -> S3 -> EventBridge -> transcribe Lambda -> ECS RunTask
                                                            |
                                                     g5.xlarge Spot
                                                     Whisper large-v3
                                                     GPU float16
                                                            |
                                                     transcript -> S3
                                                            |
                                               EventBridge -> summarize Lambda
```

주요 설계 결정:
- **ECS on EC2** (Fargate 아님): Fargate는 GPU 인스턴스를 지원하지 않습니다
- **g5.xlarge Spot**: A10G GPU 24GB VRAM, Spot ~$0.36/hr vs On-Demand $1.006/hr
- **ASG min=0**: 유휴 시 비용 $0, Capacity Provider가 task 배치 시 자동으로 0에서 1로 스케일업
- **ECR 이미지에 모델 bake**: 매 콜드스타트마다 ~3분 모델 다운로드 제거
- **Lambda 트리거**: 기존 transcribe Lambda가 `sttProvider=whisper`일 때 `ecs:RunTask` 호출
- **ALB 불필요**: 배치 처리 전용, HTTP API 불필요
- **하이브리드 전략**: 실시간 자막은 브라우저 측 Transcribe Streaming 사용, Whisper GPU는 업로드 후 고품질 최종 회의록 처리 담당

## 영향

### 긍정적
- 한영 혼용 기술 회의 STT 품질이 3.5/10에서 7.5/10으로 향상됩니다
- 회의당 비용이 $1.44에서 $0.04(Spot)로 36배 절감됩니다
- 영어 기술 용어(GPU, EKS, NLP, LLM, DCGM)가 정확하게 인식되어 대문자로 출력됩니다
- 회의 처리가 없을 때 인프라 비용이 $0입니다 (ASG min=0)
- 기존 EventBridge 파이프라인(transcript 업로드가 summarize 트리거)이 변경 없이 동작합니다

### 부정적
- ASG가 0에서 1로 스케일업할 때 3-5분의 콜드스타트 지연이 발생합니다 (비동기 업로드 후 처리에서 허용 가능)
- 새로운 인프라 관리가 필요합니다: ECS 클러스터, ASG, ECR 리포지토리, GPU AMI 업데이트
- Spot 인스턴스 중단으로 transcription task가 실패할 수 있습니다 (재시도 로직과 회의 재처리 가능성으로 완화)
- CUDA 런타임 + Whisper 모델로 인해 ECR 이미지가 ~8GB로 큽니다
- ECS private subnet에 NAT Gateway가 필요합니다 (기존 VPC와 공유하지 않을 경우 월 ~$30 고정 비용 추가)

## 참고 자료
- [STT 벤치마크 최종 결과](../stt-benchmark-final.md) -- 품질 점수와 오류 분석이 포함된 전체 3-Way 벤치마크
- [ADR-008: STT 정확도를 위한 Custom Dictionary](ADR-008-custom-dictionary-for-stt-accuracy.md) -- AWS Transcribe용 Custom Vocabulary (라이브 STT에 계속 적용)
- [faster-whisper](https://github.com/SYSTRAN/faster-whisper) -- CTranslate2 기반 Whisper 추론 엔진
- ECS Capacity Provider 관리형 스케일링: 제로 스케일 패턴에 대한 AWS 문서
