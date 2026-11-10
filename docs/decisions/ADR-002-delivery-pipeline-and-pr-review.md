# ADR-002: Delivery pipeline and AI PR review

- Status: Accepted. Consolidates the former ADR-002 (GitHub Actions on
  self-hosted runners) and ADR-043 (specialist review protocol).
- Implementation checked: 2026-11-11.

## Decisions

**Deployment.** Deploy from version-controlled GitHub Actions workflows on the
existing self-hosted runners (`ttobak-arm`, `ttobak-x86`), with AWS access from
the runner environment. There is no CodePipeline and no single deploy workflow:
`deploy-infra.yml`, `deploy-frontend.yml`, `deploy-whisper.yml`,
`deploy-research-agent.yml` and `deploy-billing.yml` each have their own
main-branch path filter and manual dispatch.

- The infra `deploy` job needs the `infra-tests` job (CDK synth and Jest).
- Stacks deploy one at a time with `--exclusively`, in dependency order, through
  `scripts/operations/http_origin_release.py` (`release`, then an always-run
  `retighten`). Never use `cdk deploy --all`: KnowledgeStack holds a staged,
  undeployed teardown and is left out of the release list.
- Storage is deployed again after Frontend so the OAC policy is tightened. The
  Storage custom resource's changing `Timestamp` forces that refresh; keep it.
- The infra workflow builds all eight Go zip entry points for Linux ARM64.
  `convert-doc` and the research agent are separate container builds.
- Frontend publication keeps the runtime `config.json` that CDK owns.
- Mac app builds are local and outside these workflows.

**PR review.** CI runs specialist review (`ROLE_REVIEW=1`): each supported model
gets one responsibility instead of every lens.

- Codex and Claude roles give independent OpenAI and Anthropic primary coverage.
  Kiro roles run only with `ROLE_REVIEW_KIRO=1`, which CI does not set.
- Reports must be complete and bound to an immutable scope. Role inputs use
  random-nonce boundaries.
- Only trusted routing marks a role inactive. A scope made only of files the
  base-approved input policy excludes completes as NOT_APPLICABLE; any
  reviewable source, unknown exclusion or failed collector blocks.
- A complete report with no blocking candidate or uncertainty gets a
  deterministic summary. The chair adjudicates substantive candidates and cannot
  waive missing or invalid coverage.
- Review limits, quotas and budgets are not raised to obtain a pass. New input
  exclusions need their own reviewed policy change. Output is English only.
- The review job uses `pull_request_target`, runs for same-repository PRs only,
  and executes scripts from the base checkout, never PR-head code.

## Limits and accepted risks

- Runner availability, patching and credentials are operational duties. A
  private repository does not remove the risk of running modified workflow code
  with deployment privileges.
- Workflow source shows configuration, not a successful run or deployed state.
- Model IDs are configuration; they do not attest model weights.
- The legacy model-by-lens matrix remains in `run-panel.sh` for runs without
  `ROLE_REVIEW=1`; CI does not use it.

## Source

`.github/workflows/`, `scripts/operations/http_origin_release.py`,
`scripts/publish-frontend.sh`, `scripts/pr-review/` (`role_review.py`,
`run-specialists.sh`, `synthesize_roles.py`, `README.md`),
`docs/pr-review-specialists.md`, `docs/runbooks/pr-review.md`.
