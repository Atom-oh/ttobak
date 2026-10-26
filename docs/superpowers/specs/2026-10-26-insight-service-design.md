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
- A TTOBAK deploy failure blocking an insight deploy, or the reverse.
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
             ├─ /*                      S3: Next.js static (incl. /insight route)
             ├─ /api/insight/*          Lambda@Edge JWT ──► OAC(SigV4) ──► Insight BFF (Lambda Web Function, IamAuth)
             ├─ /api/*                  Lambda@Edge JWT ──► HTTP API (Go)  [unchanged]
             └─ upload path (see D4)    S3 data bucket (presigned multipart)

Insight BFF ──► S3 data bucket, platform table, ttobak-main (membership)   [AWS public endpoints]
Upload  ──► jobs/*.json ──► EventBridge ──► ECS RunTask (Python worker)     [unchanged]
MCP (/api/mcp, Go) ──► Insight BFF internal routes (SigV4, see §7)
```

### 4.1 Frontend / backend split

- **Frontend** moves into the TTOBAK Next.js app as an `/insight` route (and an
  insight tab or summary card on the Account detail page). First step: host the
  existing dashboard markup and scripts inside a client component; the fetch
  shim attaches the TTOBAK Bearer token and calls `/api/insight/...`. The page
  build tool (`build_page.py`) keeps producing the body; React porting is optional
  later work.
- **Backend for frontend (BFF)** is the current Express server minus pages and
  minus cookie/OAuth login, deployed as a **Lambda Web Function**
  (`nodejs24.x`, Express unchanged, no adapter). It serves only JSON and
  presign responses under `/api/insight/*`.

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

### 4.3 Worker compute: ECS Fargate vs EKS

After the BFF leaves ECS, the only container workload left is the on-demand
Python worker (one task per upload or rerun, minutes long, zero when idle).

| | ECS Fargate RunTask (keep) | EKS |
|---|---|---|
| Trigger | EventBridge → RunTask, native | needs a controller (Lambda creating Jobs, Argo Events, KEDA) |
| Idle cost | 0 | control plane ~$0.10/h plus nodes or Fargate profile |
| Ops | none beyond task definition | version upgrades, add-ons, node/Karpenter, Pod Identity |
| Account-scoped STS session | already implemented | same code, Pod Identity/IRSA for the base role |
| Fit with TTOBAK | ECS already used (Whisper) | new platform for one batch job |

Recommendation: **keep ECS Fargate for the worker.** EKS becomes worth it only
if TTOBAK standardises on Kubernetes for several services; the existing EKS
clusters in the account are demo or other-project clusters and must not host
customer billing data.

## 5. Authentication and authorization

1. Browser sends the TTOBAK ID/access token as `Authorization: Bearer`.
2. CloudFront `/api/insight/*` runs the same Lambda@Edge JWT check as `/api/*`.
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
| `insight_accounts` | Accounts with insight data, current version, period |
| `insight_briefing` | ranked actions (what, $, owner, evidence) for an Account |
| `insight_business_signals` | new/surging tag items and verified hypotheses |
| `insight_sessions` | proposed SA sessions (type, why, audience, agenda) |
| `insight_savings` | optimisation opportunities with $ estimates |
| `insight_query` | bounded explore query over a raw dataset (filter, group, top N) |

Call path: MCP Lambda → BFF internal route signed with SigV4 by the MCP role
(`lambda:InvokeWebFunctionEndpoint`), user identity passed as the verified
subject in a dedicated header. The BFF trusts that header only on the internal
route, and only because IAM authenticated the caller as the MCP role (decision D3).
Responses are computed summaries; raw uploads never leave the data bucket.

## 8. Repository and deployment

- Code moves to `services/insight/` in the TTOBAK repo: `bff/` (Express),
  `worker/` (Python analysis), `page/` (dashboard body + build tool), tests.
  Only code moves; the old repository's design history and any customer-specific
  documents stay out.
- New CDK stack `TtobakInsightStack` in the TTOBAK infra app: Web Function
  (+ revision, endpoint, resource policy), worker task definition and
  EventBridge rule, side Lambdas, IAM. Data bucket and platform table are
  referenced by name (they are `RETAIN` today) — no copy, no import in phase 1.
- `TtobakFrontendStack` gets the `/api/insight/*` behavior (before `/api/*`) and
  the upload behavior.
- CI: a separate job that deploys `TtobakInsightStack` only when
  `services/insight/**` or its stack file changes, so a failing TTOBAK deploy
  does not block insight and vice versa.

## 9. Migration and cut-over

1. Deploy `TtobakInsightStack` alongside the existing insight stack (both read
   the same bucket/table; the old worker keeps running until step 5).
2. Add `/api/insight/*` and the upload path on the TTOBAK distribution.
3. Ship the `/insight` route; verify per-Account visibility with two test users.
4. Switch the TTOBAK entry button from hand-off to the internal route.
5. Move the worker EventBridge rule to the new stack (one owner at a time).
6. `insight.atomai.click` redirects to `ttobak.atomai.click/insight` for a while.
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

- **D1 Security exception.** The global rule is "CloudFront → internal LB via
  VPC origin". A Web Function endpoint cannot be private; it is protected by
  IAM (OAC) instead. TTOBAK's own API already uses a public HTTP API behind an
  origin secret, so IAM-only access is stricter than the current TTOBAK API, but
  it is still an exception to the written rule and needs explicit approval.
  Alternative without the exception: keep the BFF on ECS behind the existing
  internal ALB and add the ALB as a second VPC origin on the TTOBAK distribution.
- **D2 Worker compute.** Keep ECS Fargate (recommended) or move to EKS.
- **D3 MCP identity.** SigV4 + asserted subject (above) vs forwarding a user
  token the BFF can verify (requires accepting the MCP client audience).
- **D4 Upload path.** A new CloudFront prefix whose keys match the bucket
  layout (key prefix change for new uploads) vs presigning against the S3
  regional endpoint directly.
- **D5 Old domain.** Redirect period for `insight.atomai.click`, then delete.
