# Infrastructure reference

This describes checked-in CDK and workflow configuration, not a live inventory or
compliance attestation. Use `infra/bin/infra.ts` for stack construction/dependencies,
`infra/lib/*-stack.ts` for resources, and workflow logs for deployed revision/state.
Root security requirements remain requirements even where existing IaC has a gap.

## Stack ownership

Meeting PDF exports are tagged temporary objects under the existing `files/`
download prefix. StorageStack declares seven-day current/noncurrent expiration
only for `ttobak-artifact=meeting-pdf-export`; other files remain outside this
rule. Noncurrent age starts at supersession, so versioned bodies can survive
current expiration; physical cleanup is asynchronous. The declaration and synthesis
do not establish that the rule is deployed.

| Stack | Role | Dependencies explicitly declared in the app |
|---|---|---|
| TtobakWebSearchGatewayStack | AgentCore Web Search Gateway, us-east-1 | None |
| TtobakStorageStack | Main DynamoDB table, assets bucket/OAC policy | None |
| TtobakAuthStack | Cognito pools/clients, identity credentials, triggers | Storage |
| TtobakKnowledgeStack | KB bucket, AOSS declarations, external KB identifiers | Storage |
| TtobakAiStack | Service roles/policies, research/simulator permissions | Storage, Knowledge, Auth, WebSearchGateway |
| TtobakEdgeAuthStack | CloudFront viewer JWT validation, us-east-1 | Auth |
| TtobakGatewayStack | HTTP/WebSocket APIs, Go/Python functions, conversion/events | Auth, Storage, AI, Knowledge, WebSearchGateway |
| TtobakCrawlerStack | Crawling/ingestion workflow and workers; separate legacy news drain switch | AI, Storage, Knowledge, WebSearchGateway |
| TtobakResearchAgentStack | Legacy Bedrock Agent/tools and AgentCore spans log group | Storage, Knowledge |
| TtobakWhisperStack | GPU Spot ECS, ECR, production/benchmark tasks | Storage |
| TtobakFrontendStack | Static site, CloudFront, runtime config and media keys | Gateway, EdgeAuth, Auth |
| TtobakBillingStack (optional) | Billing BFF Lambda Web Function, code bucket, execution role | Auth |

Most resources use the application region (configured as ap-northeast-2).
Cross-region producers/consumers use the settings in the app; do not replace
unresolved CDK token operations with ordinary JavaScript string methods.

## Authentication and ingress

AuthStack explicitly disables self-signup. AdminCreateUser invitations are the
account-creation gate. The domain allowlist is supplemental; RESEND trigger
behavior is not verified. SPA client has no secret; server OAuth and MCP clients
have separate settings. Inspect `auth-stack.ts` before changing flows/scopes.
The existing user pool retains its logical ID, has deletion protection enabled,
and uses `Retain` for both deletion and replacement. The app also enables AuthStack
termination protection. These declarations protect identity storage; they do not
prevent every authentication configuration change or establish administrator MFA.
Before applying an auth change, inspect the diff for pool replacement. Deploy
AuthStack individually with `--exclusively`, then verify the live pool's
`DeletionProtection=ACTIVE` and stack's `EnableTerminationProtection=true`.
Intentional retirement requires a separately reviewed identity migration and
explicitly disabling both protections; retaining the pool does not preserve
every client or trigger after stack deletion.
HTTP MCP is enabled by `ttobak:mcpResourceAudience` (set in `cdk.json`). It adds
that exact HTTPS `/api/mcp` audience to the JWT authorizer next to the client
audience, and AuthStack registers the URL as a resource server and uses managed
login (v2, Cognito-provided branding per client): Cognito issues `aud`=URL for a
`resource` request only with both. GatewayStack then runs `ttobak-mcp` (Node.js 22,
ARM64, exact origin-secret read permission, 29s timeout, 25s server deadline) behind `ANY /api/mcp`
with the JWT authorizer, plus two unauthenticated static discovery routes,
`GET /.well-known/oauth-protected-resource` and `.../api/mcp` (ADR-045).
FrontendStack routes those two exact paths to the API origin without the edge
function; `/api/mcp` keeps the `/api/*` edge client-ID check, whose 401 for that
path carries `WWW-Authenticate` with the path-specific `resource_metadata` URL. The Lambda checks
`x-origin-verify` and pins Host to the public host.

The connector activation configuration enables the dedicated read and read/write
public clients and private MCP transport. Both use authorization code with PKCE,
the exact MCP resource audience and `https://ttobak.atomai.click/api/mcp/read`; only the write client
can request `https://ttobak.atomai.click/api/mcp/write`. They never enter the identity pool, API
first-party allowlist or WebSocket allowlist. Private HTTP MCP accepts only these
clients. Its edge challenge follows `connectorPrivateBridgeEnabled` and includes
the read scope; protected-resource discovery also advertises optional write.
The retained RSA key, metadata-only transactional grant reads and private
API-live/QA invocations remain unchanged. First-party stdio is independent.
See the [connector release procedure](runbooks/connector-release.md) for provider,
IAM/KMS, current-grant and rollback acceptance; source flags do not prove rollout.
These examples use the production resource URL; another deployment derives
`{MCP_RESOURCE_URL}/read` and `/write` from its exact configured URL. Both custom
scopes belong to the existing URL-identified `McpResourceServer`, alongside its
reserved `access` scope. The old `ConnectorResourceServer` is retained without
being referenced by these clients' allowed scopes. Cognito rejects resource
binding when custom scopes belong to a different resource-server identifier.

The public SPA/MCP client retains its two local port-9876 callbacks and adds
deployment-specific redirects from the SSM StringList
`/ttobak/auth/mcp-callback-urls`. Bootstrap that parameter in the deployment
account and region before deploying AuthStack; its absence fails deployment.
Use exact approved redirect URLs without empty entries or duplicate local
callbacks. Keep private workstation hostnames in SSM instead of source, CDK
context or published history. Confirm the authenticated code-server proxy reaches
the client's listener before adding its callback, then verify the deployed
Cognito callback list and actual OAuth completion separately. See the
[client instructions](../mcp-server/README.md#http-mcp-from-code-server).

The companion client (`ttobak-companion-client`, export `TtobakCompanionClientId`)
lets companion apps such as per-customer insight dashboards sign users in through
managed login with their TTOBAK identity. It is a public code-grant client
without `USER_PASSWORD_AUTH`. Direct `USER_SRP_AUTH` stays enabled, as on the
SPA client, but companion apps must use managed login, send an S256 PKCE
challenge (Cognito cannot require it per client) and exchange the code
server-side. Its ID tokens carry the companion client ID in `aud` and its access
tokens in `client_id`. The HTTP API JWT authorizer, edge auth and identity pool
accept only the SPA client. Behind them, the Go API middleware checks every
token it verifies, on the `Authorization` and `x-amzn-oidc-data` paths alike,
against `API_ALLOWED_CLIENT_IDS`, and the WebSocket authorizer against
`WS_ALLOWED_CLIENT_IDS`; both lists hold the first-party app and SPA clients
and fail closed when unset. Companion tokens therefore never act as TTOBAK API
or live-QA credentials. Rollout: AuthStack deploys before GatewayStack, so a
companion client can exist briefly before the gates; no companion app can hold
tokens until it is configured with the new client ID after both deploy. Bootstrap the SSM StringLists
`/ttobak/auth/companion-callback-urls` and `/ttobak/auth/companion-logout-urls`
with each app's exact callback and sign-out URLs before deploying AuthStack;
their absence fails deployment. Companion apps authorize by reading TTOBAK
account membership themselves; TTOBAK stacks grant them no table access. The
AWS Billing Insight companion stack grants its own redeemer `dynamodb:GetItem`
and `dynamodb:DeleteItem` on `ttobak-main`, limited by `dynamodb:LeadingKeys` to
`HANDOFF#*`, to show and then consume the API's single-use hand-off rows (see API-SPEC "Billing Insight hand-off").

The pre-signup trigger reads the configured domain allowlist. PostAuthentication
writes a separate USER#/LOGIN item for lastLoginAt, with a short abort timeout,
try/catch, no reserved concurrency and a DISABLED switch. It must fail open; it is
not called on refresh-token reauthentication. Both are Node assets, not Go binaries.

Pre-signup packages its sibling `policy.mjs` too. Its sole address exception is
`demo@atomai.click` on `PreSignUp_AdminCreateUser` (case-insensitive email match);
other addresses still follow the domain policy. It grants no group membership
and does not enable self-signup.

EdgeAuth validates viewer JWTs for application API requests. GatewayStack uses a
Cognito HTTP JWT authorizer plus Go/Python integrations: Go chi payload **1.0**,
Python QA payload **2.0**. The literal public-doc GET registration deliberately has
no authorizer; its handler verifies the bearer share token. The two MCP discovery
GETs above are the only other unauthenticated routes. The broader edge
`/api/public/*` bypass is not permission to register more public routes.

WebSocket QA uses the site's CloudFront `/ws` behavior, rewritten to `/production`.
Caching is disabled; AllViewerExceptHostHeader forwards negotiation headers and
the query token while API Gateway receives its own origin Host. `$connect` uses
the Go ws-authorizer and requires both the token and `x-origin-verify`.
GatewayStack owns a retained, generated Secrets Manager proof and a policy scoped
to that secret's GetSecretValue operation on the existing authorizer role.
CloudFront receives a dynamic reference in its private origin header; Lambda env
contains only `WS_ORIGIN_SECRET_ARN`, and public config contains only `/ws`.
The verifier caches the secret for 60 seconds, bounds each lookup to three seconds,
and denies failed/expired reads. JWT verification still runs for every connect.
`$connect`, `$disconnect`, and `$default` invoke the existing websocket Lambda;
`ask_live` asynchronously invokes Python QA. See the
[WS rollout and rotation runbook](runbooks/websocket-runtime.md).

CloudFront-only application HTTP ingress is policy. HTTP consumers require an
origin proof loaded from the exact Secrets Manager ARN and immutable version in
their environment. CloudFront resolves that version's current value through a
private dynamic reference. The normal infrastructure release verifies deployed
reader readiness and live proof agreement, seeds the same proof, then performs a
bounded consumer-first rotation with cache drains and retired-proof rejection
checks. Current source does not prove the live rotation completed: retain its
redacted receipt. See the [HTTP proof migration contract](runbooks/http-origin-secret-migration.md).
GatewayStack's optional `ttobak:httpOriginReleaseCiRoleName` context keeps
`GetSecretValue`, `PutSecretValue` and `UpdateSecretVersionStage` on the exact
`HttpOriginSecretArn` parameter and adds `lambda:InvokeFunction` only on the four
existing same-account, same-region coordinator targets: `ttobak-api`,
`ttobak-api:live`, `ttobak-qa` and `ttobak-mcp`. These statements share the existing
CI-role policy; missing context or boolean `false` omits it. Management-role
bootstrap remains separate. Source does not prove deployed permissions, CI
acceptance or completed release; see the [coordinator guide](../scripts/operations/HTTP_ORIGIN_RELEASE.md).
This HTTP rollout is distinct from the required WS secret. Existing browser Cognito/Transcribe SDK calls, signed S3
PUTs and IAM-signed server callback requests are separate service integrations,
not newly approved public application origins.

## Storage and retention

`ttobak-main` uses PK/SK strings, on-demand billing, retain-on-removal, PITR and
NEW_AND_OLD_IMAGES stream configuration. The stream's presence does not mean it
triggers summarization: current GatewayStack uses EventBridge transcript/custom
events. Key schemas are in Go model files and Python artifact implementations.

| Index | Partition / sort key | Purpose |
|---|---|---|
| GSI1 | GSI1PK / GSI1SK | Entity-specific reverse/list queries |
| GSI2 | GSI2PK / GSI2SK | Email lookup |
| GSI3 | meetingId / entityType | Direct meeting entity lookup |
| GSI4 | GSI4PK / GSI4SK (number) | Crawled-document type/date lookup |

Table TTL is `pendingShareExpiresAt`, used by pending grants, QA web-search
hourly counters, QA source-detail metadata and async job input/result/proof rows.
Grant, metadata and job reads also enforce their respective expiry. Existing
QA conversation/cache/rate-limit rows with uppercase `TTL` are not swept by this
setting. Do not claim every PII row has enforced retention, or switch TTL names
without a separate data-retention review. StorageStack does not declare a customer
managed KMS key for the table; the policy requirement is not evidence it does.

Assets and KB buckets use S3-managed encryption, versioning, retain policies and
Block Public Access. Media reads use an OAC allowlist for audio/, images/, files/,
docs/ and docs-pdf/. New categories must extend that allowlist; transcript spill
objects are not public media. Signed GETs use CloudFront with an S3 fallback when
signing material is unavailable; signed upload PUTs still target S3 directly.

Conditional transcript writers use immutable `{field}.{32-lowercase-hex}.txt`
keys under the authorized meeting's transcripts/ prefix; legacy fixed keys remain
readable. Bucket versioning is not what makes those new keys immutable. Failed
definite conditional writes clean only new spills; ambiguous writes retain them.
Deploy Go/QA reader compatibility before writers (ADR-037).

Whisper benchmark objects under bench-transcripts/ have current and noncurrent
version expiry; a delete marker is not immediate physical deletion of all versions.
Transcribe vocabulary table files under vocab/ (`vocab/{name}.{fingerprint}.tsv`) expire after
one day, current and noncurrent; the API deletes each after READY, FAILED or quota rejection. The
prefix is not in the OAC allowlist and is never served by CloudFront.
Inspect exact lifecycle values in StorageStack when changing retention. The
websocket Lambda has no current dedicated connections-table CRUD; old documentation
for a separate ttobak-connections table described an obsolete design.

## Gateway artifacts and events

| Artifact | Packaging / runtime | Timeout | Memory |
|---|---|---|---|
| api | Go ARM64 zip, provided.al2023 | 30s | 256MB |
| research-worker | Go ARM64 zip | 15m | 512MB |
| transcribe | Go ARM64 zip | 5m | 512MB |
| summarize | Go ARM64 zip | 15m | 512MB |
| process-image | Go ARM64 zip | 2m | 1024MB |
| kb | Go ARM64 zip | 12m | 1024MB |
| ws-authorizer | Go ARM64 zip | 10s | 128MB |
| websocket | Go ARM64 zip | 29s | 256MB |
| qa | Python 3.12 ARM64 | 300s | 512MB |
| sim | Python 3.12 ARM64 | 15m | 1024MB |
| document-extract | Python 3.12 ARM64, bundled pinned dependencies | 90s | 1536MB |
| convert-doc | Go/LibreOffice ARM64 container | 5m | 3008MB |

Model/environment selection belongs to each function, not one repo-wide model.
`model-config.ts` resolves `MODEL_DEPLOYMENT_MODE`, `MODEL_MEETING_SUMMARY` and
`MODEL_TRANSLATION` (or explicit `model-*` CDK context), shared by GatewayStack
and AiStack. Presets are `standard` (default Sol/Luna), `terra` (Terra/Luna) and
`sonnet` (Sonnet/Luna); individual IDs override presets. See
[model deployment](runbooks/model-deployment.md) for precedence, workflow inputs,
IAM cutover and rollback.
`MODEL_RETAIN_MEETING_SUMMARY` / `MODEL_RETAIN_TRANSLATION` keep bounded exact
outgoing grants during a staged transition. The release coordinator checks
deployed and selected model permissions before mutations and prevents premature
retained-grant removal using deployed function timeout/last-modified metadata.
`gateway-stack.ts` defaults to GPT-6.1 Sol for final notes via
`BEDROCK_SUMMARY_MODEL_ID`, while auxiliary Opus calls remain on `BEDROCK_MODEL_ID`.
Only the summary role receives the selected final-note model/profile grant.
Images use Opus 4.8, Sonnet 5.5 is used
for QA/simulator; Go refinement uses its Sonnet configuration and lightweight Go tasks
use Haiku 5.5. QA detection and Translate are separate service/model choices.
The API Lambda's `BEDROCK_TRANSLATE_MODEL_ID` defaults to GPT-6 Luna for
`/api/translate` `quality=high`; only the API role receives the selected exact
model/profile grant (`InvokeInterpreterModel`), with Amazon Translate as fallback.
Unsupported translation request families fail synthesis; currently only OpenAI GPT
IDs are accepted. Unknown presets and unsafe model IDs fail synthesis as well.
The API Lambda also receives `TRANSLATE_HOURLY_LIMIT` (default 3000) and
`TRANSLATE_HIGH_HOURLY_LIMIT` (default 1500) from CDK context
`ttobak:translateHourlyLimit` / `ttobak:translateHighHourlyLimit`; synth rejects
values that are not non-negative integers, and 0 disables a limit.
`TRANSLATE_HIGH_TIMEOUT_MS` (context `ttobak:translateHighTimeoutMs`, default 6000)
bounds each interpreter call; synth rejects values outside 500–8000. Changing either
changes the function configuration and publishes a new `live` alias version. The
counters use the existing table grant and `pendingShareExpiresAt` TTL.
`MEETING_TRANSLATION_ENABLED` (context `ttobak:meetingTranslationEnabled`, strict
boolean, default `false`) enables on-demand meeting translation, and
`MEETING_TRANSLATE_DAILY_BYTES` (context `ttobak:meetingTranslateDailyBytes`,
default 2000000, 0 disables) bounds its daily per-user input bytes; synth rejects
other values. Both are API-only and reuse the existing Translate/interpreter grants.

Custom vocabularies: AiStack's API role statement `TranscribeVocabulary` allows
Create/Update/Get/DeleteVocabulary only on
`arn:{partition}:transcribe:{region}:{account}:vocabulary/ttobak-vocab-*`; the
existing assets-bucket read/write grant covers vocab/ table writes, deletes and
the caller-credential file read by Create/UpdateVocabulary. The transcribe role
keeps `GetVocabulary` on `*` for batch jobs. GatewayStack does not set
`VOCAB_REBUILD_MIN_INTERVAL` yet, so the API uses its 10m default. Operator
checks before rollout (vocabulary quota, default 100 per account) are in ADR-008.

The Go API also depends on the transcribe Lambda so recovered-copy event guards
are installed before the recovery producer changes.

The Go API explicitly depends on the QA Lambda so private relinking cannot deploy
before the consumer's canonical publication checks.
QA REST jobs use `ttobak-qa-jobs`: SQS-managed encryption, TLS, 1800s visibility,
one-day retention/DLQ, batch one, concurrency two, partial batch failures.
GatewayStack owns scoped queue permissions and JWT submit/poll routes, injecting
`QA_JOBS_QUEUE_URL`/`QA_JOBS_QUEUE_ARN`. Job rows enforce one-hour
`pendingShareExpiresAt` expiry. FrontendStack's omitted/false
`qaAsyncJobsEnabled` still emits false. The app now explicitly opts in with true
after recorded backend acceptance; browser activation proof remains pending.
See the [activation evidence and remaining checks](runbooks/qa-current-source-rollout.md#async-ui-opt-in--2026-09-13)
and [async contract](../backend/python/qa/ASYNC_CONTRACT.md).

| Trigger | Target |
|---|---|
| S3 Object Created, audio/ | transcribe |
| S3 Object Created, transcripts/ | summarize |
| Custom AllPartsTranscribed | summarize |
| Custom ImageUploadCompleted | process-image |
| Custom ActionItemsRequested, ttobak.analysis | summarize, action analysis only |
| Custom TermsRequested, ttobak.analysis | summarize, automatic term extraction only |
| Custom DocumentUploadCompleted, ttobak.upload | document-extract, queued canonical runs only |
| S3 Object Created, docs/ slide suffix filter | convert-doc |
| One-minute ttobak-kb-index-tick | kb; configured enabled by the checked-in CDK app, all mode |
| Canonical DynamoDB stream records | kb; mapping/grants created only in all mode |
| Scheduled warming event | API alias |
| SQS ttobak-qa-jobs, single-record mapping | qa; durable job execution, concurrency two |
| API simulator invoke | sim, asynchronous |

The image rule is not a raw images/ prefix trigger, so simulator chart writes do
not invoke process-image. convert-doc produces a docs-pdf/ sidecar and has docs/*
read plus docs-pdf/* read/write permissions in isolated subnets. Source GET
ETag/version metadata and conditional preview replacement bind converted bytes.
Canonical readers validate that binding; the preview URL still checks existence.
Cross-tenant original/preview reads and parser risk remain (ADR-022).

ActionItemsRequested delivery has three retries within five minutes and an
SSE-SQS DLQ with seven-day retention. The exact rule ARN scopes Lambda invoke and
queue SendMessage. The existing summarize worker processes owner/meeting/run IDs
without rerunning transcription/summary. Analysis state and five-minute leases
live in `MEETING#{id}/ANALYSIS#actionItems`; expired work becomes retryable failure.
The DLQ covers delivery, not downstream model failures.

Core audio/crop, image, transcript, multipart and optional slide-conversion
EventBridge targets retain exhausted delivery failures in the Gateway-owned
`ttobak-pipeline-delivery-failures` queue. Transcribe, summarize, process-image and
optional convert-doc retain exhausted asynchronous execution records in the
separate `ttobak-pipeline-execution-failures` queue, preserving two Lambda retries
and the six-hour event-age default. Both queues use SSE-SQS, require TLS, retain
seven days and have no automatic consumer. Gateway-owned exact queue-send
policies avoid a reverse dependency on AiStack roles.

Backlog/age, worker failure/drop/throttle and EventBridge delivery alarms are
declared in `PipelineFailures`. Existing QA/index/action/saved-summary DLQs retain
their behavior and gain queue alarms. Notifications require the explicit
`PipelineAlarmTopicArn` parameter and separately verified topic policy/subscriber
delivery; the empty default reports `UNCONFIGURED`. The inspected replay tool
defaults to dry run, preserves original events/run IDs and never deletes messages.
See [failure retention and replay](runbooks/pipeline-failure-recovery.md) for
limits, operator prerequisites and required live acceptance.

### Attachment extraction

The app enables the DocumentExtraction construct. Its worker validates current
MEETING#/ATTACH#/ATTEXT# identities, run/lease and original ETag before publishing
immutable results under `files/{uploader}/{meetingId}/text/{attachmentId}/{runId}.json`.
Input/output bounds are 20 MiB/1 MiB. Native-text PDF, PPTX, DOCX and Markdown
extraction has explicit partial/unsupported states and no OCR/model/network fetch.
Parser subprocess environment/resource restrictions supplement deployment isolation.

The worker uses PRIVATE_ISOLATED subnets and existing S3/DynamoDB endpoints. Its
security group permits only HTTPS to those managed prefix lists, with no ingress.
IAM permits files/* reads, result-prefix PUTs and DynamoDB
GetItem/UpdateItem/ConditionCheckItem; regional ENI wildcards have RequestedRegion
conditions. These prefix/table grants span users, so canonical checks remain
essential. Logs retain 30 days; event delivery retries three times within five
minutes to a seven-day encrypted DLQ, with Lambda async retries disabled.

Deploy/verify the worker before the wired API upload/retry producers and summary
consumer. API status/text routes expose validated results; QA/UI activation remains
separate. Creating this construct does not backfill attachments. See
[worker contract](../backend/python/document-extract/LAMBDA.md) and ADR-039.

## Whisper and research

Whisper uses the imported VPC's actual private-with-egress subnets and a mixed
g4dn.xlarge/g4dn.2xlarge ASG (min/desired 0, max 10). Price-capacity-optimized
allocation uses GPU types observed in both 2a and 2b in account 180294183052 on
2026-09-17. That account's 2b did not offer g5; including it rejected the mixed
Fleet request. Recheck offerings before changing types or subnets (ADR-009).
The first active instance uses On-Demand capacity, preferring the smaller type,
so a Spot shortage cannot prevent all workers from starting. Additional instances
use Spot with the existing $1.10 hourly ceiling. The On-Demand base applies only
within desired capacity: idle capacity still scales to zero. On-Demand uses its
normal instance rate, not the Spot price ceiling; capacity is still not guaranteed.
Root disk is 200GiB encrypted gp3, and ECS stopped-task cleanup is three minutes
to avoid disk exhaustion when a host is reused. Host-network tasks request one GPU.
These choices do not prove current instances or capacity availability.

Production faster-whisper pins and pyannote 4.x/community-1 are a coherent image
configuration. Production DIARIZATION_S3_KEY is image-owned. The separate WhisperX
benchmark task has its own bundle variable; do not conflate the two. Its
run_engine.py allowlisting entrypoint must not be overridden by CDK entryPoint or
command. Engine selection uses ENGINE; image and CDK tests guard the boundary.

The capacity provider enables ECS managed draining, so aws-cdk-lib adds no
InstanceDrainHook lifecycle hook or drain Lambda; that hook could not drain a host
that had not yet registered with ECS. The `WhisperAzRebalanceSuspended` custom
resource suspends `AZRebalance` on `ttobak-whisper-asg` (its IAM is
`autoscaling:SuspendProcesses`/`ResumeProcesses` on that group only), and deleting
it resumes the process. Host user data sets `ECS_ENABLE_SPOT_INSTANCE_DRAINING=true`.
The production image sets `PYTHONUNBUFFERED=1`. Every WhisperStack deploy also
reapplies `desiredCapacity: 0`; deploy it only while the cluster is idle.

GatewayStack's `ttobak-stt-recovery` rule (`rate(15 minutes)`, retry 0, 10-minute
maximum age, no DLQ) sends the fixed input `{source: ttobak.recovery,
detail-type: SttRecoveryTick, detail: {}}` to the existing transcribe Lambda.
`STT_RECOVERY_MODE` and `STT_RECOVERY_DELETE_MODE` (`off|report|active`) default to
`report`. The transcribe role adds only `ecs:ListTasks` on `Resource: "*"`
confined by an `ArnEquals ecs:cluster` condition to the Whisper cluster (a
cluster-only ListTasks call does not match a container-instance resource);
RunTask and PassRole are unchanged. Existing AiStack table and bucket grants cover
the scan, claims, transactional deletes, transcript self-copy and object deletes.
Deletions leave noncurrent object versions in the versioned bucket. See the
[ADR-031 amendment](decisions/ADR-031-summarize-timeout-resilience.md#amendment-2026-10-17-scheduled-stt-recovery-and-failed-recording-retention).

Audio cropping uses the private `ttobak.audio` / `AudioCropRequested` EventBridge
rule to the existing transcribe Lambda and production Whisper task. It adds no
public compute origin or unauthenticated endpoint. API capability is gated by
`AUDIO_CROP_ENABLED`, default off. Enable `ttobak:audioCropEnabled=true` only after
the worker rollout and acceptance checks;
GatewayStack orders the transcribe consumer, event rule and invocation permission
before the API producer. The image must include `audio_crop.py` before enabling
the producer. Cropped-audio S3 writes do not start a second transcription task.
The source is ETag-pinned. A 30-second, run-bound heartbeat keeps long processing
current; DynamoDB writes disable SDK retries so rejected-copy cleanup is safe. FFmpeg has a 15-minute conversion timeout, only
file/pipe protocols, and an audio-container demuxer allowlist excluding playlists.
The existing image dispatcher/pins remain unchanged.

Deploy the Whisper image first, then the transcribe Lambda and GatewayStack
(`--exclusively`), then API/frontend. Before activation, verify a short real crop
and a five-hour-source/one-hour-selection case through the authenticated UI.
Local tests and synthesized dependencies do not establish deployed acceptance.

AgentCore research runs as a separate FastAPI/Strands container, updated outside
CDK by deploy-research-agent.yml. ResearchAgentStack still declares a legacy
Bedrock Agent/alias and tool Lambdas, not that AgentCore Runtime. AI imports
the pre-created ttobak-agentcore-research-role; absence from CDK creation is not proof
that the role is missing. Web search calls the us-east-1 Gateway through SigV4 from
three separately packaged callers.

The simulator interpreter role has no execution permissions and uses SANDBOX
networking. Generated option text is untrusted; its prompt/import checks are not
the security boundary. Verify role/network configuration when modifying execution.

## Knowledge and declared gaps

The activation configuration sets `knowledgeIndexingMode='all'` and
`knowledgeIndexScheduleEnabled=true`; the reusable construct's schedule default
is false. This retains the schedule and adds canonical stream mapping/read
permissions. Merge/deployment requires completed manual snapshot acceptance and
deployed, verified current-source QA. Configuration is not deployment evidence.

`cmd/kb` requires TABLE_NAME, assets BUCKET_NAME, KB_BUCKET_NAME, KB_ID,
DATA_SOURCE_ID and INDEXING_MODE. IDs must be ten alphanumeric characters; mode
must be manual-only or all, validated before AWS clients initialize. It accepts
DynamoDB Records or schedule/tick/sync envelopes; `/api/kb/*` stays in the API
Lambda. No OpenSearch or model inference permission belongs to this worker.

Manual-only creates immutable `manual-kb/v1/` and `shared-kb/v1/` snapshots from
private kb/* and authenticated-global shared/* originals. Originals are read-only;
canonical sources/jobs and legacy meeting exports remain untouched. DynamoDB
writes are restricted to KBINDEX#JOBS/KBINDEX#CONTROL through LeadingKeys plus a
non-null condition. All mode adds canonical source reads, canonical/v1/ projection
access and legacy meetings/ cleanup; it never grants writes to source rows.

Full S3 ingestion is coalesced through durable run/lease/revision conditions.
Unknown provider outcomes freeze mutations; crashes can retain the 20-minute
coordinator lease. GetKnowledgeBaseDocuments is KB-scoped, matches exact URIs and
requires initial data-source sync; partial/missing replies are not document success.
No direct ingestion is used. The external data source must include all snapshot
prefixes and support provenance filtering.

Activation order is worker with delivery off, restricted manual-only configuration,
explicit schedule enablement, snapshot/recall verification, strict QA deployment,
then all-mode canonical delivery. The checked-in enabled schedule makes rollout
preparation a deployment concern. Durable mode rejects downgrades; restore all
after a mistaken downgrade. Old-QA rollback after canonical cleanup needs reviewed
legacy re-export. Follow the [bootstrap runbook](runbooks/knowledge-index-bootstrap.md)
and [source contract](../backend/internal/service/INDEX_SOURCE_CONTRACT.md).

QA now receives separate assets and KB bucket names. AiStack grants read/version
access to assets transcripts/*, docs/*, docs-pdf/*, files/* and KB kb/*, shared/*,
news/v1/* (immutable published news only),
plus account-conditioned listing to distinguish absence from denied reads. It adds
no object writes/deletes. Handler registration and deployment acceptance status
are tracked in the [QA source contract](../backend/python/qa/SOURCE_CONTRACT.md);
IAM grants alone do not activate the consumer. Legacy cache variables remain
injected. Strict QA cutover must follow binary snapshot verification and
the [current-source rollout](runbooks/qa-current-source-rollout.md).

Deploy AiStack's `ReadCurrentPublishedNews` grant before accepting the news QA
reader. The 2026-10-21 public-source canary exposed a real `HeadObject` denial
without this prefix; API publication/detail success did not establish QA access.
Verify current-source HTTP and WebSocket answers after the grant is deployed.
The local frontend deployment entrypoint resolves the site bucket and distribution
from `TtobakFrontendStack` outputs before uploading; missing outputs stop publication.

KnowledgeStack retains externally provisioned KB/data-source IDs and AOSS-related
resources while a KB teardown remains staged and intentionally undeployed. Its
AOSS network policy currently declares AllowFromPublic; that is an existing policy
mismatch, not proof of private access or an approved pattern for new resources.
Do not deploy this stack as an incidental dependency or claim source equals live
state. Hardcoded IDs/ARNs are existing deployment debt; never replace real IDs with
PENDING placeholders.

## Frontend and deployment

FrontendStack owns the static bucket, CloudFront SPA router, runtime config,
signing key setup and media behavior. Dynamic routes are mapped by knownPages;
404 fallback alone is not the complete routing mechanism. config.json is deployed
separately from next build. Both Chat and Live QA use its relative `wsUrl: "/ws"`;
the client accepts only the same-site WS path. Local development defaults to REST;
loopback WS validation needs an explicitly supplied config and local proxy.
Invalid/missing config retains REST fallback. config.json also publishes
`mcp.url: "/api/mcp"`, the `connectorPrivateBridgeEnabled` mode and the two
dedicated public connector client IDs from AuthStack. These are nonsecret IDs;
the source flags in `infra/bin/infra.ts` select the mode.

The default application behavior has CSP, one-year HSTS, nosniff, frame denial
and no-referrer. Its cache policy enables gzip/Brotli compression and has a zero
minimum TTL so origin `Cache-Control: no-cache` is honored; objects without cache
metadata retain the previous one-day default TTL. Infrastructure owns runtime
config and its no-cache metadata; the ordered frontend publisher supplies
HTML/RSC and mutable public-asset no-cache metadata before producer release.
A separate `/_next/static/*` behavior caches immutable assets. The application
CSP preserves Cognito/Transcribe, same-site WS, signed S3 fallback, blob
workers/PDF frames, Tauri IPC and WASM; static bootstrap/native injection still
require inline scripts. Media and PDF policies remain separate. These are source
declarations, not a live-header or device acceptance claim. See the
[frontend release contract](runbooks/frontend-release.md).
The router preserves segmented `__next.*.txt` payload names and maps their route
directories. Infrastructure publishes `staticRouterRevision: "segmented-rsc-v1"`
after distribution update; the shared publisher checks it before segmented
exports, preserving runtime config rather than manufacturing readiness.

The StorageStack OAC custom resource reads the distribution ID and tightens the
bucket policy. Its changing Timestamp deliberately forces re-invocation. The infra
workflow redeploys StorageStack after FrontendStack even following earlier failure;
removing that step/property breaks the tightening mechanism.

Run synth and Jest before deployment. Deploy changed stacks individually with
`--exclusively`, following the graph above, never `--all` or implicit dependencies.
The workflow excludes KnowledgeStack. The build helper and test/deploy CI loops
build all eight declared Go zip artifacts, including websocket/ws-authorizer, with
ARM64 and lambda.norpc. convert-doc remains a separate container image. See the
[deployment runbook](runbooks/deployment.md) and exact workflow before acting.

## Saved-summary retry delivery

`TtobakGatewayStack` routes default-bus events from `ttobak.analysis` with
detail-type `SummaryRequested` to the existing summarize Lambda. Detail contains
only `{meetingId,runId}`. Delivery expires after five minutes, with three retry
attempts and a seven-day SQS-managed encrypted DLQ.

The DLQ accepts `sqs:SendMessage` only from `events.amazonaws.com`, conditioned on
this exact rule ARN; redrive from other queues is denied. Lambda invocation is
likewise scoped to the rule ARN. There is no new public endpoint or wildcard
principal. The existing API default-bus `PutEvents` permission is sufficient.

The API function explicitly depends on the summarize consumer and the rule
construct, including its invocation permission. CDK assertions pin those
dependencies so the producer cannot update ahead of delivery setup. The document
worker from #208 must still be physically deployed before document upload
producers. See [release order](runbooks/meeting-document-release.md); this code
does not claim that deployment has happened.

## Automatic term extraction delivery

`TtobakGatewayStack` routes default-bus `ttobak.analysis` events with
detail-type `TermsRequested` (rule `ttobak-terms-requested`) to the existing
summarize Lambda. Detail contains only `{ownerId,meetingId,runId}`; the worker
rereads every source. Delivery expires after five minutes, with three retry
attempts and the SSE-SQS `ttobak-terms-dlq` (seven-day retention, redrive from
other queues denied). Queue SendMessage and Lambda invocation are scoped to the
exact rule ARN, and `PipelineFailures` watches the DLQ. The summarize role's
existing default-bus `PutEvents` and Bedrock grants cover publication and the
Claude model. The API function depends on the rule, as for saved summaries.

Summarize receives `AUTO_TERMS_MODE` (`off` by default, or `personal`),
`AUTO_TERMS_USERS` (comma-separated Cognito subs, empty means every owner),
`AUTO_TERMS_MIN_CONFIDENCE` (default `0.85`, in (0, 1]) and
`BEDROCK_TERMS_MODEL_ID` (default `global.anthropic.claude-sonnet-5-5`; a
`global.`/`apac.` Claude profile) from CDK context `ttobak:autoTermsMode`,
`ttobak:autoTermsUsers`, `ttobak:autoTermsMinConfidence` and
`ttobak:bedrockTermsModelId`; invalid values fail synth. No other function
receives these variables. With the default `off`, Enqueue returns `DISABLED`
before any read, write or publish, so the rule stays idle. Set `personal` only in
a deployment after one with `off` has delivered the rule and summarize code. Before
merging, the worker migrates the owner's legacy dictionary blob through the
summarize role's existing table grant. See ADR-008.

KB documents and research reports are term sources too (layered dictionary).
Rule `ttobak-terms-source-object` sends KB-bucket `Object Created` events for
keys under `kb/` and `shared/research/` (the bucket already has EventBridge
notifications on) to summarize, which maps `kb/{uid}/{name}` (Account from the
signed `account-id` metadata) and `shared/research/{id}.md` (owner and Accounts
from the research item) to a default-bus `ttobak.analysis`
`TermsSourceRequested` event; rule `ttobak-terms-source-requested` runs the
extraction in its own summarize invocation. Both targets expire after five
minutes with three retries into the SSE-SQS `ttobak-terms-source-dlq`, which
`PipelineFailures` watches. The research agent's execution role is imported, so
nothing is published from the agent. The summarize role gains only
`s3:GetObject` on `kb/*` and `shared/research/*` of the KB bucket (statement
`TermsSourceRead`, which also covers HeadObject). Summarize receives
`KB_TERMS_MODE` and `RESEARCH_TERMS_MODE` (`off` by default, or `on`) from CDK
context `ttobak:kbTermsMode` and `ttobak:researchTermsMode`; invalid values fail
synth, and with `off` the mapper and worker log `outcome=DISABLED` and do nothing
else. The source flags are independent of `AUTO_TERMS_MODE` and
`ACCOUNT_TERMS_MODE` (only the `AUTO_TERMS_USERS` canary list is shared): a
source flag set to `on` writes the Account layer even with `ACCOUNT_TERMS_MODE`
`off`. Only plain-text KB files (`.md`, `.markdown`, `.txt`) contribute terms
today; any other type logs `outcome=UNSUPPORTED`. The mapper passes the S3 key as
delivered and retries once with its `url.QueryUnescape` form only when the key
contains `%` or `+` and the object is missing; an object still missing (404, or
403 because the role has no `s3:ListBucket`) logs `outcome=NOT_FOUND` and is not
retried.

Daily news terms feed the Common layer. Rule `ttobak-terms-news-daily`
(`cron(0 21 * * ? *)`, after the 19:00 crawl) invokes summarize with
`{"kind":"news-daily"}`; two retries and a one-hour event age lead into the
SSE-SQS `ttobak-terms-news-dlq`, which `PipelineFailures` watches. Summarize
receives `NEWS_TERMS_MODE` from `ttobak:newsTermsMode` (`off` by default); with
`off` each run logs `outcome=DISABLED`. The same pattern applies to
`ttobak:accountTermsMode` (`ACCOUNT_TERMS_MODE`) and `ttobak:transcriptCorrection`
(`TRANSCRIPT_CORRECTION_MODE`).

### Dictionary layer metrics

`PipelineFailures.watchTermsLogs` adds metric filters on the summarize log group
(`/aws/lambda/<summarize function name>`, which Lambda creates on first invocation,
so deploy after the function has run once) publishing count-1 metrics to the
`Ttobak/Dictionary` namespace:

| Metric | Log line matched |
| --- | --- |
| `TermsLayerPersonal` | `terms run:` with `status=succeeded` |
| `TermsLayerAccount` | `layer=account` with `outcome=MERGED` (runs and sources) |
| `TermsLayerCommon` | `terms news:` with `outcome=SUCCEEDED` |
| `TermsAccountFailures` | `layer=account` with `outcome=FAILED`, `MEMBERS_FAILED` or `RECORD_FAILED` (three filters, summed) |
| `TermsSourceSucceeded`, `Disabled`, `Unsupported`, `NotReady`, `NotFound`, `Unchanged` | `terms source:` with the matching `outcome=` |
| `TermsSourceTriggered` | `terms source trigger:` with `outcome=QUEUED` |
| `TermsNewsRuns`, `TermsNewsDisabled`, `TermsNewsFailed` | `terms news:` with `outcome=SUCCEEDED`, `DISABLED`, `FAILED` |
| `TranscriptCorrectionApplied` | `transcript correction outcome=APPLIED` (one per corrected-path meeting) |
| `TranscriptCorrectionNone` | `transcript correction outcome=APPLIED` with `corrections=0` |
| `TranscriptCorrectionUnavailable` | `transcript correction outcome=GLOSSARY_UNAVAILABLE` |

The correction line is `transcript correction outcome=APPLIED meeting=<id>
corrections=<n>`. A metric filter cannot extract `<n>` from the
`corrections=<n>` token, so the metrics count meetings, not corrections:
meetings with at least one correction are `Applied - None`.

Failed Account merges are not retried, so `TermsAccountFailures` is the signal
that some linked meetings missed the Account layer. Alarms `TermsAccountFailures`
and `TermsNewsFailed` fire when the daily sum of the metric of the same name is
at least 1 (one daily period, `notBreaching` on missing data, same
`PipelineAlarmTopicArn` action as the other alarms).

Alarm `TermsSourcesSilent` uses metric math over three daily-sum metrics:
`IF(FILL(triggered, 0) > 0 AND FILL(succeeded, 0) + FILL(unchanged, 0) == 0, 1, 0)`
with `triggered = TermsSourceTriggered`, `succeeded = TermsSourceSucceeded` and
`unchanged = TermsSourceUnchanged` (an unchanged re-run is a healthy run). It
evaluates one daily period, so it fires when source triggers were queued on a
UTC day and no source run completed that day (a run follows its trigger within
minutes). CloudWatch caps period times evaluation periods at one day, so a
longer streak cannot be one alarm. Triggers are logged
`QUEUED` only while `KB_TERMS_MODE` or `RESEARCH_TERMS_MODE` is `on`, so the
alarm stays quiet with both off. Missing data is `notBreaching`, and the alarm
action follows the `PipelineAlarmTopicArn` convention of the other alarms.

## Summary privacy writer activation

GatewayStack also declares `AccountDescendantReadsEnabled` (`0` or `1`, default
`0`). It supplies `ACCOUNT_DESCENDANT_READS_ENABLED` only to API and QA and binds
the API immutable version description. Deploy reviewed readers and transactional
child-reference writers disabled, bootstrap/verify metadata-only `CHILDREF#`
rows, then activate using previous values for every other parameter. References
never authorize alone. See [the release runbook](runbooks/account-descendant-sharing.md)
and ADR-058; configuration alone does not prove deployed acceptance.

GatewayStack declares `SummaryPrivacyWriterEnabled` and
`SummaryPrivacyReaderEnabled` (`0` or `1`, default `0`) plus
`SummaryPrivacyLegacyMemberCutoff` (empty installation value or fixed UTC RFC3339
timestamp ending `Z`). A CloudFormation rule rejects reader `1` with an empty
cutoff. Runtime readers additionally reject invalid calendar timestamps.

The writer parameter supplies `SUMMARY_PRIVACY_WRITER_ENABLED` only to API and
summarize. The reader and cutoff parameters supply `SUMMARY_PRIVACY_READER_ENABLED`
and `SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF` only to API and QA. All three values
enter the API version description so parameter changes replace the immutable
version before the live alias uses the configuration. Defaults retain installation
behavior; configuration wiring does not establish deployed activation.

Grant fences, pending/publication guards, owner sharing-safe regeneration and its
UI are installed. `ErrSummarySharingUnsafe` maps to 409 `SUMMARY_SHARING_UNSAFE`.
Activation still requires verified readers, canonical indexing, old-writer and
queued-delivery drain, and cross-user acceptance. Preserve the one recorded cutoff
on later deployments and rollback. Writer `0` restores legacy grant/publication
behavior, so rollback requires restricting those writes and draining legacy
exporters first. No historical backfill or grant revocation is implied. See
[the rollout contract](runbooks/summary-privacy-release.md).

## Billing Insight hand-off

Optional context `ttobak:billingInsightUrl` (`infra/lib/billing-insight.ts`) names
the companion dashboard origin; `infra/cdk.json` sets it. Unset or empty omits
the API Lambda's `BILLING_INSIGHT_URL` and the `billingInsight` flag in
`/config.json`, so the hand-off route returns 404 and no button renders. A set
value must be an https origin (optional trailing `/`, no path, query or
fragment) or synth fails; the API also revalidates it at startup. GatewayStack
passes the origin only to `ttobak-api`; FrontendStack publishes only
`billingInsight: true`, never the URL. The value is synth-time context, so a
change alters the function configuration and publishes a new version for the
`live` alias without the create-only description workaround. The API role's
existing table read/write grant covers the conditional put; no new IAM is added.

## Billing BFF stack

`TtobakBillingStack` (`infra/lib/billing-stack.ts`) is optional. `addBillingStack`
creates it only when context `ttobak:billingEnabled` is the string `"true"`;
then `ttobak:domainName` must be a bare hostname or synth fails. The key is not
checked in yet, so the default app has eleven stacks.

The data bucket and platform table physical names are never committed. The
stack reads them from SSM String parameters `/ttobak/billing/data-bucket-name`
and `/ttobak/billing/platform-table-name`, resolved by CloudFormation at
deploy. The operator creates both parameters before the first deploy. The
stack never creates, imports or deletes the bucket or the table.

Resources:

- `AWS::Lambda::WebFunction` `ttobak-billing`, one `WebFunctionRevision`
  (`nodejs24.x`, timeout 60 s, `maxConcurrencyPerEnvironment` 8) and the
  `WebFunctionEndpoint` `prod` (`HomeRegion`, `IamAuth`, 100% to the revision,
  `maxEnvironments` 10, `rateLimit` 500 requests per second). No VPC, network
  connector or security group; the BFF reaches S3 and DynamoDB over public AWS
  endpoints.
- Revision environment: `DATA_BUCKET` and `PLATFORM_TABLE` (from SSM),
  `ACCOUNT_TABLE` (`ttobak-main`), `PUBLIC_URL` (`https://` +
  `ttobak:domainName`), `USER_POOL_ID`, `BILLING_ALLOWED_CLIENT_IDS` (the SPA
  client only) and `NODE_OPTIONS=--max-old-space-size=1536`. No secrets.
- Log groups `/aws/lambda/web/ttobak-billing` and
  `/aws/lambda/web/ttobak-billing.local`, 90-day retention, retained on delete.
- Execution role trusted by `lambda.amazonaws.com`. It has no
  `Resource: *` statement and no `logs:CreateLogGroup`. It may:
  - write log streams and events in the two log groups (stack Region only);
  - read `accounts/*/versions/*`, `accounts/*/handwritten/*`,
    `accounts/*/uploads/*` and `data/whats_new_feed.json`;
  - manage uploads under `accounts/*/uploads/*` and write `jobs/*`;
  - get, put, update, delete and query items in the platform table.
- `ttobak-main` access is bound by IAM conditions that `lib/access.js` matches:
  - `GetItem` only for `ACCOUNT#*` partition keys and only for the attributes
    `PK`, `SK`, `name` and `parentAccountId`. A request without a projection is
    refused.
  - `Query` on `index/GSI1` only for `USER#*` partition keys, with
    `Select: SPECIFIC_ATTRIBUTES` and attributes limited to `PK`, `SK`, `GSI1PK`,
    `GSI1SK` and `accountId`. GSI1 projects all attributes and is shared with
    meeting and project rows.
- Dedicated code bucket: versioned, SSE-S3, TLS-only, all public access
  blocked, retained on delete, no lifecycle rule.
  - Its policy lets `lambda.amazonaws.com` read objects and object versions
    only for `web-function/ttobak-billing/revision/*`.
  - It denies `s3:DeleteObjectVersion` to the upload handler role.
  - A `BucketDeployment` (`extract: false`, `prune: false`,
    `retainOnDelete: true`) uploads the packaged `services/billing/bff/dist` as
    one content-hashed zip. It excludes `node_modules/.bin` symlinks, which the
    HTTP-origin release coordinator rejects in an assembly.
  - The revision depends on that upload, the bucket policy, the role and the
    log groups.

The CDK asset bucket cannot hold Web Function code. It encrypts objects with
the AWS managed `aws/s3` key, which Lambda cannot use, and it expires
noncurrent versions. Lambda pins each revision to an object version, and a
revision whose pinned version disappears fails permanently.

The `ttobak-main` name is passed as a literal, not a StorageStack export. The
stack imports only the existing AuthStack exports for the user pool and SPA
client IDs.

Outputs `FunctionArn`, `EndpointArn` and `EndpointDomain` have no export. The
operator copies them into `infra/cdk.json` for FrontendStack.

Deployment: `.github/workflows/deploy-billing.yml` runs on pushes to `main` that
touch `services/billing/**`, the Billing stack, the infra app, `cdk.json`, the
infra package files or the workflow, and on manual dispatch. It tests and
packages the BFF, then runs `cdk deploy TtobakBillingStack --exclusively` only
when `ttobak:billingEnabled` is `"true"`; otherwise it skips the deploy. The
HTTP-origin release coordinator's `STACKS` list does not include this stack.
Every workflow that synthesizes the app (`test-infra.yml` and both
`deploy-infra.yml` jobs) builds the BFF package first.

### Billing behaviors on the TTOBAK distribution

FrontendStack gains the Billing behaviors (ADR-056) only through context
strings, never stack exports, so the HTTP-origin release coordinator deploys it
the same way with or without Billing:

- `ttobak:billingEnabled` (`"true"`) plus all three of
  `ttobak:billingEndpointDomain`, `ttobak:billingFunctionArn` and
  `ttobak:billingEndpointArn` (the `TtobakBillingStack` outputs) turn them on.
- `billingFrontendContext` (`infra/lib/frontend-stack.ts`) fails synth when only
  some endpoint keys are set, or when any is set without Billing enabled. It
  also checks the shapes: the domain must be `<id>.<cell>.lambda-web.ap-northeast-2.on.aws`,
  the function ARN `arn:aws:lambda:ap-northeast-2:<account>:web-function/ttobak-billing`,
  and the endpoint ARN that function's `/endpoint/prod`.
- Billing enabled with no endpoint key is the bootstrap state.
  `TtobakBillingStack` exists, and FrontendStack has no Billing behaviors.
- `ttobak:billingService` (`"true"`, only with the behaviors) adds
  `billingService: true` to `config.json`: the Billing entry links to `/billing`
  instead of the companion hand-off. Unset keeps the hand-off; any other value,
  or the flag without the behaviors, fails synth. Rollback is removing the key
  and redeploying FrontendStack.

The three behaviors come after the discovery paths and before `/api/public/*`
and `/api/*`, so `/api/billing/*` wins over `/api/*`.

- `/api/billing/*`:
  - Origin: the endpoint domain over HTTPS, with an origin access control of
    type `lambda`, `always-amz-auth` and `sigv4`. It carries no `x-origin-verify`
    header; that proof stays on the API and WebSocket origins.
  - Viewer HTTPS only, all methods, compression on. CloudFront compresses only
    1 to 10 MB bodies, so the BFF compresses larger ones itself.
  - Cache policy `BillingAuthKeyed`: `Authorization` header, all query strings,
    no cookies, TTL 0/0/1 s. Without the header in the key, CloudFront would
    strip it on GET and HEAD. With it, one user's response is never served to
    another.
  - Origin request policy `AllViewerExceptHostHeader`. A viewer `Host` makes
    Lambda answer 404.
  - The existing edge JWT function at viewer request only. Origin-request and
    origin-response triggers are not allowed on a Web Function origin.
- `/billing-upload/*`:
  - Origin: `{{resolve:ssm:/ttobak/billing/data-bucket-name}}.s3.ap-northeast-2.amazonaws.com`,
    over HTTPS with a 60 s read timeout. CloudFormation resolves the SSM dynamic
    reference at deploy, so the bucket name is never committed and no stack
    parameter is added.
  - No origin access control: CloudFront adds no signature, and sends `Host` as
    the regional bucket domain each part URL was presigned for. The presign is
    the authorization.
  - Viewer HTTPS only, all methods, `CachingDisabled`. The origin request
    policy forwards all query strings, no headers and no cookies. A response
    headers policy adds `nosniff`, HSTS (the same values as the application
    policy) and `Referrer-Policy: no-referrer`, with no CSP.
  - The CloudFront Function `billing-upload-router` (`infra/lib/billing-upload-router.js`,
    `cloudfront-js-2.0`, viewer request) accepts only
    `/billing-upload/accounts/{ACCT_ID}/uploads/{UPLOAD_ID}/source.zip`, with the
    BFF's `ACCT_ID` and `UPLOAD_ID` patterns, and strips the prefix. Anything else,
    including `%`-encoded, `..` and `//` paths, gets 403.
- `/billing-app/*`:
  - The static Billing pages from the site bucket, with the application cache
    policy and no SPA router.
  - The pages are not committed. The frontend build generates them from
    `services/billing/page` into `public/billing-app/`, and
    `deploy-frontend.yml` fails unless `out/billing-app/index.html` and
    `upload.html` exist before publishing.
  - Response headers: `frame-ancestors 'self'` and `X-Frame-Options:
    SAMEORIGIN`, so only `/billing` can frame them. Every other path keeps
    `frame-ancestors 'none'` and `DENY`, and the default CSP's `frame-src 'self'`
    lets `/billing` frame them.
  - `script-src` is `'self'` only: no `'unsafe-inline'`, no hashes and no
    `'wasm-unsafe-eval'`. `services/billing/page/build_page.py` moves each
    page's inline scripts to `index.page.N.js` and `upload.page.N.js`, loaded by
    classic `<script src>` tags at the same positions. Its tests reject an
    inline script or event-handler attribute. Styles keep the application
    `style-src 'self' 'unsafe-inline'`.

`AWS::Lambda::ResourcePolicy` on the function ARN holds the Web Function's whole
resource policy. It has one statement: `cloudfront.amazonaws.com` may call
`lambda:InvokeWebFunctionEndpoint` on the `prod` endpoint ARN, with
`StringEquals aws:SourceArn` equal to this distribution's ARN.

- `PutResourcePolicy` replaces the whole policy, so nothing else may manage it.
- CDK's bundled schema predates Web Function ARNs. The two schema warnings are
  acknowledged; the live ap-northeast-2 schema accepts the ARN.

The endpoint domain itself stays reachable from the internet. Only a SigV4
call signed by this distribution, or by an account principal granted
`lambda:InvokeWebFunctionEndpoint`, gets through. A direct SigV4 call carries no Bearer token, so the BFF answers 401.
CLAUDE.md lists this and the no-OAC upload behavior as accepted limits.

Billing uploads and POSTs work only on the custom domain (`PUBLIC_URL`), not on
the raw CloudFront domain. The `config.json` deployment waits for this policy,
so the runtime config never advertises Billing before it exists.

## Project invitation activation

GatewayStack declares `ProjectInvitationsEnabled` with allowed values `false` and
`true`, defaulting to `false`. It supplies the API environment and its published
version description. Description is a create-only Lambda Version property, so a
parameter-only change publishes a matching immutable version for the `live` alias.
Enable only after the compatible API, rollback guard and clients are verified.

A separate non-expiring DynamoDB control row fences pending project queue and
grant transactions. It contains configuration/revision data, not credentials or
meeting content. IaC deployments must not remove/reset this row. The operator
rollback guard verifies the actual unweighted `live` alias and API table, pauses
this transactional fence, cancels queued grants and requires an empty queue before
resume. Environment updates or Lambda timeout changes cannot lift that fence.
