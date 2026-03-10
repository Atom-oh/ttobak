# Ttobak (또박) - Product Requirements Document

> AI 회의 비서 웹앱 - 녹음, 전사, 요약, 이미지 정리를 하나로

## 1. 제품 개요

### 1.1 배경
미팅이 많은 사용자를 위한 만능 회의 비서. iPhone/MacBook에서 녹음을 트리거하고, 음성을 STT로 변환, AI로 회의록을 자동 정리한다. 회의 중 촬영한 사진/캡처를 업로드하면 뭉개진 아키텍처 그림을 깔끔하게 재생성하고 도표를 정리해주는 기능을 포함한다.

### 1.2 타겟 사용자
- 하루 2~5회 이상 미팅에 참여하는 직장인
- 회의록 정리에 시간을 쓰고 싶지 않은 팀 리더
- 화이트보드/아키텍처 그림을 자주 사용하는 개발팀

### 1.3 핵심 가치
| 가치 | 설명 |
|------|------|
| 원클릭 녹음 | iPhone/MacBook 어디서든 한 번의 탭으로 녹음 시작 |
| 자동 전사 + 요약 | 녹음 종료 후 자동으로 STT → AI 요약 파이프라인 실행 |
| 이미지 정리 | 뭉개진 화이트보드/아키텍처 사진을 깔끔한 다이어그램으로 재생성 |
| 간편 공유 | 한 번의 클릭으로 팀원에게 회의록 공유 |

## 2. 기능 요구사항

### 2.1 인증 (Authentication)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| AUTH-01 | Email/Password 기반 로그인 (Cognito User Pool) | P0 | 구현 중 |
| AUTH-02 | 회원가입 (이메일 인증 포함) | P0 | 구현 중 |
| AUTH-03 | 로그인 없이는 모든 페이지/API 접근 차단 | P0 | 구현 중 |
| AUTH-04 | 비밀번호 재설정 | P1 | 미착수 |
| AUTH-05 | 세션 만료 시 자동 로그아웃 + 리다이렉트 | P1 | 미착수 |

### 2.2 녹음 (Recording)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| REC-01 | iPhone Safari: `<input capture>` 네이티브 레코더 사용 | P0 | 구현 중 |
| REC-02 | MacBook Chrome: MediaRecorder API 브라우저 내 녹음 | P0 | 구현 중 |
| REC-03 | 녹음 중 타이머 표시 (mm:ss 형식) | P0 | 구현 중 |
| REC-04 | 일시정지/재개 기능 | P1 | 미착수 |
| REC-05 | 녹음 중 사진 캡처 (카메라 버튼) | P0 | 구현 중 |
| REC-06 | 녹음 완료 시 자동 S3 업로드 (presigned URL) | P0 | 구현 중 |
| REC-07 | 오디오 포맷: WebM (Chrome), M4A (Safari) | P0 | 구현 중 |
| REC-08 | 녹음 중 웨이브폼 시각화 | P1 | 구현 중 |
| REC-09 | 오프라인/온라인 모드 토글 체크박스 | P0 | 미착수 |
| REC-10 | 온라인 모드 — Nova Sonic v2 WebSocket 실시간 스트리밍 | P0 | 미착수 |
| REC-11 | 온라인 모드 — 실시간 전사 결과 화면 표시 | P0 | 미착수 |
| REC-12 | 번역 언어 선택 체크박스 (한→영, 영→한, 일→한 등) | P1 | 미착수 |

### 2.3 STT (Speech-to-Text)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| STT-01 | S3 오디오 업로드 → 자동 STT 파이프라인 트리거 | P0 | 구현 중 |
| STT-02 | Amazon Transcribe 사용 (A 결과, Primary) | P0 | 구현 중 |
| STT-03 | Amazon Nova 2 Sonic 사용 (B 결과) - A/B 비교 목적 | P1 | 미착수 |
| STT-04 | 두 STT 결과를 나란히 비교하는 UI | P1 | 구현 중 |
| STT-05 | 사용자가 A 또는 B 선택 가능 | P1 | 구현 중 |
| STT-06 | 한국어 + 영어 지원 (Transcribe: 한/영, Nova 2 Sonic: 한/영 A/B) | P0 | 미착수 |
| STT-07 | 언어 무관 A/B 비교 실행 (모든 회의에 Transcribe + Nova 2 Sonic 병렬 수행) | P0 | 미착수 |

### 2.4 AI 요약 (Summarization)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| SUM-01 | STT 완료 후 자동으로 Bedrock Claude Opus 4.6 호출 | P0 | 구현 중 |
| SUM-02 | 구조화된 회의록 생성: 참석자, 안건, 결정사항, 액션아이템 | P0 | 구현 중 |
| SUM-03 | 마크다운 형식으로 저장 | P0 | 구현 중 |
| SUM-04 | 요약 결과를 에디터에서 수정 가능 | P0 | 구현 중 |

### 2.5 이미지 처리 (Image Processing)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| IMG-01 | 다건 이미지 드래그앤드롭 업로드 | P0 | 구현 중 |
| IMG-02 | Bedrock Claude Vision으로 이미지 분류 (아키텍처/표/화이트보드/일반) | P0 | 구현 중 |
| IMG-03 | 아키텍처 다이어그램 → Mermaid 코드 재생성 | P0 | 구현 중 |
| IMG-04 | 표/도표 → 마크다운 테이블 변환 | P0 | 구현 중 |
| IMG-05 | 화이트보드 → 텍스트 추출 + 구조화 | P1 | 구현 중 |
| IMG-06 | 원본 vs AI 처리 결과 비교 뷰 | P0 | 구현 중 |

### 2.6 에디터 (Meeting Editor)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| EDT-01 | Tiptap 기반 블록 에디터 | P0 | 구현 중 |
| EDT-02 | 마크다운 지원 (헤딩, 리스트, 볼드, 코드블록 등) | P0 | 구현 중 |
| EDT-03 | 이미지 인라인 삽입 | P0 | 구현 중 |
| EDT-04 | 자동 저장 (3초 디바운스) | P0 | 구현 중 |
| EDT-05 | 체크리스트 (액션아이템용) | P1 | 미착수 |

### 2.7 공유 (Sharing)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| SHR-01 | 기본적으로 본인만 접근 가능 | P0 | 구현 중 |
| SHR-02 | 사용자 이메일 검색으로 공유 대상 찾기 | P0 | 구현 중 |
| SHR-03 | read / edit 권한 분리 | P0 | 구현 중 |
| SHR-04 | 공유 취소 가능 | P0 | 구현 중 |
| SHR-05 | 공유받은 회의 목록에 표시 (Shared 탭) | P0 | 구현 중 |

### 2.8 회의 목록 (Meeting List)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| LST-01 | 본인 회의 + 공유받은 회의 통합 목록 | P0 | 구현 중 |
| LST-02 | 날짜순 정렬 (최신 우선) | P0 | 구현 중 |
| LST-03 | 검색 (제목, 내용) | P1 | 구현 중 |
| LST-04 | 카테고리 탭 (All / Recent / Shared) | P1 | 구현 중 |
| LST-05 | 회의 상태 표시 (recording/transcribing/summarizing/done) | P0 | 구현 중 |

### 2.9 실시간 번역 (Real-time Translation)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| TRN-01 | Nova Sonic 전사를 Bedrock Claude로 실시간 번역 | P0 | 미착수 |
| TRN-02 | 번역 대상 언어 선택 UI | P0 | 미착수 |
| TRN-03 | 원문 + 번역문 동시 표시 | P0 | 미착수 |

### 2.10 미팅 Q&A (Meeting Q&A)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| QNA-01 | 미팅 중 질문 입력 패널 | P0 | 미착수 |
| QNA-02 | Bedrock KB RAG 검색 답변 | P0 | 미착수 |
| QNA-03 | Q&A 히스토리, 소스 표시 | P0 | 미착수 |

### 2.11 Knowledge Base

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| KB-01 | 사용자별 글로벌 KB (md/pdf/ppt) | P0 | 미착수 |
| KB-02 | 자동 인덱싱, 파일 목록 관리 UI | P0 | 미착수 |

### 2.12 내보내기 + 외부 연동 (Export & Integration)

| ID | 요구사항 | 우선순위 | 상태 |
|----|----------|----------|------|
| EXP-01 | 회의 상세 Export 버튼 (PDF, Markdown, Notion, Obsidian) | P0 | 미착수 |
| EXP-02 | PDF 내보내기 — 서버에서 생성 | P1 | 미착수 |
| EXP-03 | Markdown 내보내기 — content 필드 다운로드 | P0 | 미착수 |
| EXP-04 | Notion 내보내기 — Notion API로 페이지 생성 | P1 | 미착수 |
| EXP-05 | Obsidian 내보내기 — YAML frontmatter + [[wikilinks]] 형식 .md 다운로드 | P0 | 미착수 |
| EXP-06 | API 키 없으면 Settings로 이동하여 입력 유도 | P0 | 미착수 |
| INT-01 | Settings 페이지에 외부 연동(Integrations) 섹션 | P0 | 미착수 |
| INT-02 | Notion API Key 입력/수정/삭제 UI | P0 | 미착수 |
| INT-03 | API 키는 DynamoDB에 암호화 저장 | P0 | 미착수 |

## 3. 비기능 요구사항

### 3.1 보안

| ID | 요구사항 | 우선순위 |
|----|----------|----------|
| SEC-01 | CloudFront만 퍼블릭 노출, Lambda/API Gateway 직접 접근 불가 | P0 |
| SEC-02 | Lambda@Edge (Viewer Request)에서 Cognito JWT 검증 | P0 |
| SEC-03 | API Gateway는 Lambda@Edge 통과 요청만 수신 (IAM 인증) | P0 |
| SEC-04 | WebSocket API Gateway: Cognito Authorizer로 연결 시 인증 | P0 |
| SEC-05 | S3 presigned URL은 인증된 사용자만 발급 가능 | P0 |
| SEC-06 | DynamoDB 모든 쿼리에 userId 필터 (소유권 확인) | P0 |
| SEC-07 | S3 OAC로 S3 직접 접근 차단 | P0 |
| SEC-08 | 외부 API 키 (Notion 등)는 DynamoDB에 암호화(KMS) 저장 | P0 |

### 3.2 성능

| ID | 요구사항 | 목표값 |
|----|----------|--------|
| PERF-01 | 회의 목록 로딩 | < 2초 |
| PERF-02 | 에디터 자동저장 | 3초 디바운스 후 < 1초 |
| PERF-03 | 이미지 업로드 (10MB) | < 5초 |
| PERF-04 | STT 처리 (30분 오디오) | < 5분 |
| PERF-05 | AI 요약 생성 | < 30초 |

### 3.3 확장성

| ID | 요구사항 |
|----|----------|
| SCAL-01 | 멀티유저 지원 (사용자 수 증가 대응) |
| SCAL-02 | DynamoDB 온디맨드 용량으로 자동 스케일링 |
| SCAL-03 | Lambda 동시 실행으로 요청 처리 스케일링 |
| SCAL-04 | S3 무제한 스토리지 |

## 4. UI/UX 디자인 사양

### 4.1 디자인 토큰

```
Primary Color: #3211d4 (Deep Indigo)
Background Light: #f6f6f8
Background Dark: #131022
Font: Inter (Google Fonts)
Icons: Material Symbols Outlined (Google Fonts)
Border Radius: rounded-xl (0.75rem) for cards
Shadows: shadow-sm for cards, shadow-lg for FAB
```

### 4.2 반응형 브레이크포인트

| 뷰포트 | 레이아웃 |
|---------|----------|
| Mobile (< 768px) | 하단 네비게이션, 단일 컬럼, max-w-md mx-auto |
| PC (>= 1024px) | 좌측 사이드바 w-64, 메인 콘텐츠 영역, 선택적 우측 패널 |

### 4.3 주요 화면

1. **로그인/회원가입** - 심플한 폼, primary 컬러 CTA
2. **회의 목록** - 카드 그리드 (PC: 3열, Mobile: 1열), 검색바, 탭 네비게이션
3. **녹음 화면** - 중앙 타이머, 웨이브폼, 컨트롤 버튼 (일시정지/중지/카메라)
4. **회의 상세** - AI Summary 박스, 액션아이템 체크리스트, 첨부파일 갤러리, 전사본
5. **공유 다이얼로그** - 사용자 검색, 권한 선택 드롭다운

### 4.4 디자인 레퍼런스
- `design_sample/recording.html` - 모바일 녹음 화면
- `design_sample/recording-pc.html` - PC 녹음 화면
- `design_sample/meeting-list.html` - 모바일 회의 목록
- `design_sample/meeting-list-pc.html` - PC 회의 목록
- `design_sample/meeting-note.html` - 모바일 회의 상세
- `design_sample/meeting-note-pc.html` - PC 회의 상세

## 5. 기술 아키텍처

### 5.1 스택

| 영역 | 기술 |
|------|------|
| Frontend | Next.js 14+ (App Router), Tailwind CSS, Tiptap Editor |
| Backend | Go (Lambda), Chi Router |
| Auth | Amazon Cognito User Pool |
| IaC | AWS CDK (TypeScript) |
| DB | Amazon DynamoDB (Single Table Design) |
| Storage | Amazon S3 |
| STT | Amazon Transcribe (Primary) + Amazon Nova 2 Sonic (A/B) |
| AI 요약 | Amazon Bedrock - Claude Opus 4.6 |
| AI 이미지 | Amazon Bedrock - Claude Opus 4.6 (Vision) |

### 5.2 인프라 아키텍처

```
CloudFront
  ├→ Lambda@Edge (Viewer Request) — JWT 검증
  ├→ S3 (정적 프론트엔드, OAC)
  ├→ API Gateway HTTP API → Lambda (REST API)
  └→ API Gateway WebSocket API → Lambda (Nova Sonic 실시간)

[사용자] → [CloudFront] ─┬─→ S3 (프론트엔드, OAC)
                          ├─→ Lambda@Edge: Cognito JWT 검증
                          ├─→ API Gateway HTTP API
                          │      └─→ Lambda (REST API) → DynamoDB / S3 / Bedrock
                          └─→ API Gateway WebSocket API
                                 └─→ Lambda (Realtime) → Nova Sonic / Bedrock Translation
```

### 5.3 데이터 플로우

```
[녹음] → S3 upload → EventBridge → Lambda(transcribe)
                                      ├── Transcribe (결과 A, 한/영)
                                      └── Nova 2 Sonic (결과 B, 한/영)
                                             ↓
                                    Lambda(summarize) → Bedrock Claude
                                             ↓
                                    DynamoDB (회의록 저장)

[이미지] → S3 upload → Lambda(process-image) → Bedrock Vision
                                                    ↓
                                              분류 + 처리 결과 저장
```

### 5.4 DynamoDB 스키마

**Meetings**
```
PK: USER#{userId}     SK: MEETING#{meetingId}
GSI1-PK: USER#{userId}  GSI1-SK: {date}
```

**Attachments**
```
PK: MEETING#{meetingId}  SK: ATTACH#{attachmentId}
```

**Sharing**
```
PK: USER#{sharedToUserId}  SK: SHARED#{meetingId}
```

## 6. 변경 이력

| 날짜 | 변경 내용 | 작성자 |
|------|-----------|--------|
| 2026-03-05 | 초기 PRD 작성 | Junseok Oh |
| 2026-03-05 | DESIGN-SPEC.md 작성 (디자인 토큰, 컴포넌트 상세) | Junseok Oh |
| 2026-03-05 | API-SPEC.md 작성 (REST API 명세, Lambda 설계) | Junseok Oh |
| 2026-03-05 | INFRA-SPEC.md 작성 (CDK 스택 상세 설계) | Junseok Oh |
| 2026-03-05 | Infra Review: Lambda VPC 제거, S3 트리거/DDB Stream 추가 결정 | Junseok Oh |
| 2026-03-05 | Backend Review: env var 이름, error format, status 값, pagination 등 8개 항목 수정 요청 | Junseok Oh |
| 2026-03-05 | Schema Update: GSI2 추가 (EMAIL# prefix, 사용자 이메일 검색용) | Junseok Oh |
| 2026-03-05 | Schema Update: User 엔티티 추가 (PK=USER#{id}, SK=PROFILE) - 공유 기능용 | Junseok Oh |
| 2026-03-05 | STT 전략 변경: Nova 2 Sonic A/B 비교를 언어 무관 적용 (한국어 스킵 로직 제거) | Junseok Oh |
