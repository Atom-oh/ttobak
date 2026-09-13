# Public QA pre-activation evidence — 2026-09-13

Authenticated synthetic checks on **2026-09-13** passed saved-note freshness,
private/shared binary retrieval, source-change rejection, public browser routing,
scheduled provider deletion and binary-fixture version cleanup. Subsequent checks
qualified partial PDF attachments and current Live input alongside saved sources.

**Prerequisite observations: PASS.** This historical evidence snapshot records
the dated results below. The original [results.json](results.json) is unchanged;
[qualification.json](qualification.json) adds later receipts and their limits.
Canonical activation remains **PENDING** normal release AI/CI gates and
deployment, followed by post-activation acceptance.

## Deployment observation

The source-match receipt observed at **2026-09-13 12:12:44 UTC** reports all **24
runtime Python files** matching reviewed commit
`11bd828fbbc3f4d5d917962a121948dbcc75a261`. It reports Active/Successful state and
a 300-second timeout. The async UI flag was not true at that observation; direct
REST job acceptance is recorded separately from UI activation. The receipt covers
runtime source files and the reported Lambda configuration.

A separate **12:28:17 UTC** configuration observation found the external S3 data
source AVAILABLE with whole-bucket coverage (empty `inclusionPrefixes`), covering
`canonical/v1/`, `manual-kb/v1/` and `shared-kb/v1/`. The index worker and
coordinator were both `manual-only` at that observation.

The later source-match receipt at **13:05:10 UTC** reports all **25 runtime Python
files** matching reviewed commit `921c2b3e0e0b5c97ab684a2adfe9a1691bcfbe2a`,
with Active/Successful state and a 300-second timeout. The async UI flag remained
false in this receipt (`ttobak-qa-latest-deployed-code-check.json`).

## Completed observations

| Case | Recorded outcome | Operator-held result artifact |
|---|---|---|
| Saved-note baseline | Owner read 200; unrelated-user meeting/read QA 404; public WS recalled the synthetic note marker with meeting attribution. | `baseline-results.json` |
| Note freshness and sharing | Conditional update advanced revision; same-session response used V2, not V1. Read-only sharing enabled access; revocation removed prior facts/attribution and returned 404. | `freshness-results.json` |
| Negative WS authentication | Bad JWT through CloudFront: 403. Valid JWT sent directly to the origin without origin proof: 401. | `ws-negative-results.json` |
| Genuine browser login and QA | Sidebar Chat and Live preparation each used one WS request and zero REST-answer fallbacks; current marker displayed, and Live cited output was copied into preparation. | `public-browser-results.json` |
| Private PDF V1 | Owner recalled the current marker/revision; the unrelated authenticated user received neither fixture marker nor current-revision attribution. | `binary-private-v1-qa-results.json` |
| Private PDF V2, corrected turn | The later registered-tool/natural-question case recalled V2 with current-revision attribution. See the initial-attempt limitation below. | `binary-private-v2-registered-tool-qa-results.json` |
| Authenticated-shared DOCX V1 | Both authenticated validation roles recalled the current marker/revision. | `binary-shared-v1-qa-results.json` |
| Public async private V2 and shared V1 | Duplicate POST with the same ID was idempotent; foreign result GET returned 404; owner result GET returned current binary evidence/revision. | `async-private-v2-results.json`, `async-shared-v1-results.json` |
| Shared V1 result after overwrite | The previously completed job's GET returned **409 SOURCE_CHANGED** and suppressed the cached answer. Current V2 acceptance is recorded separately below. | `async-shared-v1-overwritten-results.json` |
| Shared DOCX V2 | Owner WS and public REST-job cases returned the current V2 marker/revision; REST also verified same-ID idempotency and foreign-result denial. | `binary-shared-v2-qa-results.json`, `async-shared-v2-results.json` |
| Original-file deletion | Private original deleted through the authenticated public owner API; shared original deleted through a conditional SDK operation. | `binary-delete-receipts.json` |
| Completed jobs after deletion | Private-V2 and shared-V2 result GETs each returned **409 SOURCE_CHANGED**, suppressing cached answers. | `async-private-v2-deleted-results.json`, `async-shared-v2-deleted-results.json` |
| Retained WS histories after deletion | Both file cases completed without V1/V2 markers or attribution to deleted revisions. | `binary-deleted-qa-results.json` |
| Scheduled provider deletion | Both jobs reached DELETED; current inventories were empty; original/V1/V2 provider identifiers were all NOT_FOUND. | `binary-deletion-observation.json` |
| Binary fixture version cleanup | For each private/shared case: original data versions/markers removed 2/1; snapshot versions/markers removed 4/4. Receipts report no remaining versions. | `binary-version-cleanup.json` |
| Direct Chat route and reload | Both returned 200 with Assistant input visible; no QA question was submitted. | `public-chat-route-results.json` |
| Pre-activation configuration | External S3 source AVAILABLE with whole-bucket coverage; index worker/coordinator manual-only. | `preactivation-config.json` |

Binary observations followed normal scheduled snapshot/provider processing, as
recorded in the operator status.
The earlier browser artifact covers sidebar Chat; direct `/chat` navigation and
reload are now established by the separate route artifact above. No recording or
audio upload was part of these browser cases. The operator record reports
reauthentication at **12:22 UTC** using the same two validation users without
group grants.

## Later qualification and run closure

| Case | Recorded outcome | Operator-held result artifact |
|---|---|---|
| Closed validation run cleanup | Two validation users deleted; 43 DynamoDB delete requests recorded; remaining scoped user/meeting/session rows and attachment versions both zero; local credentials removed. This closes the earlier run only. | `final-cleanup.json` |
| Actual partial PDF attachment | Uploaded PDF extraction reported `partial`, `hasResult=true`, `complete=false`. Bounded reading returned continuation; foreign access returned 404. Public QA cited the partial attachment with explicit coverage. | `attachment-pdf-results.json` |
| Attachment identity and retry | Server attribution used `resourceKind=meetingAttachment` and `resourceId`. The checker was corrected against the recorded response without another QA call. During retry, `current=false`, attempt status `running`, and retained-result use were observed; retry completion is not claimed. | `qa-attachment-pdf-partial.json`, `attachment-pdf-results.json` |
| Live input and saved sources | Neutral checks passed separation of current Live input and saved-source evidence, retained labeling, and use of the corrected latest code without old values. | `live-context-neutral-results.json` |
| Five canonical canaries prepared | Genuine API creation/readback completed at **13:40 UTC** for a notes-only meeting, personal/account Markdown notes, and personal/account PDF-backed documents. The meeting was marked completed without audio. | `canonical-manifest.json`, private `ledger.json` |
| Configuration after preparation | At **13:40:56 UTC**, worker and coordinator control were `manual-only`; the external source was AVAILABLE with whole-bucket coverage. | Current-run `preactivation-config.json` |

The PDF fixture was preflighted through the local parser. The earlier oversized
Markdown fixture legitimately failed with `LIMIT_EXCEEDED` and no result; that
file was cleaned with the closed run. It is not evidence of partial extraction.

The new run's initial Live check failed after a question named a nonexistent
rollout-codename field and used an age-labeled code (`live-context-results.json`).
The later neutral case changed multiple inputs; its pass does not establish a
single cause for the initial outcome.

The prepared canaries and their manifest establish source creation before
activation, not canonical indexing, provider visibility, or post-activation
acceptance. The new run's two users remain active for those checks. Their fixtures
and sessions still require cleanup; the earlier run's closure is not global
cleanup completion.

## Initial private-V2 limitation

The initial V2 turn returned a refusal and invoked no tool
(`qa-binary-private-owner-v2.json`). Its `answer_complete` event was a transport
outcome, not successful binary acceptance. The harness named the unregistered
`search_knowledge`. A later natural-language question using the registered
`search_knowledge_base` returned V2/current-revision evidence. Multiple changes
between the turns prevent isolated causal attribution. No product-prompt change
was recorded.

## Release status and remaining work

| Gate | Status |
|---|---|
| Public current-source consumer scenarios, including shared V2 and deletion rejection | **PASS** |
| Scheduled provider deletion/current-source absence and binary old-version cleanup | **PASS** |
| Consumer/provider/binary-fixture prerequisite evidence | **PASS** |
| Earlier validation run cloud cleanup and local credential removal | **PASS — closed run only** |
| Partial PDF attachment and neutral Live-context qualification | **PASS — recorded scope above** |
| Five genuine API canaries prepared before all-mode | **PASS — preparation only** |
| Normal release AI/CI gates and canonical activation deployment | **PENDING** |
| Canonical post-activation acceptance | **PENDING** |
| Current-run fixture/user/session cleanup | **PENDING — after post-activation checks** |

Complete the normal release AI/CI gates, deploy all-mode, then verify canonical
create/edit/delete/revoke and provider outcomes. Binary-fixture version cleanup
is recorded above. Complete remaining canonical-fixture, session and
validation-user cleanup after post-activation checks, and retain verification
receipts.
Follow the [bootstrap](../../../runbooks/knowledge-index-bootstrap.md) and
[QA rollout](../../../runbooks/qa-current-source-rollout.md) gates.

## Evidence handling

[Original normalized results](results.json) and the additive
[qualification receipts](qualification.json) retain case outcomes, receipt
metadata, artifact basenames and SHA-256 hashes. The qualification record
distinguishes closed-run receipts from current-run receipts without copying
identities or source content. Raw results remain operator-held outside the
repository. Emails, user/job identifiers, credentials, JWTs, URLs, full answers
and DOM dumps are excluded.
