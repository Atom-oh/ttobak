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
- Whisper GPU STT on ECS Spot with zero-scale architecture (ADR-009)
- AgentCore ECR container for Deep Research agent
- Custom Vocabulary for AWS Transcribe (30 AWS/tech terms)
- STT 3-way benchmark (Transcribe vs Multi-Language vs Whisper GPU)
- Email domain allowlist for signup restriction (ADR-007)
- KB Q&A panel on recording page
- Research worker Lambda with AgentCore invocation
- Automated news/tech crawler with Step Functions pipeline
- Tab audio capture for Google Meet via getDisplayMedia
- CI/CD pipeline with GitHub Actions self-hosted runner
- Graceful zero-downtime deployment pipeline

### Changed
- STT pipeline now supports 3 providers: Whisper GPU (primary), AWS Transcribe, Nova Sonic
- Insights page extended with Research tab, polling, and delete functionality

### Fixed
- S3 deploy stale chunk serving issue (reverted no-delete strategy)
- AgentCore runtime-endpoint IAM subresource policy
- Auth checks, JSON parsing, XML safety from security review
- Crawler source reset and redundant IAM removal

### Security
- Secret scanning hook blocks credential commits
- Lambda@Edge JWT validation for all API routes
- KMS encryption for sensitive API keys

---

<a id="korean"></a>

# 한국어

## [Unreleased]

### Added
- Whisper GPU STT ECS Spot 제로 스케일 아키텍처 (ADR-009)
- Deep Research 에이전트용 AgentCore ECR 컨테이너
- AWS Transcribe Custom Vocabulary (AWS/기술 용어 30개)
- STT 3-Way 벤치마크 (Transcribe vs 다국어 vs Whisper GPU)
- 회원가입 이메일 도메인 제한 (ADR-007)
- 녹음 페이지 KB Q&A 패널
- AgentCore 호출 연구 워커 Lambda
- Step Functions 기반 자동 뉴스/기술 크롤러
- getDisplayMedia를 통한 Google Meet 탭 오디오 캡처
- GitHub Actions 셀프호스티드 러너 CI/CD 파이프라인
- 무중단 배포 파이프라인

### Changed
- STT 파이프라인 3개 프로바이더 지원: Whisper GPU (주), AWS Transcribe, Nova Sonic
- Insights 페이지 Research 탭, 폴링, 삭제 기능 확장

### Fixed
- S3 배포 시 오래된 청크 서빙 문제 수정 (no-delete 전략 롤백)
- AgentCore runtime-endpoint IAM 하위 리소스 정책 수정
- 보안 리뷰 반영: 인증 검증, JSON 파싱, XML 안전성
- 크롤러 소스 초기화 및 중복 IAM 제거

### Security
- 시크릿 스캐닝 훅으로 자격증명 커밋 차단
- Lambda@Edge JWT 검증 (모든 API 경로)
- 민감 API 키 KMS 암호화
