# ADR-003: MCP access: transports, tools, connector delegation and local intake

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

The MCP server is an adapter over the authenticated APIs. The sections below keep, in order, the original stdio server record, the back-data tools record, the dual-transport record, the connector capability and private delegation record, and the local intake record. Where a later section changed an earlier rule, the earlier section has been corrected to say so.

## Local MCP Access to Meeting Data

### Status notes

- Status: Accepted; tool scope extended by the back-data tools section.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

The transports section adds authenticated HTTP alongside stdio
as of 2026-09-23. The local-only description below is historical; API authorization
and the CloudFront boundary still apply.

### Context and decision

External agents need authenticated meeting retrieval and Q&A without a separate
public API or authentication bypass. Run a local MCP process that signs in with
Cognito authorization-code PKCE and calls the existing CloudFront-served API.
Reuse the public SPA client and its registered `http://localhost:9876/callback`.

API keys were rejected because they require another credential lifecycle and
changes to JWT gates. A briefing-only REST endpoint lacked the agent tool
interface. A separate MCP Cognito client added coordinated audience configuration
without a demonstrated need.

### Current implementation and supersession

- **Transport:** stdio is the default (`StdioServerTransport`). The process
  makes outbound HTTPS REST requests; its temporary HTTP listener handles OAuth
  callbacks only. `--transport http` serves authenticated Streamable HTTP, and
  the hosted `ANY /api/mcp` route exists when `ttobak:mcpResourceAudience` is
  set (see the transports section).
- **Clients:** CDK defines a confidential application client, a public SPA
  client, a companion client and two dedicated connector clients
  (`infra/lib/auth-stack.ts`). The SPA client is used by the local MCP sign-in
  and is wired into both the edge audience check and the gateway JWT
  authorizer. No dedicated `ttobak-mcp-client` exists.
- **Credentials:** PKCE uses state validation and refreshes tokens. Local storage
  is `~/.ttobak/tokens.json`, created with mode `0600` in a directory created with
  mode `0700`. Current client configuration sets 30-day refresh-token validity.
- **API boundaries:** edge validation and the gateway authorizer validate the
  configured client; Go middleware verifies JWT signature, issuer, and expiry.
  `OriginVerify` guards the Go API's CloudFront origin path. Go HTTP routes use
  API Gateway payload v1.0; Python Q&A has a separate v2.0 integration.
- **Tools:** the original five read/auth tools grew to account, document, KB,
  and project operations. `index.ts`, not a historical count, defines the registry.
- **HTTP responses:** the API client rejects non-success status codes even without
  the application's error envelope and decodes UTF-8 across response chunks.
  These HTTP behaviors do not change the stdio MCP transport.

MCP application API requests must use CloudFront. Direct Cognito authentication
and signed S3 upload requests remain existing service integrations. The sole
public-document route exception is defined by ADR-020; it does not authorize
public MCP routes or a new application origin.

### Consequences and risks

The adapter centralizes authorization in REST services and requires no public MCP
infrastructure. It requires a local Node runtime and interactive browser login.
Tokens remain sensitive local files, not encrypted credential storage; a
compromised local process can exercise the user's authorized tools. The OAuth
callback and outbound HTTP client are separate from the MCP transport.

### Evidence

- [index.ts](../../mcp-server/src/index.ts), [auth.ts](../../mcp-server/src/auth.ts),
  [api.ts](../../mcp-server/src/api.ts): transport, PKCE, tokens, HTTP handling.
- [http.test.mjs](../../mcp-server/test/http.test.mjs): byte-split UTF-8 API responses.
- [auth-stack.ts](../../infra/lib/auth-stack.ts),
  [infra.ts](../../infra/bin/infra.ts), [edge-auth-stack.ts](../../infra/lib/edge-auth-stack.ts),
  [gateway-stack.ts](../../infra/lib/gateway-stack.ts): configuration intent;
  source inspection does not establish live deployment state.
- [auth.go](../../backend/internal/middleware/auth.go): backend JWT validation.

## Bidirectional MCP Knowledge Tools

### Status notes

- Status: Accepted; extends the stdio server section.
  The original documents-only write restriction is superseded by the current tools.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

### Context and decision

External agents need to pull account knowledge for preparation and push new notes
back into TTOBAK. Implement thin MCP wrappers over authenticated REST endpoints;
keep ownership, membership, validation, and storage access in backend services.
Direct DynamoDB access would duplicate and bypass those invariants. Keeping all
MCP tools read-only would prevent the intended contribution workflow.

### Current surface and authorization

| Tool group | Backend contract |
| --- | --- |
| Account brief/insights/meetings | Account-member-gated REST reads |
| Vault export | Caller-scoped export and limits in ADR-020 |
| Put/list/get/update document | Personal partition when `accountId` is omitted; account scope when supplied; backend ownership/member gates apply |
| Upload document | Local file to a signed upload URL, then document creation; separate from inline Markdown |
| KB upload/list/delete | Authenticated user's `kb/{userID}/` objects |
| KB sync | Authenticated request to ingest the configured shared data source |
| Create account/add account member | Account creation plus existing-member-gated addition; assignable roles exclude `owner` |
| Project tools | Create/read/update and account-link operations under project service authorization (ADR-025) |

Use the registered `ttobak_*` names in `index.ts`; the old unprefixed names and
fixed tool count are historical. `ttobak_put_document` creates a fresh ID;
`ttobak_update_document` revises an existing document. The adapter exposes no
meeting-content editing tool, but writes are no longer limited to documents.

**ADR-015 supersedes the old owner-only member-add claim.** Any current account
member can add an assignable role; removal/pending-revocation remain owner-only
in the REST service. Account hierarchy does not confer inherited authorization.

The shared KB is shared infrastructure, not unrestricted user-upload visibility.
Q&A filters user uploads to the caller's prefix alongside authorized meetings and
shared crawler documents. S3 deletion alone does not immediately remove the
indexed content; visibility changes with ingestion.

### Ingestion and local-file limits

`SyncKB` returns `skipped` when KB configuration is absent; configured ingestion
failures return an error rather than `skipped`. IaC supplies `KB_ID` and
`KB_DATASOURCE_ID` to the API and a KB-scoped `bedrock:StartIngestionJob` grant.
The summarize worker's variable is separately named `DATA_SOURCE_ID`. These are
source/configuration facts, not proof that a specific deployment contains them.

Meeting-summary ingestion and document-bearing crawler runs can also index the
shared data source. A zero-change crawler run skips ingestion, so these fallback
triggers do not guarantee a deadline for indexing an MCP upload. Older broader
worker ingestion grants remain existing IAM tightening work, not a template for
new wildcard grants.

`guardUploadPath` resolves symlinks, blocks sensitive paths/filenames, and applies
50 MiB KB / 100 MiB document limits. It is not a sandbox; the MCP host's tool-call
approval remains the local-file access gate. Signed S3 PUT requests use the URL's
signature, not the Cognito bearer token.

### Consequences and accepted risks

Agents can contribute through the same service boundaries as the app. Token
compromise now exposes account, project, document, and KB capabilities, so the
original documents-only mitigation no longer describes the blast radius. The
Markdown loop guard prevents accidental reimports, not low-quality or duplicative
content; binary uploads do not run that Markdown guard. Tool/client/bundle/docs
must stay aligned, and uploads/deletions remain subject to asynchronous indexing.

### Evidence

- [index.ts](../../mcp-server/src/index.ts), [api.ts](../../mcp-server/src/api.ts),
  [documents.test.mjs](../../mcp-server/test/documents.test.mjs): tools and contracts.
- [account.go](../../backend/internal/service/account.go),
  [project.go](../../backend/internal/service/project.go),
  [kb.go](../../backend/internal/service/kb.go): actual authorization and ingestion.
- [handler.py](../../backend/python/qa/handler.py): KB access filtering.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts),
  [ai-stack.ts](../../infra/lib/ai-stack.ts),
  [gateway-stack.test.ts](../../infra/test/gateway-stack.test.ts): env/IAM wiring.
- [ingest_trigger.py](../../backend/python/crawler/ingest_trigger.py): zero-change skip.

## Stdio and authenticated Streamable HTTP MCP

### Status notes

- Status: Accepted for implementation; remote deployment/consumer acceptance separate.
- Decision date: 2026-09-23.
- Supersedes: The stdio server section's stdio-only transport decision, not its API authorization.

### Context

Claude Code, Codex, Kiro and Amazon Quick need a shared TTOBAK MCP interface.
Quick requires a remote endpoint; the other clients can also use local stdio.
Tool-search deferral is a host capability and is not enabled merely by HTTP.

The old entry point coupled one local Cognito token cache to one server process.
Reusing it as a shared HTTP service would share a user's credentials and turn
client file paths into server filesystem reads.

### Decision

Keep stdio as the default and expose `--transport http` as an explicit installed-package mode.
A shared server factory owns the tools and their handlers. HTTP creates fresh
server/transport/API instances for each authenticated POST and uses JSON responses.
It keeps no cross-request sessions, user token cache or refresh-token storage.

Verify Cognito access-token signatures, issuer, expiry, client ID, user identity,
scope and exact resource audience before any MCP initialization or tool operation.
Clients own authorization-code PKCE, refresh and logout. Public discovery metadata
advertises the actual Cognito issuer and scopes; it exposes no meeting data.
Public auth metadata does not authorize unauthenticated application operations.

Use the same TTOBAK CloudFront application origin for the public resource and
upstream APIs. The deployment admits the exact resource audience (`ttobak:mcpResourceAudience`
in `cdk.json`) alongside its existing client audience. The MCP verifier and
edge client-ID checks remain in force; no new public application origin or
anonymous MCP API route is introduced by that configuration.

HTTP hides the local login/logout tools and accepts bounded base64 bytes for
the two upload tools. A file path is rejected both at dispatch and the API adapter.
Stdio keeps its browser login, local file guards and existing larger file limits.
Vault export remains an API operation; oversized HTTP results fail rather than
being truncated. Successful writes with oversized responses instead return a
compact completion receipt preserving returned record IDs; omitted content is explicit.

HTTP bounds are 1 MiB request JSON, 512 KiB decoded uploads, 32,000-byte tool
results, 32 active requests and a maximum 55-second absolute deadline by default
(`TTOBAK_HTTP_TIMEOUT_MS`); the hosted function sets 25 seconds to fit the
gateway's 30-second limit. The tighter
reading-page limits remain. Abort upstream HTTP on disconnect/deadline, never
retry a write automatically, and do not describe an uncertain mutation as rejected.

Addendum, 2026-09-24: stdio also offers macOS microphone recording (ffmpeg child
process) and meeting audio upload through the existing meeting, presign and
upload-complete APIs. These tools are in `LOCAL_ONLY_TOOLS` and never exposed over
HTTP, which must not spawn processes or read server files. See the MCP README.

### Verification and deployment boundary

Protocol tests exercise concurrent users, bad/expired/wrong-resource JWTs,
metadata, per-request authorization, file-path rejection, canonical upload bytes,
body/result limits, deadlines and stdio regression behavior. The standalone
client bundle is built reproducibly and tested as stdio; HTTP hosting is tested
from the installed package with synthetic data. HTTP clients connect by URL and
do not need the downloadable adapter.

Hosting and each client's registered OAuth callback are deployment configuration.
Do not claim Claude/Codex/Kiro/Quick login acceptance from SDK tests. Cognito's
OIDC discovery and resource binding, API audience, edge behavior, scopes and
verified-email-dependent flows require live acceptance. Do not fabricate missing
email claims from an access token or weaken existing invitation checks.

The implementation provisions no HTTP MCP origin or public route. Any hosted
rollout must separately wire the authenticated endpoint and minimal public
discovery through the existing approved ingress.

Addendum, 2026-10-11 (hosted rollout): the `ttobak-mcp` Lambda behind the existing
HTTP API is that wiring, enabled by `ttobak:mcpResourceAudience`. `ANY /api/mcp`
keeps the CloudFront edge check and the Cognito JWT authorizer in front of the
server's own verification; only the two static discovery GETs are unauthenticated.
A throwaway pool showed Cognito sets `aud` to the requested resource only with
managed login and that URL registered as a resource server (otherwise the code
exchange fails `invalid_grant`), so AuthStack moves the domain to managed login v2.
The 30s API Gateway limit caps the hosted deadline at 25s. Per-client login
acceptance remains a live check. Follow-up 2026-10-12: live testing showed the edge
function, not the authorizer, answers token-less requests, so its `/api/mcp` 401
now carries the `resource_metadata` challenge (other paths are unchanged).

### References

- [MCP operation and client configuration](../../mcp-server/README.md)
- [Shared tools](../../mcp-server/src/index.ts)
- [HTTP transport](../../mcp-server/src/http.ts)
- [Cognito verification](../../mcp-server/src/http-auth.ts)
- [Protocol tests](../../mcp-server/test/http-transport.test.mjs)

## Connector capabilities and private delegation

### Status notes

Date: 2026-10-21

Status: Accepted; the connector flags are enabled in source (`infra/bin/infra.ts`) after the acceptance recorded in `docs/runbooks/connector-activation-2026-10-21.md`, and each deployment from main requires exact-commit attestation

### Context

Connector access needs an explicit operation policy independent of HTTP verbs
and the existing browser session. Read access must not implicitly enable note
creation, sharing, deletion, asynchronous research or mutation-capable streaming.
Earlier bounded meeting/document/account/project readers remain the producers;
this work does not replace their authorization or source-revision checks.

### Decision

Maintain one versioned operation inventory in
`contracts/connector/v1/operations.json`. Generate matching Go, TypeScript and
Python tables, with a SHA-256 of the complete inventory in each output. Unknown
operations and mismatched destinations fail closed. A route's method is a binding,
not its capability classification. For example, revoking a share requires
`share`, and question detection can be classified as `read` despite using POST.

Capabilities are sorted, unique subsets of `delete`, `read`, `share`, `write`,
always including `read`. Effective privileges intersect the current enabled
client maximum, the current user's approval and the requested delegation.
Elevated requests bind the exact approval generation and cannot outlive its
expiry. Revoked, missing, changed or foreign approvals reject the request rather
than silently reducing its privileges. Generation zero is read-only.

Inventory eligibility is not executor availability. The API reader allowlist
names only the eight existing bounded reading families, including account and
project reading. The private protocol separately binds verified client identity, exact operation
and argument bytes, destination, request identity and expiry. Ordinary browser
authorization, domain ownership and canonical source revalidation remain
independent requirements.

### Current implementation and remaining delivery

This revision supplies policy types, operation tables, deterministic generation,
local RSA-PSS delegation verifiers and shared synthetic signature tests.
The [wire contract](../security/connector-delegation-v1.md) defines exact bytes,
key pinning and lifetime limits. Current grant readers now exist in Go and Python:
client policy and user approval are read together with `TransactGetItems`, with
strict identity, type and configured issuer/audience checks. Malformed existing
approval rows reject rather than becoming a missing read-only approval.

Policy and approval writers atomically publish the expected generation and a
bounded audit receipt. Policy profiles are read or read/write. Grant elevation
checks the enabled client maximum as a DynamoDB String Set; revocation and blocking remain possible after
administrators disable or tighten that policy. Blocking has no TTL, while ordinary
expired approvals and audit receipts retain the documented 30-day cleanup period.
A removal-only revoke cannot clear an existing block: it returns a condition
conflict unless the caller retains the block. Unblocking requires explicit
re-approval through the enabled-policy and capability checks.
SDK transaction retries carry a stable request token; a condition conflict is
separate from an unknown transport outcome that requires generation inspection.
No live signer, approval endpoint, OAuth client, IAM permission
or scheduler is activated by this foundation. The API auth boundary now applies a dedicated
read-client policy when configured.
Its default configuration is empty; without an injected current-grant resolver,
every dedicated client is denied. Existing first-party client authorization remains
in force. A successful dedicated read requires fresh enabled-client grants before
execution and again before releasing its bounded response. The QA HTTP boundary
similarly admits only bounded question detection and denies
dedicated clients without a current-grant resolver. Both boundaries reject
mutation-capable and unbounded routes before dispatch; QA does not decode caller
headers to establish a configured client's identity.

The API Lambda now distinguishes a top-level private invoke frame from API
Gateway payload 1.0. A configured exact KMS signing-key ARN enables a lazy,
bounded public-key lookup and local signature verification; empty configuration
keeps the private consumer unavailable. The bridge binds operation, exact argument
bytes and current grants before calling only the bounded API read families (the
original eight plus the account contact list and account event list/get,
2026-11-08),
then verifies again before releasing at most 14,000 body bytes. The router accepts
only its verifier-created in-process principal; no HTTP header or nested request
body selects this path. Existing services still check resource ownership and
source revisions. The API's dedicated HTTP read boundary now receives the same
atomic grant resolver. Its private mutations are the personal-note executor below
and, since 2026-11-08, account contact creation: a separate executor with direct
membership, current approval and an arguments-bound receipt in one transaction,
storing contacts sealed under ADR-021 (contract in `docs/API-SPEC.md`). Account
event creation uses an equivalent executor that also writes the event's
reference row in the same transaction.

The private QA consumer now accepts only `qa.readonlyAsk`, with a question and
optional meeting ID. It verifies the same signed delegation against the QA
destination and current grants, then reads through the existing current-source
boundary. One bounded model call runs without tools, sessions or research/job
creation. Current source checks and grant checks precede result delivery.
Model evidence includes verified attachment text and its partial/retained/unavailable
status; transcript, notes and serialized attachment context share the 24,000-byte
limit. Attachment citations correspond to the evidence supplied to the model.
Only the pinned public key is cached; KMS and grant clients use bounded timeouts
and one attempt. Dedicated HTTP detection now receives the same current-grant
reader. Empty signing-key configuration leaves private execution unavailable.
The deployment artifact must contain the pinned ARM64 cryptography wheel before
key configuration is enabled; source installation does not prove deployment.

The MCP HTTP adapter now has an opt-in private transport for the bounded API
reads (thirteen since the account contact list and account event list/get were
added on 2026-11-08), `qa.readonlyAsk`, personal note creation and account
contact and event creation. Request-scoped verified identity/scopes, atomic
grant reads, exact-byte KMS signatures and service-IAM invocation replace bearer
forwarding in this mode. It rechecks grants and expiry before returning bounded
results, with one SDK attempt and bounded calls. The flag defaults off and a
private failure never falls back to a legacy request. Stdio remains unchanged.

The first-party settings page now manages the two configured connector profiles.
Users can block their connection, restore baseline read access, approve personal
note writing for one hour or revoke writing. Only verified administrators change
global client enablement. Handler-to-service-to-repository calls pin the policy
generation/binding, approval generation and audit record transactionally; an
uncertain outcome is reconciled with a fresh view before resubmission. Management
routes remain nondelegable, so connector tokens cannot approve themselves.

Personal-note creation requires matching OAuth read/write scopes and a current
write-approval generation. The signed request ID doubles as a caller retry key;
the server generates a fresh random UUID for a new personal document. The
user/client/key-scoped receipt remains authoritative for retries. Reusing a key
after receipt removal creates a separate identity, keeping retained shares of a
deleted note from authorizing new content. An expired but present receipt still
conflicts. Current
policy and approval conditions, a new document and its 30-day receipt share one
DynamoDB transaction. Replays validate the receipt and existing note without
overwriting edits or recreating deletions. Exact body bytes bind the receipt;
different content conflicts. SDK create retries are disabled; uncertain results
reconcile through the receipt. The bounded response confirms creation only,
and grants are checked again before its delivery. No account, file or sharing
mutation is exposed by this executor.

The infrastructure supplies two dedicated public clients, a retained
RSA signing key, metadata-only transaction grants, private invocation to the API
live alias and QA, and a hash-pinned ARM64 cryptography layer. Existing pool/client
identities and first-party/WebSocket allowlists remain unchanged. Application
configuration enables issuance and private HTTP transport together. The edge
challenge follows that private flag and requires the same read scope as MCP;
discovery also lists optional write. HTTP hosts must use a dedicated client and
fresh login, while stdio keeps its existing first-party authentication.
The [release runbook](../runbooks/connector-release.md) defines separate
deployment verification. Installed stdio bundle refresh, real provider tokens,
audience and scope checks, current-source reads, expiry/revocation/disable
behavior, actual KMS/IAM execution and rollback require recorded acceptance
before main integration of any revision that enables the connector flags.
Per-host native OAuth migration is not part of that gate: each HTTP host needs
its own dedicated-client login, refresh and read verification, recorded only
when that host's evidence exists, and the host guidance keeps that limit. The
source/artifact-bound
[dated acceptance record](../runbooks/connector-activation-2026-10-21.md) must be
complete and land with, or before, that revision. An enabled source flag without
that record does not satisfy this ordering. The exact merged commit is attested
before its deployment, as the order below requires.
No additional external callback or unauthenticated route is approved here.

#### Integration and activation order

Merging a revision that sets the connector flags to true is source integration,
not activation. Such a merge is permitted only while the push-triggered Deploy
Infrastructure and Deploy Frontend workflows are both disabled, and it still
requires current-head review and required CI.

The flags are plain source constants, so every deployment of AuthStack,
EdgeAuthStack, GatewayStack or FrontendStack from a main commit carrying them is
an activation, including an unrelated `--exclusively` hotfix. Before any such
deployment, and before re-enabling either workflow, an independent source/artifact
comparison must bind that exact main commit to the recorded production acceptance.
Record the workflow state, the comparison result and the deployed versions in a
dated connector activation record under `docs/runbooks/`, as the
[release runbook](../runbooks/connector-release.md) describes. A missing exact-commit attestation for an unmerged
candidate is a deployment precondition to report, not a merge blocker; it never
waives the deploy-time comparison.

### Verification

`scripts/connector/generate_contract.py --check` compares all generated files
against the same inventory. Language tests cover inventory hashes, explicit
capabilities, unknown operations, destination binding, immutable returned policy
and current approval intersection. MCP tests also check the current Go route
inventory. These source tests do not prove deployed provider or consumer behavior.

## Local MCP intake

### Status notes

- Status: Accepted; the CLI runner, the Mac Settings UI and the app-managed
  schedule are implemented. Calendar acceptance on the user's Mac is pending
  (see the runbook). Amended 2026-11-10 for the meeting SpecReq task step
  (code only; Mac acceptance pending).
- Decision date: 2026-11-09.
- Runbook: [local MCP intake](../runbooks/local-mcp-intake.md).
- Related: The stdio server section (stdio-only calendar tools), ADR-024 (Mac control path).

### Context

The user's Mac already runs authenticated internal MCP servers (calendar, SFDC,
SpecReq) in Claude Code, Codex and Kiro. Their credentials are Midway cookies
and host-specific environment entries. TTOBAK should stay current with a few
of these sources without holding provider credentials and without copying host
configuration.

### Decision

- **Deterministic connectors.** A local runner (`ttobak-mcp intake`, in
  `mcp-server/src/intake/`) is an MCP client of one selected server per
  connector. It calls fixed read tools with fixed arguments, parses results
  strictly and writes through the user's existing TTOBAK API token. AI hosts
  never write to TTOBAK on a schedule. A later optional AI job may produce
  proposals only.
- **References, not copies.** The selection stores a host and server name, the
  approved `PATH` directories and non-secret TTOBAK endpoints. Each run
  re-reads that one host entry and spawns it with argv, never a shell. Command,
  arguments and environment stay in memory. They are never written to disk,
  printed, logged or sent to TTOBAK. The one exception is the SA performance report run's temporary
  0600 configuration file, deleted after the run (ADR-023).
- **Calendar.** `aws-outlook-mcp` `calendar_view` is the only calendar source.
  It follows the [calendar sync runbook](../runbooks/local-outlook-calendar-sync.md):
  - occurrence `meetingId` as `externalId`;
  - join links stripped from locations;
  - private and confidential items excluded when flagged;
  - removals only for cancelled or excluded tracked occurrences;
  - a journaled batch before each send, identical retry on ambiguous delivery,
    and `complete:true` last;
  - a 409 stops the runner until `--reconcile`.
- **Residency.** Calendar stays private. The SFDC connector's owner-private
  fields and its account-shared contacts follow ADR-023's residency tables,
  including the SFDC contacts row (name, title, company and IDs; never email or
  phone). The SpecReq connector stores ID, title, status, account and a summary
  of at most 1,000 bytes.
- **Task connectors (amendment, 2026-11-10).** After a synced calendar run,
  under the same run lock, the runner keeps one private TTOBAK task per
  occurrence ([meeting SpecReq tasks design](../superpowers/specs/2026-11-10-meeting-specreq-tasks-design.md),
  `mcp-server/src/intake/tasks.ts`). It uses three more selected servers, each
  limited to an exact tool allowlist (`connectors.ts`); any other name is
  refused before the call:
  - `meetings` (`amazon-meetings-mcp`): `list_meetings`, `get_meeting`;
  - `specreq` (`spec-req-mcp`): `check_user_is_tam`, `search_requests_advanced`,
    `get_request`, `acknowledge_request`;
  - `slack` (`slack-mcp`): `lookup_user`, `open_dm_channel`, `list_drafts`,
    `post_draft`.
  Never `post_message`, `schedule_message`, `complete_request` or any other
  write. Classification, linking and drafting are deterministic; no model runs.
- **Mac notices (amendment, 2026-11-10).** The runner appends one line per
  (task, state) to `~/.ttobak/intake/notify.jsonl` (contract in
  `mcp-server/src/intake/notify.ts`). The Mac app reads it
  (`mac-app/src-tauri/src/intake_notify.rs`) and posts a notification per
  unseen `key`, tracked in `intake-notified.json` in its app data directory. A
  line must pass strict shape, age (at most 24 hours old) and path checks; a
  partial or malformed line is skipped. Notifications are informational: they
  never navigate the app (a reload could discard unsaved input), and the task
  strip on the meetings page shows the task. Delivery is unverified until the
  Mac acceptance in the runbook.
- **SFDC contacts step (amendment, 2026-11-11).** At the end of each `sfdc`
  connector run, on the same session, the runner reads `search_contacts` (a
  read tool on the `sfdc` allowlist) for every SFDC account mapped to a TTOBAK
  Account, matches the people against that Account's contacts on the Mac and
  sends one bounded batch per Account to `POST /api/accounts/{id}/contacts/sfdc-sync`
  (ADR-021). Emails are compared on the Mac only; no email or phone reaches a
  request, log, status file or ledger. A failure of the step is a
  `PARTIAL_FAILURE` of `intake run`; `AUTH_EXPIRED_MIDWAY` stops it.
  `search_contacts` is allowed and required only while the step is on, so a
  changed contacts tool fails only the step (`SCHEMA_CHANGED`). No new Account
  starts after 8 minutes; the rest are `deferred` to the next run, so the step
  always records its status before the app's 20-minute run timeout.
- **Sync options (amendment, 2026-11-11).** `intake options` stores a
  top-level `options` object in `selection.json`: `calendarResponses` (the
  owner's own responses to sync; default accept, organizer and tentative;
  declined events never sync) and `sfdcContacts` (default on; off records the
  contacts step as `disabled`). An event with an unselected response is
  excluded like a private one, so a tracked copy is removed through the
  existing removal path. The response is read on the Mac only. The Mac app
  sets the options with typed values (`intake_set_options`). The first real
  run after the update, possibly scheduled, therefore removes synced events
  that are declined, unanswered or have a missing or unknown response; the
  owner keeps unanswered ones by selecting `none` (Settings or `intake
  options`) before that run, and Settings shows that note while the defaults
  apply. Declined events cannot be kept. A complete read with items but no
  string `response` on any of them fails the calendar step with
  `SCHEMA_CHANGED` before any mutation, so a renamed field cannot remove every
  synced event.
- **Writes stay stdio/local.** No new HTTP MCP tool or public route is added.
- **Scheduling** is owned by the Mac app, which runs `intake run
  --noninteractive` (all selected sync connectors) itself, with launch and wake
  catch-up and at most two attempts per slot. A user-installed launchd
  LaunchAgent running the same command stays optional; one CLI run lock covers
  both. Background runs never open a browser login.
- **Setup (amendment, 2026-11-10).** `intake setup` may install that
  LaunchAgent itself, now running `intake run --noninteractive`, only with an
  explicit `--schedule` or an interactive yes; `intake schedule <value|off>`
  changes it alone. A connector setup skips is disabled, keeping its source ID. It probes only the known server
  names above and still needs explicit calendar sensitivity consent.

### Consequences

- Local state lives in `~/.ttobak/intake/` (0700 directory, 0600 files): the
  selection, per-source ledger, pending batch, status and a run lock.
- Midway expiry surfaces as `AUTH_EXPIRED_MIDWAY` and needs a human `mwinit`.
- The calendar source exposes no sensitivity flag. On 2026-11-09 a synthetic
  event marked Private in Outlook arrived from `calendar_view` with the same
  fields as a public one, and no `aws-outlook-mcp` calendar tool mentions
  sensitivity. Private items are therefore neither hidden nor flagged. Real runs
  fail closed with `SENSITIVITY_UNKNOWN`; `--accept-unknown-sensitivity` means
  accepting that private items reach the user's private TTOBAK calendar. Dry
  runs report the count.
- The task step adds exactly two provider writes, both by the deterministic
  runner (this replaces "no unattended provider writes" for these two tools):
  - `acknowledge_request`, only when `get_request` shows `NEXT_UP` with me as
    resolver, for a request linked to exactly one "theirs" task, once per
    request ID ever. The local ack ledger entry is written (fsync, atomic
    rename) before the call; an uncertain outcome is decided by a later read,
    never by a second call. `--auto-ack off` replaces it with a notice.
  - `post_draft`, which saves an unsent Slack draft in the counterpart's DM,
    only after `list_drafts` shows no draft there, once per task. It never
    overwrites a draft and never sends.
- Task rows are private to the user (`USER#`/`SRTASK#`). They hold occurrence
  identity, account, path, state, the counterpart's TTOBAK user ID and the
  linked SpecReq ID, title and status. Attendee addresses, Slack IDs and text,
  and SpecReq bodies stay on the Mac.
- `get_meeting` `isPrivate` is a second sensitivity source, which
  `calendar_view` lacks. A private meeting gets no task, and an unknown flag or
  attendee list means no task.
- Codex TOML is read by a bounded in-repo parser (`toml.ts`); no new dependency.
- The Mac app spawns node with argv only and a cleared environment (`HOME`,
  `LANG`, `PATH` from a one-time login-shell probe). Cancel, timeout and app
  exit SIGTERM the run's process group and always SIGKILL it after a grace
  period, even if the CLI leader exited; app exit also blocks later spawns.
- Adapter update channel: the Settings button fetches the exact https URL
  `https://ttobak.atomai.click/mcp/ttobak-mcp.mjs` (the setup guide's URL; no
  redirects, at most 8 MiB, must contain `intake setup`) and swaps it in
  atomically, keeping one `server.mjs.bak`. Update is refused during a run;
  check is allowed.

