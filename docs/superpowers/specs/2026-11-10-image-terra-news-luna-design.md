# Image and news model cost migration

**Status:** Historical implementation record (2026-11-10); delivery and runtime acceptance are pending.

The user selects GPT-6.1 Terra for uploaded-image analysis and GPT-6 Luna for
news summaries. Both run through the existing authenticated Bedrock runtime.

Image classification and classification-specific analysis use an independent
`BEDROCK_IMAGE_MODEL_ID=global.openai.gpt-6.1-terra`; shared auxiliary Claude and
meeting-summary settings stay independent. Native OpenAI multimodal requests
preserve diagram Mermaid and whiteboard/screenshot/photo Markdown output.
Disable reasoning for these bounded extraction calls. Reject blank or refused
results; retain the existing usable partial analysis behavior. Bound uploaded
image reads to 16 MiB and model responses to 128 KiB.

Legacy news briefings and structured news publication both select
`global.openai.gpt-6-luna`. Converse uses the provider-supported
`reasoning: {effort: none}` field. Preserve source evidence, injection defenses,
quality validation, two-call repair ceilings, current-source publication guards,
and the 100,000-input-token bound. Update metering to the verified global
on-demand USD 0.10 uncached input / USD 0.125 cache write / USD 0.01 cache
read / USD 0.50 output per million tokens. Count cached input too and use the
highest USD 0.125 input rate conservatively, with existing
run/month budget limits unchanged.

Grant only the exact new model/profile ARNs in each caller's existing region.
Retain existing image/crawler Claude permissions during rollout, because the
shared crawler still has Claude workloads and old image invocations may be
running. The structured worker retains its exact outgoing Opus grant during
the transition; this is a permission overlap, never a runtime model fallback.
Do not enable news pipeline modes, schedules or legacy writers.

Update current technical references and the public guide's model table and
image-path explanation. Existing genuine UI captures remain valid. Deployment
requires relevant tests, current-head PR review, content quality score >=85,
and production configuration verification.
