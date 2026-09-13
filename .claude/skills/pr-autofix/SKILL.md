---
name: pr-autofix
description: Resolve current-HEAD AI PR findings, rerun required checks, and merge when the user's authorized conditions hold.
---

# PR review completion

Follow `docs/runbooks/pr-review.md` and the root project's delivery policy.

1. Identify the PR, current full HEAD SHA, target branch, and predecessor PRs.
2. Read paginated issue comments, inline comments and review records. The current
   marker is `<!-- multi-ai-pr-review -->`. Match the comment's commit SHA to HEAD;
   timestamps and earlier-commit reviews are insufficient.
3. Verify Critical/Major findings against the proposed code. Fix actual issues,
   run relevant tests, commit/push, and await the new HEAD's completed review.
4. Missing/failed review or insufficient required coverage is unfinished work.
   Retry recoverable failures and surface concrete external blockers. Do not
   disable checks, weaken criteria, or stop merely at an arbitrary iteration count.
5. Minor/Info alone does not block completion. When current-HEAD review is complete,
   no Critical/Major issue remains, and required CI/protection is satisfied, recheck
   HEAD and integration path and merge under the user's standing authorization.
6. Report fixes, verification, PR URL and merge outcome. Honor later review-only or
   no-merge instructions. Do not send external messages without authorization.

Poll at reasonable intervals and respect the workflow's actual timeout; the
60-minute job cannot be classified failed merely because ten minutes elapsed.
Workflow defects may be fixed when in scope, but never by bypassing required gates.
