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
| STT | AWS Transcribe Streaming (browser), Web Speech API, Nova Sonic |

## 아키텍처

```
                        ┌─────────────────────────────┐
                        │  CloudFront Distribution     │
                        │  d2olomx8td8txt.cloudfront   │
                        └──────┬──────┬──────┬────────┘
                               │      │      │
                    ┌──────────┘      │      └──────────┐
                    ▼                 ▼                  ▼
           Lambda@Edge         S3 (Static)      API Gateway HTTP
           (JWT Auth)          (Next.js SPA)         API
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
│   ├── cmd/                    # Lambda 진입점 (5개 함수)
│   │   ├── api/                # REST API (chi router)
│   │   ├── transcribe/         # S3 audio → Transcribe 트리거
│   │   ├── summarize/          # S3 transcript → Bedrock 요약
│   │   ├── process-image/      # S3 image → Bedrock Vision
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
│   │   │   ├── files/           # 오디오 파일 관리
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
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

### 환경 변수

**Frontend** (`.env.local`):
```
NEXT_PUBLIC_API_URL=https://d2olomx8td8txt.cloudfront.net
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

## 테스트 시나리오: 보험상품 콜센터 상담

녹음 페이지에서 Q&A 감지 및 KB 기반 답변을 테스트하기 위한 대화 스크립트입니다. 두 명이 번갈아 읽거나, 한 명이 순서대로 읽으면 됩니다.

> **사전 준비**: `docs/insurance/` 폴더의 3개 문서를 KB에 업로드하고 동기화 완료 후 테스트

### 대화 스크립트

**상담원**: 안녕하세요, 또박보험 고객센터입니다. 무엇을 도와드릴까요?

**고객**: 네 안녕하세요. 제가 지금 자동차보험 갱신 시기가 다가와서 전화드렸는데요, 이번에 운전자보험도 같이 알아보려고요.

**상담원**: 네, 고객님. 먼저 현재 가입하신 자동차보험 만기일이 언제인지 확인해 드릴까요?

**고객**: 다음 달 15일이 만기예요. 지금 대물 5천만 원에 자차까지 들어있는데, 대물을 1억으로 올리고 싶어요.

**상담원**: 네, 알겠습니다. 최근에 대물사고 보상액이 커지고 있어서 1억 원으로 올리시는 게 좋습니다. 고객님 차량이 쏘나타이시고 만 35세이시니까, 대물 1억에 자차 포함하면 기본 보험료가 월 78,000원 정도 나옵니다.

**고객**: 생각보다 좀 비싼데요. 할인 받을 수 있는 방법이 있을까요?

**상담원**: 고객님, 무사고 기간이 어떻게 되세요?

**고객**: 5년째 무사고예요.

**상담원**: 그러시면 무사고 할인 20퍼센트를 받으실 수 있고요, 블랙박스 장착하고 계시면 추가 2퍼센트, 온라인으로 직접 가입하시면 3퍼센트 할인이 더 됩니다. 그리고 만기 30일 전에 갱신하시면 조기 갱신 할인 2퍼센트도 적용돼요.

**고객**: 마일리지 할인도 있다고 들었는데, 저는 출퇴근만 해서 1년에 7천 킬로 정도밖에 안 타거든요.

**상담원**: 연간 7천 킬로미터 이하시면 마일리지 할인 8퍼센트를 받으실 수 있습니다. 다만 OBD 단말기를 장착하시거나 저희 앱으로 주행거리를 측정하셔야 해요.

**고객**: 그러면 전부 합치면 할인이 얼마나 되는 거예요?

**상담원**: 무사고 20퍼센트, 마일리지 8퍼센트, 블랙박스 2퍼센트, 온라인 가입 3퍼센트, 조기 갱신 2퍼센트 하면 총 35퍼센트인데요, 최대 할인 한도가 35퍼센트라서 딱 맞습니다. 월 보험료가 78,000원에서 약 50,700원 정도로 내려갑니다.

**고객**: 오 그 정도면 괜찮네요. 그런데 운전자보험은 자동차보험이랑 뭐가 다른 건가요?

**상담원**: 자동차보험은 상대방 피해 보상이 중심이고, 운전자보험은 고객님 본인의 형사적 책임과 상해를 보장합니다. 예를 들어 교통사고로 형사합의금이나 벌금이 나오면 운전자보험에서 보장해 드려요.

**고객**: 형사합의금이 최대 얼마까지 보장되나요?

**상담원**: 저희 표준형 기준으로 일반 교통사고는 최대 3천만 원, 12대 중과실 사고는 최대 5천만 원까지 보장됩니다. 벌금은 최대 2천만 원, 변호사 선임 비용도 사건당 200만 원까지 나와요.

**고객**: 12대 중과실이 뭔가요?

**상담원**: 신호위반, 중앙선 침범, 속도위반, 횡단보도 보행자 사고 같은 중대한 과실 항목 12가지를 말합니다. 이런 사고는 종합보험에 가입되어 있어도 형사 처벌을 받을 수 있어서 운전자보험이 특히 중요해요.

**고객**: 보험료는 어느 정도 하나요?

**상담원**: 표준형이 월 28,500원이고요, 부상 치료비랑 입원 일당, 후유장해까지 포함된 프리미엄형은 월 42,300원입니다. 고객님처럼 운전을 많이 안 하시면 기본형 15,800원도 있는데, 형사합의금이 3천만 원으로 제한되고 치료비 보장이 빠져요.

**고객**: 자기부담금은 어떻게 되죠? 자동차보험에서 자차 사고 나면 자기부담금을 내야 한다고 들었는데요.

**상담원**: 네, 자차 보험에서 일반 사고는 건당 20만 원의 자기부담금이 있고요, 단독사고의 경우에는 수리비의 30퍼센트 또는 50만 원 중 큰 금액을 부담하셔야 합니다. 운전자보험에는 별도 자기부담금이 없습니다.

**고객**: 알겠습니다. 그러면 자동차보험은 대물 1억으로 올리고, 운전자보험은 표준형으로 가입할게요. 면책사항 같은 건 없나요?

**상담원**: 자동차보험에서는 음주운전이나 무면허 운전 중 사고는 보장이 안 되고요, 운전자보험도 마찬가지로 음주운전 관련 벌금이나 형사합의금은 면책입니다. 그리고 운전자보험은 가입 후 1개월간 면책 기간이 있어서 그 기간에 발생한 사고는 보장이 안 됩니다.

**고객**: 네, 이해했습니다. 그러면 진행해 주세요.

**상담원**: 네, 자동차보험 갱신과 운전자보험 표준형 신규 가입 도와드리겠습니다. 감사합니다.

---

### KB 업로드용 문서

`docs/insurance/` 폴더에 3개의 참조 문서가 준비되어 있습니다:

| 파일 | 내용 |
|------|------|
| [auto-insurance-guide.md](docs/insurance/auto-insurance-guide.md) | 자동차보험 상품 가이드 (보장 항목, 보험료, 면책사항) |
| [driver-insurance-guide.md](docs/insurance/driver-insurance-guide.md) | 운전자보험 상품 가이드 (플랜별 보장, 가입 조건) |
| [discount-special-terms.md](docs/insurance/discount-special-terms.md) | 할인특약 안내 (10종 할인, 적용 예시) |

KB 업로드 방법:
```bash
# 1. KB 관리 페이지에서 파일 업로드 또는 API 사용
# 2. 동기화 트리거
curl -X POST https://d2olomx8td8txt.cloudfront.net/api/kb/sync \
  -H "Authorization: Bearer {token}"
```

## 라이선스

Private project.
