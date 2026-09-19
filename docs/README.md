# Documentation map

Repository documentation and AI review output are English-only. Read the smallest relevant current
reference; do not preload the historical archive into a PR review.

Verbatim generated note-quality outputs are archived evaluation data. Keep their
original bytes and hashes; only explanatory prose is translated. The documentation
check pins those four named artifacts by SHA-256, while checking all authored
Markdown for English language and historical/current status.

## Current references

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
| [PR review runbook](runbooks/pr-review.md) | Context delivery, evidence, latest-HEAD merge gate | Review scripts and workflow |
| [Specialist review](pr-review-specialists.md) | Current role assignment, coverage and synthesis contract | Trusted workflow and review scripts |
| [Deployment runbook](runbooks/deployment.md) | Build/deploy/rollback constraints | Deployment workflows |
| [Unverified account recovery](runbooks/unverified-account-recovery.md) | Identity-verified operator password recovery | Cognito and the authenticated verification flow |
| [WebSocket runtime](runbooks/websocket-runtime.md) | CloudFront ingress, secret rotation, real acceptance | Frontend/Gateway stacks and ws-authorizer |

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
