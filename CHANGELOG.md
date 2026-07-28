# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## [Unreleased]

### Added
- Export button on Research Detail page (Copy Markdown, Download .md, Notion)
- AI Code Review workflow with Bedrock Claude Opus 4.7 on PR open/sync
- Insights tab URL sync (`/insights?tab=research`) for back/forward navigation
- Deep research uses Opus 4.7 via US CRIS profile (us-east-1)
- Whisper model loaded from S3 at runtime (~30s, image stays ~2GB)
- Custom Vocabulary terms injected as Whisper `initial_prompt`
- Whisper GPU STT on ECS Spot with zero-scale architecture (ADR-009)
- AgentCore ECR container for Deep Research agent (FastAPI pattern)
- Custom Vocabulary for AWS Transcribe (30 AWS/tech terms)
- STT 3-way benchmark (Transcribe vs Multi-Language vs Whisper GPU)
- Email domain allowlist for signup restriction (ADR-007)
- KB Q&A panel on recording page
- Cross-meeting chat assistant with tool-use
- Deep Research Agent (Bedrock Agent, then AgentCore Runtime)
- Automated news/tech crawler with Step Functions pipeline
- Tab audio capture for Google Meet via getDisplayMedia
- Graceful zero-downtime deployment pipeline

### Changed
- CI/CD split into 6 independent workflows (test-backend, deploy-infra, deploy-frontend, deploy-whisper, deploy-research-agent, pr-review)
- All CI runners on ARC (Kubernetes) with setup actions for ephemeral containers
- Deep research model: Opus 4.7 via `us-east-1`, quick/standard: Sonnet 4.6 via `ap-northeast-2`
- STT pipeline supports 3 providers: Whisper GPU (primary), AWS Transcribe, Nova Sonic
- Dockerfile base images upgraded to CUDA 12.9.1 + Python 3.13
- All GitHub Actions upgraded to v6 (Node 24 native)
- All env vars moved to GitHub repository variables
- Insights page extended with Research tab, polling, and delete functionality
- Cognito config loaded at runtime via `/config.json` (no build-time env vars)
- CloudFront SPA router fixed for `/insights/research/*` route conflict

### Fixed
- CloudFront SPA router wrongly rewriting `/insights/research/*` to `/insights/_/_`
- Bedrock ReadTimeoutError in deep research (read_timeout 120s to 300s)
- AWS CLI install on non-root ARC runners (`$HOME/.local/bin`)
- Reserved `aws/spans` log group name changed to `/ttobak/agentcore/spans`
- S3 deploy stale chunk serving (reverted no-delete strategy)
- AgentCore runtime-endpoint IAM subresource policy
- Auth checks, JSON parsing, XML safety from security review

### Security
- AI code review on every PR (Claude Opus 4.7)
- Secret scanning hook blocks credential commits
- Lambda@Edge JWT validation for all API routes
- KMS encryption for sensitive API keys
- Crawler filters paywalled URLs and requires 100+ chars body

---

<a id="korean"></a>

# 한국어

## [Unreleased]

### Added
- Research Detail 페이지 Export 버튼 (Markdown 복사, .md 다운로드, Notion)
- Bedrock Claude Opus 4.7 기반 AI 코드 리뷰 워크플로우 (PR 오픈/동기화 시)
- Insights 탭 URL 동기화 (`/insights?tab=research`) — 뒤로가기/북마크 지원
- Deep research Opus 4.7 US CRIS 프로필 사용 (us-east-1)
- Whisper 모델 S3에서 런타임 로드 (~30초, 이미지 ~2GB)
- Custom Vocabulary 용어를 Whisper `initial_prompt`로 주입
- Whisper GPU STT ECS Spot 제로 스케일 아키텍처 (ADR-009)
- Deep Research 에이전트 AgentCore ECR 컨테이너 (FastAPI 패턴)
- AWS Transcribe Custom Vocabulary (AWS/기술 용어 30개)
- STT 3-Way 벤치마크 (Transcribe vs 다국어 vs Whisper GPU)
- 회원가입 이메일 도메인 제한 (ADR-007)
- 녹음 페이지 KB Q&A 패널
- 크로스 미팅 챗 어시스턴트 (tool-use)
- Deep Research Agent (Bedrock Agent → AgentCore Runtime)
- Step Functions 기반 자동 뉴스/기술 크롤러
- getDisplayMedia를 통한 Google Meet 탭 오디오 캡처
- 무중단 배포 파이프라인

### Changed
- CI/CD 6개 독립 워크플로우로 분리 (test-backend, deploy-infra, deploy-frontend, deploy-whisper, deploy-research-agent, pr-review)
- 모든 CI 러너 ARC (Kubernetes) 기반 — ephemeral 컨테이너에 setup actions 사용
- Deep research: Opus 4.7 `us-east-1`, quick/standard: Sonnet 4.6 `ap-northeast-2`
- STT 파이프라인 3개 프로바이더 지원: Whisper GPU (주), AWS Transcribe, Nova Sonic
- Dockerfile 베이스 이미지 CUDA 12.9.1 + Python 3.13 업그레이드
- GitHub Actions 전체 v6 업그레이드 (Node 24 네이티브)
- 모든 환경변수 GitHub repository variables로 이관
- Insights 페이지 Research 탭, 폴링, 삭제 기능 확장
- Cognito 설정 런타임 로드 (`/config.json`) — 빌드 타임 환경변수 불필요
- CloudFront SPA router `/insights/research/*` 라우트 충돌 수정

### Fixed
- CloudFront SPA router `/insights/research/*`를 `/insights/_/_`로 잘못 rewrite하는 문제
- Deep research Bedrock ReadTimeoutError (read_timeout 120초 → 300초)
- Non-root ARC runner AWS CLI 설치 경로 (`$HOME/.local/bin`)
- 예약된 `aws/spans` 로그 그룹 이름 `/ttobak/agentcore/spans`로 변경
- S3 배포 시 오래된 청크 서빙 문제 (no-delete 전략 롤백)
- AgentCore runtime-endpoint IAM 하위 리소스 정책
- 보안 리뷰 반영: 인증 검증, JSON 파싱, XML 안전성

### Security
- 모든 PR에 AI 코드 리뷰 (Claude Opus 4.7)
- 시크릿 스캐닝 훅으로 자격증명 커밋 차단
- Lambda@Edge JWT 검증 (모든 API 경로)
- 민감 API 키 KMS 암호화
- 크롤러 페이월 URL 차단 및 본문 100자 이상 필수
