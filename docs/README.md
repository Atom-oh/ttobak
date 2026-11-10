# Documentation map

Repository documentation and AI review output are English-only. Read the smallest relevant current
reference; do not preload the historical archive into a PR review.

Verbatim generated note-quality outputs are archived evaluation data. Keep their
original bytes and hashes; only explanatory prose is translated. The documentation
check pins those four named artifacts by SHA-256, while checking all authored
Markdown for English language and historical/current status.

## Current references

Billing team access: [ADR-060](decisions/ADR-060-billing-shared-team-access.md) supersedes the direct-membership-only Billing policy. [Billing release](runbooks/billing-release.md) covers the BFF, static pages and separate companion worker image.

| Reference | Purpose | Verify against |
|---|---|---|
| [Project guide](../CLAUDE.md) | Canonical constraints and accepted limits | Code, tests, manifests, CDK |
| [Review extract](../AGENTS.md) | Generated common context for reviewers | `scripts/docs/sync_review_context.py` |
| [Architecture](architecture.md) | Components, data flows, ownership | `infra/bin/infra.ts`, module entry points |
| [API](API-SPEC.md) | Routes and contracts | `backend/cmd/api/main.go`, handlers/models, Python QA |
| [Infrastructure](INFRA-SPEC.md) | Stack roles and safe deployment | `infra/lib/`, `.github/workflows/` |
| [UI](DESIGN-SPEC.md) | Current components and interaction rules | `frontend/src/` |
| [Product](PRD.md) | Implemented product scope and remaining work | API/frontend and accepted decisions |
| [Onboarding](onboarding.md) | Local setup | Dependency manifests and project guide |
| [Connector inventory](../contracts/connector/v1/README.md) | Current operation classification; eligibility is not availability | API routes, assembled QA tool registry and coverage tests |
| [MCP adapter](../mcp-server/README.md) | Stdio/HTTP transports, OAuth, local recording and Mac app control | `mcp-server/src/`, `mcp-server/test/` |
| [Local Outlook sync](runbooks/local-outlook-calendar-sync.md) | Local reader prerequisites, explicit lifecycle/checkpoints, user-local scheduling and acceptance limits | Calendar handler/service/model, `mcp-server/src/calendar.ts`, guide and protocol tests |
| [Local MCP intake](runbooks/local-mcp-intake.md) | `ttobak-mcp intake` calendar runner over a local `aws-outlook-mcp`, meeting SpecReq task step, acceptance and LaunchAgent template | `mcp-server/src/intake/`, ADR-065, `test/intake.test.mjs`, `test/intake-tasks.test.mjs` |
| [Mac app](../mac-app/README.md) | Core Audio capture, permissions, MCP control socket, meeting detection prompts, caption overlay windows | `mac-app/src-tauri/`, ADR-046, ADR-061, ADR-069 |
| [PR review runbook](runbooks/pr-review.md) | Context delivery, evidence, latest-HEAD merge gate | Review scripts and workflow |
| [Specialist review](pr-review-specialists.md) | Current role assignment, coverage and synthesis contract | Trusted workflow and review scripts |
| [Deployment runbook](runbooks/deployment.md) | Build/deploy/rollback constraints | Deployment workflows |
| [Descendant Account sharing](runbooks/account-descendant-sharing.md) | Read-only inheritance, conditional relation bootstrap, activation and rollback | ADR-058, API/QA feature parameter |
| [Model deployment](runbooks/model-deployment.md) | Environment/preset selection, exact model grants, acceptance and rollback | Model configuration, AI/Gateway stacks and infrastructure workflow |
| [Observability](runbooks/observability.md) | OTel collector, ClickHouse PrivateLink, credentials and activation | ObservabilityStack and AWS-Demo-Platform ClickHouse |
| [Frontend release](runbooks/frontend-release.md) | Dependency acceptance, CSP/cache, ordered publication and recovery | FrontendStack, npm lockfiles and shared publisher |
| [Unverified account recovery](runbooks/unverified-account-recovery.md) | Identity-verified operator password recovery | Cognito and the authenticated verification flow |
| [WebSocket runtime](runbooks/websocket-runtime.md) | CloudFront ingress, secret rotation, real acceptance | Frontend/Gateway stacks and ws-authorizer |
| [Pipeline recovery](runbooks/pipeline-failure-recovery.md) | Failure queues, alarm routing and controlled replay | GatewayStack, failure construct and replay tool |
| [Knowledge-grade news release](runbooks/knowledge-grade-news-release.md) | Canary acceptance, retained-legacy archive, verified prices and rollback | News runtime/recovery, ADR-052 |
| [HTTP origin proof migration](runbooks/http-origin-secret-migration.md) | Consumer contract and coordinated rotation gates | Go/QA/MCP origin verifiers and deployment configuration |
| [Connector capabilities](decisions/ADR-053-connector-capability-private-delegation.md) | Operation policy and recorded private-delegation delivery gates | Versioned inventory, generated tables and contract tests |
| [Connector delegation wire](security/connector-delegation-v1.md) | Canonical signed bytes, key pinning and lifetime limits | Go/TypeScript/Python verifiers and dated connector acceptance |
| [Connector release](runbooks/connector-release.md) | Dedicated clients, private IAM/KMS, ARM64 packaging and provider acceptance | Reviewed CDK artifacts and dated deployed evidence |
| [Connector activation acceptance](runbooks/connector-activation-2026-10-21.md) | Dated source/artifact-bound provider, grant, rollback, fixture disposition and guide evidence | Private receipt hashes; native-host limitations remain explicit |
| [Summary privacy release](runbooks/summary-privacy-release.md) | Reader/writer targets, immutable cutoff, drain and cross-user acceptance | Exact deployed versions, parameters and dated acceptance evidence |
| [Self-service Account invitations](decisions/ADR-054-self-service-account-invitations.md) | Member-authorized email invitation and identity-bound Account grants | Account service/handler, pending transactions and frontend invitation receipts |
| [Designated initial Account Owner](decisions/ADR-055-designated-account-owner.md) | Owner invitation, optional creator AM membership and bounded creation receipts | Atomic creation, authenticated Owner resend and creator checkbox |
| [Billing service origin and identity](decisions/ADR-056-billing-service-origin-and-identity.md) | IamAuth Web Function behind OAC, BFF-verified TTOBAK JWT, direct/ancestor membership access (ADR-060), upload prefix | On main: BFF, `TtobakBillingStack`, CloudFront behaviors and Phase 2 worker code; production's separate companion ECS image uses the [Billing release](runbooks/billing-release.md) procedure |
| [Seekable audio playback copies](decisions/ADR-062-seekable-audio-playback-copies.md) | Remuxed `audio-playback/` copies bound to source ETag/version; originals unchanged | Storage prefix, cleanup, `ttobak-audio-remux` worker, `GET /audio` selection and `scripts/audio_playback_backfill.py` |
| [Local MCP intake](decisions/ADR-065-local-mcp-intake.md) | Deterministic local connectors over host MCP servers; references not copies; calendar first, AI proposals only; amended for the SpecReq task step's two provider writes | `mcp-server/src/intake/`; calendar and task acceptance pending |
| [Account contact encryption and retention](decisions/ADR-063-account-contact-encryption-and-retention.md) | Contact fields KMS envelope-encrypted per row, bound to account/contact; kept until deleted, PITR window disclosed | `backend/internal/sealing`, contact repository, `KMS_KEY_ID` |
| [Account relationship links, signals and KB references](decisions/ADR-064-account-relationship-links.md) | Typed account links with manual/auto origin and read-time revalidation, account news sources, KB refs, bounded overview reads; QA retrieval unchanged | `backend/internal/service/account_links.go`, `account_overview.go`, `account_kb.go`, `account_news.go` |
| [Account people edges](decisions/ADR-067-account-people-edges.md) | Deterministic member/contact/entity resolution into role-keyed `LINK#meeting#` edges; manual wins, mention review, leased runs | `backend/internal/service/account_people*.go`, `backend/cmd/backfill-account-people` |
| [SA performance data residency](decisions/ADR-068-sa-performance-data-residency.md) | Owner-private `PERF*` field table, default-encryption exception, per-row retention (year+2 TTL for dated rows), organizer-domain calendar counterpart | `backend/internal/model/performance.go`, `backend/internal/repository/performance.go` |
| [Mac caption overlay](decisions/ADR-069-mac-caption-overlay.md) | Floating per-language caption windows (PR 2/4); translation on the user's own AWS profile (Bedrock Luna finals, Amazon Translate interims) and a native confirmation of profile changes; seven validated remote commands (three in 1/4, four overlay commands in 2/4); listen-only overlay capability (2/4); Zoom whole-screen limit | `mac-app/src-tauri/src/caption_*.rs`, `capabilities/caption-overlay.json` (2/4), `frontend/src/lib/captionOverlay.ts` (3/4) |
| [Shareable-by-default note provenance](decisions/ADR-070-shareable-default-note-provenance.md) | No linked-predecessor evidence, human edits keep `safe` provenance, first live draft safe, own image analyses safe, Korean-only final notes, owner-only sharing-safe reset | `backend/internal/repository/summary_privacy.go`, `backend/internal/service/bedrock.go` |

## Feature workflows

- [Public user guide](user-guide/README.md): Korean end-user instructions and
  GitHub Pages publishing, explicitly requested by the user. This public HTML
  guide is a scoped language exception; engineering references remain English.
- [SA meeting workflow](features/sa-meeting-workflow.md): preparation, source-aware
  notes, customer/project connection, and private follow-up documents.

## Decisions and history

`decisions/ADR-*.md` preserve decision dates, rationale, consequences, and explicit
successors. An accepted decision can have a historical implementation detail that
has since changed. Follow its current-state/supersession notes and verify code.
The latest ADR number alone does not override unrelated decisions.

`superpowers/plans/`, `superpowers/specs/`, `.kiro/specs/`, `research/`, benchmark
reports, `AUDIO-REVIEW.md`, `CODE-REVIEW.md`, `review/TRI-MODEL-REVIEW.md`, and
`ISSUES.md` contain historical plans or observations. They neither prove a feature
is deployed nor turn an old finding into a current PR defect. Keep them for
rationale and investigation, not as duplicate live specifications.

When documentation and code disagree, correct the factual reference and identify
any unresolved policy gap. Do not silently weaken a security requirement or mark
an accepted limitation fixed. Changes to a policy need their own explicit rationale.

After editing `CLAUDE.md`, regenerate `AGENTS.md` and check the documentation:

```bash
python3 scripts/docs/sync_review_context.py
python3 scripts/docs/check_docs.py
```

- [Cognito app client write attributes and self-service email change](decisions/ADR-057-cognito-client-write-attributes.md)
- [Descendant Account read sharing](decisions/ADR-058-descendant-account-read-sharing.md)
- [Owner-approved Account sharing](decisions/ADR-059-owner-approved-account-sharing.md)
- [Browser recording recovery and retention](decisions/ADR-048-browser-recording-backups.md)
- [Private audio crop worker](../backend/whisper/AUDIO_CROP.md)
- [Private audio crops and activation](decisions/ADR-047-private-audio-crops.md)
- [Conditional news publication and cost reservations](decisions/ADR-052-conditional-news-publication.md)
- [Shared speaker correction and account publication](decisions/ADR-051-shared-speaker-correction-and-account-publication.md)
- [Versioned HTTP origin proof rotation](decisions/ADR-050-http-origin-proof-rotation.md)
- [Summary sharing and source provenance](decisions/ADR-049-summary-sharing-source-provenance.md)
