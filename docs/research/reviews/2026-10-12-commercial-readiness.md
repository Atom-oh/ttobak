# Commercial meeting notes, MCP and security review — 2026-10-12

Historical, point-in-time assessment of source revision
`640d297707b22fcc3959b48668b210c673750fa1`. This report records evidence and
recommended release gates; it is not a deployment or compliance certificate.
The review began at `8c187a3`; changes merged during review were rechecked against
`640d297`. An unrelated working-tree change promoting the existing STS dependency
in `backend/go.mod` was preserved. No application fixes, deployments, private
meeting reads or paid model calls were performed by this review.

## Release judgment

The implementation has strong foundations for a controlled pilot: verified
authentication, explicit sharing, source-aware conditional summary publication,
recording recovery, action-analysis state, current-source QA contracts and bounded
MCP meeting reads. Broad commercial sign-off is not supported yet. Confidentiality
defects, incomplete refinement validation and mismatched quality evaluation need
remediation. Authenticated client acceptance, real-audio quality and production
failure recovery also need measured evidence.

This is a baseline product assessment, not a PR review. Existing accepted limits
are identified separately and are not presented as new regressions.

## Confirmed release findings

P1 means resolve before expanding access to confidential customer material.
P2 means resolve or explicitly qualify the relevant feature before commercial
release. Priorities reflect this product's confidentiality and reliability needs;
they do not assert that an incident occurred.

| ID | Priority | Finding and failing scenario | Source evidence and recommended change |
|---|---|---|---|
| R1 | P1 | A private meeting can include a private predecessor's context while generation runs, then acquire a reader before publication. That reader can receive predecessor-derived content without predecessor access. | `buildLinkedMeetingContext` checks collaborators before generation, but `batchSummaryFields` and `SaveMeetingSummary` do not bind sharing state, linked-source revision or an authorization revision. Direct sharing is permitted during processing. Atomically advance and condition on an authorization revision; pin derived-source dependencies and define later-sharing behavior for already generated content. |
| R2 | P1 | The HTTP origin verification secret is a literal in tracked CDK configuration. Repository/template readers can reproduce the origin proof. JWT checks still apply. | `infra/cdk.json` and its propagation through `infra/bin/infra.ts`, `GatewayStack` and `FrontendStack`. Move the proof to Secrets Manager, rotate the disclosed value, and verify direct-origin rejection. Requiring a nonempty secret does not fix disclosure. The secret value is intentionally omitted here. |
| R3 | P1 | A public-document access request writes its reusable share capability to logs. A log reader can use that capability until it is revoked. | API `middleware.Logger` runs before `/api/public/docs/{token}`; chi's logger includes `RequestURI`. `ResolvePublicShare` checks token identity but has no token expiry. `CreateUserDocPublicShare` also logs a token on orphan-cleanup failure. Redact capability paths, remove token-bearing error messages, review log access/retention and rotate affected links. The five-minute signed download URL does not expire the share token. |
| R4 | P2 | Malformed refinement output places meeting-derived text in logs. | `BedrockService.refineChunk` embeds the first 200 output characters in its parsing error; sequential and parallel refinement log that error. Log categories, sizes, chunk indices and correlation IDs instead of generated text or uncontrolled provider messages. |
| R5 | P2 | Valid refinement JSON can contain impossible timestamps and omit source speech, yet replace the transcript. | `refineChunk` decodes JSON and checks cross-speaker merges but does not validate timeline bounds/order or source coverage. A synthetic two-utterance, four-second input accepted one segment with `startTime=9999` and `endTime=-1`. Validate completion, text, schema, timeline and coverage; fall back to raw segments and expose degraded output. |
| R6 | P2 | The note-quality workflow can pass while evaluating a different model from final notes. | `evaluate-notes.yml` reads `BEDROCK_MODEL_ID`; `note_quality_eval_test.go` invokes `ClaudeOpusModelID`. Production resolves `BEDROCK_SUMMARY_MODEL_ID`, configured as GPT-6.1 Sol. Exercise the effective production model and completion/continuation path; record model, prompt, source and deployed-package hashes. |
| R7 | P2 | Local MCP OAuth callbacks accept an invalid-state error before state validation, return unescaped error HTML and terminate login. The listener has no explicit loopback binding. | `CognitoAuth.waitForCallback` and `html` in `mcp-server/src/auth.ts`. Bind loopback, validate every callback state before handling its result, escape text, handle listener failures and serialize login. Synthetic reproduction established reflected HTML and login disruption, not account takeover. |
| R8 | P2 | Stdio deployment B reuses deployment A's cached token and can forward it to B. | `CognitoAuth` uses one `~/.ttobak/tokens.json` regardless of issuer, client or API. Namespace credentials by deployment/profile, validate cached claims and display the signed-in identity. Restrictive creation permissions do not provide deployment isolation. |
| R9 | P2 | HTTP MCP cannot read a valid 40KB document: it returns `RESULT_TOO_LARGE` and suggests narrowing, but the read has no cursor or range. | `get_document` schema/handler and HTTP result cap in `mcp-server/src/index.ts`. Markdown creation accepts up to 300KiB. Add authenticated, revision-bound document pages; extend bounded reads to catalogs, briefs and vault export. Preserve the cap. |
| R10 | P2 | All 30 HTTP tools use the same `openid email profile` grant, including destructive and sharing mutations. A connector cannot receive a server-enforced read-only profile. | `http-auth.ts`, `createMcpServer` and `AuthStack`. Add dedicated connector clients and read/write/share/delete capabilities enforced by both MCP and ordinary API routes, with read-only defaults and attributable mutation audit records. The same bearer is accepted by the ordinary API; restricting only MCP leaves a direct-API bypass. Tool annotations and host prompts cannot substitute for authorization. |
| R11 | P2 | Core audio/transcript/image deliveries can exhaust retries without a durable failure destination or a configured alarm. | Synthesized templates contain zero CloudWatch alarms. Core event targets, including `TranscriptUploadRule` and `AllPartsTranscribedRule`, have no DLQ; transcribe/summarize have no Lambda failure destinations. Add monitored queues/destinations and tested replay. Existing action-item, saved-summary, extraction and QA queue controls should be retained. |
| R12 | P2 | Accidental AuthStack deletion/replacement can delete production identities. | `AuthStack` uses `RemovalPolicy.DESTROY`; synthesized user-pool deletion/replacement policies are `Delete`, with no deletion protection. Retain the production pool, enable deletion protection and define administrator MFA requirements. |
| R13 | P2 | Selecting PDF export produces no download. | Backend `ExportMeeting` returns text content and a `.txt` filename for `pdf`; frontend `ExportMenu` only opens a returned URL, which that branch does not provide. Implement actual PDF delivery or accurately label and deliver the existing format. |

Relevant source paths:

- [Batch summary entry point](../../../backend/cmd/summarize/main.go),
  [summary snapshot](../../../backend/internal/repository/summary_snapshot.go),
  [publication](../../../backend/internal/repository/summary_publish.go) and
  [meeting grants](../../../backend/internal/service/meeting.go).
- [Refinement](../../../backend/internal/service/bedrock.go),
  [quality evaluator](../../../backend/internal/service/note_quality_eval_test.go)
  and [workflow](../../../.github/workflows/evaluate-notes.yml).
- [API routes and logging](../../../backend/cmd/api/main.go),
  [document sharing](../../../backend/internal/service/account.go),
  [export handler](../../../backend/internal/handler/export.go) and
  [export UI](../../../frontend/src/components/ExportMenu.tsx).
- [MCP OAuth](../../../mcp-server/src/auth.ts),
  [HTTP auth](../../../mcp-server/src/http-auth.ts),
  [tools](../../../mcp-server/src/index.ts) and
  [API adapter](../../../mcp-server/src/api.ts).
- [Infrastructure assembly](../../../infra/bin/infra.ts),
  [GatewayStack](../../../infra/lib/gateway-stack.ts),
  [AuthStack](../../../infra/lib/auth-stack.ts) and
  [FrontendStack](../../../infra/lib/frontend-stack.ts).

Synthetic probes exercised production methods with mock SDK transport or
in-memory MCP transport. They confirmed the missing visibility condition,
refinement log disclosure, invalid refinement acceptance, OAuth callback behavior,
token-cache confusion and oversized-document failure. They do not establish live
exploitation, incident frequency or model error rates.

## Meeting-note quality and operation

Keep the implemented protections: selected A/B source guards; preservation of
human edits; conditional batch/saved-summary publication; verified immutable
document evidence; acoustic label remapping and ambiguous-merge fallback;
namespaced multipart speakers; action run/source/lease state; preservation of
task completion and prior results on failure; explicit unknown/failed analysis UI.
Current-source QA validates authorization and source revisions rather than
treating retrieval metadata or cached history as access.

Whisper emits diarization availability and speaker counts, but Go's
`WhisperMetadata` omits them. Unlabeled input allows text-inferred speakers.
Persist and display whether labels came from acoustic diarization, text inference
or no reliable assignment. A successful pipeline should not silently imply
reliable speaker identity.

The September 12 four-case corpus and September 23 GPT-6 Sol comparison are
historical synthetic evidence. They do not qualify the current GPT-6.1 Sol
configuration, real Korean audio or long-meeting accuracy. Establish a
permissioned, anonymized or synthetic audio corpus covering overlapping speech,
domain terminology, numbers/units, negation, late corrections, decisions versus
proposals, task agreement/owner/deadline, document grounding and quoted attacks.
Measure factual omissions and unsupported claims alongside ASR/diarization
metrics; inspect citation support rather than only citation-ID validity.

Set product targets before acceptance: successful upload/final-note completion,
recovery success, latency distributions, cost per meeting and concurrent-load
limits. Measure realistic durations and Spot interruptions. Test lost delivery,
duplicate events, model timeout, throttling, source edits during generation and
operator replay. Existing retry eligibility alone does not schedule redelivery.
Native macOS capture, permissions and mobile interruption/recovery require
device acceptance; this Linux review does not validate them.

## MCP interoperability and AI usefulness

The adapter already uses request-scoped HTTP JWT/API/server objects, verifies
signature/issuer/expiry/client/audience/user/scopes, checks Host/Origin, hides local
file/recording tools remotely, bounds upload/request/response sizes and deadlines,
and avoids automatic mutation retries. Meeting reads default to notes and follow
opaque, source-bound API cursors without full-meeting fallback. Upstream reading
responses and serialized tool results have separate 32,000-byte caps; other
HTTP upstream responses have a 1MiB cap.

Public probes established that both exact protected-resource metadata routes
return JSON, and unauthenticated `/api/mcp` returns 401 with the expected metadata
challenge. They do not prove login, token refresh or authenticated tool operation.
The actual Cognito OIDC metadata lacks `code_challenge_methods_supported`;
OAuth authorization-server discovery returns 400.

The [stable MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
requires clients to verify advertised PKCE support. The installed SDK 1.29.0
accepts an absent PKCE metadata field in its authorization helper, and the full
authorization flow uses that helper too. The passing compatibility test therefore
does not establish strict specification compliance or actual client acceptance. Record actual
acceptance for each claimed Claude, Codex, Kiro and Quick configuration: exact
registered callback, login, refresh, 401 recovery, resource binding, paged reads,
intentional mutations, account switching, timeouts and revocation behavior.
Preregistration is valid; absence of dynamic client registration alone is not a
defect.

The specification also forbids passing the received client access token unchanged
to an upstream API. Current HTTP MCP forwards its validated bearer to the shared
application API, whose authorizer admits the MCP audience. The shared-application
trust boundary is intentional and inbound audience checking exists. Resolve and
document its protected-resource design, or introduce a separate downstream
credential/token exchange, before claiming standards-complete interoperability.
This finding does not demonstrate a wrong-audience authentication bypass.

For better AI operation, all tools need appropriate read/mutation annotations,
validated output schemas and structured results. Discovery currently has 30 HTTP
tools, six annotations and no output schemas. Add focused metadata/search/filter
tools and bounded document reads so clients can find evidence efficiently before
retrieving text. Include current-source identity, coverage, continuation and
degraded status in machine-readable responses. Measure tool-selection success,
answer grounding, follow-up continuity, tokens and round trips in actual clients.

## Additional security and privacy obligations

- The live homepage returned no CSP, HSTS, `nosniff`, frame policy or referrer
  policy. `FrontendStack` applies security-header policies to media, not the
  default HTML behavior. Add a tested application policy, including `frame-ancestors`,
  without breaking authentication, static scripts, WebSockets or native bridges.
  JWT/refresh tokens reside in localStorage, so same-origin script compromise has
  significant impact. Markdown sanitization and strict Mermaid mode already exist;
  this observation alone does not prove XSS.
- The frontend workflow syncs and invalidates but does not force HTML upload or
  set no-cache metadata. The live homepage likewise lacked `Cache-Control`.
  Implement the documented HTML-refresh release requirement and verify
  HTML/chunk consistency after deployment.
- Main-table PITR and retention exist. No customer-managed table KMS key is
  configured; this does not mean DynamoDB is unencrypted. Its TTL attribute is
  `pendingShareExpiresAt`; uppercase QA `TTL` rows remain unswept. Define lifecycle
  requirements for audio, transcripts, documents, versions, backups and logs.
  Twenty-four synthesized log groups retain 731 days, three retain 30 days, and
  one has no declared retention.
- Cognito self-signup is disabled and admin authorization uses verified groups.
  Issued JWTs and existing WebSockets are not immediately invalidated by every
  account disable/logout operation. User deletion preserves historical profile
  and data. These accepted limits need explicit customer/operator expectations.
- Browser backups are account-filtered but remain unencrypted in the browser
  profile after sign-out by design. Mac leftovers and local MCP recordings lack
  Cognito ownership binding. Preserve recovery while adding shared-device and
  account-switch controls; do not silently purge the only recording copy.
- Manual QA web search can transmit meeting-derived queries externally. Hashing
  logs does not prevent egress; the UI opt-in gates proactive search only.
  Add a server-enforced tenant policy for external search and document approved
  model/provider/region data flows.
- The undeployed/staged KnowledgeStack declares public AOSS networking; IAM data
  access still applies. This is documented IaC debt, not proof of live exposure.
  Inspect live policy before remediation and preserve the prohibition on incidental
  KnowledgeStack deployment.
- Synthesized resources have no WAF or configured CloudFront/API access logging.
  Imported or externally managed controls were not attested. Add reviewed,
  redacted audit logging, suitable abuse controls and alarms. Scope remaining IAM
  wildcards where the AWS action supports resource restrictions.
- Document conversion has documented cross-tenant S3 read permission but isolated
  execution. Per-job key scoping remains an improvement. The native app's existing
  ad-hoc signing is not evidence of a notarized commercial release.

## Dependency audit

Official npm production-dependency audits succeeded on the checked-in lockfiles:

| Module | Affected package entries | Critical | High | Moderate | Low |
|---|---:|---:|---:|---:|---:|
| Frontend | 18 | 1 | 9 | 7 | 1 |
| MCP | 9 | 1 | 4 | 3 | 1 |
| Infrastructure tooling | 1 | 0 | 1 | 0 | 0 |

These are package findings, including transitive propagation, not counts of
independently exploitable application defects. A compact lockfile-pinned receipt
is in [the review evidence](2026-10-12-commercial-readiness-evidence.json).

Frontend Next.js server-side critical advisories do not establish an RCE in this
S3 static export: no production Next.js request server or image optimizer is
deployed by this frontend, and image optimization is disabled. Browser-loaded
Mermaid, DOMPurify, TipTap and ProseMirror dependencies still require advisory
triage and updates. Strict Mermaid mode and HTML sanitization are useful
protections, not a replacement for dependency maintenance.

Read-only esbuild dependency-graph inspection found MCP's critical `proxy-addr`
and the SDK OAuth-client module absent from both actual Lambda and stdio artifacts.
Express/Hono trust-proxy paths are not used. Lambda includes `@hono/node-server`
but uses neither its Windows static-file path nor the affected `serve-static`
module. No reachable critical MCP exploit was established. Update resolved
dependencies, regenerate both distributed artifacts, rerun tests and repeat
the audit; avoid an indiscriminate force upgrade.

## Verification and remaining acceptance

| Check | Result and scope |
|---|---|
| Go | Full `go test ./... -count=1`, including `cmd/*`, and `go vet ./...` passed. Linux ARM64 API and summarize builds passed. |
| Python | QA 376, Whisper 118, extraction 49, simulator 22, crawler 136 and research-agent 20 tests passed: 721 total. |
| Frontend | Lint passed with 15 warnings and no errors; production static build passed; eight script test files passed. |
| MCP | TypeScript/Lambda build, 96 main tests and 13 bundle tests passed; public/generated stdio bundle comparison passed. |
| Infrastructure | 15 suites / 104 tests passed; 11-stack synth passed using cached context and explicit account/region. |
| Public deployment | Homepage/config/discovery GETs succeeded; MCP and meeting APIs rejected unauthenticated GETs. Config advertised `/ws` and async QA. Header and OAuth metadata observations above were directly read. |
| AWS control plane | STS returned `NoCredentials`; deployed package hashes, IAM, AOSS network policy, encryption choices, alarms, secret rotation and authenticated recovery were not attested. |
| Devices and traffic | No native macOS capture, real browser recording, authenticated MCP-client flow, real-audio benchmark or production load/SLO acceptance was performed. |

The prescribed local Go path was absent; Go 1.25 at
`/home/atomoh/go-sdk/go/bin/go` was used. Extraction needed Python 3.12 with
existing packaged pypdf 6.18.1, defusedxml 0.7.1 and boto3 1.43.93; default
Python 3.9 failures were environmental. Temporary fixture caches were redirected
to `/tmp`. Sandbox restrictions on local sockets/processes/Docker were resolved
through approved reruns. No private credentials or meeting content were printed.

Recommended release sequence: resolve R1–R5 and local credential/callback defects;
align and expand quality evaluation; introduce bounded document reads and enforced
connector capabilities; add monitored failure recovery and production identity
protection; update and triage dependencies; then record authenticated client,
device, source-grounding and load acceptance against the deployed revision.
