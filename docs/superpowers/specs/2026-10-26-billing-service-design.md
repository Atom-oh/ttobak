# Billing Insight as a TTOBAK Service — Design

> Status: Draft for review. Date: 2026-10-26. Author: Junseok Oh, with Claude.
> Scope: move the AWS Billing Insight companion (today a separate app at
> `insight.atomai.click`) into the TTOBAK project as its own service, served
> from the TTOBAK domain with TTOBAK sign-in, and expose its results to the
> TTOBAK assistant through MCP tools.

## 1. Why

Insight already depends on TTOBAK for everything that defines a user:

- the same Cognito user pool (TTOBAK managed login, companion client),
- the same Account membership (`ttobak-main`, GSI1 `USER#{sub}` → `ACCOUNT#{id}`),
- the TTOBAK web-search gateway (FSI peer-case search).

Keeping it a separate site costs a second domain, a second session, a hand-off
endpoint (`POST /api/billing-insight/handoff`, `infra/lib/billing-insight.ts`,
`backend/internal/service/billing_insight.go`), an OAuth code-exchange Lambda,
and a second deploy pipeline. Users must leave TTOBAK to see billing insight,
and the assistant cannot use it while preparing for a meeting.

Goal in one line: **an SA opens an Account in TTOBAK and sees, or asks about,
its billing insight without a second login or a second site.**

Naming: TTOBAK already has an **Insights** area (curated news crawling, deep
research agent, custom dictionary). To avoid two "insights", this service is
called **Billing** inside TTOBAK (`/billing`, `/api/billing/*`,
`TtobakBillingStack`, `services/billing/`, MCP tools `billing_*`). See §12 for
the menu reorganisation.

## 2. Goals, non-goals, anti-goals

Goals
- One domain (`ttobak.atomai.click`), one sign-in, one Account model.
- Insight stays a separately deployable service (own stack, own runtime, own
  failure domain) inside the TTOBAK repository.
- The assistant can query insight results through MCP with the caller's own
  Account permissions.
- No data migration: the existing data bucket and platform table are reused.

Non-goals
- Rewriting the analysis pipeline (Python ingest, area agents, verifier).
- Porting the D3 dashboard to React in this project (allowed later, not required).
- Changing how uploads are analysed or versioned.

Anti-goals (a result that "works" but fails the intent)
- Any path where a user sees an Account they are not a member of, through the
  UI, the API, or MCP.
- A TTOBAK deploy failure blocking a billing deploy, or the reverse.
- Customer names or customer-specific content in TTOBAK code or docs.

## 3. Current state (facts)

Insight today
- Express server (Node 22) on ECS Fargate ARM64, 2 tasks, behind an internal ALB,
  reached only through its own CloudFront distribution via a VPC origin.
- Session: signed cookie `tb_sess` after TTOBAK OAuth (code + PKCE) or the
  TTOBAK hand-off (`/login/ttobak`).
- API: `GET /api/accounts`, `GET /api/data/:id.json` (per-Account filtered
  datasets, some several MB), `GET /api/accounts/:acct/report`, upload routes
  under `/api/accounts/*` (presigned multipart PUT to the data bucket through a
  CloudFront `accounts/*` behavior).
- Pages: `/` (dashboard, D3 + a small fetch shim), `/upload`, login pages.
- Worker: EventBridge on `jobs/*.json` → ECS RunTask (Python, Account-scoped STS
  session) → versioned outputs in the data bucket, state in the platform table.
- Side Lambdas: OAuth exchange (non-VPC), FSI search (non-VPC, calls the TTOBAK
  web-search gateway), daily What's New collector.
- Network: isolated subnets of the shared demo VPC, VPC endpoints only, no NAT.

TTOBAK today
- Frontend: Next.js static export on S3 behind CloudFront.
- API: Go on Lambda behind an API Gateway HTTP API; CloudFront `/api/*` adds
  `x-origin-verify` and runs a Lambda@Edge Bearer-JWT check
  (`infra/lib/edge-auth-stack.ts`); the browser sends `Authorization: Bearer`
  (`frontend/src/lib/api.ts`).
- MCP: `/api/mcp` on the same API (resource-bound access tokens).
- Batch: ECS on EC2 for Whisper; no EKS anywhere in TTOBAK.

## 4. Target architecture

```
Browser ──► CloudFront (ttobak.atomai.click)
             ├─ /*                      S3: Next.js static (incl. /billing route)
             ├─ /api/billing/*          Lambda@Edge JWT ──► OAC(SigV4) ──► Insight BFF (Lambda Web Function, IamAuth)
             ├─ /api/*                  Lambda@Edge JWT ──► HTTP API (Go)  [unchanged]
             └─ upload path (see D4)    S3 data bucket (presigned multipart)

Insight BFF ──► S3 data bucket, platform table, ttobak-main (membership)   [AWS public endpoints]
Upload  ──► jobs/*.json ──► EventBridge ──► ECS RunTask (ingest) ──► AgentCore Runtime (agents, no VPC)
MCP (/api/mcp, Go) ──► Insight BFF internal routes (SigV4, see §7)
```

### 4.1 Frontend / backend split

- **Frontend** moves into the TTOBAK Next.js app as a `/billing` route (and a
  billing tab or summary card on the Account detail page). First step: host the
  existing dashboard markup and scripts inside a client component; the fetch
  shim attaches the TTOBAK Bearer token and calls `/api/billing/...`. The page
  build tool (`build_page.py`) keeps producing the body; React porting is optional
  later work.
- **Backend for frontend (BFF)** is the current Express server minus pages and
  minus cookie/OAuth login, deployed as a **Lambda Web Function**
  (`nodejs24.x`, Express unchanged, no adapter). It serves only JSON and
  presign responses under `/api/billing/*`.

### 4.2 Why a Lambda Web Function for the BFF

- Runs the existing Express app as-is (listens on `AWS_LAMBDA_HTTP_ENDPOINT`);
  each environment serves many concurrent requests; scales to zero; response
  streaming built in.
- Removes the ALB, the VPC origin, the 2 always-on tasks and the cookie session.
- Fits TTOBAK's serverless footprint.

Constraints to design for (from the Lambda Web Functions docs)
- Node.js only (`nodejs24.x`); the Python worker stays where it is.
- 2 GB memory per environment, default V8 heap 1,024 MB (raise with
  `NODE_OPTIONS`). Large dataset reads must stream or stay under the heap with
  concurrency; set `maxConcurrencyPerEnvironment` accordingly.
- Request line + headers ≤ 128 KiB; HTTP/1.1 only; no WebSocket.
- The endpoint domain is always internet-reachable; PrivateLink is not
  available. With `IamAuth`, unsigned or unauthorized requests are rejected.
- CloudFront must not forward the viewer `Host` header
  (`AllViewerExceptHostHeader` or an explicit header list).
- CloudFormation types exist in ap-northeast-2 (`AWS::Lambda::WebFunction`,
  `WebFunctionRevision`, `WebFunctionEndpoint`); CDK has L1 constructs from
  aws-cdk-lib 2.273 (TTOBAK infra is on 2.261 → bump needed).
- Outbound: without an egress network connector the function reaches public AWS
  endpoints directly, which is all the BFF needs (S3, DynamoDB, STS). No VPC.

### 4.3 What runs where

Principle (owner, 2026-10-26): move to Lambda whatever gains from it; nothing
that only calls AWS APIs (Bedrock, S3, DynamoDB) needs to sit in a VPC.

| Workload | Today | Target | Why |
|---|---|---|---|
| BFF (Express API) | ECS Fargate ×2 + internal ALB | Lambda Web Function, no VPC | HTTP, Node, I/O-bound, scale to zero |
| Agent stage (area agents, synthesis, verifier, FSI search, `rerun-agents` jobs) | inside the Fargate worker, Bedrock via VPC endpoint | Bedrock AgentCore Runtime (Python container, ARM64, no VPC) | Bedrock/S3/DDB only; sessions up to 8 h leave room as stages are added (observed runs ≤ 2.6 min today); built-in agent tracing; same platform as the planned BI chatbot; FSI search calls the AgentCore Gateway directly |
| Ingest (unzip, parse COT xlsx / CUR, build datasets) | Fargate worker 4 vCPU / 16 GB / 100 GB disk | stays on ECS Fargate RunTask | CUR zips can exceed Lambda's 10 GB memory / 10 GB `/tmp` / 15 min |
| What's New collector, FSI search | Lambda, no VPC | unchanged | already serverless |
| OAuth exchange | Lambda | removed | TTOBAK sign-in replaces it |

Options considered for the agent stage (owner chose AgentCore Runtime, 2026-10-26):
- Lambda Web Function — Node.js only and request-bound; the agents are Python.
- Lambda MicroVMs — built for isolating user/AI-generated code with
  suspend/resume; the agents call Bedrock and verify, they run no foreign code.
- Python Lambda — simplest, but the 15-minute ceiling limits future stages.
- **AgentCore Runtime** — long sessions, agent observability, one platform with
  the planned chatbot. Cost: an ECR image build and an async invoke contract.

Flow: upload → `jobs/*.json` → EventBridge → Fargate ingest → writes the
version's datasets → invokes the agent runtime asynchronously (Account,
version, source) → the runtime reads the computed datasets, runs the stages,
verifies, and writes notes, sessions and market back to the version, then
updates job state in the platform table. A rerun skips ingest and invokes the
runtime directly. The runtime role gets an Account-scoped session policy, as
the worker has today. `AWS::BedrockAgentCore::Runtime` is available in
ap-northeast-2.

EKS was considered and rejected (owner, 2026-10-26): with the BFF on a Web Function and
the agents on AgentCore Runtime, the only container left is one on-demand ingest task. EKS would add a
control plane (~$0.10/h), upgrades and a job controller for that single task.
The EKS clusters already in the account are demo or other-project clusters and
must not host customer billing data.

## 5. Authentication and authorization

1. Browser sends the TTOBAK ID/access token as `Authorization: Bearer`.
2. CloudFront `/api/billing/*` runs the same Lambda@Edge JWT check as `/api/*`.
3. CloudFront signs the origin request with OAC (origin type `lambda`,
   `always-amz-auth`); Lambda accepts it only because the function's resource
   policy allows `cloudfront.amazonaws.com` with `aws:SourceArn` = the TTOBAK
   distribution. The viewer `Authorization` header is forwarded to the app.
4. The BFF verifies the JWT itself (issuer = TTOBAK pool, allowed client IDs =
   TTOBAK web client), then resolves Account membership from `ttobak-main` and
   filters every dataset by Account, exactly as today.
5. Cookie session, OAuth exchange Lambda, companion client usage and the
   hand-off endpoint are removed after cut-over.

Defence in depth: edge check (cheap reject), IAM at the endpoint (only this
distribution), JWT + membership in the app (the actual authorization).

## 6. Uploads

The browser uploads multipart parts with presigned URLs. Today they go through
a CloudFront `accounts/*` behavior that maps 1:1 to bucket keys. On the TTOBAK
distribution `/accounts/*` is already a Next.js page route, so the path must
change (decision D4).

## 7. MCP tools

Added to the TTOBAK MCP server; each call carries the caller's identity and is
answered only for Accounts the caller belongs to.

| Tool | Returns |
|---|---|
| `billing_accounts` | Accounts with billing data, current version, period |
| `billing_briefing` | ranked actions (what, $, owner, evidence) for an Account |
| `billing_business_signals` | new/surging tag items and verified hypotheses |
| `billing_sessions` | proposed SA sessions (type, why, audience, agenda) |
| `billing_savings` | optimisation opportunities with $ estimates |
| `billing_query` | bounded explore query over a raw dataset (filter, group, top N) |

Call path: MCP Lambda → BFF internal route signed with SigV4 by the MCP role
(`lambda:InvokeWebFunctionEndpoint`), user identity passed as the verified
subject in a dedicated header. The BFF trusts that header only on the internal
route, and only because IAM authenticated the caller as the MCP role (decision D3).
Responses are computed summaries; raw uploads never leave the data bucket.

## 8. Repository and deployment

- Code moves to `services/billing/` in the TTOBAK repo: `bff/` (Express),
  `worker/` (Python analysis), `page/` (dashboard body + build tool), tests.
  Only code moves; the old repository's design history and any customer-specific
  documents stay out.
- New CDK stack `TtobakBillingStack` in the TTOBAK infra app: Web Function
  (+ revision, endpoint, resource policy), agent runtime (ECR image), ingest
  task definition and EventBridge rule, side Lambdas, IAM. Data bucket and platform table are
  referenced by name (they are `RETAIN` today) — no copy, no import in phase 1.
- `TtobakFrontendStack` gets the `/api/billing/*` behavior (before `/api/*`) and
  the upload behavior.
- CI: a separate job that deploys `TtobakBillingStack` only when
  `services/billing/**` or its stack file changes, so a failing TTOBAK deploy
  does not block billing and vice versa.

## 9. Migration and cut-over

1. Deploy `TtobakBillingStack` alongside the existing insight stack (both read
   the same bucket/table; the old worker keeps running until step 5).
2. Add `/api/billing/*` and the upload path on the TTOBAK distribution.
3. Ship the `/billing` route; verify per-Account visibility with two test users.
4. Switch the TTOBAK entry button from hand-off to the internal route (`/billing`).
5. Move the worker EventBridge rule to the new stack (one owner at a time).
6. `insight.atomai.click` redirects to `ttobak.atomai.click/billing` for a while.
7. Remove the old web service, ALB, VPC origin, OAuth exchange, hand-off code.

Rollback: until step 7 the old site still works; revert the button and the
CloudFront behaviors.

## 10. Security checklist

- No `0.0.0.0/0` / `::/0` ingress; no internet-facing LB; no public IP (the ALB
  is removed, not exposed).
- Web Function endpoint `IamAuth` only, resource policy scoped to the TTOBAK
  distribution ARN (+ MCP role). Never `ApplicationManaged`.
- Self sign-up stays disabled on the pool.
- Per-Account filtering on every route, covered by tests for UI, API and MCP.
- No customer names in code, tests or docs.

## 11. Decisions needed

- **D1 Security exception — resolved 2026-10-26.** No load balancer is created;
  the Web Function endpoint is IAM-only behind CloudFront OAC with a resource
  policy scoped to the TTOBAK distribution ARN (+ MCP role). The owner approved
  this as an allowed pattern; `ApplicationManaged` endpoints remain forbidden.
- **D2 Compute — resolved 2026-10-26.** BFF on a Lambda Web Function, agent
  stage on AgentCore Runtime (no VPC), ingest stays on ECS Fargate; no EKS.
- **D3 MCP identity.** SigV4 + asserted subject (above) vs forwarding a user
  token the BFF can verify (requires accepting the MCP client audience).
- **D4 Upload path.** A new CloudFront prefix whose keys match the bucket
  layout (key prefix change for new uploads) vs presigning against the S3
  regional endpoint directly.
- **D5 Old domain.** Redirect period for `insight.atomai.click`, then delete.

## 12. Navigation reorganisation

Today the sidebar is one flat list (Meetings, Accounts, Projects, Documents,
Assistant, Files, Knowledge Base, Insights, Settings) plus a separate
"Billing Insight" hand-off button; the custom dictionary sits inside Settings.
"Insights" mixes news crawling and deep research, and billing would be a third
kind of insight. Proposed grouping by what the user is doing:

| Group | Items | Route (unchanged unless noted) |
|---|---|---|
| Work | Meetings, Accounts, Projects, Docs | `/`, `/accounts`, `/projects`, `/docs` |
| AI | Assistant, Deep Research | `/chat`, `/insights/research` |
| Knowledge | Library, News & Updates, Custom Dictionary | `/kb` (Library), `/insights`, new `/dictionary` (moved out of Settings) |
| Customer Analytics | Billing (all my Accounts), per-Account Billing tab | new `/billing`, Account detail tab |
| — | Settings | `/settings` |

- Documents vs Files vs Knowledge Base overlapped: `/docs` holds documents
  written in TTOBAK (markdown, slides); `/files` is a read-only roll-up of
  meeting attachments (with its own "Documents" filter); `/kb` holds uploads for
  Q&A. Proposed: **Docs** (`/docs`) = what you write; **Library** (`/kb`) = every
  file that came in, filterable by source (meeting attachment / uploaded),
  type, and whether it is indexed for Q&A. `/files` redirects to
  `/kb?source=meeting`. Open: index meeting attachments for Q&A automatically?
- Existing `/insights/...` URLs keep working; only labels and grouping change.
- The hand-off button is removed; Billing is a normal menu item, shown only to
  users who belong to at least one Account with billing data.
- Mobile bottom bar stays at five items (Home, AI, Record, Knowledge, Profile);
  Billing is reached from Accounts and the menu sheet.
- Open: whether Deep Research belongs under AI or Knowledge (it is started from
  the assistant but its reports are reference material).
