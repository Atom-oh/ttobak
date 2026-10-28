# ADR-056: Billing service origin, identity and Account access

- Status: Accepted; implemented on the Billing phase 1 branch, not yet deployed.
- Decision date: 2026-10-27 (owner decisions D1 on 2026-10-26, D3–D5 and the
  membership rule on 2026-10-27).
- Implementation checked: 2026-10-28, against the Billing phase 1 branch (`services/billing/bff`,
  `infra/lib/billing-stack.ts`, `infra/lib/frontend-stack.ts`); not deployed.
- Refines the [Billing service design](../superpowers/specs/2026-10-26-billing-service-design.md):
  it replaces that record's §5 step 3 and §7 token transport (a dedicated header
  and an MCP SigV4 path) and its "exactly as today" Account filtering.

## Context

The Billing Insight companion moves into TTOBAK as its own service. Its Express
backend for frontend (BFF) moves to a Lambda Web Function. A Web Function
endpoint cannot be private: its domain is always internet-reachable. TTOBAK
policy allows only CloudFront as a public entry point. The owner approved one
exception pattern for an AWS-managed Lambda endpoint: `IamAuth`, CloudFront OAC,
a resource policy scoped to the distribution ARN, and application-level auth.

The companion signs users in with its own cookie session. Its access check also
lets a member of a parent Account act on child Accounts, which conflicts with
the TTOBAK rule that hierarchy grants no access (ADR-036/055).

## Decision and rationale

**Origin (D1).**
- The BFF endpoint is `HomeRegion` with `AuthType` `IamAuth`; `ApplicationManaged`
  is forbidden.
- The TTOBAK distribution reaches it under `/api/billing/*` through an OAC with
  origin type `lambda` and signing behavior `always-amz-auth`, the only behavior
  CloudFront accepts for Web Function origins.
- The Web Function's single resource policy has exactly one statement: allow
  `cloudfront.amazonaws.com` `lambda:InvokeWebFunctionEndpoint` on that
  endpoint, conditioned on `aws:SourceArn` equal to the TTOBAK distribution ARN.
  No other principal is added.
- The behavior keeps the existing viewer-request Lambda@Edge JWT check.
  Origin-request/response Lambda@Edge is not supported on Web Function OAC
  behaviors and must not be added.
- The origin request policy is `AllViewerExceptHostHeader`. The cache policy
  includes `Authorization`, because CloudFront strips it from GET/HEAD requests
  otherwise. The BFF answers every route with `Cache-Control: private, no-store`.

**Identity (D3).**
- OAC does not sign with the viewer's `Authorization` header, and Lambda removes
  only values that start with `AWS4-`. The browser's
  `Authorization: Bearer <TTOBAK JWT>` therefore reaches the BFF unchanged; no
  dedicated header is needed.
- The BFF verifies that JWT itself: RS256 signature against the TTOBAK pool JWKS,
  issuer, expiry and `token_use`. ID tokens must have `aud`, and access tokens
  `client_id`, in an explicit allowlist. Phase 1 allows only the SPA client
  (`authStack.spaClient`), the client the edge check and the browser use. This
  is a deliberate subset of the Go API's `API_ALLOWED_CLIENT_IDS`, which also
  includes `userPoolClient`.
- MCP billing tools will call `/api/billing/*` through CloudFront with the
  caller's own token, exactly as the MCP server calls the Go API today. They
  will not use a SigV4 path or an asserted-identity header.
- Accepting the MCP resource audience waits for the MCP plan, which must also
  apply connector grant checks.
- No IAM permission, at the endpoint or anywhere else, grants identity or
  Account access.

**Account access.**
- Only canonical direct membership grants access: the item
  `PK=ACCOUNT#{accountId}`, `SK=MEMBER#{sub}` in `ttobak-main`.
- Parent or child Accounts grant nothing, and `PENDING_ACCOUNT#` rows never count.
- Any member role grants Billing access, including read, upload and rerun. The
  member's `role` attribute is not consulted, as in the companion today.
- Account-scoped routes (dataset reads of a version, report, every upload
  mutation) re-read the canonical item on every request, with no cache.
- The Account list may enumerate candidates from GSI1
  (`GSI1PK=USER#{sub}`, `GSI1SK` begins with `ACCOUNT#`), but returns only
  candidates confirmed by the canonical item.
- Members of a parent who relied on inherited visibility must be added directly
  before cut-over.

**Uploads (D4).**
- Keep presigned multipart uploads and the `accounts/{id}/uploads/*` bucket
  keys. The TTOBAK distribution adds `/billing-upload/*`: S3 data-bucket origin,
  no OAC (the parts are already SigV4-presigned by the BFF role), all query
  strings, no cookies.
- A viewer-request CloudFront Function strips the prefix. It rejects any path
  that is not `/billing-upload/accounts/{id}/uploads/...` with a valid Account
  ID, and any `..`, encoded separator or empty segment.
- The bucket keeps Block Public Access; only a valid presigned request writes.

**Old domain (D5).** `insight.atomai.click` redirects to `/billing` for two
weeks; then the old distribution and DNS record are deleted.

## Current implementation

Implemented on the Billing phase 1 branch, not yet deployed. The companion still
runs on ECS Fargate behind its own distribution until cutover.

- The BFF is `services/billing/bff`, packaged by `npm run package` and run as a
  Lambda Web Function by `TtobakBillingStack` (`infra/lib/billing-stack.ts`).
- `infra/lib/frontend-stack.ts` adds the `/api/billing/*`, `/billing-upload/*` and
  `/billing-app/*` behaviors and the endpoint resource policy, only when
  `ttobak:billingEnabled` and the three endpoint context keys are set.
- The data bucket and platform table names are never committed. They come from
  the SSM String parameters `/ttobak/billing/data-bucket-name` and
  `/ttobak/billing/platform-table-name`, resolved by CloudFormation at deploy and
  used only when `ttobak:billingEnabled` is `"true"`.

## Alternatives and consequences

- Rejected: keeping the BFF on ECS behind an internal ALB through a CloudFront
  VPC origin. Policy-compliant without an exception, but it keeps two always-on
  tasks, the ALB and the VPC origin.
- Rejected: an MCP SigV4 path with an asserted subject header. It would make an
  IAM grant equivalent to user identity.
- Rejected: the companion's hierarchy-based access. It contradicts ADR-036/055.
- Residual risk: the endpoint domain is internet-reachable.
  - Same-account principals whose identity policies allow
    `lambda:InvokeWebFunctionEndpoint` on it (for example administrators, or the
    `AWSLambdaInvokeWebFunctionEndpointAccess` managed policy) can call it
    directly with SigV4.
  - Such a call still needs a valid TTOBAK JWT; Lambda strips the SigV4
    `Authorization` before the app sees it, so a direct call cannot also carry a
    Bearer token and receives 401.
- Residual risk: a presigned part URL works until it expires, as today.
- Access logging is a distribution-level setting. The TTOBAK distribution
  declares none today; any future logging must omit query strings, which carry
  `X-Amz-Security-Token`.

## Evidence and open questions

- AWS Lambda Developer Guide, "Lambda Web Function endpoint access control": OAC
  pass-through, `AWS4-` stripping, `always-amz-auth`, the resource-policy shape,
  and same-account identity-policy invocation.
- CloudFront Developer Guide, "Restrict access to an AWS Lambda Web Function
  origin": the `Authorization` cache-policy requirement,
  `AllViewerExceptHostHeader`, and the Lambda@Edge origin-trigger limitation.
- The resource policy is the native `AWS::Lambda::ResourcePolicy`
  (`lambda.CfnResourcePolicy`) in FrontendStack, not a custom resource. It owns
  the function's whole policy. aws-cdk-lib 2.273.0 also exposes `CfnWebFunction`,
  `CfnWebFunctionRevision` and `CfnWebFunctionEndpoint`.
- Related: ADR-022 (public routes), ADR-036/055 (Account hierarchy), ADR-045
  (MCP resource-bound tokens), `backend/internal/middleware/auth.go`
  (`ParseVerifiedJWT`, `API_ALLOWED_CLIENT_IDS`), `infra/lib/edge-auth-stack.ts`.
