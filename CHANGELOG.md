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
- Editable research title, distinct from the immutable original research prompt (`PUT /api/research/{researchId}`)
- Admin user-management panel: list/delete/enable/disable/resend-invite/force-reset-password for Cognito users, with a fail-open PostAuthentication trigger recording last-login timestamps (ADR-032)
- Meeting-driven cost/sizing simulator: extracts quantitative requirements from a meeting, runs a Sonnet-generated Python computation in AgentCore Code Interpreter to compare architecture options (ADR-033)
- PendingShare queue: Account/Meeting shares to an invited-but-not-yet-logged-in email are queued instead of failing, then materialized into a real AccountMember/Share row on the invitee's first authenticated request after accepting the invite (`DELETE /api/accounts/{accountId}/members/pending`, `DELETE /api/meetings/{meetingId}/share/pending`)
- Zoom/pan and fullscreen lightbox for mermaid diagrams rendered from meeting notes
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
- Mobile mic input (waveform and live captions both) freezing indefinitely after a screen lock, with no way to recover — the automatic resume paths (`onstatechange`, watchdog `resume()` retries, visibility-triggered reconnect) all call into the Web Audio API from an event listener, not a real click, and iOS Safari's autoplay policy can refuse `resume()` forever without a fresh user gesture. Added a manual, gesture-backed recovery path: a "탭하여 재개" waveform banner (new stall watchdog + imperative `resumeAudio()` on `RecordButton`) and a "지금 다시 연결" live-captions banner (`SttManager.manualStallRecovery()`), both wired synchronously to their button clicks so they inherit the click's activation privilege. Also: the first stall now notifies the page immediately instead of only after the automatic retry also fails (previously 25-60s of silence), and the mobile-unavailable caption message no longer falsely promises an automatic recovery
- "녹음 복구" always failing, on every platform, not just iOS — the checkpoint recovery lookup was hardcoded to `.webm`, but iOS Safari's MediaRecorder only produces `.m4a`, so it's now located by prefix instead of a fixed extension. That alone still wasn't enough: the upload endpoint's filename sanitizer unconditionally prepends a `{timestamp}_` prefix to every uploaded filename, so the checkpoint's intended fixed key never actually matched the prefix search on any platform; checkpoint uploads now skip that prefixing so the fixed-key overwrite works as intended
- Mermaid diagrams, code blocks, and tables rendering as a hardcoded dark card in light mode — the markdown surface now observes `<html>`'s `.dark` class via a new `useTheme` hook (the sidebar's theme toggle moved to this hook too) and re-renders mermaid/shiki with a matching light or dark palette
- Mobile recording silently dying on screen lock (mic indicator stayed "live" but no audio captured) — Screen Wake Lock during recording, `AudioContext` resume guard + stall watchdog in Transcribe Streaming client, `pageshow`/`focus` restart triggers in Web Speech fallback
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
- 리서치 제목 편집 기능 — 원본 리서치 프롬프트(topic)와 분리된 편집 가능 제목(title) 추가 (`PUT /api/research/{researchId}`)
- 관리자 사용자 관리 패널 — Cognito 사용자 목록/삭제/활성화/비활성화/초대 재발송/비밀번호 강제 재설정, fail-open PostAuthentication 트리거로 최종 로그인 시각 기록 (ADR-032)
- 미팅 기반 비용·사이징 시뮬레이터 — 정량 요구사항 추출 후 AgentCore Code Interpreter에서 Sonnet이 생성한 파이썬으로 아키텍처 대안 비교 (ADR-033)
- PendingShare 큐 — 아직 로그인하지 않은(초대만 된) 이메일에 대한 Account/Meeting 공유를 실패시키지 않고 대기열에 넣고, 초대받은 사용자의 첫 인증 요청에서 실제 AccountMember/Share로 전환 (`DELETE /api/accounts/{accountId}/members/pending`, `DELETE /api/meetings/{meetingId}/share/pending`)
- 회의록 mermaid 다이어그램 확대·이동 및 전체화면 라이트박스
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
- 모바일에서 화면 잠금 후 마이크 입력(파형·실시간 자막 모두)이 영구적으로 멈추고 복구할 방법이 없던 문제 — 자동 복구 경로(`onstatechange`, watchdog의 `resume()` 재시도, visibility 트리거 재연결)가 전부 실제 클릭이 아닌 이벤트 리스너에서 Web Audio API를 호출하는데, iOS Safari의 오토플레이 정책상 사용자 제스처 없이는 `resume()`이 영구히 거부될 수 있음. 사용자 탭(제스처) 기반의 수동 복구 경로 추가: 파형용 "탭하여 재개" 배너(`RecordButton`에 새 stall watchdog + `resumeAudio()` 노출)와 실시간 자막용 "지금 다시 연결" 배너(`SttManager.manualStallRecovery()`) 모두 클릭 안에서 동기적으로 호출해 제스처 특권을 물려받음. 첫 stall 감지 시 즉시 페이지에 알리도록 개선(이전엔 자동 재시도가 실패해야만 표시, 25-60초 지연), 모바일 자막 불가 안내 문구의 거짓 자동 복구 약속도 제거
- "녹음 복구"가 iOS뿐 아니라 모든 플랫폼에서 항상 실패하던 문제 — 체크포인트 조회가 `.webm`으로 하드코딩돼 있었는데 iOS Safari의 MediaRecorder는 `.m4a`만 생성해 고정 확장자 대신 접두사로 찾도록 수정했으나, 그것만으로는 부족했음: 업로드 엔드포인트의 파일명 sanitizer가 모든 업로드 파일명에 `{timestamp}_` 접두사를 무조건 붙여서 체크포인트가 의도한 고정 키에 실제로 저장된 적이 없었고, 접두사 검색 자체가 어떤 플랫폼에서도 매칭될 수 없었음; 체크포인트 업로드는 이제 이 sanitizer를 건너뛰어 고정 키 덮어쓰기가 실제로 동작함
- 라이트 모드에서 mermaid 다이어그램·코드블록·표가 다크 전용 카드로 렌더되던 문제 — 마크다운 표면이 새 `useTheme` 훅으로 `<html>`의 `.dark` 클래스를 관찰해(사이드바 테마 토글도 이 훅으로 이동) mermaid/shiki를 라이트·다크에 맞는 팔레트로 재렌더
- 모바일 화면 잠금 시 녹음이 조용히 멈추는 문제 (마이크 표시는 계속 "켜짐"이지만 오디오는 캡처되지 않음) — 녹음 중 Screen Wake Lock 유지, Transcribe Streaming 클라이언트에 AudioContext resume 가드 + stall watchdog 추가, Web Speech 폴백에 `pageshow`/`focus` 재시작 트리거 추가
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
