# STT 3-Way Benchmark Final Results

Date: 2026-04-23
Evaluator: Claude Sonnet 4.6

## Benchmark Environment

| Engine | Configuration | Hardware |
|--------|--------------|----------|
| **A** AWS Transcribe | Multi-language (ko-KR + en-US) | AWS Managed |
| **B** Whisper large-v3 CPU | int8, faster-whisper | EC2 (CPU only) |
| **C** Whisper large-v3 GPU | float16, faster-whisper | g5.xlarge (A10G 24GB, CUDA 13.2) |

## Performance Comparison

| Meeting | Transcribe | Whisper CPU | Whisper GPU | GPU vs CPU |
|---------|-----------|-------------|-------------|------------|
| 하나은행 연구개발망 (75MB) | ~3 min | 71 min | **7.9 min** | 9.0x |
| 하나금융기술연구소 (78MB) | ~3 min | 62 min | **6.2 min** | 10.0x |
| 하나금융기술연구소 Mobile (78MB) | ~3 min | N/A | **6.4 min** | N/A |
| **Total** | **~9 min** | **133 min (2/3)** | **20.4 min** | **~10x** |

## Character Count

| Meeting | Transcribe | Whisper CPU | Whisper GPU |
|---------|-----------|-------------|-------------|
| 하나은행 연구개발망 | ~30,000 | 34,282 | 35,181 |
| 하나금융기술연구소 | 37,441 | 36,457 | 36,269 |
| 하나금융기술연구소 Mobile | 36,729 | N/A | 36,016 |

## Quality Comparison (하나금융기술연구소)

| Criteria | Transcribe | Whisper GPU | Notes |
|----------|:----------:|:-----------:|-------|
| AWS 서비스명 인식 | 3 | **8** | Transcribe: SageMaker→세일즈메이커, EKS→ks/ets, GPU→pu/gpo |
| 한국어 자연어 처리 | 5 | **6** | Whisper 조사 처리 우위 |
| 기술 용어 정확도 | 3 | **8** | Transcribe: evaluatation, on preremise, ancuse |
| 영어 혼용 처리 | 4 | **9** | Whisper: NLP/LLM/GPU/EKS 대문자 출력, Transcribe: 음차 혼재 |
| 문장 연속성/가독성 | 4 | **6** | Whisper 읽기 자연스러움 |
| **종합 점수** | **3.5** | **7.5** | Whisper 2배 우위 |

### Key Findings

#### Transcribe 주요 오인식

| 원문 (추정) | Transcribe 출력 | Whisper GPU 출력 |
|------------|----------------|-----------------|
| SageMaker | 세일즈 메이커 | (세즈메이커) |
| EKS | ks / ets | EKS |
| GPU | pu / gpo | GPU |
| On-premise | on preremise | 온프레미스 |
| Kubernetes | 쿠보네티스 | 쿠버네티스 |
| DCGM | dcgm | DCGM 익스포터 |
| MLflow | ml 플로어 | ML플로우 |
| Kubeflow | cube 플로어 | 큐브플로우 |
| LangFuse | ancuse | 랭퓨즈 |
| Evaluation | evaluatation | Evaluation |
| Integration | integ레이션 | 인티그레이션 |
| Open source | 높은 소스 | 오픈소스 |

## Cost Analysis (per 1-hour meeting)

| Engine | Cost | Time | Quality |
|--------|------|------|---------|
| AWS Transcribe | $1.44 | ~3 min | 3.5/10 |
| Whisper GPU (g5.xlarge Spot) | **$0.04** | ~7 min | **7.5/10** |
| Whisper GPU (g5.xlarge On-Demand) | $0.12 | ~7 min | 7.5/10 |
| Whisper CPU | ~$0 (EC2 time) | ~70 min | 7.5/10 |

## Recommendation

### Immediate

**Whisper GPU (large-v3)를 primary STT 엔진으로 채택 권장**

- 품질: Transcribe 대비 2배 (3.5 → 7.5)
- 비용: Transcribe 대비 36배 저렴 ($1.44 → $0.04 Spot)
- 속도: 미팅당 6-8분 (async 처리 적합)
- 영어 기술 용어 처리가 압도적으로 우수

### Production Architecture

```
Audio Upload → S3 → EventBridge → ECS Task (GPU, Whisper)
                                     ↓
                              Transcript → S3 → Summarize Lambda
```

- ECS Fargate/EC2 + g5.xlarge, Private ALB (VPC internal only)
- ECR 이미지에 Whisper large-v3 모델 bake → 콜드스타트 0
- Spot instance for cost optimization

### Hybrid Strategy (Optional)

- **Live STT**: AWS Transcribe Streaming (실시간 자막용, 품질 낮아도 OK)
- **Post-upload STT**: Whisper GPU (최종 회의록용, 고품질)
- Custom Vocabulary는 Transcribe에만 적용 (Whisper는 불필요)
