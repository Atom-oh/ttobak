# Note quality evaluation

Status: current native runner protocol, with explicitly separate historical
single-call compatibility below. No new live acceptance is claimed by this guide.

The current evaluator exercises production `SummarizeTranscript`, including
`invokeCompleteSummary`, the native provider adapter, bounded continuation,
completion checks and citation postprocessing. Repository reads/writes and
attachments are synthetic. Live mode contacts only Bedrock for model inference;
it does not read customer meetings.

The unchanged four-case fictional corpus checks source selection, numbers/units,
negation, decisions versus proposals, owners/deadlines, and separation of saved
notes from transcript evidence. Source IDs and citation checks do not establish
complete semantic accuracy. Inspect every generated note.

## Current native evaluation

Run **Evaluate Note Quality** on main. Its offline tests run first. Discovery
reads the deployed summarizer's `BEDROCK_SUMMARY_MODEL_ID`, `BEDROCK_MODEL_ID`,
region, code hash, revision and stable deployment status. The actual Go service
constructor resolves the final model: summary setting, then auxiliary setting,
then the production default. The workflow exports the raw settings, including
empty values, so an inherited runner setting cannot replace an absent deployed
setting. It does not duplicate the production model default in Python.

The runner needs Lambda GetFunctionConfiguration and Bedrock InvokeModel in the
configured region. Only selected nonsecret configuration fields are retained.
Endpoint overrides, including shared-profile overrides, are rejected in live mode.
A new empty output directory prevents mixing evidence from separate runs.

```bash
# Export the raw model settings matching the deployment snapshot first.
export TTOBAK_NOTE_EVAL_DEPLOYMENT=/absolute/deployment-snapshot.json
scripts/eval-note-quality.sh live /absolute/new-output-directory
```

The snapshot uses the workflow's discovery schema: `summaryModelId`,
`auxiliaryModelId`, `region`, `codeSha256`, `state`, `lastUpdateStatus`, and
`revisionId`. Missing model settings may be null/empty. The snapshot must be
Active/Successful and contain a valid deployed code SHA-256 and revision.
`BEDROCK_REGION` defaults to `us-west-2`, matching the summarize entry point.
Set `TTOBAK_GO_BINARY` if Go is not installed at the documented default path.
The former single-fixture native smoke opt-in is rejected with migration guidance
because it did not retain all request/response evidence.

Each case uses the production limit of two continuations: at most three native
model requests. SDK retries are disabled so every attempt remains visible.
Recording happens at the SDK HTTP boundary and retains exact request/response
body bytes, response status, limited response headers and call order. Transport
failures are explicit. Usage sums every returned native response; exhausted,
refused, malformed and failed completions do not receive a quality pass.

Artifacts include:

- `deployment.json`: the discovery snapshot, copied before model calls.
- `<case>.call-01.request.json` and `.response.json`, followed by each additional
  call in order; a transport failure has an explicit failure record instead of
  a fabricated provider response.
- `<case>.native-evidence.json`: schema version 2, model/region, source and corpus
  hashes, production/evaluator source hashes, deployment configuration/package
  hashes, initial prompt hash, per-call hashes/order, and completion/output hash.
- `<case>.md` and `report.json`: postprocessed notes, usage, completion and the
  unchanged rubric results. Failed runs remain available for inspection.

Production source hashing includes backend runtime Go files and dependency
manifests. Evaluator hashing includes the evaluation and rubric Go files. The
report also records the checkout commit. A deployed package hash identifies the
observed package; this harness runs checkout source, not that Lambda package.
The report explicitly sets `deployedPackageExecuted: false`. Source equivalence,
live deployment acceptance and general accuracy still require separate evidence.

## Offline export and replay

Default unit tests never invoke a model. Request export executes the production
path until the first native request and then stops without a synthetic completion:

```bash
scripts/eval-note-quality.sh requests /absolute/new-request-directory
```

Exported requests are useful for inspection, but they have no responses and cannot
produce a quality pass. A single externally supplied response cannot stand in for
a complete native continuation run.

To regrade a version-2 live run, retain the same model settings and matching source
checkout, and write results into a separate new directory:

```bash
scripts/eval-note-quality.sh grade /absolute/evidence-directory /absolute/new-regrade-directory
```

Replay makes no network calls. It checks the source/model/deployment binding and
all call hashes, then regenerates **every** native request through production code
and compares its exact bytes before returning the recorded response. Missing,
extra, reordered or modified calls fail. The final postprocessed note must match
the recorded completion/output hash. Input evidence is never overwritten.
Hashes and SDK labels remain operator attestations, not cryptographic proof that
AWS executed the calls; evidence provenance must be reviewed independently.

## Historical single-call evidence

Archived outputs and hashes remain immutable. Their original format evaluated
one provider call through a canonical Anthropic prompt adapter. It did not qualify
the current final-model selection or continuation path. Current `grade` rejects
that format rather than silently treating it as native evidence.

For explicit historical analysis, use the source/model context required by the
archive and a separate output directory:

```bash
BEDROCK_MODEL_ID=historical-model-id \
  scripts/eval-note-quality.sh historical-grade /absolute/archive-copy /absolute/new-historical-report
```

Historical reports set `historical: true` and `currentAcceptance: false`, even
when their original rubric passes. The same evidence completeness checks and
rubric apply; no missing attested field is filled from an expected value.

The [September 12 probe](2026-09-12-note-quality-probe.md) and
[archived four-case live evaluation](evaluations/2026-09-12-note-quality/README.md)
remain historical evidence. Their passing checks are not a new evaluation of the
current deployed model, real audio, or current production package.
