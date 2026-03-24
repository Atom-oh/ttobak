# 음성비서 기술 리서치 보고서

**프로젝트**: Ttobak (또박) — 한국어 AI 미팅 어시스턴트
**작성일**: 2026-03-24
**분석 범위**: STT 기술 트렌드, 경쟁 제품, AWS 음성 파이프라인

---

## 1. Executive Summary

한국어 미팅 어시스턴트를 위한 음성 기술 환경을 분석한 결과:

1. **한국어 실시간 STT 선택지가 제한적** — AWS Transcribe는 한국어 스트리밍 미지원(배치만), Nova Sonic은 한국어 자체 미지원
2. **Deepgram Nova-3가 최적의 실시간 STT** — 한국어 스트리밍 지원, AWS Transcribe 대비 10배 저렴
3. **WebSocket이 오디오 스트리밍 표준** — WebRTC보다 단순하며 모든 주요 STT 서비스가 채택
4. **클라이언트 VAD로 30-50% 비용 절감 가능** — Silero VAD 브라우저 적용
5. **한국 시장에 "우수 STT + 우수 AI" 조합 부재** — Ttobak의 핵심 기회

---

## 2. 실시간 오디오 스트리밍 프로토콜 비교

| 항목 | WebRTC | WebSocket | SSE |
|------|--------|-----------|-----|
| **방향** | 양방향 (P2P) | 양방향 | 서버→클라이언트 |
| **지연시간** | ~50-150ms | ~100-300ms | ~100-300ms |
| **오디오** | 네이티브 (Opus, PCM) | 바이너리 프레임 | 텍스트만 (base64) |
| **에코 제거** | 내장 | 수동 구현 | N/A |
| **구현 복잡도** | 높음 (STUN/TURN/ICE) | 중간 | 낮음 |
| **적합 용도** | P2P 통화, 화상회의 | 클라우드 STT 스트리밍 | 결과 수신 |

**결론**: WebSocket이 클라우드 STT 스트리밍에 최적. Ttobak의 현재 WebSocket 접근은 올바름.

---

## 3. 클라우드 STT 서비스 비교

### 3.1 서비스별 상세 비교

| 서비스 | 한국어 지원 | 실시간 스트리밍 | 비용/시간 | 화자 분리 | 지연시간 |
|--------|-----------|--------------|----------|----------|---------|
| **AWS Transcribe** | 배치만 | 한국어 불가 | $4.50 | 배치만 | ~2-5s |
| **Amazon Nova Sonic** | 비공식 동작* | 지원 | N/A | 토큰 기반 | ~200ms |
| **Deepgram Nova-3** | 스트리밍+배치 | 지원 | $0.46 | 지원 | ~300ms |
| **AssemblyAI Universal-2** | 스트리밍+배치 | 지원 | $0.15-0.45 | +$0.02-0.12 | ~500ms |
| **OpenAI Whisper API** | 배치만 | 불가 | $0.36 | 미지원 | ~3-10s |
| **Whisper (ECS 자체호스팅)** | 배치 | 청크 기반* | GPU 비용 | 미지원 | ~2-5s |
| **RTZR/Vito API** | 최우수 | 지원 | 경쟁사 1/3 | 10명 | <300ms |

*Whisper는 청킹으로 준실시간 가능하나 네이티브 스트리밍 미설계

### 3.2 비용 비교 (시간당)

```
AWS Transcribe   ████████████████████████████████████████████████  $4.50
Whisper 자체호스팅 ████████████████                                  $0.50-1.50
Deepgram Nova-3  █████                                             $0.46
OpenAI Whisper   ████                                              $0.36
AssemblyAI       ██                                                $0.15-0.45
```

### 3.3 핵심 발견: Nova Sonic 한국어 비공식 지원

AWS 공식 문서에는 Nova Sonic의 한국어가 미기재(영어, 스페인어, 독일어 등만 명시)되어 있으나, **실제로는 한국어가 동작하는 것이 확인됨**. 비공식 지원 상태이므로 품질/안정성 변동 가능성은 있으나, Ttobak의 A/B 테스트 옵션으로 유지하는 것이 타당함.

### 3.4 추천

| 우선순위 | 조치 | 이유 |
|---------|------|-----|
| **P0** | Deepgram Nova-3 도입 검토 | 한국어 실시간 스트리밍, 10배 저렴 |
| **P1** | AWS Transcribe 배치 유지 | 후처리 백업, 화자 분리 |
| **P2** | RTZR API 평가 | 한국어 최고 품질 (WER 4.66%) |
| **유지** | Nova Sonic A/B 테스트 | 비공식이나 한국어 동작 확인됨 |

---

## 4. 화자 분리 (Speaker Diarization)

### 4.1 기술 비교

| 기술 | 최대 화자 | 실시간 | 한국어 | 정확도 (DER) | 배포 |
|------|---------|--------|--------|-------------|------|
| AWS Transcribe | 30명 | 배치만 | 배치만 | ~15-25% | 관리형 |
| Deepgram | 10+명 | 지원 | 지원 | ~15-20% | 관리형 |
| pyannote.audio | 무제한 | 준실시간 | 지원* | **7-15%** | 자체호스팅 |
| AssemblyAI | 10+명 | 추가 비용 | 지원 | ~15-20% | 관리형 |

*pyannote는 언어 무관 (화자 임베딩 기반)

### 4.2 추천: 하이브리드 접근

1. **실시간**: Deepgram 내장 화자 분리로 라이브 표시
2. **후처리**: pyannote.audio로 최종 회의록 정확도 향상 (DER 2-3배 개선)
3. **장기**: 화자 임베딩 저장으로 미팅 간 화자 식별

---

## 5. 음성 활동 감지 (VAD)

### 5.1 기술 비교

| 기술 | 모델 크기 | 지연시간 | 브라우저 | 정확도 |
|------|---------|---------|---------|--------|
| **Silero VAD** | 2MB | <1ms | ONNX Web | 우수 |
| WebRTC VAD | 내장 | <1ms | 네이티브 | 양호 |
| RNNoise | 100KB | <1ms | WASM | 양호 |
| Whisper VAD | N/A | ~100ms | 서버만 | 양호 |

### 5.2 클라이언트 VAD 도입 효과

- **대역폭**: 60-80% 감소 (침묵 필터링)
- **STT 비용**: 30-50% 절감
- **전사 품질**: 노이즈/침묵 제거로 Whisper 환각 감소

### 5.3 추천

Silero VAD를 ONNX Web으로 브라우저에 적용. MIT 라이선스, 6000+ 언어 학습, <1ms 지연.

```
[마이크] → [Silero VAD] → 음성만 → [WebSocket] → [STT 서비스]
                ↓
            침묵 필터링 (30-50% 절감)
```

---

## 6. 경쟁 제품 분석

### 6.1 시장 구조

```
                    AI 기능 정교함
                         ↑
                         |
         Otter.ai ●      |      ● Ttobak (목표)
        Fireflies ●      |
            Grain ●      |
            tl;dv ●      |     ● Daglo
                         |
    ─────────────────────┼──────────────────→ 한국어 STT 품질
                         |
                         |     ● RTZR/Vito
                         |
```

### 6.2 기능 매트릭스

| 기능 | Otter | Fireflies | Grain | tl;dv | RTZR | Daglo | **Ttobak** |
|------|-------|-----------|-------|-------|------|-------|------------|
| 실시간 전사 | O | O | X | X | O | O | **O (듀얼)** |
| AI 요약 | O | O | O | O | △ | O | **O (Claude)** |
| 액션 아이템 | O | O | O | O | X | △ | 계획 |
| AI Q&A | O | O | O | O | X | O | **O** |
| 화자 분리 | O | O | O | O | O | O | O |
| 한국어 품질 | 낮음 | 낮음 | 낮음 | 보통 | **최우수** | **우수** | 양호 |
| 미팅 봇 | O | O | O | O | X | X | X |
| 모바일 앱 | O | O | X | X | O | O | 계획 |
| Notion 연동 | O | X | X | O | X | X | 계획 |

### 6.3 아키텍처 접근 비교

| 방식 | 제품 | 장점 | 단점 |
|------|------|------|------|
| **미팅 봇** | Otter, Fireflies, Grain, tl;dv | 자동 녹음, 플랫폼 연동 | "봇 참가" 알림, 프라이버시 |
| **브라우저 확장** | Otter, Fireflies, tl;dv | 비침습적 | 브라우저 전용 |
| **네이티브 녹음** | RTZR, Daglo, **Ttobak** | 오프라인, 프라이버시 | 수동 시작 필요 |

### 6.4 한국 시장 분석

| 플레이어 | 강점 | 약점 | 기회 |
|---------|------|------|------|
| **RTZR/Vito** | 최고 한국어 STT (WER 4.66%) | AI 기능 부족 | STT API 제공자 |
| **Daglo** | 멀티모델 AI, 좋은 UX | 미팅 특화 X | 학생/전문가 |
| **Clova Note** | 네이버 생태계 | 2025.7 종료 → LINE WORKS 이관 | - |
| **Ttobak** | Claude AI + 듀얼 STT | 연동 부족, 모바일 미비 | **STT+AI 조합 시장 공백** |

### 6.5 가격 벤치마크

| 플랜 | 업계 평균 | Ttobak 추천 |
|------|---------|------------|
| 무료 | 300분-무제한/월 | 5회/월 또는 300분 |
| Pro | $10-19/사용자/월 | ₩15,000/월 |
| Business | $19-30/사용자/월 | ₩25,000/사용자/월 |
| Enterprise | 커스텀 | 커스텀 |

---

## 7. AWS 음성 파이프라인 옵션

### 7.1 아키텍처 패턴 비교

| 패턴 | 장점 | 단점 | Ttobak 적합성 |
|------|------|------|-------------|
| **EventBridge + Lambda** (현재) | 단순, 이벤트 기반, 종량제 | 콜드스타트, 15분 제한 | 현재 적합 |
| **Step Functions** | 시각적 워크플로우, 재시도 | 추가 비용, 과도 | 미팅 수 증가 시 |
| **WebSocket + Lambda** | 실시간, 양방향 | 연결 관리 복잡 | 실시간 전사에 적합 |

### 7.2 추천 하이브리드 아키텍처

```
[브라우저]
    ↓
[Silero VAD] → 침묵 필터링
    ↓
[WebSocket] → 16kHz PCM 오디오 청크
    ↓
[API Gateway WebSocket → Lambda]
    ↓
    ├──→ [Deepgram Nova-3 Streaming] → 실시간 전사 → 브라우저 푸시
    │
    └──→ [S3 audio/] → 비동기 저장
              ↓
         [EventBridge]
              ↓
         [Lambda] → 후처리
              ├──→ [AWS Transcribe] → 배치 백업 전사
              ├──→ [pyannote.audio] → 화자 분리 보정
              └──→ [S3 transcripts/]
                        ↓
                   [Lambda summarize]
                        ↓
                   [Bedrock Claude] → 최종 요약
                        ↓
                   [DynamoDB]
```

### 7.3 비용 추정 (미팅 시간당)

| 컴포넌트 | 현재 | 최적화 후 |
|---------|------|---------|
| STT (실시간) | $0.50-1.50 (ECS GPU) | $0.46 (Deepgram) |
| STT (배치) | $4.50 (Transcribe) | $0 (생략 가능) |
| AI 요약 | $0.27 (Opus) | $0.05 (Sonnet) |
| 라이브 요약 | $0.10 (Opus) | $0.01 (Haiku) |
| Lambda/API GW | $0.02 | $0.02 |
| S3/DynamoDB | $0.01 | $0.01 |
| **합계** | **~$5.40** | **~$0.55** |

**90% 비용 절감 가능**

---

## 8. 종합 추천 로드맵

### 단기 (1주)
- [ ] Bedrock 모델 교체: 요약 Opus→Sonnet, 라이브 요약→Haiku
- [ ] Nova Sonic STT 옵션 UI에서 제거 또는 "한국어 미지원" 표시
- [ ] DynamoDB `GetMeetingByID` 테이블 스캔 → GSI 추가

### 중기 (1개월)
- [ ] Deepgram Nova-3 평가 및 통합 (WebSocket 스트리밍)
- [ ] Silero VAD 브라우저 클라이언트 적용
- [ ] 이중 전사 제거 (ECS Whisper 사용 시 Transcribe 스킵)
- [ ] ECS Spot 인터럽션 핸들링 (2분 경고 시 오디오 플러시)

### 장기 (3개월)
- [ ] pyannote.audio 후처리 화자 분리 추가
- [ ] 액션 아이템 자동 추출
- [ ] Notion/Slack 연동
- [ ] 모바일 앱 (React Native 또는 Flutter)
- [ ] Recall.ai 연동으로 미팅 봇 옵션 추가

---

## 참고 자료

1. AWS Transcribe 문서: https://docs.aws.amazon.com/transcribe/latest/dg/
2. Amazon Nova Sonic: https://docs.aws.amazon.com/nova/latest/userguide/speech.html
3. Deepgram 언어 지원: https://developers.deepgram.com/docs/models-languages-overview
4. Silero VAD: https://github.com/snakers4/silero-vad
5. pyannote.audio: https://github.com/pyannote/pyannote-audio
6. RTZR: https://rtzr.ai
7. Recall.ai: https://recall.ai

---

*Ttobak 음성비서 기술 리서치 — 2026-03-24*
