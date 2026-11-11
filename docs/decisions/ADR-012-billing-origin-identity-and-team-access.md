# ADR-012: Billing: service origin, identity and team access

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

Billing is a separate service behind the existing CloudFront distribution. The sections below keep the service origin and identity record and the shared team access record.

## Billing service origin, identity and Account access

Account access is superseded by the team access section: Billing also accepts validated ancestor membership. Origin and identity requirements remain.

- Status: Accepted; implemented and deployed (see Current implementation).
- Decision date: 2026-10-27 (owner decisions D1 on 2026-10-26, D3–D5 and the
  membership rule on 2026-10-27).
- Implementation checked: 2026-11-11, against `services/billing/bff`,
  `infra/lib/billing-stack.ts` and `infra/lib/frontend-stack.ts`.
- Refines the [Billing service design](../superpowers/specs/2026-10-26-billing-service-design.md):
  it replaces that record's §5 step 3 and §7 token transport (a dedicated header
  and an MCP SigV4 path) and its "exactly as today" Account filtering.

### Context

The Billing Insight companion moves into TTOBAK as its own service. Its Express
backend for frontend (BFF) moves to a Lambda Web Function. A Web Function
endpoint cannot be private: its domain is always internet-reachable. TTOBAK
policy allows only CloudFront as a public entry point. The owner approved one
exception pattern for an AWS-managed Lambda endpoint: `IamAuth`, CloudFront OAC,
a resource policy scoped to the distribution ARN, and application-level auth.

The companion signs users in with its own cookie session. Its access check also
lets a member of a parent Account act on child Accounts, which conflicts with
the TTOBAK rule that hierarchy grants no access (ADR-015).

### Decision and rationale

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
  `client_id`, in an explicit allowlist. The BFF allows only the SPA client
  (`authStack.spaClient`), the client the edge check and the browser use. This
  is a deliberate subset of the Go API's `API_ALLOWED_CLIENT_IDS`, which also
  includes `userPoolClient`.
- MCP billing tools will call `/api/billing/*` through CloudFront with the
  caller's own token, exactly as the MCP server calls the Go API today. They
  will not use a SigV4 path or an asserted-identity header.
- Accepting the MCP resource audience needs the MCP plan, which must also
  apply connector grant checks.
- No IAM permission, at the endpoint or anywhere else, grants identity or
  Account access.

**Account access.**
- Access comes from canonical direct membership, the item
  `PK=ACCOUNT#{accountId}`, `SK=MEMBER#{sub}` in `ttobak-main`, or from validated
  membership of an ancestor Account (see the team access section).
- Child Accounts grant nothing, and `PENDING_ACCOUNT#` rows never count.
- Any member role grants Billing access, including read, upload and rerun. The
  member's `role` attribute is not consulted, as in the companion today.
- Account-scoped routes (dataset reads of a version, report, every upload
  mutation) re-read the canonical item on every request, with no cache.
- The Account list may enumerate candidates from GSI1
  (`GSI1PK=USER#{sub}`, `GSI1SK` begins with `ACCOUNT#`), but returns only
  candidates confirmed by the canonical item.
- Inherited Billing access follows the ancestor rule in the team access
  section; direct membership is not required.

**Uploads (D4).**
- Keep presigned multipart uploads and the `accounts/{id}/uploads/*` bucket
  keys. The TTOBAK distribution adds `/billing-upload/*`: S3 data-bucket origin,
  no OAC (the parts are already SigV4-presigned by the BFF role), all query
  strings, no cookies.
- A viewer-request CloudFront Function (`infra/lib/billing-upload-router.js`)
  accepts only `/billing-upload/accounts/{id}/uploads/{uploadId}/source.zip`
  with valid IDs, answers 403 for any other path, and strips the prefix.
- The bucket keeps Block Public Access; only a valid presigned request writes.

**Old domain (D5).** `insight.atomai.click` was deleted at the 2026-11-04
cutover; no redirect is served.

### Current implementation

Deployed. The companion stack, its distribution and `insight.atomai.click` were
deleted on 2026-11-04 after the data cutover (amendment below).

- The BFF is `services/billing/bff`, packaged by `npm run package` and run as a
  Lambda Web Function by `TtobakBillingStack` (`infra/lib/billing-stack.ts`).
- `infra/lib/frontend-stack.ts` adds the `/api/billing/*`, `/billing-upload/*` and
  `/billing-app/*` behaviors and the endpoint resource policy, only when
  `ttobak:billingEnabled` and the three endpoint context keys are set.
- The data bucket and platform table names are never committed. They come from
  the SSM String parameters `/ttobak/billing/data-bucket-name` and
  `/ttobak/billing/platform-table-name`, resolved by CloudFormation at deploy and
  used only when `ttobak:billingEnabled` is `"true"`.

#### Amendment 2026-11-04: data and worker ownership

The user decided that Billing is one TTOBAK service holding multiple customer
accounts, with no separate per-customer stack. `TtobakBillingStack` therefore
owns the data bucket, platform table, worker, What's New collector and FSI
search (`infra/lib/billing-data-plane.ts`); the companion stack is retired after
its data is copied. The SSM parameters stay the BFF's and upload origin's single
source of the bucket and table names, so the cutover repoints them instead of
editing the BFF. The origin, identity and access decisions above are unchanged.
D5 was shortened by the same decision: the old domain was deleted at cutover
instead of redirecting for two weeks, and the hand-off context was removed.
The worker's FSI search keeps the companion's external egress: it sends
model-composed generic queries that pass `safe_query` (no digits, vocabulary
allowlist, no private term) to the web search provider through the TTOBAK
gateway. As with manual QA search (ADR-028), those checks reduce but do not
eliminate customer billing context leaving the account.

### Alternatives and consequences

- Rejected: keeping the BFF on ECS behind an internal ALB through a CloudFront
  VPC origin. Policy-compliant without an exception, but it keeps two always-on
  tasks, the ALB and the VPC origin.
- Rejected: an MCP SigV4 path with an asserted subject header. It would make an
  IAM grant equivalent to user identity.
- Rejected: the companion's hierarchy-based access. It contradicts ADR-015.
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

### Evidence and open questions

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
- Related: ADR-020 (public routes), ADR-015 (Account hierarchy), ADR-003
  (MCP resource-bound tokens), `backend/internal/middleware/auth.go`
  (`ParseVerifiedJWT`, `API_ALLOWED_CLIENT_IDS`), `infra/lib/edge-auth-stack.ts`.

## Billing shared team access

### Status notes

- Status: Accepted on 2026-11-04 following the request to allow all shared team members to upload Billing files.
- Supersedes the service origin section's direct-membership-only Account access and ADR-015's Billing exclusion. Other ADR-015 mutation restrictions remain.

Billing grants read, upload, status, rerun and version-management access to any current direct member or member of an ancestor Account, independent of member role. Descendant membership does not grant parent or sibling access. Pending invitations grant nothing.

The BFF discovers the complete canonical parent chain with strongly consistent uncached reads, then confirms every observed parent link and the granting membership in one atomic `TransactGetItems` snapshot before granting inherited access. A changed link, removed grant, incomplete snapshot or storage error never grants access. Missing, malformed, cyclic or more-than-64-ancestor inherited paths deny access. The snapshot contains at most 65 projected META rows plus one projected MEMBER row and uses the existing scoped `GetItem` permissions. Direct membership retains its existing behavior independently of hierarchy validity. Removal and reparenting revoke subsequent requests; display metadata is never authorization evidence.

GSI1 memberships and paginated `ACCOUNT#{parent}/CHILDREF#{child}` keys only discover candidates. Every candidate is independently authorized. Discovery has a 1,000-account and 4,096-DynamoDB-request budget; exhaustion returns an explicit unavailable response instead of partial results. Current child references must already be bootstrapped under ADR-015.

The discovery IAM grant restricts base-table Query to `ACCOUNT#*` partitions and projected PK/SK attributes. IAM cannot constrain the sort-key prefix, so the service role can enumerate other sort keys in those partitions, including opaque member subs. This accepted service-role enumeration limit exposes no email attributes and grants clients no additional access; the BFF queries only CHILDREF rows and independently confirms authorization.

The Account frontend shows Billing only when the authenticated BFF returns an authorized entry. Other Account edit controls retain direct-membership restrictions. JWT verification, CSRF checks, Account-scoped multipart keys and CloudFront ingress remain.

Release requires exact-head reviews, tests and the content gate, followed by the Billing stack (which owns the worker) and frontend deployment under the [Billing release runbook](../runbooks/billing-release.md). Deployment and authenticated-user acceptance require separate evidence.
