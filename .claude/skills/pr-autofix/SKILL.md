---
name: pr-autofix
description: After creating a PR, poll for AI review comments and auto-fix issues (max 3 iterations)
---

# PR Auto-Fix Skill

After you create a PR (via `gh pr create`), automatically wait for the AI Code Review CI to complete, then read review feedback and fix issues. Repeat up to 3 times until the review passes.

## When to use

Invoke this skill immediately after creating a PR. It replaces the manual cycle of:
1. Push → wait for CI review → read comments → fix → push again

## Flow

```
PR created → poll for review comment → FAIL? → read issues → fix code → commit & push → repeat
                                      → PASS? → done
                                      → 3 iterations? → stop, notify user
```

## Steps

### 1. Identify the PR

Get the PR number from the most recent `gh pr create` output, or from the current branch:

```bash
PR_NUMBER=$(gh pr list --head "$(git branch --show-current)" --json number --jq '.[0].number')
```

### 2. Poll for AI review comment

The AI Code Review workflow (`pr-review.yml`) posts a comment containing `<!-- bedrock-pr-review -->`. Poll until it appears or is updated (check the `updated_at` timestamp is after the last push).

```bash
gh api "repos/{owner}/{repo}/issues/${PR_NUMBER}/comments" \
  --jq '.[] | select(.body | contains("<!-- bedrock-pr-review -->"))'
```

Poll every 60 seconds. If no review comment appears within 10 minutes, stop and inform the user.

### 3. Check verdict

Extract the verdict from the review comment body:
- Contains `**Status: PASSED**` → done, inform user
- Contains `**Status: BLOCKED**` → proceed to fix

### 4. Fix issues (if BLOCKED)

Read the review comment and the current diff. Fix ONLY the issues mentioned:
- Focus on **CRITICAL** and **MAJOR** issues first
- Fix **MINOR** issues if trivial
- Do NOT refactor beyond what the review asks
- Do NOT modify CI/CD workflow files (.github/workflows/*)
- Verify the fix compiles (Go build, frontend build as needed)

### 5. Commit and push

```bash
git add <changed-files>
git commit -m "fix: address AI review feedback (iteration N/3)

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
git push
```

### 6. Repeat or stop

- If iteration < 3: go back to step 2 (poll for new review)
- If iteration == 3 and still BLOCKED: stop and tell the user that manual review is needed
- Track iteration count by counting commits with message prefix `fix: address AI review feedback`

## Important constraints

- **Max 3 iterations** — after 3 failed attempts, stop unconditionally
- **Never modify workflow files** — the review CI itself must not be changed during autofix
- **Scope discipline** — only fix what the review mentions, nothing else
- **Build verification** — always verify the code compiles before committing
- **Polling patience** — CI takes 2-5 minutes; poll at 60s intervals, not faster
