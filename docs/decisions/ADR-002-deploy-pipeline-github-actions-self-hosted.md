# ADR-002: GitHub Actions Deployment on Self-Hosted Runners

- Status: Accepted; current workflow layout supersedes the original sketches.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Context and decision

Manual builds, uploads, and cache invalidations made deployment inconsistent.
Use version-controlled GitHub Actions workflows on existing self-hosted runners,
with AWS access supplied by the runner environment. This reuses available
capacity and supports ARM64 Lambda builds without introducing CodePipeline.

## Current implementation

| Workflow | Responsibility |
| --- | --- |
| `deploy-infra.yml` | On `ttobak-arm`, gate deployment on CDK synth and Jest tests, build Go artifacts, deploy stacks |
| `deploy-frontend.yml` | On `ttobak-x86`, build the static SPA, sync S3, invalidate CloudFront |
| `deploy-whisper.yml` | Build the production GPU image and register an ECS task revision |
| `deploy-research-agent.yml` | Deploy the separate AgentCore research container |

These workflows have independent main-branch path filters and manual dispatch.
There is no current unified `deploy.yml` or manual `all/backend/frontend/infra`
selector. Separate PR workflows cover backend, infra, MCP, and AI review.

Deployment must name each stack with `--exclusively`, in dependency order.
Never use `cdk deploy --all` or allow implicit dependency deployment:
`TtobakKnowledgeStack` contains a deliberately staged, undeployed KB teardown.
The infra workflow omits it and re-deploys Storage after Frontend to tighten
the OAC policy. Preserve the Storage custom resource's changing `Timestamp`;
it forces that refresh even when ordinary resources have no diff.

Frontend sync preserves `/config.json` with `--exclude "config.json"` because CDK
owns runtime Cognito configuration. Go zip artifacts target Linux ARM64;
`convert-doc` is a separate container build. The infra workflow explicitly builds
six Go entry points; it does not build the implemented `websocket` and
`ws-authorizer` entry points. That artifact-loop discrepancy remains visible;
it does not mean WebSockets are unimplemented or establish live deployment state.

## Alternatives, consequences, and risks

GitHub-hosted runners reduce machine maintenance; CodeBuild/CodePipeline provide
AWS-managed execution. Existing runners were preferred for control and reuse,
not a guarantee of zero total cost.

Runner availability, patching, credentials, and trusted workflow execution remain
operational responsibilities. A private repository does not eliminate the risk
of executing modified workflow code with deployment privileges. Mac app builds
remain local and are not covered by these workflows. Source inspection establishes
workflow configuration, not successful execution or deployed artifact freshness.

## Evidence

- [.github/workflows](../../.github/workflows): triggers, runners, gates, builds,
  and deployment commands.
- [infra.ts](../../infra/bin/infra.ts): stack dependency graph.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts): HTTP and WebSocket resources.
- [storage-stack.ts](../../infra/lib/storage-stack.ts): OAC refresh and retention.
- [frontend-stack.ts](../../infra/lib/frontend-stack.ts): runtime configuration.
