# Ttobak (또박) - AI Meeting Assistant

회의를 녹음하고, AI가 자동으로 텍스트 변환과 요약을 생성하는 미팅 어시스턴트입니다.

## 주요 기능

- **녹음 & 실시간 자막** — 브라우저에서 바로 녹음, Web Speech API 기반 실시간 자막 표시
- **A/B 음성 인식** — AWS Transcribe와 Nova Sonic 두 가지 STT 엔진으로 동시 변환, 결과 비교 후 선택
- **AI 요약** — Amazon Bedrock Claude가 회의 내용을 자동 요약하여 마크다운 노트 생성
- **실시간 번역** — 녹음 중 Amazon Translate로 6개 언어(EN, JA, ZH, ES, FR, DE) 실시간 번역
- **라이브 요약** — 200단어마다 Bedrock Claude가 점진적 요약 생성
- **Notion 스타일 에디터** — TipTap 기반 리치 텍스트 에디터로 회의록 편집
- **이미지 첨부** — 화이트보드, 다이어그램 등 이미지 업로드 시 Bedrock Vision으로 자동 분석
- **Knowledge Base** — Bedrock KB + OpenSearch Serverless 기반 RAG로 회의 내용 Q&A
- **공유 & 협업** — 이메일 기반 사용자 검색, 읽기/편집 권한으로 회의록 공유
- **내보내기** — PDF, Notion, Obsidian 포맷으로 회의록 내보내기
- **반응형 UI** — 모바일/데스크톱 모두 지원하는 반응형 디자인

## 기술 스택

| 레이어 | 기술 |
|--------|------|
| Frontend | Next.js 16, React 19, Tailwind CSS v4, TipTap Editor |
| Backend | Go 1.24, chi router, AWS Lambda (ARM64) |
| Auth | Amazon Cognito User Pool + Lambda@Edge JWT 인증 |
| Database | DynamoDB (Single-Table Design) |
| Storage | S3 + CloudFront (OAC) |
| AI/ML | Amazon Bedrock (Claude), AWS Transcribe, Nova Sonic, Amazon Translate |
| Knowledge | Bedrock Knowledge Base + OpenSearch Serverless |
| IaC | AWS CDK (TypeScript), 7개 스택 |
| Realtime | API Gateway WebSocket |

## 아키텍처

```
                        ┌─────────────────────────────┐
                        │  CloudFront Distribution     │
                        │  d115v97ubjhb06.cloudfront   │
                        └──────┬──────┬──────┬────────┘
                               │      │      │
                    ┌──────────┘      │      └──────────┐
                    ▼                 ▼                  ▼
           Lambda@Edge         S3 (Static)      API Gateway HTTP
           (JWT Auth)          (Next.js SPA)    + WebSocket API
           us-east-1                                    │
                                                        ▼
                                              ┌─────────────────┐
                                              │  Lambda Functions │
                                              │  (Go, ARM64)     │
                                              ├─────────────────┤
                                              │ api (chi router) │
                                              │ transcribe       │
                                              │ summarize        │
                                              │ process-image    │
                                              │ websocket        │
                                              │ kb               │
                                              └────┬───┬───┬────┘
                                                   │   │   │
                            ┌──────────────────────┘   │   └────────────────┐
                            ▼                          ▼                    ▼
                       DynamoDB                   S3 (Assets)         Bedrock / KB
                     (Single Table)             audio, images,      Claude, Transcribe,
                                               transcripts          OpenSearch Serverless
```

### 이벤트 기반 파이프라인

```
audio/ 업로드 ──→ EventBridge ──→ transcribe Lambda ──→ Transcribe + Nova Sonic ──→ transcripts/ S3
transcripts/ 업로드 ──→ EventBridge ──→ summarize Lambda ──→ Bedrock Claude ──→ DynamoDB (content)
images/ 업로드 ──→ EventBridge ──→ process-image Lambda ──→ Bedrock Vision ──→ DynamoDB (attachment)
```

### CDK 스택 의존성

```
Auth ─────┐
          ├──→ AI ──→ Knowledge ──→ Gateway ──→ Frontend
Storage ──┘                           ▲
                                      │
EdgeAuth (us-east-1) ─────────────────┘
```

## 프로젝트 구조

```
ttobak/
├── backend/
│   ├── cmd/                    # Lambda 진입점 (6개 함수)
│   │   ├── api/                # REST API (chi router)
│   │   ├── transcribe/         # S3 audio → Transcribe 트리거
│   │   ├── summarize/          # S3 transcript → Bedrock 요약
│   │   ├── process-image/      # S3 image → Bedrock Vision
│   │   ├── websocket/          # WebSocket 연결 관리
│   │   └── kb/                 # Knowledge Base 동기화
│   ├── internal/
│   │   ├── handler/            # HTTP 핸들러
│   │   ├── service/            # 비즈니스 로직
│   │   ├── repository/         # DynamoDB 접근
│   │   ├── model/              # 데이터 모델, DDB 키 프리픽스
│   │   └── middleware/         # JWT 인증, CORS, Recovery
│   ├── go.mod
│   └── go.sum
├── frontend/
│   ├── src/
│   │   ├── app/                # Next.js App Router 페이지
│   │   │   ├── page.tsx        # 메인 (회의 목록 + 로그인)
│   │   │   ├── record/         # 녹음 페이지
│   │   │   ├── meeting/[id]/   # 회의 상세 (에디터, Q&A, 공유)
│   │   │   ├── kb/             # Knowledge Base 관리
│   │   │   └── settings/       # 연동 설정 (Notion)
│   │   ├── components/         # React 컴포넌트
│   │   ├── hooks/              # 커스텀 훅 (useAudioDevices 등)
│   │   ├── lib/                # API 클라이언트, 인증, WebSocket
│   │   └── types/              # TypeScript 타입 정의
│   └── package.json
├── infra/
│   ├── bin/infra.ts            # CDK 앱 진입점 (7개 스택)
│   └── lib/                    # CDK 스택 정의
│       ├── auth-stack.ts       # Cognito User Pool
│       ├── storage-stack.ts    # DynamoDB + S3
│       ├── ai-stack.ts         # IAM 역할 (Bedrock, Transcribe 등)
│       ├── knowledge-stack.ts  # OpenSearch Serverless + Bedrock KB
│       ├── edge-auth-stack.ts  # Lambda@Edge (us-east-1)
│       ├── gateway-stack.ts    # API Gateway + Lambda + EventBridge
│       └── frontend-stack.ts   # S3 + CloudFront
├── design_sample/              # HTML 디자인 목업 (모바일/PC)
└── CLAUDE.md                   # AI 코딩 어시스턴트 가이드
```

## 시작하기

### 사전 요구사항

- Go 1.24+
- Node.js 20+
- AWS CLI (설정 완료)
- AWS CDK CLI (`npm install -g aws-cdk`)

### 로컬 개발

```bash
# Frontend 개발 서버
cd frontend
npm install
npm run dev          # http://localhost:3000

# Backend 빌드 (Lambda용 ARM64 바이너리)
cd backend
GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

### 인프라 배포

```bash
# CDK 의존성 설치
cd infra
npm install

# 전체 스택 배포
npx cdk deploy --all

# 개별 스택 배포
npx cdk deploy TtobakAuthStack
npx cdk deploy TtobakStorageStack
npx cdk deploy TtobakAiStack
npx cdk deploy TtobakKnowledgeStack
npx cdk deploy TtobakEdgeAuthStack
npx cdk deploy TtobakGatewayStack
npx cdk deploy TtobakFrontendStack
```

### Frontend 배포

```bash
cd frontend
npm run build

# S3 업로드 + CloudFront 캐시 무효화
aws s3 sync out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3BPV9VFNI1H2S --paths "/*"
```

### 환경 변수

**Frontend** (`.env.local`):
```
NEXT_PUBLIC_API_URL=https://d115v97ubjhb06.cloudfront.net
NEXT_PUBLIC_COGNITO_USER_POOL_ID=ap-northeast-2_u01c65YjO
NEXT_PUBLIC_COGNITO_CLIENT_ID=2gfu9i3vk53idetqnu0tqb3sog
```

**Backend** (Lambda 환경변수 — CDK에서 자동 설정):
```
TABLE_NAME=ttobak-main
BUCKET_NAME=ttobak-assets-*
KB_BUCKET_NAME=ttobak-kb-*
KB_ID=XGFBOMVSS8
KB_DATASOURCE_ID=34EK3IZECI
```

## API 엔드포인트

| Method | Path | 설명 |
|--------|------|------|
| GET | `/api/health` | 헬스체크 (인증 불필요) |
| GET | `/api/meetings` | 회의 목록 조회 |
| POST | `/api/meetings` | 회의 생성 |
| GET | `/api/meetings/:id` | 회의 상세 조회 |
| PUT | `/api/meetings/:id` | 회의 수정 |
| DELETE | `/api/meetings/:id` | 회의 삭제 |
| PUT | `/api/meetings/:id/transcript` | 트랜스크립트 선택 (A/B) |
| POST | `/api/meetings/:id/share` | 회의 공유 |
| POST | `/api/meetings/:id/ask` | KB 기반 Q&A |
| POST | `/api/meetings/:id/export` | 내보내기 (PDF/Notion/Obsidian) |
| POST | `/api/meetings/:id/summarize` | 실시간 요약 생성 |
| POST | `/api/upload/presigned` | S3 Presigned URL 발급 |
| POST | `/api/translate` | 텍스트 번역 |
| POST | `/api/kb/upload` | KB 파일 업로드 URL |
| POST | `/api/kb/sync` | KB 동기화 트리거 |
| GET | `/api/settings/integrations` | 연동 설정 조회 |

## DynamoDB 테이블 설계

싱글 테이블 디자인 (`ttobak-main`):

| Entity | PK | SK |
|--------|----|----|
| Meeting | `USER#{userId}` | `MEETING#{meetingId}` |
| Attachment | `MEETING#{meetingId}` | `ATTACH#{attachmentId}` |
| User Profile | `USER#{userId}` | `PROFILE` |
| Share (수신자) | `USER#{sharedToId}` | `SHARED#{meetingId}` |
| Share (회의별) | `MEETING#{meetingId}` | `SHARE_TO#{userId}` |

- **GSI1**: 날짜순 정렬 (`GSI1PK=USER#{userId}`, `GSI1SK=timestamp`)
- **GSI2**: 이메일 검색 (`GSI2PK=EMAIL#{email}`, `GSI2SK=USER#{userId}`)

## 문서

상세 설계 문서는 `docs/` 폴더에 있습니다:

| 문서 | 설명 |
|------|------|
| [PRD.md](docs/PRD.md) | 제품 요구사항, 기능별 우선순위 및 진행 상태 |
| [API-SPEC.md](docs/API-SPEC.md) | REST + WebSocket API 명세 (요청/응답 스키마) |
| [INFRA-SPEC.md](docs/INFRA-SPEC.md) | CDK 스택 상세 설계, Lambda 설정, 크로스리전 배포 |
| [DESIGN-SPEC.md](docs/DESIGN-SPEC.md) | 디자인 토큰, 컴포넌트 스펙, 아이콘 매핑 |
| [CODE-REVIEW.md](docs/CODE-REVIEW.md) | 코드 리뷰 결과, 알려진 이슈, 결정 필요 사항 |

## 라이선스

Private project.
