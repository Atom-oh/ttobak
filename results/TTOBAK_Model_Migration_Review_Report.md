# Image and news model migration review

Status: verification record, 2026-11-10; deployed acceptance is tracked separately.

## Content quality gate

Reviewed `docs/user-guide/index.html`, README and current technical model references
with the installed AWS Content Plugin `agents/content-review-agent.md` rubric,
inline in Codex. Score: **88.5/90, PASS**, above the mandatory >=85 gate.
The existing accepted duplicate Account/People viewport remains the only warning.
All other category scores and limitations from
`TTOBAK_User_Guide_Review_Report.md` remain applicable. This migration adds no
images or screenshot claims. Playwright MCP was tried again and could not launch
its absent Chrome distribution; its 10-point category is exempt. Local
Playwright/Chromium supplied 23 fresh browser checks separately.

The changed table shows Terra for image classification and extraction, and Luna
for both news paths. It explains the four image categories and Mermaid/text
outputs, rather than calling the feature an unexplained upload path. The two
cells were checked against actual code and Lambda configuration producers.
FHD/4K/tablet/mobile layouts have no page overflow; all 36 genuine images load,
all remain printable, themes/search work and the model table is readable without
JavaScript. The model chapter screenshot was visually inspected. Browser results
and all 40 updated artifact hashes are in
`TTOBAK_Model_Migration_Guide_Checks.json`. Earlier PDF/browser artifacts remain
historical verification of their own identified bytes.

Source-omission check: both requested model changes and the image-path explanation
are included. Other chapters and genuine captures retain their reviewed source.
No new statistical savings claim, production-data example or unverified model
comparison was introduced.

## AWS security review

Applied the loaded Ops Security Audit procedure to the changed IaC. No new mandate
violation was found. The new image grant permits only InvokeModel on the exact
Terra global foundation model, us-west-2 foundation model and invocation profile.
The shared crawler grant similarly scopes Luna to the stack region. The structured
worker adds the same three Luna resources and retains its exact outgoing Opus
resources for running invocations and rollback. Existing image Claude permissions
and shared technical-source crawler grants remain; none is a runtime fallback.
No model grant uses a wildcard model name, wildcard principal or unconditioned
`Resource: "*"`. No ingress, public route, resource replacement, secret environment
value or source activation change is part of this migration.

## Transport and behavior evidence

- Both classification and extraction requests send Terra multimodal data URLs,
  independent of summary selection, with bounded output and reasoning disabled.
  Tests preserve usable partial auxiliary text and reject empty/refused/oversized
  results. Empty, unsupported and >16 MiB uploads cannot invoke the model.
- A generated red PNG passed actual native Terra InvokeModel in us-west-2;
  the response was `Red.`, finish `stop`, 30 input/6 output tokens, no reasoning.
- A short synthetic Luna Converse probe verified `reasoning.effort=none` in Seoul.
  Native Chat `reasoning_effort` is rejected by Converse and is not sent there.
- A fictional bank article passed the unchanged structured enrichment gate with
  Luna: 674 summary characters, 3 key facts, 6 evidence entries and no repair.
  This establishes synthetic quality/transport, not customer-source acceptance.
- Converse exposes cache input separately. The adapter includes cached input in
  both budget and settlement; metering conservatively charges all input at the
  maximum verified USD 0.125 rate, output at 0.50, per million tokens. Run/month
  limits and the 100,000-input-token cap remain. Cached usage has a regression test.

## Validation

Go tests include all command packages, vet and Linux ARM64 API/image builds.
All 651 crawler tests passed, including source/quality/injection/runtime/metering
contracts. All 334 CDK tests passed; synth and the three-stack read-only diff
completed. Documentation validation and all 18 docs/media unit tests passed.
Latest-head independent CI review and required checks precede merge; production
Lambda/IAM/configuration and Pages bytes will be verified after deployment.
