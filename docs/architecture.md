# Architecture / 아키텍처

<p align="center">
  <kbd><a href="#한국어">한국어</a></kbd>&nbsp;&nbsp;
  <kbd><a href="#english">English</a></kbd>
</p>

---

## 한국어

### 시스템 개요

Ttobak(또박)은 한국어 AI 회의 어시스턴트입니다. 브라우저에서 오디오를 녹음하고, 실시간 음성 인식(Web Speech API / AWS Transcribe Streaming)과 후처리 배치 STT(기본: Whisper GPU on ECS Spot, 폴백: AWS Transcribe)로 텍스트를 추출한 후, Bedrock Claude로 요약을 생성합니다. Next.js 16 정적 SPA → CloudFront → API Gateway → Go Lambda → DynamoDB/S3 아키텍처입니다.

### 컴포넌트 레이어

| 레이어 | 컴포넌트 | 기술 |
|--------|----------|------|
| **프레젠테이션** | 정적 SPA | Next.js 16, React 19, Tailwind v4, TipTap |
| **인증** | JWT 인증 | Cognito User Pool, Lambda@Edge |
| **인제스트** | 오디오 업로드 | S3 Presigned URL, EventBridge |
| **처리** | STT 파이프라인 (실시간) | Web Speech API, AWS Transcribe Streaming |
| **처리** | STT 파이프라인 (배치, 기본) | Whisper GPU large-v3 (ECS g5.xlarge Spot) |
| **처리** | STT 파이프라인 (배치, 폴백) | AWS Transcribe |
| **처리** | AI 요약 | Bedrock Claude (Opus/Haiku) |
| **처리** | 이미지 분석 | Bedrock Vision |
| **저장** | 데이터 | DynamoDB (단일 테이블), S3 |
| **쿼리** | RAG Q&A | Bedrock Knowledge Base, OpenSearch Serverless |
| **보안** | 암호화 | KMS (Notion API 키), S3 SSE |
| **인프라** | IaC | AWS CDK TypeScript (10 스택) |

### 전체 아키텍처 다이어그램

```
┌──────────────────────────────────────────────────────────────────┐
│                        CloudFront (CDN)                          │
│  d2olomx8td8txt.cloudfront.net                                   │
├──────────┬────────────────────┬──────────────────────────────────┤
│          │                    │                                   │
│  ┌───────▼───────┐  ┌────────▼────────┐                         │
│  │ Lambda@Edge   │  │  S3 OAC         │                         │
│  │ (JWT Auth)    │  │  (Static SPA)   │                         │
│  │ us-east-1     │  │  frontend/out/  │                         │
│  └───────┬───────┘  └─────────────────┘                         │
│          │                                                       │
└──────────┼───────────────────────────────────────────────────────┘
           │
   ┌───────▼──────────────────────────────────┐
   │        HTTP API Gateway (v1.0)            │
   │  /api/* → ttobak-api Lambda (chi router) │
   │  /api/qa/* → ttobak-qa Lambda (Python)   │
   └───────┬──────────────────────────────────┘
           │
   ┌───────▼──────────────────────────────────────────────────┐
   │  ttobak-api Lambda (Go, chi router)                       │
   │  ├─ /api/meetings/* → DynamoDB CRUD                       │
   │  ├─ /api/uploads/*  → S3 Presigned URL                    │
   │  ├─ /api/translate   → Amazon Translate                   │
   │  ├─ /api/summarize-live → Bedrock Claude                  │
   │  ├─ /api/export/*   → Markdown/Notion Export              │
   │  ├─ /api/kb/*       → Knowledge Base                      │
   │  └─ /api/settings/* → KMS-encrypted Notion key            │
   └──────────────────────────────────────────────────────────┘

   ┌──────────── Event-Driven Pipeline ────────────────────────┐
   │                                                            │
   │  S3 audio/ PUT ──▶ EventBridge ──▶ ttobak-transcribe      │
   │                                    ├─ Whisper GPU (ECS, default)│
   │                                    └─ AWS Transcribe (fallback) │
   │                         │                                  │
   │  S3 transcripts/ PUT ──▶ EventBridge ──▶ ttobak-summarize │
   │                                         └─ Bedrock Claude  │
   │                                              │              │
   │  S3 images/ PUT ──▶ EventBridge ──▶ ttobak-process-image  │
   │                                    └─ Bedrock Vision       │
   │                                                            │
   │  All results ──▶ DynamoDB (ttobak-main)                   │
   └────────────────────────────────────────────────────────────┘

   ┌──────────── Knowledge Base (RAG) ─────────────────────────┐
   │  S3 (ttobak-kb) ──▶ Bedrock KB ──▶ OpenSearch Serverless  │
   │  ttobak-qa Lambda (Python) ──▶ Bedrock Retrieve & Generate│
   └────────────────────────────────────────────────────────────┘
```

### 데이터 흐름 요약

```
브라우저 녹음 → S3 업로드 → EventBridge → Transcribe Lambda → Whisper GPU (ECS Spot) → S3 transcript
→ EventBridge → Summarize Lambda → Bedrock Claude → DynamoDB → 프론트엔드 표시
```

### CDK 스택 구성

| 스택 | 리소스 | 의존성 |
|------|--------|--------|
| `TtobakAuthStack` | Cognito User Pool, Client | - |
| `TtobakStorageStack` | DynamoDB, S3 (assets) | - |
| `TtobakAIStack` | IAM Roles, KMS Key | Auth, Storage |
| `TtobakKnowledgeStack` | S3 (KB), OpenSearch, Bedrock KB | AI |
| `TtobakEdgeAuthStack` | Lambda@Edge (us-east-1) | Auth |
| `TtobakGatewayStack` | API Gateway HTTP/WebSocket, 8 Lambdas, EventBridge | AI, Knowledge |
| `TtobakWhisperStack` | ECS Cluster, ECR, ASG (GPU Spot, min=0, max=10) | Storage |
| `TtobakCrawlerStack` | Step Functions, 4 크롤러 Lambda, 일일 EventBridge | Knowledge |
| `TtobakResearchAgentStack` | Bedrock Agent (Deep Research), 도구 Lambda | Knowledge |
| `TtobakFrontendStack` | CloudFront, S3 (site) | Gateway, EdgeAuth |

### 주요 설계 결정

1. **정적 SPA + Lambda@Edge 인증**: SSR 대신 정적 내보내기로 S3 호스팅 비용 최소화. Lambda@Edge로 API 경로만 JWT 검증.
   - *이유*: 서버리스 비용 최적화, CloudFront 캐싱 활용

2. **단일 테이블 DynamoDB 설계**: 미팅, 사용자, 첨부파일, 공유를 하나의 테이블에 GSI로 관리.
   - *이유*: DynamoDB 트랜잭션으로 원자적 삭제, 비용 절감

3. **이벤트 기반 파이프라인**: S3 업로드 → EventBridge → Lambda 체인으로 비동기 처리.
   - *이유*: API 응답 시간과 처리 시간 분리, Lambda 타임아웃 독립 관리

4. **Whisper GPU on ECS Spot (Zero-Scale)**: 벤치마크 결과 AWS Transcribe(3.5/10) 대비 Whisper GPU(7.5/10)가 품질 2배, 비용 36배 저렴.
   - *이유*: 한영 혼용 기술 회의에서 영어 약어/서비스명 인식이 압도적으로 우수. ASG min=0으로 유휴 비용 $0.

### 아키텍처 결정 기록 (ADR)

- [ADR-001: 원격 회의 시스템 오디오 캡처](decisions/ADR-001-system-audio-capture-for-remote-meetings.md) — `getDisplayMedia` + `AudioContext` 믹싱 (제안됨)
- [ADR-002: GitHub Actions + Self-Hosted Runner](decisions/ADR-002-deploy-pipeline-github-actions-self-hosted.md) (승인됨)
- [ADR-003: 외부 미팅 접근을 위한 MCP 서버](decisions/ADR-003-mcp-server-for-external-meeting-access.md) (승인됨)
- [ADR-004: SA 지식베이스를 위한 크롤러 및 인사이트](decisions/ADR-004-crawler-insights-for-sa-knowledge-base.md) (승인됨)
- [ADR-005: STT 다국어 자동 감지](decisions/ADR-005-multi-language-auto-detection-for-stt.md) (승인됨)
- [ADR-006: 탭 오디오 캡처 + Tauri Mac App](decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md) (승인됨)
- [ADR-007: 회원가입 이메일 도메인 허용 목록](decisions/ADR-007-email-domain-allowlist-for-signup.md) (승인됨)
- [ADR-008: STT 정확도 향상을 위한 커스텀 사전](decisions/ADR-008-custom-dictionary-for-stt-accuracy.md) (제안됨, 구현 대기)
- [ADR-009: Whisper GPU ECS Spot Zero-Scale](decisions/ADR-009-whisper-gpu-ecs-spot-zero-scale.md) — AWS Transcribe → Whisper GPU 전환, 품질 2배·비용 36배 절감 (승인됨)
- [ADR-010: Insights Obsidian 스타일 마크다운 렌더링](decisions/ADR-010-insights-obsidian-style-markdown-rendering.md) (승인됨)
- [ADR-011: 대화형 계획 수립 기반 인터랙티브 딥 리서치](decisions/ADR-011-interactive-deep-research.md) (승인됨)

### 운영

- 런북: `docs/runbooks/` 참조
- 인프라 스펙: `docs/INFRA-SPEC.md`
- API 스펙: `docs/API-SPEC.md`

---

## English

### System Overview

Ttobak is a Korean AI meeting assistant. It records audio in the browser, extracts text via real-time speech recognition (Web Speech API / AWS Transcribe Streaming) and post-recording batch STT (default: Whisper GPU on ECS Spot; fallback: AWS Transcribe), then generates summaries with Bedrock Claude. Architecture: Next.js 16 static SPA → CloudFront → API Gateway → Go Lambda → DynamoDB/S3.

### Components by Layer

| Layer | Component | Technology |
|-------|-----------|------------|
| **Presentation** | Static SPA | Next.js 16, React 19, Tailwind v4, TipTap |
| **Auth** | JWT Authentication | Cognito User Pool, Lambda@Edge |
| **Ingestion** | Audio Upload | S3 Presigned URL, EventBridge |
| **Processing** | STT Pipeline | Whisper GPU (ECS Spot), AWS Transcribe, Web Speech API |
| **Processing** | AI Summary | Bedrock Claude (Opus/Haiku) |
| **Processing** | Image Analysis | Bedrock Vision |
| **Storage** | Data | DynamoDB (single-table), S3 |
| **Query** | RAG Q&A | Bedrock Knowledge Base, OpenSearch Serverless |
| **Security** | Encryption | KMS (Notion API key), S3 SSE |
| **Infrastructure** | IaC | AWS CDK TypeScript (10 stacks) |

### Full Architecture Diagram

(Same diagram as Korean section above)

### Data Flow Summary

```
Browser Recording → S3 Upload → EventBridge → Transcribe Lambda → Whisper GPU (ECS Spot) → S3 Transcript
→ EventBridge → Summarize Lambda → Bedrock Claude → DynamoDB → Frontend Display
```

### CDK Stack Composition

| Stack | Resources | Dependencies |
|-------|-----------|-------------|
| `TtobakAuthStack` | Cognito User Pool, Client | - |
| `TtobakStorageStack` | DynamoDB, S3 (assets) | - |
| `TtobakAIStack` | IAM Roles, KMS Key | Auth, Storage |
| `TtobakKnowledgeStack` | S3 (KB), OpenSearch, Bedrock KB | AI |
| `TtobakEdgeAuthStack` | Lambda@Edge (us-east-1) | Auth |
| `TtobakGatewayStack` | API Gateway HTTP/WebSocket, 8 Lambdas, EventBridge | AI, Knowledge |
| `TtobakWhisperStack` | ECS Cluster, ECR, ASG (GPU Spot, min=0, max=10) | Storage |
| `TtobakCrawlerStack` | Step Functions, 4 crawler Lambdas, daily EventBridge | Knowledge |
| `TtobakResearchAgentStack` | Bedrock Agent (Deep Research), tool Lambdas | Knowledge |
| `TtobakFrontendStack` | CloudFront, S3 (site) | Gateway, EdgeAuth |

### Key Design Decisions

1. **Static SPA + Lambda@Edge Auth**: Static export instead of SSR minimizes S3 hosting costs. Lambda@Edge validates JWT only on API routes.
   - *Why*: Serverless cost optimization, leverages CloudFront caching

2. **Single-Table DynamoDB Design**: Meetings, users, attachments, and shares in one table with GSIs.
   - *Why*: Atomic deletes via DynamoDB transactions, cost savings

3. **Event-Driven Pipeline**: S3 upload → EventBridge → Lambda chain for async processing.
   - *Why*: Decouples API response time from processing time, independent Lambda timeout management

4. **Whisper GPU on ECS Spot (Zero-Scale)**: Per benchmarks, Whisper GPU (7.5/10) outperforms AWS Transcribe (3.5/10) — 2× quality, 36× cheaper for Korean/English mixed technical meetings. ASG min=0 means $0 idle cost. AWS Transcribe is retained as a fallback engine.
   - *Why*: Superior recognition of English acronyms/AWS service names in mixed-language meetings; cost optimization via Spot + zero-scale.

### Architecture Decision Records (ADR)

- [ADR-001: System Audio Capture for Remote Meetings](decisions/ADR-001-system-audio-capture-for-remote-meetings.md) — `getDisplayMedia` + `AudioContext` mixing (Proposed)
- [ADR-002: GitHub Actions + Self-Hosted Runner](decisions/ADR-002-deploy-pipeline-github-actions-self-hosted.md) (Accepted)
- [ADR-003: MCP Server for External Meeting Access](decisions/ADR-003-mcp-server-for-external-meeting-access.md) (Accepted)
- [ADR-004: Crawler & Insights for SA Knowledge Base](decisions/ADR-004-crawler-insights-for-sa-knowledge-base.md) (Accepted)
- [ADR-005: Multi-Language Auto-Detection for STT](decisions/ADR-005-multi-language-auto-detection-for-stt.md) (Accepted)
- [ADR-006: Tab Audio Capture + Tauri Mac App](decisions/ADR-006-tab-audio-capture-and-tauri-mac-app.md) (Accepted)
- [ADR-007: Email Domain Allowlist for Signup](decisions/ADR-007-email-domain-allowlist-for-signup.md) (Accepted)
- [ADR-008: Custom Dictionary for STT Accuracy](decisions/ADR-008-custom-dictionary-for-stt-accuracy.md) (Proposed, implementation pending)
- [ADR-009: Whisper GPU ECS Spot Zero-Scale](decisions/ADR-009-whisper-gpu-ecs-spot-zero-scale.md) — Migrate from AWS Transcribe to Whisper GPU; 2× quality, 36× cost reduction (Accepted)
- [ADR-010: Obsidian-style Rich Markdown Rendering for Insights](decisions/ADR-010-insights-obsidian-style-markdown-rendering.md) (Accepted)
- [ADR-011: Interactive Deep Research with Conversational Planning](decisions/ADR-011-interactive-deep-research.md) (Accepted)

### Operations

- Runbooks: see `docs/runbooks/`
- Infrastructure spec: `docs/INFRA-SPEC.md`
- API spec: `docs/API-SPEC.md`
