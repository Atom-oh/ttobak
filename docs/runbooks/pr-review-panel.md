# Runbook: AI PR-review panel startup and coverage

## Severity

P2 for unavailable review coverage. Escalate an agent fallback during a review as a
security incident: untrusted PR input may have reached a tool-enabled agent. A failed
preflight withholds PR input from Kiro. Never merge without the required review coverage.

## Symptoms

The panel writes diagnostic flags and the chair displays corresponding banners:

- `kiro-preflight.flag`: startup did not establish the required no-tools behavior.
- `kiro-quota.flag`: Kiro reported a quota signature on stderr.
- `kiro-agent-fallback.flag`: Kiro reported a missing or rejected agent configuration.
- `coverage-severe.flag`: the existing coverage contract cannot pass.

A startup failure skips all Kiro review cells while Codex still runs. Quota or fallback
signatures during startup retain their diagnostic banners; the log reports
`Kiro preflight failed`. During a review, quota errors log
`Kiro monthly request quota exhausted`, and fallback errors log `kiro-cli ignored --agent`.
Malformed agent configuration or a failed file copy can abort before banners are produced;
inspect the failed step log as well as the PR comment.

## Diagnosis steps

1. Read the latest HEAD's `AI Code Review` run and inline comments. The workflow uses
   trusted base-branch scripts; a PR cannot validate its own newly changed runner behavior.
2. Check the CLI version logged by `run-panel.sh` and the affected model/lens. The no-tools
   agent configuration was originally validated against kiro-cli 2.11.1.
3. Distinguish provider quota responses from agent fallback, authentication, timeout, or
   unexpected preflight replies. A `ServiceQuotaExceededException` response establishes
   what the provider reported, not the billing entitlement or the cause of key selection.
4. Check actual completed review outputs. Empty output and nonzero exits, including a
   timeout that leaves partial stdout, are excluded after bounded retries. Preflight
   replies never count as review cells.

These signatures are interpreted only in Kiro stderr. Codex can quote the PR diff on
stderr; quoted Kiro error text must not discard a valid Codex review.

## Resolution steps

### Quota response

Verify the runner uses the intended current `KIRO_API_KEY` from Secrets Manager
`/demo-platform/actions/AI-key` through the `ai-panel-keys` ExternalSecret. Compare version
identities without printing key values. Check provider account entitlement and the returned
reset information before deciding whether a key change or account action is appropriate.
After the cause is resolved, retry the failed run. Do not repeatedly rotate a verified
current key merely because the provider still reports a quota exception.

### Agent fallback or failed preflight

Validate `scripts/pr-review/agents/pr-review-notools.json` using the runner's CLI version:

```bash
kiro-cli agent validate --path scripts/pr-review/agents/pr-review-notools.json
```

The script also validates the no-tools configuration, including duplicate JSON keys. CLI
schema validation alone does not replace the behavioral preflight. Each configured Kiro
model receives a fixed canary request in an isolated directory with the same agent as its
review cells. Passing requires exit 0, exactly `NO_TOOLS`, and no tool-use, quota, or fallback
signal. Neither stdin nor the prompt contains the PR diff. Both models must pass before
any Kiro review begins; each preflight is bounded by `KIRO_PREFLIGHT_TIMEOUT` (60 seconds).

Keep `--agent pr-review-notools` and the per-cell `.kiro/agents/` copy. Do not replace them
with an empty `--trust-tools=` argument or change engine to bypass a failed check. In the
original 2.11.1 investigation, the empty argument was ignored and the v3 engine did not
honor the agent's empty tool list. Revalidate behavior when upgrading the runner.

The AWS-Demo-Platform repository owns the runner image and CLI version. Updating it is a
separate change; this repository owns the trusted review scripts and their tests.

## Verification and prevention

```bash
bash tests/run-all.sh
python3 -m unittest discover -s scripts/pr-review -p 'test_*.py' -v
python3 scripts/docs/check_docs.py
```

The documentation/review-context CI runs the shell suite using local CLI stubs. It covers
healthy reviews, startup refusal, quota and fallback signals, partial output with nonzero
exit or timeout, retry recovery, and isolation of provider-specific diagnostics. Prompt
tests retain full shared context and diff delivery within the argument-size budget.
These offline checks do not establish live provider availability. Inspect the real latest
HEAD review after pushing and apply the repository's coverage requirements before merging.

## Related

- `scripts/pr-review/run-panel.sh`, `scripts/pr-review/synthesize.sh`
- `scripts/pr-review/agents/pr-review-notools.json`
- `.github/workflows/test-docs-review.yml`, `.github/workflows/pr-review.yml`
- `docs/runbooks/pr-review.md`
