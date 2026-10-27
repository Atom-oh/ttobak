# Model selection at deployment

Meeting notes and high-quality translation have independent deployment settings.
The CDK app resolves one configuration and passes it to both GatewayStack's
Lambda environments and AiStack's exact model/profile IAM grants.

## Settings

| Environment variable | CDK context | Default |
|---|---|---|
| `MODEL_DEPLOYMENT_MODE` | `model-deployment-mode` | `standard` |
| `MODEL_MEETING_SUMMARY` | `model-meeting-summary` | Selected preset |
| `MODEL_TRANSLATION` | `model-translation` | `openai.gpt-6-luna` |

| Preset | Final meeting notes | High-quality translation |
|---|---|---|
| `standard` | `openai.gpt-6.1-sol` | `openai.gpt-6-luna` |
| `terra` | `openai.gpt-6.1-terra` | `openai.gpt-6-luna` |
| `sonnet` | `anthropic.claude-sonnet-5-5` | `openai.gpt-6-luna` |

Individual selections override the preset. For each setting, nonempty CDK context
overrides the environment; blank or absent values inherit the next level.
The legacy spelling `model-meeting-summery` is also accepted as a context alias.
Conflicting spellings fail synthesis. Prefer `model-meeting-summary` in new usage.

Bare IDs are normalized to `global.` inference profiles. Existing `global.` IDs
are accepted unchanged. Only exact OpenAI GPT and Anthropic Claude IDs are
accepted for final notes. High-quality translation currently accepts OpenAI GPT
only because its native request format is OpenAI-specific. Sonnet translation
requires a separate backend adapter and cannot be enabled with this setting.
An unknown preset, unsupported format, ARN, resource wildcard or path fails
before synthesis. Syntactic validation does not prove that a new model exists,
has a global profile, or is accessible in the invocation region.

The selected IDs become `BEDROCK_SUMMARY_MODEL_ID` on `ttobak-summarize` and
`BEDROCK_TRANSLATE_MODEL_ID` on `ttobak-api`. These deployment variables do not
change auxiliary/refinement/image/QA models, request parameters, rate limits,
translation's six-second default deadline or Amazon Translate fallback.

## Local synthesis

Build the HTTP MCP bundle and install the infrastructure dependencies as described
in [deployment](deployment.md). Examples from the repository root:

```bash
MODEL_DEPLOYMENT_MODE=terra npm --prefix infra run cdk -- synth
```

```bash
MODEL_MEETING_SUMMARY=openai.gpt-6.1-terra \
MODEL_TRANSLATION=openai.gpt-6-luna \
npm --prefix infra run cdk -- synth
```

```bash
npm --prefix infra run cdk -- synth \
  -c model-meeting-summary=openai.gpt-6.1-terra \
  -c model-translation=openai.gpt-6-luna
```

Before selecting a model, verify its Bedrock global inference profile and access
in the configured invocation region. Review the synthesized Gateway Lambda IDs
and the AiStack `InvokeFinalSummaryModel` / `InvokeInterpreterModel` resources.
Both resources name the exact chosen foundation model and global profile;
region wildcards support cross-region routing, not additional model families.
Other pre-existing auxiliary model grants retain their separate scope.

## GitHub Actions and persistence

`Deploy Infrastructure` exposes optional manual inputs:
`model_deployment_mode`, `model_meeting_summary` and `model_translation`.
Empty inputs inherit the corresponding repository variables, then CDK defaults.
Nonempty manual inputs override repository variables for that run only.
The same workflow-level environment reaches the synth/test prerequisite and the
release coordinator; deployment cannot bypass the prerequisite job.

For persistent selection across push deployments, set GitHub repository variables
`MODEL_DEPLOYMENT_MODE`, `MODEL_MEETING_SUMMARY`, and/or `MODEL_TRANSLATION`.
For example, set the mode to `terra` and leave individual variables unset to
use Terra/Luna. An existing explicit `MODEL_MEETING_SUMMARY` continues to override
a new mode; clear it if the preset should control final notes.
Record the selected model IDs alongside the source revision and deployment receipt.
Model IDs are nonsecret configuration.

A manual input alone is temporary: a subsequent push uses repository variables
or defaults. Changing a Lambda environment directly is also temporary and does
not update IAM; the next CDK deployment restores the synthesized selection.
Use the existing release coordinator and preserve its explicit stack order,
origin/readiness gates and KnowledgeStack exclusion. This feature does not enable
disabled workflows or itself constitute a deployment authorization.

## Runtime acceptance and rollback

Changing models updates function configuration and publishes a new live version.
AiStack and GatewayStack updates are not atomic. Before changing persistent
selection, drain active summaries/translation calls and use the normal controlled
release window: IAM grants change before the new Lambda configuration is applied.
Keep the prior exact selection and source revision for recovery.

Verify the deployed `live` versions and their model variables, then use synthetic
current-source notes and translation requests to check completion, attribution,
negation, deadlines, fallback and account-sharing safety. Successful synthesis or
a model benchmark is not deployed acceptance. Existing summary publication and
privacy guards remain required.

Rollback restores the previous environment selections or repository variables
and reruns the same controlled deployment path. Restore both IAM and Lambda
configuration together; do not roll back only one function's environment.
Clearing all selections restores Sol/Luna defaults.

## Cost evidence

The [2026-10-27 synthetic comparison](../research/evaluations/2026-10-27-model-candidates/README.md)
used seven note fixtures twice per model.
Terra used 22,758 input and 12,382 output tokens; Sonnet used 38,660 input and
23,034 output tokens. Their median note latencies were 4.45s and 8.05s.
This supports lower token usage and latency for Terra on that corpus, not a
universal quality ranking or a verified production billing difference.

The checked OpenAI direct-API Terra standard prices were $1 input / $5 output
per million tokens. AWS's us-west-2 Bedrock catalog listed global Sonnet 5.5
at $2 input / $10 output per million tokens. The catalog did not establish Terra's
Bedrock price. Applying those different-provider reference rates to the measured
uncached token totals gives $0.084668 and $0.307660 for the fourteen calls,
respectively. These are illustrative estimates, not comparable Bedrock invoices;
summary cache counters were not retained. Confirm Terra's Bedrock rate before
using the difference in a production budget.

Official references checked on 2026-10-27:

```text
https://developers.openai.com/api/docs/models/gpt-6.1-terra.md
https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/us-west-2/index.json
https://docs.aws.amazon.com/bedrock/latest/userguide/global-cross-region-inference.html
```
