# Infrastructure reference

This describes checked-in CDK and workflow configuration, not a live inventory or
compliance attestation. Use `infra/bin/infra.ts` for stack construction/dependencies,
`infra/lib/*-stack.ts` for resources, and workflow logs for deployed revision/state.
Root security requirements remain requirements even where existing IaC has a gap.

## Stack ownership

| Stack | Role | Dependencies explicitly declared in the app |
|---|---|---|
| TtobakWebSearchGatewayStack | AgentCore Web Search Gateway, us-east-1 | None |
| TtobakStorageStack | Main DynamoDB table, assets bucket/OAC policy | None |
| TtobakAuthStack | Cognito pools/clients, identity credentials, triggers | Storage |
| TtobakKnowledgeStack | KB bucket, AOSS declarations, external KB identifiers | Storage |
| TtobakAiStack | Service roles/policies, research/simulator permissions | Storage, Knowledge, Auth, WebSearchGateway |
| TtobakEdgeAuthStack | CloudFront viewer JWT validation, us-east-1 | Auth |
| TtobakGatewayStack | HTTP/WebSocket APIs, Go/Python functions, conversion/events | Auth, Storage, AI, Knowledge, WebSearchGateway |
| TtobakCrawlerStack | Crawling/ingestion workflow and workers | AI, Storage, Knowledge, WebSearchGateway |
| TtobakResearchAgentStack | Legacy Bedrock Agent/tools and AgentCore spans log group | Storage, Knowledge |
| TtobakWhisperStack | GPU Spot ECS, ECR, production/benchmark tasks | Storage |
| TtobakFrontendStack | Static site, CloudFront, runtime config and media keys | Gateway, EdgeAuth, Auth |

Most resources use the application region (configured as ap-northeast-2).
Cross-region producers/consumers use the settings in the app; do not replace
unresolved CDK token operations with ordinary JavaScript string methods.

## Authentication and ingress

AuthStack explicitly disables self-signup. AdminCreateUser invitations are the
account-creation gate. The domain allowlist is supplemental; RESEND trigger
behavior is not verified. SPA client has no secret; server OAuth and MCP clients
have separate settings. Inspect `auth-stack.ts` before changing flows/scopes.

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
no authorizer; its handler verifies the bearer share token. The broader edge
`/api/public/*` bypass is not permission to register more public routes.

WebSocket is implemented for QA streaming. `$connect` uses the Go ws-authorizer
with query-string token identity; `$connect`, `$disconnect`, and `$default` invoke
the websocket Lambda. `ask_live` asynchronously invokes Python QA. This is not a
Cognito HTTP authorizer or a server-side audio-transcription protocol.

CloudFront-only application HTTP ingress is policy. `originVerifySecret` is optional
CDK context and defaults empty in the app; do not infer effective origin blocking
from the presence of middleware alone. Existing browser Cognito/Transcribe SDK
calls, signed S3 PUTs and authenticated WebSocket transport are separate service
integrations, not newly approved public application origins.

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

Table TTL is `pendingShareExpiresAt`, used by pending grants and QA web-search
hourly counters. Application code enforces grant expiry synchronously. Existing
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
| qa | Python 3.12 ARM64 | 60s | 512MB |
| sim | Python 3.12 ARM64 | 15m | 1024MB |
| document-extract | Python 3.12 ARM64, bundled pinned dependencies | 90s | 1536MB |
| convert-doc | Go/LibreOffice ARM64 container | 5m | 3008MB |

Model/environment selection belongs to each function, not one repo-wide model.
`gateway-stack.ts` selects Opus 5 for summary, Opus 4.8 for images, Sonnet 5 for
QA/simulator; Go refinement uses its Sonnet configuration and lightweight Go tasks
use Haiku. QA detection and Translate are separate service/model choices.

| Trigger | Target |
|---|---|
| S3 Object Created, audio/ | transcribe |
| S3 Object Created, transcripts/ | summarize |
| Custom AllPartsTranscribed | summarize |
| Custom ImageUploadCompleted | process-image |
| Custom ActionItemsRequested, ttobak.analysis | summarize, action analysis only |
| Custom DocumentUploadCompleted, ttobak.upload | document-extract, queued canonical runs only |
| S3 Object Created, docs/ slide suffix filter | convert-doc |
| One-minute ttobak-kb-index-tick | kb; enabled in the current app, manual-only |
| Canonical DynamoDB stream records | kb; mapping/grants created only in all mode |
| Scheduled warming event | API alias |
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

Deploy/verify the worker before upload/retry producers, then consumers. The current
API does not wire its queue/read services, and summary/QA integration remains
staged; creating this construct does not backfill attachments. See
[worker contract](../backend/python/document-extract/LAMBDA.md) and ADR-039.

## Whisper and research

Whisper uses the imported VPC's actual private-with-egress subnets and a g5.xlarge
Spot ASG (min/desired 0, max 10), not a hardcoded single-AZ restriction. Root disk is
200GiB encrypted gp3, and ECS stopped-task cleanup is shortened to three minutes
to avoid disk exhaustion when a host is reused. Host-network tasks request one GPU.
These choices do not prove current instances or capacity availability.

Production faster-whisper pins and pyannote 4.x/community-1 are a coherent image
configuration. Production DIARIZATION_S3_KEY is image-owned. The separate WhisperX
benchmark task has its own bundle variable; do not conflate the two. Its
run_engine.py allowlisting entrypoint must not be overridden by CDK entryPoint or
command. Engine selection uses ENGINE; image and CDK tests guard the boundary.

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

The app currently sets `knowledgeIndexingMode='manual-only'` and
`knowledgeIndexScheduleEnabled=true`; the reusable construct's schedule default
is false. This enables the scheduled bootstrap in the synthesized configuration,
not evidence of a deployed producer. Canonical stream mapping/read permissions
remain absent until explicit `all` mode.

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
plus account-conditioned listing to distinguish absence from denied reads. It adds
no object writes/deletes. SourceAccess/ToolHistory/strict account helpers remain
unwired in handler.py; these grants do not activate them. Legacy cache variables
remain injected. Strict QA cutover must follow binary snapshot verification and
the [current-source rollout](runbooks/qa-current-source-rollout.md).

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
separately from next build.

The StorageStack OAC custom resource reads the distribution ID and tightens the
bucket policy. Its changing Timestamp deliberately forces re-invocation. The infra
workflow redeploys StorageStack after FrontendStack even following earlier failure;
removing that step/property breaks the tightening mechanism.

Run synth and Jest before deployment. Deploy changed stacks individually with
`--exclusively`, following the graph above, never `--all` or implicit dependencies.
The workflow excludes KnowledgeStack. Go build lists are not identical everywhere:
the legacy build helper and current CI loops omit some of the eight declared zip
artifacts. For a change to websocket/ws-authorizer, build that artifact explicitly;
do not claim the partial loops cover all eight. See the
[deployment runbook](runbooks/deployment.md) and exact workflow before acting.
