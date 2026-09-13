# ADR-033: Meeting cost and sizing simulations

- Status: Accepted; uses ADR-013 transcript anchors and ADR-027 media prefixes.
- Decision date: 2026-08-18.
- Code checked: 2026-09-13; configured isolation is not verification of a deployed execution role or network.

## Original decision and rationale

Turn quantitative meeting requirements into executed, inspectable Python calculations. Choose owner-triggered extraction, confirmation, then execution rather than automatic simulations on every meeting. Keep a job entity separate from attachments, and use polling instead of extending QA token-stream WebSockets.

## Current behavior and trust boundary

- Go extracts draft requirements from the selected A/B transcript, using segments only when they match that text; note-body fallback has no transcript anchors. The owner of a completed meeting submits requirements and two or three options; `validateSimRequirements` checks known keys, finite numeric ranges/enums, duplicates, required values, and text length/character limits. The form is UX; server validation is mandatory.
- The Python worker receives identifiers, requirements, and options, **not raw transcript text**. JSON still contains bounded free-text labels and option names/descriptions. JSON encoding and character limits do not prevent prompt injection; extracted values and option text can influence generated code.
- `SimCodeInterpreter` explicitly uses `SANDBOX`, with a construct-created execution role to which this stack attaches no policies. Preserve **zero execution-role permissions plus SANDBOX**: network mode alone is not an AWS-authorization boundary. The separate orchestrating Lambda role has pricing, storage, model, and session permissions; generated code must not receive them.
- The import regex is defense-in-depth, not a sandbox. Never relax network/IAM isolation because a source scan passed. The worker uses the configured interpreter identifier, whose live configuration must be checked independently.
- The worker fetches a price snapshot through the `us-east-1` Pricing client and writes requirements/options/prices into the session. The implemented snapshot covers selected Lambda, API Gateway, DynamoDB, S3, and CloudFront dimensions, not general EC2/RDS/ALB comparisons. Per-service lookup failures are recorded while other prices remain available.
- Code generation has at most three execution attempts and retains code, report, charts, and prices. Prompts require snapshot-based prices and a verification banner; execution success/report presence do **not** mechanically validate arithmetic, SKU selection, banner compliance, or absence of invented prices.
- `MEETING#{meetingId}/SIMRUN` is a singleton, independently of `Meeting.Status`. A conditional transaction checks meeting existence and availability of the slot. Go/Python field updates require matching `simRunId`; stale workers cannot overwrite a newer run's row.
- Reads identify queued/running runs older than 20 minutes and **attempt to persist** an error with the matching-run condition. On successful persistence retries can proceed; a failed persistence is logged and the fetched state remains. The earlier description of display-only reconciliation is obsolete.
- Artifacts use `images/{owner}/{meeting}/sim/{run}/` and `files/{owner}/{meeting}/sim/{run}/`. These retain ADR-027's prefixes. Image processing is triggered by `ImageUploadCompleted`, not every S3 image write. The UI polls at five-second intervals.

## Tradeoffs, policy, and accepted residual risks

Executed code is auditable but can be confidently wrong or nondeterministic. Human review of requirements, chosen SKUs, assumptions, and results remains necessary. Bounded text can still steer code; isolation limits capability, not output integrity or wasted compute.

The existing `PricingReadOnly` statement uses `Resource: "*"` without a condition for two read-only price APIs. This narrowly recorded exception does not permit unrelated unconditional wildcards and must never extend to the interpreter's role. Giving generated code AWS/network access, repurposing `Attachment`, and adding Step Functions/WebSocket orchestration were rejected.

## Evidence

- [Validation/job lifecycle](../../backend/internal/service/sim.go), [Go tests](../../backend/internal/service/sim_test.go), [source-selection tests](../../backend/internal/service/transcript_source_policy_test.go), [conditional persistence](../../backend/internal/repository/sim.go).
- [Worker](../../backend/python/sim/handler.py), [code generation](../../backend/python/sim/codegen.py), [pricing](../../backend/python/sim/pricing.py), [Python tests](../../backend/python/sim/test_handler.py).
- [Interpreter and worker IAM](../../infra/lib/ai-stack.ts), [worker/event configuration](../../infra/lib/gateway-stack.ts).
