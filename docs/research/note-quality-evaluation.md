# Note quality evaluation

The reference corpus uses four fictional meetings. It checks source selection,
numbers/units, negation, decisions versus proposals, owners/deadlines and the
distinction between personal notes and transcript evidence.

The evaluator captures requests from the production `SummarizeTranscript` path.
DynamoDB operations and attachments are synthetic; live mode invokes only
Bedrock. Each case has at most one model request. The response passes through
the same production completion validation and transcript-anchor resolution.

## Run

Run the **Evaluate Note Quality** workflow on main. It reads only the deployed
summarize function's model/region, code hash and deployment status, then evaluates
the four cases and uploads the results. It does not load customer meetings. The runner needs Lambda GetFunctionConfiguration for the summarizer and Bedrock InvokeModel in its configured region; only selected nonsecret configuration fields are archived.

For local SDK credentials, use a new empty output directory:

```bash
scripts/eval-note-quality.sh live /absolute/new-output-directory
```

Set `TTOBAK_GO_BINARY` if Go is not installed at the documented default path.
`BEDROCK_MODEL_ID` and `BEDROCK_REGION` must match the deployed summarizer; region
defaults to `us-west-2`, as in `cmd/summarize`. Endpoint overrides are rejected
in live mode to avoid confusing emulator responses with real model evidence.

Default unit tests skip live evaluation. Request export makes no model calls:

```bash
scripts/eval-note-quality.sh requests /absolute/new-request-directory
```

External invocation can use those exact request bytes with the configured model.
Save the provider response body as `<case>.response.json` and its evidence as
`<case>.evidence.json`, containing `transport` (`aws-mcp` or `aws-sdk`), `modelId`,
`region`, raw `requestSha256`/`responseSha256`, and `observedAt` (RFC3339). Verify
the external tool actually made the API call. Supplied evidence is an operator
attestation, not cryptographic proof of AWS execution.

```bash
scripts/eval-note-quality.sh grade /absolute/existing-request-directory
```

Grade mode decodes an empty evidence record and requires every attested field; it never fills missing fields with expected values. It rejects missing evidence, modified requests and mismatched hashes.
Never grade a mock response as real model output.

## Interpret

`report.json` records the source commit, corpus hash, model/region, invocation and
completion status, usage and individual criteria. Raw request/response files,
rendered Markdown and per-call evidence support inspection. A missing or partial
response fails; exporting requests alone never produces a quality pass.

The rubric accepts reviewed reference answers and rejects deliberate bad amounts,
units, negations, decisions, owners, deadlines and invented evidence. Its anchor
check verifies allowed segment IDs, not every claim's semantic citation accuracy.
Patterns are limited to this corpus and require inspection of every real output;
passing is not a general accuracy percentage or a substitute for real meeting
evaluation. Preserve failed runs and explain any rubric correction separately
from changes to the model or production prompt.

An [exploratory real-model probe](2026-09-12-note-quality-probe.md) found an inferred follow-up task. The rubric and prompts now cover that failure. The subsequent [complete live evaluation](evaluations/2026-09-12-note-quality/README.md) passed all 25 checks across four cases; its raw responses, hashes, manual observations and stricter regrade are preserved with the report.
