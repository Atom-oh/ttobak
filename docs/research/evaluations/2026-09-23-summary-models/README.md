# Final-note model comparison — 2026-09-23

Historical evaluation evidence and the rationale for selecting GPT-6 Sol for
final meeting notes. This is a small synthetic regression comparison, not a
general model ranking or a production-traffic benchmark.

## Decision

Select `global.openai.gpt-6-sol` for final notes through the dedicated
`BEDROCK_SUMMARY_MODEL_ID` setting. Both batch and saved-source summaries share
this completion path. The shared prompt, selected-source handling, document
grounding, timestamp processing and conditional publication remain in place.
Refinement, images, action extraction and other auxiliary calls keep their
existing independent model selection.

GPT completed all 14 evaluated notes, retained the tested facts, and took about
half as long as Opus 5.5. The anonymous Kiro review of the first repetition also
preferred GPT, primarily because it did not turn a stated backup-verification
need into an agreed task. That Opus behavior occurred once and did not recur in
the second repetition; it is a concrete observation, not a claimed general
accuracy difference.

## Results

| Measurement | Claude Opus 5.5 | GPT-6 Sol |
| --- | ---: | ---: |
| Completed real Bedrock calls | 14/14 | 14/14 |
| Automated case passes | 12/14 | 14/14 |
| Automated checks | 86/88 | 88/88 |
| Mean wall time | 16.19 s | 7.71 s |
| Median wall time | 14.05 s | 6.68 s |
| Slowest observed call | 32.63 s | 16.12 s |
| Reported input tokens | 38,660 | 22,758 |
| Reported output tokens, including reasoning | 25,847 | 13,848 |

The two Opus automated failures are **false positives for factual quality**.
In both quoted-instruction cases it wrote the required no-task sentence followed
by another sentence explaining that no task, owner or deadline was agreed.
The existing strict pattern accepts only the standalone no-task wording. It did
not execute the quoted attack, invent its deadline, or publish its fake marker.
The raw failed results are preserved; the rubric was not relaxed to obtain a pass.

Conversely, the old owners/deadlines rubric permits an unknown-owner backup
checkbox even when a need has not explicitly become an agreed task. It therefore
did not catch the first Opus run's extra checkbox. The production prompt's
explicit-agreement rule and the source support preferring GPT's treatment.
Kiro noted this ambiguity in the legacy reference and did not treat model
agreement as proof.

Both models preserved the selected B amount instead of stale A, negative
approvals, proposals versus decisions, late budget/owner/deadline corrections,
mixed English/Korean numbers and units, and the separation of personal notes
from spoken evidence. Both sometimes grouped several facts under a representative
timestamp; the prompt allows this. The comparison does not prove sentence-level
timestamp alignment.

An additional volume check used 131,190 UTF-8 bytes of selected transcript
(264,509 bytes with its segment JSON). GPT completed in 15.93 seconds with
30,220 input / 2,039 output tokens; Opus completed in 32.07 seconds with
56,418 input / 3,934 output tokens. Both retained all 48 A/B sample numerals
and the late budget, owner and deadline corrections, without truncation.
These two calls are separate from the table above and preserved under
`large-input/`. Repeated discussion inflated the input deliberately; this
checks volume and completion behavior, not realistic long-meeting accuracy.

## Price evidence

AWS's September 22 price list for standard global inference in `us-west-2`
confirms the user's observation that Opus became cheaper:

| Model | Input / 1M tokens | Output / 1M tokens |
| --- | ---: | ---: |
| Opus 5 | $5 | $25 |
| Opus 5.5 | $4 | $20 |

Both uncached rates fell **20%**. Opus 5.5 cache-read pricing is $0.20/1M
tokens versus Opus 5's $0.50. The measured 14 Opus 5.5 calls had no cache
reads/writes and total **$0.67158 at those published rates**. This is a calculated
inference charge, not an invoice or a comparison against measured Opus 5 usage.
Opus 5.5's default adaptive reasoning is always enabled; output usage includes it.

GPT-6 Sol's official OpenAI direct-API reference is $2 input / $10 output per
million tokens. **Its Bedrock price was not established by the fetched AWS
catalogs.** Those direct-API prices must not be presented as confirmed AWS
charges or used to promise AWS savings. Availability and actual invocation were
verified independently. The model choice rests on observed quality and latency,
not an unverified Bedrock price assumption.

`prices.json` and `aws-opus-price-evidence.json` preserve the price basis.
Official sources fetched for this work:

```text
https://aws.amazon.com/bedrock/pricing/
https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/us-west-2/index.json
https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-5-5.html
https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-openai.html
https://developers.openai.com/api/docs/models/gpt-6-sol
```

## Method and evidence

Seven fictional cases ran twice per model through Bedrock `InvokeModel` in
`us-west-2`, from the verified TTOBAK account. No customer meetings were loaded.
Four cases are the existing note-quality corpus. Three additional cases cover a
longer transcript with late corrections, mixed-language numeric boundaries, and
an attack quoted as meeting data. The longer fixture has 28 timestamped segments
and 24 distinct operational topics; it is still not a real hour-long meeting.

Both providers received the exact same production system and user text and
16,000 output-token ceiling. Native protocol fields differ: Anthropic uses
`system` and `max_tokens`; OpenAI uses a `developer` message and
`max_completion_tokens`. Neither effort nor temperature was overridden.
Provider defaults and tokenizers differ, so equal token limits do not imply an
equal visible-text or reasoning budget. GPT's reported automatic cache usage is
preserved in its raw responses; latency includes any benefit from it.

There was one concurrent stream per provider, with sequential calls within each
stream, followed by a second repetition. SDK retries were disabled. Wall time
includes client/network/provider work. This sample does not establish sustained
throughput, a p95 latency, regional latency, or long-context reliability.
Two earlier single-case compatibility probes are separate from the 28-call table.

The original provider requests, responses, SHA-256 attestations and reports are
under `opus-5-5/run-{1,2}` and `gpt-6-sol/run-{1,2}`. The rendered notes use
`.note.txt` so their Korean Markdown remains immutable evaluation data rather
than authored English documentation. Renaming changed no content bytes.
`evidence-sha256.json` pins the archived evidence.
The report commit fields identify the checkout base. `source-sha256.json`
identifies the actual changed implementation/evaluation files, and a local
regrade reproduced all 28 original findings with the shared production builder
and parser without new model calls.

`blind-quality-review.json` is the complete Kiro review of repetition 1:
X was GPT-6 Sol and Y was Opus 5.5. Names, latency and prices were withheld.
It used the configured Kiro model/effort without overrides and a zero-tool agent
with `--no-interactive --trust-tools=`. Its claims were checked against the
source, output and actual prompt. It is qualitative evidence from one reviewer,
not an additional accuracy metric.

The evaluation adapters now share the production OpenAI request builder and
strict completion parser. Raw OpenAI envelopes remain intact as evidence;
only completed visible text is fed through the shared note postprocessing.
Refusal, truncation, empty content, unexpected roles and tool calls cannot become
successful final notes.

Reproduce each model in a fresh directory:

```bash
export BEDROCK_REGION=us-west-2
export TTOBAK_NOTE_EVAL_EXTRA_CASES="$PWD/backend/internal/service/testdata/note-quality/stress-cases.json"
BEDROCK_MODEL_ID=global.anthropic.claude-opus-5-5 \
  scripts/eval-note-quality.sh live /absolute/new-opus-run
BEDROCK_MODEL_ID=global.openai.gpt-6-sol \
  scripts/eval-note-quality.sh live /absolute/new-gpt-run
```

Use `grade` on a scratch copy of an archived run to verify its exact native
request and evidence hashes without further paid calls. It recreates `.md`
renderings in that scratch directory; original `.note.txt` files remain unchanged.

## Release validation and activation

Full Go tests, including `cmd/*`, and `go vet` passed. Linux/ARM64 API and
summary builds passed. CDK synth, all 84 infrastructure tests, the documentation
check and six documentation tests passed. The actual
`BedrockService.invokeCompleteSummary` path also made a successful synthetic
GPT call in 6.60 seconds, without database access; see `native-smoke/`.
`implementation-review.json` records Kiro's PASS with one nonblocking observation
about unsupported bare provider IDs. Deployment uses the tested global profile.

The existing deployed summary package identified source commit
`6193cc944b40c44ce2ad422465f86d3bd5490c9b`. The working branch has unrelated
differences from that production revision, so the release binary was rebuilt
from that exact deployed commit with only `bedrock.go` and `openai_summary.go`
changed in runtime code. Full Go tests and vet passed again on that release
baseline. The prompts and summary entry point were identical between baselines.

The deployment assembly copies the current live CloudFormation templates.
Only the summary-role policy changes in AiStack; only summary Lambda code and
the dedicated model override change in GatewayStack. Existing parameters,
outputs, other Lambda packages and environment values are preserved. Each stack
deploys separately with `--exclusively`, IAM first. Baseline template/revision
checks reject concurrent changes, and the original verified Lambda ZIP is
retained locally for rollback.

Activation completed on **2026-09-23 at 03:01:44 UTC**. The summary function is
Active with a Successful update and selects `global.openai.gpt-6-sol`.
`deployed-before.json`, `deployed-after.json` and `deployed-iam-simulation.json`
record the transition and resource-specific grants. The downloaded deployed ZIP
and its bootstrap bytes matched the tested release build.

A temporary private Lambda using the actual summary execution role also
completed `GenerateResummary` on a synthetic snapshot in 7.79 seconds.
It preserved selected B, negative approval and resolved transcript links.
The function and its log group were deleted and their absence verified;
see `role-smoke.json`. This validates the role and shared summary-generation
path, not a customer meeting or the full EventBridge/storage pipeline.

Removing the summary override
returns the new binary to its existing Opus configuration; the default Claude
path remains covered by the full summary test suite.

## Repository integration update

The original September 23 deployment was subsequently replaced by the regular
infrastructure deployment of main PR #289. This integration preserves that PR's
long-summary continuation fixes and selects GPT-6 Sol in source, so future
deployments retain the chosen model. OpenAI completions now have the same bounded
continuation/output-budget contract; auxiliary Claude calls remain unchanged.

Original generated requests/responses, notes and receipts are preserved byte-for-byte
in `raw-evidence.zip`; extract it to a scratch directory before regrading. Top-level
metrics and the original activation receipt remain historical, not a claim about
the deployment of this PR. Archive SHA-256: `297d248b32371ad39938f68a39643e50eb0364398d10ee5d31e7acf2ac491ad3`.
