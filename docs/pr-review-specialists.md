# Specialist PR review

CI assigns distinct responsibilities instead of asking every model to repeat
every review lens. The trusted workflow enables this protocol with
`ROLE_REVIEW=1`; legacy matrix entrypoints remain for regression fixtures.

| Slot | Configured model | Responsibility |
| --- | --- | --- |
| `codex` | `global.openai.gpt-6-astra` | Implementation, concurrency, errors and tests |
| `kiro-fable` | `claude-opus-5` | AWS architecture, IAM, networking and service constraints |
| `kiro-sol` | `gpt-5.6-sol` | Deployment order, component contracts, lifecycle and recovery |
| `claude-self` | `global.anthropic.claude-fable-5-1` | Authentication, data boundaries, requirements, API and ADR consistency |

The shared `kiro-fable` tag identifies the Opus slot. Kiro catalog aliases differ
from Bedrock inference-profile IDs. These are configured model identities, not
attestation of the provider's internal routing or weights.

## Routing and evidence

Trusted code determines which roles apply. Codex and Claude review the full change
boundary, retaining independent OpenAI/Anthropic checks for sensitive changes.
Kiro roles run for applicable AWS and operational changes, including relevant
documentation. Unfamiliar paths route conservatively. Only deterministic routing
may record NOT_APPLICABLE; provider failures never do.

`prepare_roles.py` verifies the pinned base checkout, resolves the immutable merge
base, fetches Git objects and generates a complete diff without executing head
code. It reads reviewer instructions from the base Git object. Candidate context
is checked for availability, size and generated-source freshness, then discarded.
The shared context ceiling is 24,000 bytes; repositories may enforce a smaller one.

Every result confirms its role, HEAD and reviewed paths. Host metadata binds it
to the prepared request and records the process status. Nonzero exits, malformed
or empty reports, missing paths, invalid fingerprints, model selection errors,
quota exhaustion and failed required roles block coverage. A JSON shape is
evidence of protocol completion, not proof that the model found every defect.

The common protocol accepts a complete diff within 3,000 lines and 95,000 UTF-8
bytes. It blocks oversized input without awarding credit for a prefix. Existing
repository-specific chunking is governed by its own implementation and budget;
do not remove chunk attestations or raise limits to obtain a pass.

## Execution and synthesis

Each applicable model receives one specialist request. Both Kiro roles use fresh
HOME/cwd directories and an explicit empty tool catalog with no MCP resources or
hooks. Each active Kiro job first receives a fixed canary check without PR data;
only an exact successful no-tools response permits the actual review. Its child
environment excludes AWS and GitHub credentials. Errors remain visible; no
automatic quota or billing changes are made.

Codex retains its read-only sandbox and configured Bedrock provider. Claude's
specialist has no tools. The chair has bounded local read tools and no GitHub
token. Review output is scrubbed before becoming a public artifact.

Complete, valid results with no Critical/Major candidate or uncertainty receive
a deterministic summary. Other valid results require chair adjudication. A
coverage failure receives a deterministic failure; a chair cannot waive it.
Minor/Info findings remain in the report.

Code/configuration examples belong in closed top-level fences at column one;
inline backticks are limited to single-line symbol/path references. The
[module contract](../scripts/pr-review/README.md) describes the bounded validator:
it checks explicit markup and recognized sensitive assignments before and after
masking, without classifying every unmarked phrase as source. Complete fenced JSON
retains the existing decoding limits. A complete original blocking chair verdict
survives a formatting failure as a static FAIL with details withheld; quota and
output-limit failures keep precedence.

Review prompts prefer plain prose and unquoted references to avoid invalid inline
assignments, argument-bearing calls, and HTML/JSX fragments. Claude's specialist
schema excludes backticks from prose fields before host validation; necessary
examples can use top-level tilde fences. This generation constraint does not
replace identity, coverage, confidentiality, or verdict checks. Chair output that
ignores the common format guidance still fails closed.
Kiro responses with malformed JSON or an invalid JSON wrapper use the existing
bounded retry budget and a fresh request nonce. Exhaustion still blocks coverage;
valid reviews, schema/identity failures, and terminal provider diagnostics are
not retried by this syntax check. Before a zero-exit syntax retry, recognized
terminal diagnostics in non-JSON stdout are preserved as failures. Successfully
parsed review JSON is not scanned as a diagnostic stream. Both existing
classifiers inspect raw, control-normalized and scrubbed text in both streams
before a syntax retry, so terminal rendering or secret masking cannot hide a
recognized signal.
Post-scrub or combined-diagnostic overflow remains terminal.

Kiro review attempts share the existing total time allowance instead of each
restarting a slow response at the nominal timeout. The default remains 600
seconds across at most two calls, with the same per-call maximum of 900 seconds.
A slow first call may leave no retry; fast failures can use only the remaining
time. Startup probes, requested models, input scope and all validation stay intact.

With all four roles active, the ordinary path uses four review calls and two
Kiro startup checks. Adjudication adds one chair call; retries and fallback add
calls only when needed. This reduces duplicate requests, but is not a measured
wall-clock speedup. Per-role timing artifacts support before/after measurement.

## Failure publication and reruns

Evidence uploads use `specialist-review-<HEAD>-<run_attempt>` without overwrite,
so rerunning the same workflow preserves earlier attempts. GitHub documents
[immutable v4 artifacts](https://github.com/actions/upload-artifact/tree/v4#not-uploading-to-the-same-artifact)
and an incrementing `github.run_attempt` for reruns.

Upload requires successful workspace initialization and evidence validation.
Initialization creates a private fresh directory and records its device/inode;
validation reopens that same directory without following links. Only the existing
plan/source/summary and known specialist result/request/timing/flag names qualify.
Directory/file symlinks, hardlinks, nonregular files and replacement workspaces
withhold upload. The validator opens files relative to pinned directory descriptors
with `O_NOFOLLOW`, copies regular files into a fresh private staging directory and
rejects changes during copying. The action uploads only these copies, preserving
the artifact layout without following later source-tree replacements. Raw diff,
prompt and CLI output paths remain excluded. Safe partial diagnostics can still be
archived after a failed review; failed initialization/validation posts BLOCKED
without uploading the unsafe inputs.

The full final `review.md` uses the same validated copies; the gate reads that
archived report. Comments stay below 60,000 UTF-8 bytes, linking the complete
artifact instead of inlining reports above 50,000 bytes. Only presentation is
bounded: findings, review input, coverage and verdict are not truncated.

The workflow gate and comment run after failures with `!cancelled()`. PASS requires
successful specialist/synthesis execution, successful evidence upload with an
artifact ID, no failed-coverage signal, and exactly one terminal PASS verdict.
Missing/skipped execution, missing evidence, upload failure or invalid output
produces BLOCKED with explicit execution/evidence/gate outcomes. Partial or stale
PASS text is not republished on these paths. The final job gate fails closed even
if the gate step itself fails.

Publication still verifies the current PR HEAD and uses the existing authorized
same-repository, trusted-base workflow. Cancellation does not post a replacement
comment. Runner loss, job startup failure or unavailable GitHub APIs can prevent
publication; a previous comment never substitutes for the current attempt's
required checks. Offline publication tests execute the workflow shell with local
GitHub responses, including failure, cancellation and HEAD-change cases.

## Maintenance and release

Run `python3 -m unittest discover -s scripts/pr-review -p 'test_*role*.py' -v`
and the repository's existing review tests. Offline fake CLIs validate routing,
scope, subprocess status and safety boundaries without spending model credits.
They do not establish successful live model execution.

Native `pull_request_target` uses base scripts, so a workflow-changing PR must
also have offline checks for the candidate implementation. Review the latest HEAD,
resolve real Critical/Major findings, satisfy required CI and branch rules, and
verify the integration path before merge. Missing review or quota failure is not
a clean result. Model limits and required gates remain in force.

## Approved source scope

`role-input-scope.json` preserves this repository's existing lockfile/generated-
asset exclusions. The trusted base copy classifies immutable Git paths before
requests are prepared. Provenance records every excluded path and both raw and
approved diff hashes. Renames are expanded into deletion/addition records so a
source path cannot disappear through an artifact rename. A verified exclusions-
only change is explicitly NOT_APPLICABLE and invokes no model; missing inputs,
unknown exclusions and truncated required source remain blocked. The policy does
not authorize excluding additional source merely to obtain a pass.

Approved exclusions-only input explicitly supplies `--allow-exclusions-only` and
a private `--policy` file copied from Git BASE. The engine checks its byte hash
and retains an anchor through aggregation; arbitrary provenance cannot opt in.
