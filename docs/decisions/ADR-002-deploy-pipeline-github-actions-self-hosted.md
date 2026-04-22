# ADR-002: GitHub Actions + Self-Hosted Runner를 통한 배포 파이프라인

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

Ttobak currently has no CI/CD pipeline. All deployments are performed manually from a developer's local machine:

1. **Backend**: Build 5 Go Lambda binaries (`GOOS=linux GOARCH=arm64`), then `cdk deploy`
2. **Frontend**: `npm run build` to generate static export, then `aws s3 sync` + CloudFront invalidation
3. **Infrastructure**: `cdk synth` + `cdk deploy --all` for 7 CDK stacks

This manual process is error-prone: forgetting to build a binary, deploying from a dirty working tree, or skipping CloudFront invalidation are common mistakes. The project needs an automated pipeline that:

- Builds and deploys on push to `main`
- Supports ARM64 Lambda builds (the project uses `GOARCH=arm64` for Graviton)
- Has access to AWS credentials for S3, CloudFront, Lambda, and CDK operations
- Keeps costs low (the project is a single-developer side project)

### Current Deployment Commands

```bash
# Backend (5 Lambda functions)
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done

# CDK deploy
cd infra && npx cdk deploy --all

# Frontend
cd frontend && npm run build
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"

# Python Lambda (Q&A)
cd backend/python/qa && zip -r ../../../qa-lambda.zip . && aws lambda update-function-code ...
```

### Self-Hosted Runner Environment

Two EC2 instances are available as GitHub Actions self-hosted runners:

| Runner Label | Architecture | Use Case |
|---|---|---|
| `ttobak-x86` | x86_64 | Frontend build (Node.js), CDK synth/deploy |
| `ttobak-arm` | ARM64 (Graviton) | Go Lambda cross-compilation, native ARM builds |

Both runners have: Go 1.22+, Node.js 22+, AWS CLI v2, CDK CLI, Python 3.12, Docker. AWS credentials are pre-configured via instance profiles.

## Options Considered

### Option 1: GitHub Actions with Self-Hosted Runners

Use GitHub Actions workflows with `runs-on: ttobak-x86` or `runs-on: ttobak-arm` labels to target the existing EC2 runner instances.

- **Pros**:
  - Zero cost for CI/CD minutes (runners already provisioned as EC2 instances)
  - AWS credentials available via instance profile (no GitHub secrets rotation needed for IAM)
  - Native ARM64 builds on the `ttobak-arm` runner (no cross-compilation emulation)
  - Familiar workflow syntax; can split jobs by architecture
  - Full control over runner environment (pre-installed tools, caches)
  - Workflow files live in the repo (`.github/workflows/`) — versioned with code
- **Cons**:
  - Runner maintenance burden (OS patches, tool upgrades)
  - Runners must be online for CI to work (no auto-scaling)
  - Security: self-hosted runners on public repos can execute arbitrary code from PRs (mitigated: repo is private)

### Option 2: GitHub Actions with GitHub-Hosted Runners

Use standard `runs-on: ubuntu-latest` GitHub-hosted runners.

- **Pros**:
  - Zero maintenance — runners are ephemeral and managed by GitHub
  - Auto-scaling — no capacity concerns
  - Built-in caching actions for Go modules and npm
- **Cons**:
  - Cost: GitHub Free tier gives 2,000 minutes/month; `ubuntu-latest` is x86_64 only
  - ARM64 Go builds require `GOOS=linux GOARCH=arm64` cross-compilation (works but slower)
  - AWS credentials must be stored as GitHub Secrets and rotated manually, or use OIDC federation (additional setup)
  - No pre-installed project-specific tools (CDK, Go, Python must be installed each run)
  - Slower cold starts vs warm self-hosted runners

### Option 3: AWS CodePipeline + CodeBuild

Use AWS-native CI/CD with CodePipeline for orchestration and CodeBuild for build/deploy.

- **Pros**:
  - Native AWS integration (IAM roles, no credential management)
  - CodeBuild supports ARM64 build environments (`BUILD_GENERAL1_SMALL` with ARM image)
  - Can trigger on S3 events or CodeCommit (but also supports GitHub via CodeStar connection)
  - Pay-per-build pricing
- **Cons**:
  - Additional AWS service cost and complexity
  - Pipeline definition in CloudFormation/CDK, not in the repo (harder to review)
  - CodeBuild build spec syntax is less expressive than GitHub Actions
  - GitHub PR integration (status checks, comments) requires additional setup
  - Two systems to maintain (GitHub for code, AWS for CI/CD)

## Decision

**Choose Option 1: GitHub Actions with Self-Hosted Runners.**

Rationale:
- The EC2 runner instances (`ttobak-x86`, `ttobak-arm`) already exist and have all required tools pre-installed
- Zero incremental cost — the runners are already paid for as EC2 instances
- Native ARM64 builds on `ttobak-arm` avoid cross-compilation overhead for Go Lambda binaries
- AWS credentials are available via instance profiles — no secret rotation needed
- Workflow files in `.github/workflows/` are versioned alongside the code they deploy
- The project is a private repo with a single developer, so self-hosted runner security concerns (arbitrary PR execution) do not apply

### Workflow Design

Three separate workflows:

1. **`deploy-backend.yml`** — Triggers on push to `main` when `backend/**` changes
   - `runs-on: ttobak-arm` (native ARM64 Go builds)
   - Build all 5 Go Lambda binaries
   - Package Python Q&A Lambda
   - `cdk deploy` backend-related stacks

2. **`deploy-frontend.yml`** — Triggers on push to `main` when `frontend/**` changes
   - `runs-on: ttobak-x86` (Node.js build)
   - `npm ci && npm run build`
   - `aws s3 sync out/ s3://ttobak-site-... --delete`
   - `aws cloudfront create-invalidation`

3. **`deploy-infra.yml`** — Triggers on push to `main` when `infra/**` changes
   - `runs-on: ttobak-x86` (CDK synth/deploy)
   - `npx cdk diff` (for review)
   - `npx cdk deploy --all --require-approval never`

All workflows also support `workflow_dispatch` for manual triggers.

## Post-Implementation Updates

The actual implementation diverged from the original design in the following ways:

1. **Single unified workflow**: Instead of 3 separate workflow files, a single `deploy.yml` handles all deployment targets. A `detect-changes` job uses path-based change detection to determine which components (backend, frontend, infra) need deployment. This reduces workflow duplication and simplifies maintenance.
2. **Single runner label**: Uses `self-hosted` label instead of architecture-specific `ttobak-x86`/`ttobak-arm` labels. Go ARM64 cross-compilation (`GOOS=linux GOARCH=arm64`) works reliably from x86 runners, making a dedicated ARM runner unnecessary.
3. **`workflow_dispatch` with target selection**: Manual triggers support selecting `all`, `backend`, `frontend`, or `infra` as the deploy target, enabling selective manual deployments.
4. **CDK stacks expanded**: The project now has 9 stacks (originally 7): added `CrawlerStack` and `ResearchAgentStack`.

## Consequences

### Positive
- Eliminates manual deployment errors (forgotten builds, dirty working trees)
- Automated CloudFront invalidation on every frontend deploy
- Push-to-deploy on `main` — merge a PR and deployment happens automatically
- Workflow files are code-reviewed alongside application code
- Single workflow is simpler to maintain than three separate ones
- `workflow_dispatch` enables selective manual deployments when needed

### Negative
- Runner instance must remain online; if the instance is stopped, CI breaks
- No auto-scaling — concurrent pushes queue on a single runner
- Runner maintenance (OS updates, Go/Node upgrades) is the developer's responsibility
- If the project goes public in the future, self-hosted runners need additional hardening (e.g., restrict to `main` branch only, disable on PR events from forks)

## Implementation Notes

### Affected Files
- `.github/workflows/deploy-backend.yml` — New
- `.github/workflows/deploy-frontend.yml` — New
- `.github/workflows/deploy-infra.yml` — New

### Runner Labels
- `ttobak-x86` — For Node.js, CDK, and general tasks
- `ttobak-arm` — For Go ARM64 Lambda builds

### AWS Credentials
Instance profiles on the runner EC2 instances provide credentials. No `aws-actions/configure-aws-credentials` step needed unless assuming a different role.

### Path Filters
Each workflow uses `paths:` filters to avoid unnecessary builds:
```yaml
on:
  push:
    branches: [main]
    paths: ['backend/**']  # or 'frontend/**' or 'infra/**'
```

### Future Considerations
- Add a `ci.yml` workflow for PR checks (lint, type-check, `go vet`) that runs on every PR
- Add Slack/Discord notification on deploy success/failure
- Consider GitHub Environments with protection rules if staging/production split is needed

## References
- [GitHub Actions: Self-hosted runners](https://docs.github.com/en/actions/hosting-your-own-runners)
- [GitHub Actions: Workflow syntax — `runs-on`](https://docs.github.com/en/actions/using-workflows/workflow-syntax-for-github-actions#jobsjob_idruns-on)
- Current deploy commands: `CLAUDE.md` Build Commands section
- CDK stacks: `infra/lib/` (7 stacks, dependency order in CLAUDE.md)

---

<a id="korean"></a>

# 한국어

## 상태
수락됨

## 배경

Ttobak에는 현재 CI/CD 파이프라인이 없습니다. 모든 배포가 개발자의 로컬 머신에서 수동으로 수행됩니다:

1. **백엔드**: 5개 Go Lambda 바이너리 빌드 (`GOOS=linux GOARCH=arm64`), 이후 `cdk deploy`
2. **프론트엔드**: `npm run build`로 정적 내보내기 생성, 이후 `aws s3 sync` + CloudFront 무효화
3. **인프라**: 7개 CDK 스택에 대해 `cdk synth` + `cdk deploy --all`

이 수동 프로세스는 오류가 발생하기 쉽습니다: 바이너리 빌드 누락, 더티 워킹 트리에서 배포, CloudFront 무효화 건너뛰기 등이 흔한 실수입니다.

### Self-Hosted Runner 환경

두 개의 EC2 인스턴스가 GitHub Actions self-hosted runner로 사용 가능합니다:

| Runner 레이블 | 아키텍처 | 용도 |
|---|---|---|
| `ttobak-x86` | x86_64 | 프론트엔드 빌드 (Node.js), CDK synth/deploy |
| `ttobak-arm` | ARM64 (Graviton) | Go Lambda 크로스 컴파일, 네이티브 ARM 빌드 |

양쪽 러너 모두: Go 1.22+, Node.js 22+, AWS CLI v2, CDK CLI, Python 3.12, Docker가 설치되어 있으며, 인스턴스 프로파일을 통해 AWS 자격 증명이 사전 구성되어 있습니다.

## 검토한 옵션

### 옵션 1: GitHub Actions + Self-Hosted Runners

`runs-on: ttobak-x86` 또는 `runs-on: ttobak-arm` 레이블로 기존 EC2 러너 인스턴스를 대상으로 하는 GitHub Actions 워크플로우를 사용합니다.

- **장점**:
  - CI/CD 분 수에 대한 비용 없음 (러너가 이미 EC2 인스턴스로 프로비저닝됨)
  - 인스턴스 프로파일을 통한 AWS 자격 증명 사용 가능 (GitHub Secrets 로테이션 불필요)
  - `ttobak-arm` 러너에서 네이티브 ARM64 빌드 (크로스 컴파일 에뮬레이션 불필요)
  - 익숙한 워크플로우 문법; 아키텍처별로 잡 분리 가능
  - 러너 환경 완전 제어 (사전 설치된 도구, 캐시)
- **단점**:
  - 러너 유지보수 부담 (OS 패치, 도구 업그레이드)
  - CI가 동작하려면 러너가 온라인이어야 함 (자동 스케일링 없음)

### 옵션 2: GitHub Actions + GitHub-Hosted Runners

표준 `runs-on: ubuntu-latest` GitHub 호스팅 러너를 사용합니다.

- **장점**:
  - 유지보수 불필요 — 러너가 일회성이며 GitHub에서 관리
  - 자동 스케일링 — 용량 걱정 없음
- **단점**:
  - 비용: GitHub Free 티어는 월 2,000분 제공; x86_64만 지원
  - ARM64 Go 빌드에 크로스 컴파일 필요 (동작하지만 느림)
  - AWS 자격 증명을 GitHub Secrets에 저장하고 수동 로테이션 필요

### 옵션 3: AWS CodePipeline + CodeBuild

AWS 네이티브 CI/CD를 사용합니다.

- **장점**:
  - 네이티브 AWS 통합 (IAM 역할, 자격 증명 관리 불필요)
  - CodeBuild가 ARM64 빌드 환경 지원
- **단점**:
  - 추가 AWS 서비스 비용 및 복잡성
  - 파이프라인 정의가 CloudFormation/CDK에 있어 코드 리뷰가 어려움
  - GitHub PR 통합에 추가 설정 필요

## 결정

**옵션 1: GitHub Actions + Self-Hosted Runners를 채택합니다.**

근거:
- EC2 러너 인스턴스(`ttobak-x86`, `ttobak-arm`)가 이미 존재하며 필요한 모든 도구가 사전 설치됨
- 추가 비용 없음 — 러너가 이미 EC2 인스턴스로 비용 지불 중
- `ttobak-arm`에서 네이티브 ARM64 빌드로 Go Lambda 바이너리의 크로스 컴파일 오버헤드 제거
- 인스턴스 프로파일을 통한 AWS 자격 증명 — 시크릿 로테이션 불필요
- 프라이빗 레포이므로 self-hosted runner 보안 우려 해당 없음

### 워크플로우 설계

3개의 별도 워크플로우:

1. **`deploy-backend.yml`** — `main` 푸시 시 `backend/**` 변경 감지
   - `runs-on: ttobak-arm` (네이티브 ARM64 Go 빌드)
   - 5개 Go Lambda 바이너리 빌드 + Python Q&A Lambda 패키징
   - `cdk deploy` 백엔드 관련 스택

2. **`deploy-frontend.yml`** — `main` 푸시 시 `frontend/**` 변경 감지
   - `runs-on: ttobak-x86` (Node.js 빌드)
   - `npm ci && npm run build`
   - `aws s3 sync` + CloudFront 무효화

3. **`deploy-infra.yml`** — `main` 푸시 시 `infra/**` 변경 감지
   - `runs-on: ttobak-x86` (CDK synth/deploy)
   - `npx cdk deploy --all --require-approval never`

모든 워크플로우는 수동 트리거를 위한 `workflow_dispatch`도 지원합니다.

## 구현 후 업데이트

실제 구현은 원래 설계에서 다음과 같이 변경되었습니다:

1. **단일 통합 워크플로우**: 3개 별도 워크플로우 파일 대신 하나의 `deploy.yml`이 모든 배포 대상을 처리합니다. `detect-changes` 잡이 경로 기반 변경 감지를 사용하여 어떤 컴포넌트(backend, frontend, infra)를 배포해야 하는지 결정합니다.
2. **단일 러너 레이블**: 아키텍처별 `ttobak-x86`/`ttobak-arm` 레이블 대신 `self-hosted`를 사용합니다. Go ARM64 크로스 컴파일(`GOOS=linux GOARCH=arm64`)이 x86 러너에서 안정적으로 동작하여 전용 ARM 러너가 불필요합니다.
3. **`workflow_dispatch` 대상 선택**: 수동 트리거가 `all`, `backend`, `frontend`, `infra`를 배포 대상으로 선택할 수 있어 선택적 수동 배포가 가능합니다.
4. **CDK 스택 확장**: 프로젝트가 현재 9개 스택으로 확장되었습니다 (원래 7개): `CrawlerStack`과 `ResearchAgentStack`이 추가되었습니다.

## 영향

### 긍정적
- 수동 배포 오류 제거 (빌드 누락, 더티 워킹 트리)
- 프론트엔드 배포마다 자동 CloudFront 무효화
- `main`에 push-to-deploy — PR 병합 시 자동 배포
- 워크플로우 파일이 코드와 함께 리뷰됨
- 단일 워크플로우가 3개 별도 파일보다 유지보수가 간단
- `workflow_dispatch`로 필요 시 선택적 수동 배포 가능

### 부정적
- 러너 인스턴스가 온라인 상태여야 함; 인스턴스 중지 시 CI 중단
- 자동 스케일링 없음 — 동시 푸시가 단일 러너에서 대기열
- 러너 유지보수 (OS 업데이트, Go/Node 업그레이드)가 개발자 책임
- 프로젝트가 퍼블릭으로 전환되면 self-hosted runner 추가 보안 강화 필요

## 참고 자료
- `.github/workflows/deploy.yml` — 통합 배포 워크플로우 (원래 3개에서 1개로 통합)
- [GitHub Actions: Self-hosted runners](https://docs.github.com/en/actions/hosting-your-own-runners)
- 현재 배포 명령어: `CLAUDE.md` Build Commands 섹션
- CDK 스택: `infra/lib/` (9개 스택, 의존성 순서는 CLAUDE.md 참조)
