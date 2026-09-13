---
name: pr-autofix
description: Resolve current-HEAD AI PR findings within the authorized scope, rerun required checks, and merge when the user's conditions hold.
---

# PR review completion

Follow `docs/runbooks/pr-review.md` and the root project's delivery policy.
This procedure does not expand the user's authorization.

1. Identify the PR, current full HEAD SHA, authorized change scope, target branch,
   and predecessor PRs. Read paginated issue comments, inline comments and review
   records. Match `<!-- multi-ai-pr-review -->` and its full commit SHA to HEAD;
   timestamps and earlier-commit reviews are insufficient.
2. Verify Critical/Major findings against the proposed code. Fix actual issues
   within scope, run relevant tests, commit/push, and await the new HEAD's review.
3. Use at most five corrective push/review rounds per autofix batch. At that
   limit, end this batch and return to the coordinating agent for fresh root-cause
   diagnosis and a concrete revised plan; do not repeat the same edits indefinitely.
   The overall authorized task remains unfinished, not passed. A new batch needs
   new diagnostic evidence, not a reset counter used to evade this bound.
4. Missing/failed review or insufficient required coverage is unfinished work.
   Retry recoverable execution failures only after identifying their cause.
   Failed checks and unresolved Critical/Major findings always block merge.
5. Minor/Info alone does not block completion. After current-HEAD review and
   required CI/protection pass, recheck HEAD and the integration path and merge
   under the user's standing authorization. Honor later review-only/no-merge scope.
6. Report fixes, verification, PR URL and merge outcome. Do not send unrelated
   external messages without authorization.

## Workflow and gate boundary

Generic requests to fix review feedback do **not** authorize changes under
`.github/workflows/**` or `scripts/pr-review/**`, changes to branch protection,
review criteria, reviewer coverage, credentials, or runner permissions. Do not
edit those controls merely to make a failed review/check pass.

When the user's original task explicitly includes review/CI tooling, or the user
has approved a concrete PR containing those changes, treat them as the authorized
feature change: review the actual control diff independently, run its checks on
the proposed HEAD, and retain every required gate. This skill supplies no additional
permission to expand that scope. Already authorized tooling changes follow the
same latest-HEAD merge conditions; do not invent a second approval requirement.

Poll at reasonable intervals and respect the actual workflow timeout. A
60-minute job is not failed merely because ten minutes elapsed. A review failure
cannot be resolved by disabling its workflow, suppressing a real finding, or
reducing required coverage.
