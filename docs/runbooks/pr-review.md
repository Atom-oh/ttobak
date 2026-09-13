# PR review operations

## Context and trust

`.github/workflows/pr-review.yml` uses `pull_request_target` and checks out trusted
base code for same-repository PRs. It reads the PR diff as data; never execute
PR-head scripts in that privileged job. A separate unprivileged test workflow
checks proposed review scripts and documentation.

`CLAUDE.md` is canonical. `scripts/docs/sync_review_context.py` publishes selected
review sections into `AGENTS.md` with a source hash; local Kiro steering references
that file. CI Kiro cells have isolated HOME/cwd and request no trusted tools, so
`scripts/pr-review/build-prompts.py` embeds AGENTS.md in every lens prompt. Steering
alone cannot supply that context inside the isolated CI directory.

Tool trust and tool availability are different controls. The current invocation
uses `--trust-tools=`; local stub tests check the requested flags, not the actual
runner's capability list. Open PR #193 records a separate zero-tool-agent and
fallback-detection fix. Do not claim that the empty trust flag proves no tools
exist. This context-delivery change does not grant file reads or depend on them;
verify the selected runner/agent before claiming a zero-tool security boundary.
Kiro's custom-agent documentation defines availability through the `tools` list.

The builder rejects missing, stale or oversized context. Keep the extract below
24,000 bytes to leave room for Kiro's inline diff and lens instructions. Do not
preload ADRs/plans or silently crop project rules. All prompts and findings use
concise English. Reviewers must distinguish current code, policy, accepted gaps,
and superseded historical decisions.

## Review pipeline

- Four independent lenses: L2 correctness, L3 security, L4 conventions, L5 docs/CDK.
- `run-panel.sh` runs Codex and two configured Kiro models for each lens. The Kiro
  roster is in that script; Codex's model comes from runner configuration, so
  different alias strings are not automatically drift.
- `synthesize.sh` consolidates findings and verifies evidence rather than counting
  model votes. It requires a terminal `VERDICT: PASS` or `VERDICT: FAIL`.
- Empty/invalid chair output fails closed. Slow failure skips fallback; fast failure
  may use the configured fallback. Defaults are owned by `synthesize.sh` and
  checked by `chair-timeout-policy-check.sh`.
- Existing coverage logic warns when one whole model row is absent and forces FAIL
  when at most one model remains. A response is not proof that every relevant line
  was reviewed; inspect coverage and truncation notices before merging.

Current limits: workflow input is the first 3,000 filtered diff lines; Kiro also
has a 100,000-byte diff cap. These are partial-review limits, not proof of a clean
full diff. For a change exceeding them, split into reviewable PRs or obtain
complete, attributable review coverage before merging. Do not disable gates or
claim the excluded suffix passed review. Prompt changes are first used by the
privileged workflow after reaching the trusted base; local/mock validation does
not prove a live model's false-positive rate.

## Latest-HEAD completion

The posted issue-comment marker is `<!-- multi-ai-pr-review -->`, not the retired
`bedrock-pr-review` marker. Match the full `Triggered by commit` SHA in the body
against the PR's `headRefOid`; an updated timestamp alone is insufficient. Also
read inline review comments and review records, following API pagination.

```bash
gh pr view <PR> --json headRefOid,baseRefName,headRefName,statusCheckRollup,mergeStateStatus,reviews
gh api --paginate repos/<OWNER>/<REPO>/issues/<PR>/comments
gh api --paginate repos/<OWNER>/<REPO>/pulls/<PR>/comments
gh api --paginate repos/<OWNER>/<REPO>/pulls/<PR>/reviews
```

Verify Critical/Major findings against code, fix actual defects, run relevant tests,
commit/push, and wait for the new HEAD's review. Review failure, no response or
inadequate coverage is unfinished work. Minor/Info alone is not a blocker. Rerun
recoverable failed jobs; report a concrete permission/external-state blocker if
one prevents completion. Do not use an arbitrary retry count as a clean verdict.

Immediately before merge, re-read HEAD, verify it matches the reviewed SHA, check
required CI and branch protection, and confirm the base and predecessor PR state.
The user has authorized merge when those conditions are met. A later review-only
or no-merge instruction overrides that authorization.

## Local validation

```bash
python3 scripts/docs/sync_review_context.py
python3 scripts/docs/check_docs.py
python3 -m unittest discover -s scripts/docs -p 'test_*.py' -v
python3 -m unittest discover -s scripts/pr-review -p 'test_*.py' -v
bash scripts/pr-review/chair-timeout-policy-check.sh
for script in scripts/pr-review/*.sh; do bash -n "$script"; done
```

The tests capture prompts using fake CLIs, proving context/diff delivery and requested
invocation flags without submitting data to providers. A live review remains separate.
