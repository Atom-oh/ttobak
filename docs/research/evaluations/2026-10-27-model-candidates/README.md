# Meeting-note and translation model comparison

Status: historical candidate experiment, October 27, 2026. This record does not
approve a production model change or satisfy the deployed-acceptance gate.

GPT-6.1 Terra is the strongest candidate for reducing final-note latency in this
sample. Keep GPT-6 Luna for live translation: it was faster, and Terra repeatedly
changed a deadline into a duration. Sonnet 5.5 completed both workloads, but this
experiment did not establish a factual-quality advantage over the current models.

## Measured results

Seven fictional meeting cases ran twice per model through the current-source
production summarization pipeline. Twelve fictional Korean-to-English and
English-to-Korean translation cases also ran twice per model. No customer meetings
or customer repositories were read or changed.

| Final notes | GPT-6.1 Sol | GPT-6.1 Terra | Sonnet 5.5 |
| --- | ---: | ---: | ---: |
| Completed calls | 14/14 | 14/14 | 14/14 |
| Median wall time | 13.57 s | 4.45 s | 8.05 s |
| Mean wall time | 18.55 s | 5.07 s | 11.53 s |
| Maximum observed wall time | 52.18 s | 9.38 s | 24.48 s |
| Fixed automated case passes | 12/14 | 12/14 | 12/14 |
| Fixed automated checks | 86/88 | 86/88 | 86/88 |
| Reported input tokens | 22,758 | 22,758 | 38,660 |
| Reported output tokens | 15,558 | 12,382 | 23,034 |

Terra's median latency was approximately 67% lower than Sol's. All models retained
the tested amounts, units, negations, proposal/decision boundaries, agreed owners
and deadlines, and separation of personal notes from spoken evidence.

Each model's two automated failures were the same section-placement restriction
in the quoted-instruction case. The rubric requires the unapproved deployment
status inside the decisions section. Every generated note instead retained that
status in the overview or discussion and listed only the actual agreed cost cap
as a decision. Inspection found no approval reversal or invented attacker task.
The original failed findings remain unchanged; these are not 100% automated passes.

The longer fixture contains 28 timestamped segments and 24 operational topics.
All six outputs retained its 48 distinct A/B sample numerals and the final budget,
deployment cancellation, corrected owner and deadline. Sol took 50.69/52.18 seconds,
Terra 9.38/8.94 seconds, and Sonnet 24.48/23.49 seconds.
Sol and Terra retained links for all 24 topic segments; Sonnet grouped the topics
under two representative segment links. This is a citation-granularity difference,
not evidence that Sonnet lost the associated numeric facts. The production prompt
permits representative timestamps for grouped facts.

| Live translation | GPT-6 Luna | GPT-6.1 Terra | Sonnet 5.5 |
| --- | ---: | ---: | ---: |
| Completed within six seconds | 24/24 | 24/24 | 24/24 |
| Passed production text validation | 24/24 | 24/24 | 24/24 |
| Median wall time | 0.49 s | 0.79 s | 1.04 s |
| Maximum observed wall time | 0.81 s | 1.36 s | 2.33 s |
| Observed deadline-meaning errors | 0 | 2 | 0 |
| Reported input/output tokens | 3,346/648 | 3,346/827 | 5,068/943 |

In both repetitions of `ko-context`, Terra rendered a Korean commitment meaning
"by Friday" as "through Friday." Luna and Sonnet preserved the deadline.
Production validation accepted all three, illustrating that formatting validation
does not establish semantic fidelity. Other inspected outputs preserved numeric
corrections, percentage points, conditions, negation, identifiers, quoted attack
text, line breaks and placeholders. These observations are host inspection of this
small corpus, not a general translation-accuracy score.

## Configuration and comparison boundaries

The source snapshot is GitHub main commit
`2196ede863f4d9dac79112a7368e9fc8aa1640da`. It was extracted separately from
the user's modified checkout. Deployment configuration was read independently:
the final-note model was `global.openai.gpt-6.1-sol`, the translation model was
`global.openai.gpt-6-luna`, and the live translation timeout was 6,000 ms.
Sonnet 5.5 was already configured for transcript refinement and terminology extraction.
This experiment invoked local current-source code, not the deployed Lambda package.

Calls used Bedrock `InvokeModel` in `us-west-2`, with exact global inference
profiles and SDK retries disabled. All summary providers received identical system
and user text and a 16,000-token output ceiling. Provider reasoning defaults were
retained, rather than normalized to a supposedly equivalent reasoning budget.
The source selection, completion checks, bounded continuation, citation processing
and synthetic repository remained the production implementations.

OpenAI translation used the production request builder, 2,048 completion tokens,
`reasoning_effort=low`, the six-second deadline, strict completion parsing and
production text validation. Sonnet used an experimental Anthropic request adapter
with the same prompt, output ceiling and deadline, and
`thinking: {"type": "between_tools"}`. The provider accepted this setting and all
24 requests completed. The production translation service currently accepts only
OpenAI request formats; Sonnet requires a provider adapter and corresponding model
permissions before it can be used there. Changing the model ID alone would select
the Amazon Translate fallback.

The initial Sonnet translation adapter incorrectly requested
`thinking: {"type": "disabled"}`. Bedrock rejected all 24 attempts with HTTP 400 and
explicitly required `between_tools`. These configuration failures are preserved
separately and excluded from the completed-model quality and latency comparison.
The corrected Sonnet batch ran later; this is not a simultaneous controlled
load test. OpenAI and corrected Sonnet translation responses reported zero cached
input tokens. Summary cache counters were not retained.

There were 114 successful model calls and 24 rejected configuration probes.
Repetition two rotated the summary/OpenAI translation provider order. Each stream
ran sequentially, with summary and translation streams running concurrently.
Wall time includes client, network and provider work. Two repetitions of seven
or twelve inputs do not establish p95 latency, sustained throughput, an independent
sample of 14/24 distinct inputs, or performance on realistic hour-long meetings.
Diagram-heavy notes, attachments, additional languages and adversarial long-context
inputs were not covered.

## Price evidence

| Standard uncached tokens, per million | Input | Output | Verified basis |
| --- | ---: | ---: | --- |
| GPT-6.1 Sol | $2 | $10 | Official OpenAI direct API |
| GPT-6.1 Terra | $1 | $5 | Official OpenAI direct API |
| GPT-6 Luna | $0.10 | $0.50 | Official OpenAI direct API |
| Sonnet 5.5 | $2 | $10 | AWS Oregon global standard Marketplace price list |

Terra's published direct-API input/output rates are half Sol's. Its measured
summary output-token total was also lower. The fetched AWS price list did not
establish the three OpenAI models' Bedrock rates. These facts cannot support a
claim of confirmed AWS savings or an estimate of the actual invoice. Sonnet's
AWS price list was published October 12, 2026, with the selected rates effective
October 1. Pricing modes, caches and long-context premiums require separate
consideration.

## Evidence and reproducibility

[metrics.json](metrics.json) contains the exact measurements and unchanged failures.
[evidence.zip](evidence.zip) contains the synthetic cases, generated notes,
summary call metadata/hashes, reconstructed first requests, translation requests
and responses, the two experiment harness versions, deployment observation,
selected pricing evidence and the offline audit.
[evidence-sha256.json](evidence-sha256.json) pins the archive and metrics.

The summary harness persisted response hashes and final parsed notes, but omitted
the original provider response bodies. Consequently those responses cannot be
replayed through the completion parser or independently checked for reasoning/cache
counters. This recording limitation is explicit: the archive is operator-recorded
experimental evidence, not complete native deployed-acceptance evidence.
The first requests were reconstructed without model calls, and their hashes,
original source/model bindings, all 42 output hashes and original rubric findings
were reproduced successfully. Provider-independent prompt equivalence was also
checked. No response bodies were fabricated to fill the missing artifacts.

The experiment harness and audit were kept outside application source. Targeted
note-quality, provider and translation tests passed; the opt-in experiments were
skipped in that verification run. The 42-note offline audit passed independently
of further paid calls. The live experiment finishing successfully means its
measurements completed, not that every fixed rubric finding passed.

Official references were fetched on October 27, 2026:

```text
https://developers.openai.com/api/docs/models/gpt-6.1-terra
https://developers.openai.com/api/docs/models/gpt-6.1-sol
https://developers.openai.com/api/docs/models/gpt-6-luna
https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-5-5.html
https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/us-west-2/index.json
```

Recommendation: evaluate Terra for final-note rollout using a larger realistic
corpus and the existing deployed-acceptance process. Keep Luna for live translation.
Retain Sonnet's existing auxiliary duties; this sample does not justify selecting
it as the default final-note or translation model.
