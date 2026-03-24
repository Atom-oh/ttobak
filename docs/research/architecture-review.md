# Ttobak 아키텍처 리뷰 보고서

**프로젝트**: Ttobak (또박) — 한국어 AI 미팅 어시스턴트
**작성일**: 2026-03-24
**분석 범위**: STT 파이프라인, 실시간 처리, 요약, 데이터 아키텍처, 비용, 보안

---

## 1. 현재 아키텍처 개요

```
┌─────────────────────────────────────────────────────────────┐
│                    Frontend (Next.js 16 SPA)                 │
│  ┌───────────┐  ┌──────────────┐  ┌────────┐  ┌──────────┐ │
│  │RecordButton│  │STTOrchestrator│  │LiveQ&A │  │LiveSummary│ │
│  │(MediaRec.) │  │ Fallback→ECS │  │(Haiku) │  │(Bedrock) │ │
│  └─────┬─────┘  └──────┬───────┘  └────┬───┘  └────┬─────┘ │
└────────┼───────────────┼───────────────┼───────────┼────────┘
         │               │               │           │
         ▼               ▼               ▼           ▼
┌─────────────────────────────────────────────────────────────┐
│              CloudFront (d115v97ubjhb06)                     │
│  ├─ Lambda@Edge (JWT auth on /api/*)                         │
│  ├─ S3 OAC (static frontend)                                │
│  ├─ API Gateway HTTP API → Lambda (REST)                     │
│  └─ ALB → ECS Fargate Spot (WebSocket STT)                   │
└─────────────────────────────────────────────────────────────┘
         │                               │
         ▼                               ▼
┌────────────────────┐    ┌──────────────────────────────────┐
│  Lambda Functions   │    │  ECS Cluster (ttobak-realtime)   │
│  ├─ api (chi)       │    │  GPU Spot (g4dn/g5/g6.xlarge)   │
│  ├─ transcribe      │    │  faster-whisper large-v3         │
│  ├─ summarize       │    │  + Translate + Haiku Q-detect    │
│  ├─ process-image   │    │  desiredCount: 0→1 on-demand    │
│  ├─ websocket       │    └──────────────────────────────────┘
│  └─ kb              │
└────────┬───────────┘
         │
         ▼
┌────────────────────┐    ┌──────────────────────────────────┐
│ DynamoDB            │    │ S3 (ttobak-assets)               │
│ (ttobak-main)       │    │ ├─ audio/{userId}/{meetingId}/   │
│ Single-table design │    │ ├─ transcripts/{meetingId}.json  │
│ GSI1: date sort     │    │ └─ images/{userId}/{meetingId}/  │
│ GSI2: email lookup  │    │ EventBridge enabled              │
└────────────────────┘    └──────────────────────────────────┘
```

### 이벤트 기반 파이프라인

```
audio/ 업로드 → EventBridge → transcribe Lambda → AWS Transcribe → transcripts/ S3
transcripts/ 업로드 → EventBridge → summarize Lambda → Bedrock Claude → DynamoDB
images/ 업로드 → EventBridge → process-image Lambda → Bedrock Vision → DynamoDB
```

---

## 2. 컴포넌트별 분석

### 2.1 STT 파이프라인

#### 강점
| 항목 | 상세 | 파일 |
|------|------|------|
| Graceful degradation | Web Speech API 즉시 시작, ECS 백그라운드 폴링 | `sttOrchestrator.ts:61-84` |
| Spot 회수 대응 | WebSocket 끊김 시 Web Speech 자동 폴백 | `sttOrchestrator.ts:196-204` |
| 오버랩 버퍼 | 1.5초 윈도우로 재시작 시 중복 텍스트 방지 | `speechRecognition.ts:84-87` |
| 인터림 프로모션 | 소스 전환 전 임시 텍스트 확정 처리 | `speechRecognition.ts:129-144` |
| 백엔드 오디오 집계 | ECS에서 고품질 16kHz WAV S3 저장 | `server.py:235-278` |

#### 약점
| 이슈 | 심각도 | 상세 | 파일 |
|------|--------|------|------|
| **이중 전사 낭비** | HIGH | ECS Whisper + AWS Transcribe 동시 실행 | `transcribe/main.go:79-82` |
| **ECS 콜드스타트 2-5분** | HIGH | GPU 인스턴스 + ECS 태스크 프로비저닝 | `realtime.go:46` |
| **Whisper 인터림 결과 없음** | MEDIUM | 모든 전사가 `isFinal: true`로 전송 | `server.py:147-153` |
| **Nova Sonic 한국어 비공식** | LOW | 공식 문서 미기재이나 실제 동작 확인됨 | `RecordingConfig.tsx:97-107` |

#### Critical: 이중 오디오 업로드
```
경로 1: ECS → realtime_{sessionId}.wav → S3 (Transcribe 스킵됨 ✓)
경로 2: MediaRecorder → recording_{timestamp}.webm → S3 → EventBridge → Transcribe (불필요 실행!)
```
`record/page.tsx:378-403`에서 `backendAudioKey`를 확인하나, 타이밍에 따라 두 경로 모두 실행될 수 있음.

---

### 2.2 실시간 처리

#### 강점
| 항목 | 상세 |
|------|------|
| Scale-to-zero | `desiredCount: 0`, 미사용 시 비용 $0 |
| Spot 다양화 | g4dn/g5/g6 인스턴스 타입, capacity-optimized |
| GPU 모델 캐싱 | Docker 이미지에 large-v3 사전 다운로드 |
| 비동기 번역 | 번역을 백그라운드 태스크로 실행 (STT 미차단) |

#### 약점
| 이슈 | 심각도 | 상세 |
|------|--------|------|
| **Spot 인터럽션 시 오디오 손실** | HIGH | 2분 경고 핸들링 없음, 버퍼 데이터 유실 가능 |
| **단일 WebSocket/유저** | MEDIUM | 수평 확장 불가 |
| **레이트 리미팅 없음** | MEDIUM | 오디오 플러딩 DoS 가능 |

---

### 2.3 요약 파이프라인

#### 강점
| 항목 | 상세 |
|------|------|
| N단어 간격 라이브 요약 | 사용자 설정 가능 (100/200/500/1000w) |
| 이전 요약 컨텍스트 전달 | 점진적 개선, 중복 감소 |
| 화자 분리 요약 | AWS Transcribe 화자 세그먼트 활용 |
| 구조화된 출력 | 한국어 회의록 포맷 강제 |

#### 약점 — 모델 비용 과다

| 태스크 | 현재 모델 | 추천 모델 | 비용 절감 |
|--------|---------|---------|----------|
| 최종 요약 | Claude Opus 4.6 | **Claude Sonnet** | ~5배 |
| 라이브 요약 | Claude Opus 4.6 (API) | **Claude Haiku** | ~20배 |
| 이미지 분석 | Claude Opus 4.6 | **Claude Sonnet** | ~5배 |
| 질문 감지 | Claude Haiku 4.5 | 적절 | - |
| Q&A (에이전트) | Claude Opus 4.6 | 적절 (도구 사용) | - |

`bedrock.go:19`에서 모든 요약에 `claude-opus-4-6-v1` 사용 중 — **가장 빠른 비용 절감 포인트**.

---

### 2.4 데이터 아키텍처

#### 강점
| 항목 | 상세 |
|------|------|
| 싱글테이블 설계 | 깔끔한 키 접두사, AWS 모범 사례 |
| GSI 활용 | GSI1 (날짜 정렬), GSI2 (이메일 검색) |
| PAY_PER_REQUEST | 변동 부하에 최적 |

#### Critical: GetMeetingByID Full Table Scan

```go
// dynamodb.go:98-140 — 매 호출마다 전체 테이블 스캔!
func (r *DynamoDBRepository) GetMeetingByID(ctx context.Context, meetingID string) (*model.Meeting, error) {
    filterEx := expression.Name("meetingId").Equal(expression.Value(meetingID)).
        And(expression.Name("entityType").Equal(expression.Value("MEETING")))
    // ... SCAN with filter (not Query!)
}
```

**호출 지점**: transcribe Lambda, summarize Lambda, bedrock service — **모든 미팅 처리마다 실행**

**영향**: 10,000건 미팅 시 매 Lambda 호출이 전체 아이템을 읽음. DynamoDB는 스캔된 전체 용량에 과금.

**해결**: GSI3 추가 `GSI3PK=MEETING#{meetingId}` → O(1) 직접 조회

#### 약점 목록

| 이슈 | 심각도 | 상세 |
|------|--------|------|
| **GetMeetingByID 테이블 스캔** | CRITICAL | 미팅 수 증가 시 비용/지연 폭증 |
| **DynamoDB 400KB 아이템 제한** | HIGH | 긴 미팅 전사본이 제한 초과 가능 |
| **N+1 공유 미팅 쿼리** | MEDIUM | `dynamodb.go:330-336` 공유 건마다 GetMeetingByID |

---

### 2.5 보안

#### 강점
- Lambda@Edge JWT 검증 (엣지에서 차단)
- S3 퍼블릭 접근 전면 차단
- Cognito 표준 인증 플로우

#### 약점

| 이슈 | 심각도 | 상세 |
|------|--------|------|
| **백엔드 JWT 서명 미검증** | MEDIUM | 디코드만 수행, 서명 검증 X |
| **API Gateway CORS 와일드카드** | MEDIUM | `gateway-stack.ts:155`: `allowOrigins: ['*']` |
| **ECS에 Lambda 역할 공유** | LOW | 과도한 권한 |
| **WAF/레이트 리미팅 없음** | MEDIUM | DoS 위험 |

---

## 3. 비용 분석

### 3.1 현재 미팅당 비용 (30분 미팅)

| 컴포넌트 | 사용량 | 단가 | 추정 비용 |
|---------|-------|------|---------|
| ECS GPU Spot (g4dn.xlarge) | 30분 | ~$0.16/hr | $0.08 |
| AWS Transcribe | 30분 오디오 | $0.024/min | **$0.72** |
| Bedrock Claude Opus | ~10K 토큰 | $15/M in, $75/M out | **$0.27** |
| DynamoDB | ~60 읽기/쓰기 | PAY_PER_REQUEST | $0.01 |
| S3 + CloudFront | ~50MB | - | $0.001 |
| Lambda | ~10 호출 | - | $0.0001 |
| **합계** | | | **~$1.08** |

### 3.2 최적화 후 비용

| 최적화 | 절감액 |
|--------|-------|
| Transcribe 스킵 (Whisper 사용 시) | -$0.72 |
| Opus → Sonnet 요약 | -$0.22 |
| Opus → Haiku 라이브 요약 | -$0.10 |
| 이중 오디오 업로드 제거 | -$0.01 |
| **최적화 후 미팅당** | **~$0.30** |

### 3.3 월간 비용 추정

| 시나리오 | 미팅/월 | 현재 | 최적화 후 |
|---------|--------|------|---------|
| 개인 사용자 | 20 | $21.60 | $6.00 |
| 소규모 팀 (5명) | 100 | $108 | $30 |
| 중규모 조직 (50명) | 1,000 | $1,080 | $300 |

---

## 4. 확장성 평가

| 차원 | 현재 용량 | 병목 | 확장 방법 |
|------|---------|------|---------|
| 동시 미팅 | 1 (단일 ECS 태스크) | GPU 인스턴스 | ASG maxCapacity 증가 |
| 미팅 시간 | ~60분 (DynamoDB 400KB) | 아이템 크기 | 전사본 S3 저장 |
| 히스토리 미팅 | ~10K (스캔 성능) | GetMeetingByID | GSI3 추가 |
| 미팅당 참가자 | 무제한 | - | 양호 |

---

## 5. 개선 로드맵

### P0 — 즉시 (1주)

| # | 항목 | 효과 | 작업량 |
|---|------|------|--------|
| 1 | **GetMeetingByID GSI 추가** | 스캔 비용 제거, 응답 속도 향상 | 2-4시간 |
| 2 | **요약 모델 Sonnet으로 교체** | 미팅당 $0.22 절감 | 1시간 |
| 3 | **라이브 요약 Haiku로 교체** | 미팅당 $0.10 절감 | 1시간 |
| 4 | **이중 전사 제거** | Whisper 사용 시 Transcribe 스킵 | 1-2시간 |

### P1 — 단기 (1개월)

| # | 항목 | 효과 | 작업량 |
|---|------|------|--------|
| 5 | **Silero VAD 브라우저 적용** | 대역폭/STT 비용 30-50% 절감 | 8-16시간 |
| 6 | **Deepgram Nova-3 통합 평가** | 한국어 실시간 스트리밍, 비용 절감 | 8-16시간 |
| 7 | **ECS Spot 인터럽션 핸들링** | 오디오 손실 방지 | 4-8시간 |
| 8 | **전사본 S3 저장** | DynamoDB 400KB 제한 해소 | 4-8시간 |
| 9 | **Nova Sonic 한국어 품질 모니터링** | 비공식 지원 상태 추적 | 지속 |
| 10 | **JWT 서명 검증 추가** | 보안 심층 방어 | 2-4시간 |

### P2 — 중기 (3개월)

| # | 항목 | 효과 | 작업량 |
|---|------|------|--------|
| 11 | **pyannote 후처리 화자 분리** | DER 2-3배 개선 | 16-24시간 |
| 12 | **Whisper 인터림 결과 구현** | 실시간 응답성 향상 | 8-16시간 |
| 13 | **ECS 수평 확장** | 동시 미팅 지원 | 4-8시간 |
| 14 | **CORS 제한** | 보안 강화 | 1시간 |
| 15 | **액션 아이템 자동 추출** | 경쟁력 확보 | 8-16시간 |
| 16 | **Notion/Slack 연동** | 유저 워크플로우 통합 | 16-24시간 |
| 17 | **모바일 앱** | 한국 시장 필수 | 80-120시간 |

---

## 6. 트레이드오프 정리

| 선택지 | 장점 | 단점 | 추천 |
|--------|------|------|------|
| Whisper+Transcribe 유지 | A/B 비교, 화자 분리 | 2배 비용 | 단기 유지, 중기 Deepgram 전환 |
| Whisper만 | 저비용, 단순 | 화자 분리 없음 | 화자 분리 불필요 시 |
| Deepgram 전환 | 실시간 스트리밍, 저비용, 화자 분리 | 외부 의존 | **중기 추천** |
| GPU 상시 유지 | 즉시 STT | ~$115/월 Spot 24/7 | 사용량 많을 때만 |
| Scale-to-zero | 비용 $0 미사용 시 | 2-5분 콜드스타트 | **현재 적합** |
| 전사본 S3 이관 | 용량 제한 없음 | 추가 S3 GET | **미팅 길어지면 필수** |

---

## 7. 결론

Ttobak의 아키텍처는 이벤트 기반 설계와 점진적 향상(Web Speech → ECS Whisper) 패턴이 잘 설계되어 있으나, 다음 3가지 핵심 이슈를 우선 해결해야 합니다:

1. **DynamoDB 테이블 스캔** — 사용자 증가 시 비용/성능 병목
2. **모델 비용 과다** — Opus→Sonnet/Haiku 교체로 70% 즉시 절감
3. **이중 전사 낭비** — 실시간 STT 사용 시 배치 Transcribe 스킵

이 3가지만 해결해도 미팅당 비용이 $1.08 → $0.30으로 72% 절감되며, 확장성 병목이 제거됩니다.

---

*Ttobak 아키텍처 리뷰 — 2026-03-24*
