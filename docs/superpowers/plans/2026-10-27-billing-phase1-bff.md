# Billing Phase 1 (BFF on a Lambda Web Function) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> Historical plan record. Status: Draft, 2026-10-27; not started. This is
> implementation intent, not proof of delivery or deployment.

**Goal:** Serve the Billing dashboard at `ttobak.atomai.click/billing` with TTOBAK
sign-in. The companion's Express BFF runs as an `IamAuth` Lambda Web Function
behind the TTOBAK distribution and reads the existing data bucket and platform
table.

**Architecture:**
- The BFF code moves to `services/billing/bff` and is mounted under
  `/api/billing`. It verifies TTOBAK JWTs itself and grants Account access only
  through direct canonical membership.
- A new optional `TtobakBillingStack` owns the Web Function, its revision,
  endpoint and execution role.
- `TtobakFrontendStack` gains three behaviors (`/api/billing/*`,
  `/billing-upload/*`, `/billing-app/*`) and the resource-policy custom resource.
- The dashboard HTML runs unchanged in a same-origin iframe hosted by the Next.js
  `/billing` page.
- The companion's worker and its EventBridge rule stay in the old stack for this
  phase, so the old site keeps working as rollback.

**Tech Stack:** Node.js 24 (Express 4, `node:test`, `aws-jwt-verify` 5.2.1,
AWS SDK v3), AWS CDK 2.273.0 L1 `CfnWebFunction*`, CloudFront Functions
(`cloudfront-js-2.0`), Next.js 16 static export, Python 3 (page build script).

**Spec:**
- [Billing service design](../specs/2026-10-26-billing-service-design.md)
- [ADR-056](../../decisions/ADR-056-billing-service-origin-and-identity.md),
  which overrides the spec on token transport, MCP path and Account access.

## Global Constraints

- Endpoint `AuthType: IamAuth`, `EndpointType: HomeRegion`. `ApplicationManaged` is forbidden.
- OAC origin type `lambda`, signing behavior `always-amz-auth`.
- The resource policy has exactly one statement: `cloudfront.amazonaws.com`,
  `lambda:InvokeWebFunctionEndpoint`, `aws:SourceArn` = the TTOBAK distribution ARN.
- No Lambda@Edge origin-request/response on `/api/billing/*`. Keep the existing viewer-request edge JWT check.
- `/api/billing/*` cache policy includes `Authorization`; origin request policy `AllViewerExceptHostHeader`.
- The BFF sends `Cache-Control: private, no-store` on every response.
- Identity comes only from `Authorization: Bearer <JWT>`, verified by the BFF:
  issuer, signature, expiry, `token_use`, and client allowlist
  `BILLING_ALLOWED_CLIENT_IDS` (phase 1: the SPA client `authStack.spaClient` only).
- Access is the canonical `PK=ACCOUNT#{id}`, `SK=MEMBER#{sub}` item only. Hierarchy and `PENDING_*` rows grant nothing.
- Uploads keep `accounts/{id}/uploads/*` keys. The viewer prefix is `/billing-upload`.
- No new VPC, no security-group ingress, no public IP, no new unauthenticated API route.
- Runtime `nodejs24.x`; the entry listens on `AWS_LAMBDA_HTTP_ENDPOINT` (`host:port`).
- Reuse the data bucket and platform table by name. Never create, import or delete them.
- Billing context lives in the checked-in `infra/cdk.json`, like every other
  durable `ttobak:` key:
  - `ttobak:billingDataBucket` and `ttobak:billingPlatformTable` enable
    `TtobakBillingStack`.
  - `ttobak:billingEndpointDomain`, `ttobak:billingEndpointArn` and
    `ttobak:billingFunctionArn` enable the FrontendStack behaviors.
  - FrontendStack reads Billing values only from context strings, never
    CloudFormation exports. The HTTP-origin release coordinator
    (`scripts/operations/http_origin_release.py`) deploys a fixed stack list and
    must keep deploying FrontendStack whether or not Billing exists.
  - The endpoint keys are committed only after Task 10 deploys `TtobakBillingStack`.
- Every workflow that synthesizes the app must build `services/billing/bff/dist`
  and the resource-policy bundle first: `test-infra.yml` and both jobs of
  `deploy-infra.yml`.
- No customer names in code, tests, fixtures or docs.
- Never `cdk deploy --all`. Deploy changed stacks individually with
  `--exclusively` in dependency order: Billing, then Frontend. Each deploy needs
  owner approval.

## Review Focus

1. **A JWT expires while the dashboard is open.** The shim refreshes through the
   parent once and retries; a second 401 sends the top window to `/` (sign-in),
   never a loop. Test in Task 6.
2. **A user whose access was only inherited from a parent Account.** They see an
   empty Billing page and `/api/billing/accounts` returns `[]`, not 401 or 503.
   Tests in Tasks 3 and 6.
3. **Two users' GET responses through CloudFront.** User A's data must never be
   served to user B: `Authorization` is in the cache key and responses are
   `no-store`. Tests in Tasks 5 and 9.
4. **Traversal or cross-Account upload paths.** For example
   `/billing-upload/accounts/a%2F..%2Fb/uploads/x`, `/billing-upload/accounts/A/versions/x`,
   or `/billing-upload/jobs/x` return 403 at the edge. Test in Task 9.
5. **A direct SigV4 call to the endpoint domain.** Lambda strips the `AWS4-`
   `Authorization`, so the app sees no Bearer token and answers 401. Test in
   Task 2 (`AWS4-HMAC-SHA256 ...` header gives 401).

## Pre-flight record (checked 2026-10-27)

- `aws cloudformation describe-type` in ap-northeast-2:
  - `AWS::Lambda::WebFunction` and `WebFunctionEndpoint` are `FULLY_MUTABLE`.
  - `WebFunctionRevision` is `IMMUTABLE`.
  - `AWS::BedrockAgentCore::Runtime` exists (phase 2).
- aws-cdk-lib 2.273.0 L1 constructs:
  - `CfnWebFunction{functionName}`
  - `CfnWebFunctionRevision{functionName, buildConfig{codeConfig{s3Object{bucket,key}}, runtimeConfig{runtime}}, serviceConfig{executionRoleArn, environmentVariables, maxConcurrencyPerEnvironment, timeoutSeconds}}`
  - `CfnWebFunctionEndpoint{functionName, endpointName, endpointType, authType, revisionWeights, scalingConfig}`
- `@aws-sdk/client-lambda-web@3.1158.0` (aws-sdk-js-v3) has
  `PutResourcePolicyCommand{resourceArn, policy, revisionId}`, plus
  `GetResourcePolicyCommand` and `DeleteResourcePolicyCommand`.
- Local AWS CLI 2.35.11 lacks `lambda-web`; use the SDK.
- `aws-jwt-verify@5.2.1` is on npm.
- **Unverified:** the IAM action names for `PutResourcePolicy`/`DeleteResourcePolicy`,
  and the Web Function execution-role trust principal. Task 8 and Task 9 each
  start by looking these up.

## File Structure

```
services/billing/
  README.md                         import provenance (source commit), local test commands
  bff/
    package.json                    deps pinned; scripts: test, package
    index.js                        Web Function entry: listen on AWS_LAMBDA_HTTP_ENDPOINT
    app.js                          createApp() — routes under /api/billing, no pages/cookies/OAuth
    lib/auth.js                     JWT verification middleware (new)
    lib/access.js                   direct canonical membership (rewritten)
    lib/store.js                    platform-table access (imported unchanged)
    lib/uploads.js                  upload routes (imported; prefix + origin changes)
    scripts/package.mjs             builds dist/ (prod node_modules + sources) for the asset
    test/*.test.js                  node:test suites; test/fake-ddb.js imported
  page/
    body.html, upload.html          dashboard bodies (imported)
    dash-shim.js                    data runtime (imported; token + paths changes)
    build_page.py                   writes frontend/public/billing-app/* (imported; new targets)
    test_build_page.py              regenerated output equals committed output
    package.json                    d3 7.9.0, pretendard 1.3.9 (vendor sources only)
frontend/public/billing-app/        GENERATED by build_page.py (header says so)
frontend/src/app/billing/page.tsx   iframe host + token bridge
frontend/src/lib/billingBridge.ts   window.ttobakBilling bridge (pure, unit-tested)
infra/lib/billing-stack.ts          TtobakBillingStack
infra/lib/billing-upload-router.js  CloudFront Function source (cloudfront-js-2.0)
infra/lambda/billing-resource-policy/  custom-resource handler + bundle script
infra/test/billing-stack.test.ts, infra/test/billing-frontend.test.ts
.github/workflows/test-billing.yml  services/billing tests
```

---

### Task 1: Import the BFF core under `/api/billing`

**Files:**
- Create: `services/billing/README.md`, `services/billing/bff/{package.json,app.js}`,
  `services/billing/bff/lib/{store.js,uploads.js,access.js}`,
  `services/billing/bff/test/{fake-ddb.js,app.test.js,store.test.js,uploads.test.js}`,
  `.github/workflows/test-billing.yml`
- Source: companion repository `dashboard-app/app/{server.js,lib/*}` and `tests/dom/*`.
  Use the commit currently deployed (confirm with the owner; `dd540f5` at
  2026-10-26) and record its SHA in the README.

**Interfaces:**
- Produces: `createApp({ s3, ddb, env, authenticate, presign, now }) -> express.Application`.
  - `authenticate` is Express middleware that sets `req.sub` and `req.user` or answers 401.
  - `env` keys: `DATA_BUCKET, ACCOUNT_TABLE, PLATFORM_TABLE, PUBLIC_URL, AWS_REGION`.
- Routes, every one behind `authenticate`: `GET /api/billing/accounts`,
  `GET /api/billing/data/:id.json`, `GET /api/billing/accounts/:acct/report`,
  and the upload router at `/api/billing/accounts`. There is no health route; a
  Web Function needs no load-balancer probe.

- [ ] **Step 1: Copy the sources and drop what Phase 1 removes.**
  - Copy `lib/store.js`, `lib/uploads.js`, `lib/access.js` and `tests/dom/fake-ddb.js`.
  - In `app.js`, keep `createCache`, `scopeRows`, `scopeCatalog`, `mergeCatalog`,
    the dataset constants and the S3/feed/version readers.
  - Delete `authPage`, cookie `seal`/`unseal`, `startSession`, every `/login*`,
    `/auth/callback`, `/logout`, `/signed-out` and `/static*` route, the page
    templates, `OAUTH_FUNCTION`, `SESSION_SECRET` and the Lambda client.
  - `req.accounts` is now resolved after `authenticate` by `access.visible(req.sub)`.
- [ ] **Step 2: Write the failing tests in `test/app.test.js`.**
  - `routes_live_only_under_api_billing`: `GET /api/accounts` gives 404; `GET /` gives 404.
  - `every_route_requires_auth`: with an `authenticate` stub that always 401s,
    every route above (and `GET /api/billing/health`) gives 401.
  - Port `tests/dom/data.test.js` and `scope.test.js` cases to the new paths,
    using an `authenticate` stub that sets `req.sub = 'u1'`.
  - Port the store and upload suites unchanged except for paths.
- [ ] **Step 3: Run** `cd services/billing/bff && npm ci && npm test`. Expected:
  the new route tests FAIL before Step 1's wiring exists and PASS after.
- [ ] **Step 4: Add `.github/workflows/test-billing.yml`.**
  - Triggers: `pull_request` and `push` to `main`, paths `services/billing/**` and the workflow itself.
  - Runner `ttobak-arm` with the same fork guard as `test-backend.yml`.
  - Steps: `setup-node` 24, then in `services/billing/bff` run `npm ci && npm test`,
    then in `services/billing/page` run `npm ci && python3 -m unittest test_build_page -v`.
- [ ] **Step 5: Customer-name check.** `git grep -niE "<customer names from the companion repo>" services/billing`
  returns nothing. The implementer gets the list from the owner; it is not written into the repo.
- [ ] **Step 6: Commit** `feat(billing): import BFF core under /api/billing`.

### Task 2: Verify TTOBAK JWTs in the BFF

**Files:** Create `services/billing/bff/lib/auth.js`, `services/billing/bff/test/auth.test.js`.

**Interfaces:**
- Produces:
  `createAuthenticate({ userPoolId, clientIds, verifier? }) -> (req, res, next) => Promise<void>`.
  - It sets `req.sub` (the `sub` claim) and `req.user` (`email` claim, or `''`).
  - `verifier` defaults to
    `CognitoJwtVerifier.create({ userPoolId, tokenUse: null, clientId: clientIds })`
    from `aws-jwt-verify`. It checks `aud` for ID tokens and `client_id` for
    access tokens.
- Error body: `{ "error": { "code": "UNAUTHORIZED", "message": "..." } }` with HTTP 401.
- Rule: read only the `Authorization` header and accept only the `Bearer ` scheme.
  Never read cookies, query strings or other headers for identity.

- [ ] **Step 1: Write the failing tests** (`verifier` stubbed):
  - `missing_header_401`
  - `non_bearer_scheme_401`: `Authorization: AWS4-HMAC-SHA256 Credential=...` gives 401.
  - `verifier_rejects_401`: expired, wrong issuer or wrong client give 401.
  - `empty_client_list_fails_closed`: `createAuthenticate` throws when `clientIds` is empty.
  - `valid_token_sets_sub_and_user`: `req.sub === 'sub-1'`, `req.user === 'a@example.com'`.
  - `query_token_ignored`: `?access_token=...` without the header gives 401.
- [ ] **Step 2: Run** `npm test -- --test-name-pattern=auth`. Expected: FAIL (module missing).
- [ ] **Step 3: Implement `createAuthenticate`.** Wire it in `index.js` (Task 5) with
  env `USER_POOL_ID` and `BILLING_ALLOWED_CLIENT_IDS` (comma-separated, trimmed,
  empty entries dropped).
- [ ] **Step 4: Run** `npm test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(billing): verify TTOBAK JWTs in the BFF`.

### Task 3: Direct canonical membership only

**Files:** Rewrite `services/billing/bff/lib/access.js`; Create `services/billing/bff/test/access.test.js`
(replaces the ported hierarchy tests).

**Interfaces:**
- Produces: `createAccess({ ddb, ttobakTable, now }) -> { canAct(sub, acct): Promise<boolean>, visible(sub): Promise<Array<{id,name,parentId,member:true,canAct:true}>> }`.
  - `canAct`: `GetItem` `PK=ACCOUNT#{acct}`, `SK=MEMBER#{sub}`, `ProjectionExpression: PK`,
    `ConsistentRead: true`, with no cache. False for an invalid `ACCT_ID` without
    calling DynamoDB.
  - `visible`: paginated GSI1 query (`GSI1PK=USER#{sub}`, `begins_with(GSI1SK,'ACCOUNT#')`).
    For each candidate it runs `canAct` and reads `META` `name, parentAccountId` for
    display; it returns confirmed rows only, sorted by name then id. No ancestors,
    descendants or `known()` index.
- Consumers: `app.js` (`req.accounts`, the version/report routes) and
  `uploads.js` (every mutation already calls `access.canAct(..., { fresh: true })`;
  the option is now ignored).

- [ ] **Step 1: Write the failing tests** with `fake-ddb` and `pageSize: 1`:
  - `member_can_act`
  - `parent_member_cannot_act_on_child`
  - `child_member_cannot_act_on_parent`
  - `pending_row_does_not_grant`: a `PENDING_SHARE#x` / `PENDING_ACCOUNT#acct` item exists, `canAct` is false.
  - `stale_gsi_row_without_canonical_item_is_hidden`
  - `visible_paginates`: three memberships across pages.
  - `no_memberships_returns_empty`
  - `removed_member_loses_access_immediately`: delete the canonical item; the next `canAct` is false.
  - `can_act_reads_consistently`: the recorded `GetItem` input has `ConsistentRead: true`.
- [ ] **Step 2: Run** `npm test -- --test-name-pattern=access`. Expected: FAIL.
- [ ] **Step 3: Implement.** In `app.js`, when `visible` returns `[]`,
  `GET /api/billing/accounts` answers `200 []` (Review Focus 2) instead of the
  companion's 401/clear-cookie path.
- [ ] **Step 4: Run** `npm test`. Expected: PASS, including `app.test.js`.
- [ ] **Step 5: Commit** `feat(billing): grant access by direct membership only`.

### Task 4: Upload part URLs under `/billing-upload`

**Files:** Modify `services/billing/bff/lib/uploads.js`; Modify `services/billing/bff/test/uploads.test.js`.

**Interfaces:**
- `createUploadRoutes({ ..., publicUrl, uploadPrefix = '/billing-upload' })`.
  - Part URLs are `origin + uploadPrefix + u.pathname + u.search`, where `u` is
    the presigned virtual-hosted S3 URL, so its path is the key.
  - The CSRF gate stays: JSON content type, `X-TB: 1`, `Origin === new URL(publicUrl).origin`.
  - `publicUrl` is `https://ttobak.atomai.click`.

- [ ] **Step 1: Write the failing tests.**
  - `part_urls_use_prefix`: every returned URL starts with
    `https://ttobak.atomai.click/billing-upload/accounts/A1/uploads/` and keeps
    the `X-Amz-Signature` query.
  - `presigner_is_regional_virtual_hosted`: the presigned host is
    `<bucket>.s3.ap-northeast-2.amazonaws.com`, matching the Task 9 origin.
  - `presigned_key_unchanged`: the key passed to `UploadPartCommand` has no prefix.
  - `foreign_origin_rejected`: `Origin: https://insight.atomai.click` gives 403.
- [ ] **Step 2: Run, implement, run.** Expected: FAIL, then PASS.
- [ ] **Step 3: Commit** `feat(billing): serve upload parts under /billing-upload`.

### Task 5: Web Function entry, headers and package

**Files:**
- Create `services/billing/bff/index.js`, `services/billing/bff/scripts/package.mjs`,
  `services/billing/bff/test/entry.test.js`.
- Modify `app.js`, `.gitignore` (add `services/billing/bff/dist/`), and
  `docs/API-SPEC.md` (new "Billing service" section listing the five routes,
  auth, the 401 shape and that bodies are Billing-specific `{error: string}`
  except 401).

**Interfaces:**
- `index.js` reads `AWS_LAMBDA_HTTP_ENDPOINT` (`host:port`; default `127.0.0.1:8080` when unset).
  - Required env, else `process.exit(1)` with the missing key name only:
    `DATA_BUCKET, ACCOUNT_TABLE, PLATFORM_TABLE, PUBLIC_URL, USER_POOL_ID, BILLING_ALLOWED_CLIENT_IDS`.
- `app.js` sets `Cache-Control: private, no-store` before routing, including on errors and 404s.
- `npm run package` writes `dist/` with `index.js`, `app.js`, `lib/` and production `node_modules`
  (`npm ci --omit=dev` into a temp copy). `dist/` is the CDK asset path.

- [ ] **Step 1: Write the failing tests in `entry.test.js`.**
  - `listens_on_lambda_endpoint`: spawn `node index.js` with
    `AWS_LAMBDA_HTTP_ENDPOINT=127.0.0.1:<free port>` and stub env.
    `GET /api/billing/accounts` without a token gives the 401 JSON body.
  - `missing_env_exits_nonzero`
  - `no_store_on_every_response`: 401, 404 and a stubbed 200 all carry `private, no-store`.
- [ ] **Step 2: Run, implement, run.** Expected: FAIL, then PASS.
- [ ] **Step 3: Run** `npm run package && ls dist/node_modules/express`. Expected: present, and no `test/` directory.
- [ ] **Step 4: Commit** `feat(billing): Web Function entry and package`.

### Task 6: Dashboard pages as same-origin static assets

**Files:**
- Create `services/billing/page/{body.html,upload.html,dash-shim.js,build_page.py,test_build_page.py,package.json}`.
- Generate `frontend/public/billing-app/{index.html,upload.html,dash-shim.js,tokens.css,vendor/...}`.

**Interfaces:**
- `build_page.py` (run `python3 -I build_page.py`) writes each HTML with a first
  line `<!-- GENERATED by services/billing/page/build_page.py; do not edit -->`.
  - It copies `d3/dist/d3.min.js` and the Pretendard dynamic-subset CSS and fonts
    from `page/node_modules` into `vendor/`. All asset URLs are relative to `/billing-app/`.
  - The `{{USER}}`/`{{ACCOUNTS}}` placeholders are removed.
  - The upload/dashboard links become `upload.html` / `index.html`.
- `dash-shim.js` contract with the parent (Task 7):
  - It obtains a token via `await window.parent.ttobakBilling.token()` and sends
    `Authorization: Bearer <token>`.
  - It loads `/api/billing/accounts` first, replacing the injected `window.TB_ACCOUNTS`.
  - Data URLs are `/api/billing/data/<id>.json?...`, and the report and upload calls
    use `/api/billing/accounts/...`.
  - On 401 it calls `window.parent.ttobakBilling.refresh()` once and retries; a
    second 401 calls `window.parent.ttobakBilling.signedOut()`.
  - Expose the pure helpers as `window.__billingShim = { dataUrl, withAuth }` for tests.

- [ ] **Step 1: Write the failing `test_build_page.py`.**
  - `test_generated_output_is_current`: run the build into a temp dir and compare byte-for-byte with `frontend/public/billing-app/`.
  - `test_no_placeholders_or_login_links`: no `{{`, `/login` or `/static/`.
  - `test_api_paths_are_billing_scoped`: no `api/data/` without the `/api/billing/` prefix.
- [ ] **Step 2: Write `services/billing/bff/test/shim.test.js`** (load `dash-shim.js` in `node:vm` with a fake `window`):
  - `retries_once_after_refresh`: first fetch 401, `refresh()` called once, second fetch 200 gives data.
  - `second_401_signs_out`: `signedOut()` is called and the fetch is not retried a third time.
  - `empty_accounts_renders_empty_state`: `/api/billing/accounts` returns `[]`; the shim sets the entity to null without throwing.
- [ ] **Step 3: Run both; implement; run.** Expected: FAIL, then PASS.
- [ ] **Step 4: Commit** `feat(billing): dashboard pages as same-origin static assets`.

### Task 7: Next.js `/billing` host page

**Files:**
- Create `frontend/src/app/billing/page.tsx`, `frontend/src/lib/billingBridge.ts`,
  `frontend/scripts/billing-bridge.test.mjs`.
- Modify `frontend/src/components/layout/BillingInsightButton.tsx`,
  `infra/lib/frontend-stack.ts` (`knownPages` gains `'/billing'`),
  `infra/test/frontend-stack.test.ts`, and `docs/DESIGN-SPEC.md`.

**Interfaces:**
- `installBillingBridge(win, { getToken, refresh, onSignedOut }) -> () => void` sets
  `win.ttobakBilling = { token, refresh, signedOut }` and returns an uninstaller.
  - `getToken` is `getIdToken` from `src/lib/auth.ts`, and `refresh` is `refreshSession`.
  - `onSignedOut` is `triggerAuthFailure` from `src/components/auth/AuthProvider.tsx`,
    the same teardown `src/lib/api.ts` uses on a failed refresh. It does not just
    navigate.
- `page.tsx` renders inside the app shell an `<iframe src="/billing-app/index.html">`
  (`upload.html` when `?view=upload`) with `title="Billing"`, full height, no `sandbox`
  (same origin is required).
- `BillingInsightButton`: when runtime config `billingService === true`, it is a
  plain link to `/billing`. Otherwise the current hand-off stays. The default is
  false; cut-over flips the config value.

- [ ] **Step 1: Write the failing `billing-bridge.test.mjs`.**
  - `token_returns_current_id_token`
  - `refresh_returns_new_token`
  - `refresh_failure_calls_signed_out`
  - `uninstall_removes_bridge`
- [ ] **Step 2: Run** `cd frontend && npm run test:unit`. Expected: FAIL.
- [ ] **Step 3: Implement**, then add the `knownPages` assertion (`'/billing'` present) to `frontend-stack.test.ts`.
- [ ] **Step 4: Run** `npm run lint && npm run test:unit && npm run build`, and in `infra`, `npm test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): /billing route hosting the Billing dashboard`.

### Task 8: `TtobakBillingStack`

**Files:**
- Create `infra/lib/billing-stack.ts`, `infra/test/billing-stack.test.ts`.
- Modify `infra/bin/infra.ts` and `docs/INFRA-SPEC.md`.
- Modify `.github/workflows/test-infra.yml` and both jobs of
  `.github/workflows/deploy-infra.yml`: build `services/billing/bff` with
  `npm ci && npm run package` before synth, next to the MCP bundle step.

**Interfaces:**
- `new BillingStack(scope, 'TtobakBillingStack', { userPoolId, webClientId, dataBucketName, platformTableName, ttobakTableName, publicUrl })`.
  - It is instantiated only when `ttobak:billingDataBucket` and `ttobak:billingPlatformTable` context are set.
  - `CfnOutput`s `FunctionArn`, `EndpointArn` and `EndpointDomain` have no
    `exportName`. The operator copies them into `cdk.json` in Task 10; no other
    stack imports them.
- Resources:
  - `CfnWebFunction` `ttobak-billing`.
  - `CfnWebFunctionRevision`:
    - runtime `nodejs24.x`; code from `s3_assets.Asset(services/billing/bff/dist)`.
    - `timeoutSeconds: 60`, `maxConcurrencyPerEnvironment: 8`.
    - env from Task 5, with `BILLING_ALLOWED_CLIENT_IDS` = `authStack.spaClient.userPoolClientId`.
  - `CfnWebFunctionEndpoint`: `endpointName: 'prod'`, `HomeRegion`, `IamAuth`,
    100% weight to the revision, `scalingConfig.maxEnvironments: 10`.
- Execution role grants (copied from the companion web task, minus OAuth invoke):
  - `s3:GetObject` on `accounts/*/versions/*`, `accounts/*/handwritten/*`,
    `accounts/*/uploads/*` and `data/whats_new_feed.json`.
  - Upload actions (`s3:PutObject`, `s3:PutObjectTagging`, `s3:AbortMultipartUpload`,
    `s3:ListMultipartUploadParts`, `s3:DeleteObject`) on `accounts/*/uploads/*`.
  - `s3:PutObject` on `jobs/*`.
  - Platform table read/write.
  - `dynamodb:Query` on `ttobak-main` `index/GSI1`.
  - `dynamodb:GetItem` on `ttobak-main` with `ForAllValues:StringLike dynamodb:LeadingKeys ACCOUNT#*`.
    LeadingKeys is the real bound. Every BFF `GetItem` sends a
    `ProjectionExpression` (`PK` for membership; `PK, SK, name, parentAccountId`
    for META), but do not claim a `dynamodb:Attributes` condition enforces that.

- [ ] **Step 1: Look up the Web Function execution-role trust principal** in the
  Lambda Developer Guide ("Web Function execution role"). Record the URL in a code comment.
- [ ] **Step 2: Write the failing `billing-stack.test.ts`.**
  - `endpoint_is_iam_auth_home_region`
  - `no_application_managed_anywhere`
  - `revision_runtime_and_limits`: `nodejs24.x`, 60, 8.
  - `env_has_allowed_clients_and_no_secrets`: no key matching `/SECRET|PASSWORD|TOKEN/`.
  - `role_has_no_resource_star`
  - `no_vpc_no_security_group`
  - `stack_absent_without_context`: `infra.ts` synth without context has no `TtobakBillingStack`.
  - `outputs_are_not_exported`: no `Export` on any output.
- [ ] **Step 3: Run** `cd infra && npm test -- billing-stack`. Expected: FAIL.
- [ ] **Step 4: Implement**, then run `npx cdk synth` (with and without the context) and `npm test`. Expected: PASS.
- [ ] **Step 5: Confirm the release coordinator ignores the extra stack.**
  - Run `python3 -m unittest discover -s scripts/operations -p 'test_http_origin_release.py' -v`
    and the coordinator's template checks against a synth that includes `TtobakBillingStack`.
  - If it rejects the unknown stack, stop and ask the owner. Do not edit the coordinator under this plan.
- [ ] **Step 6: Commit** `feat(infra): TtobakBillingStack with an IamAuth Web Function`.

### Task 9: TTOBAK distribution behaviors and the resource policy

**Files:**
- Create `infra/lib/billing-upload-router.js`, `infra/lambda/billing-resource-policy/{index.mjs,package.json}`
  (pins `@aws-sdk/client-lambda-web` 3.1158.0; `npm run bundle` with esbuild to `dist/index.mjs`),
  and `infra/test/billing-frontend.test.ts`.
- Modify `infra/lib/frontend-stack.ts`, `infra/lib/frontend-policy.ts`,
  `infra/bin/infra.ts`, and `docs/INFRA-SPEC.md`.
- Modify `.github/workflows/test-infra.yml` and both `deploy-infra.yml` jobs: add the policy bundle step.
- Modify `CLAUDE.md`: an accepted-limits row for ADR-056's internet-reachable
  IamAuth endpoint and no-OAC upload behavior. Then run
  `python3 scripts/docs/sync_review_context.py`.

**Interfaces:**
- `FrontendStack` props gain an optional
  `billing?: { endpointDomain: string; functionArn: string; endpointArn: string; dataBucketName: string }`.
  - `infra.ts` builds it only when all three endpoint keys
    (`ttobak:billingEndpointDomain`, `ttobak:billingFunctionArn`,
    `ttobak:billingEndpointArn`) are set; `dataBucketName` comes from
    `ttobak:billingDataBucket`.
  - It throws when only some of the three endpoint keys are set, or when they
    are set without `ttobak:billingDataBucket`. The Task 10 bootstrap state
    (bucket and table keys only) is valid and yields no Billing behaviors.
- The three behaviors and the custom resource exist only when `billing` is set.
  All are ordered before `/api/*`.
  - `/api/billing/*`:
    - Origin: `HttpOrigin(endpointDomain)` with a `CfnOriginAccessControl`
      (`originAccessControlOriginType: 'lambda'`, `signingBehavior: 'always-amz-auth'`, `signingProtocol: 'sigv4'`).
    - `ALLOW_ALL` methods.
    - Cache policy `BillingAuthKeyed`: header allowlist `Authorization`, cookies
      and query strings all, `minTtl 0`, `defaultTtl 0`, `maxTtl 1s`.
    - Origin request policy `ALL_VIEWER_EXCEPT_HOST_HEADER`.
    - The existing edge-function version as a `VIEWER_REQUEST` edge lambda.
  - `/billing-upload/*`:
    - Origin: `origins.HttpOrigin('<dataBucketName>.s3.ap-northeast-2.amazonaws.com', { protocolPolicy: HTTPS_ONLY, readTimeout: 60s })`,
      the companion's working setup. There is no OAC, so CloudFront adds no
      signature and sends `Host` = the regional bucket domain the URL was signed for.
    - `ALLOW_ALL` methods, `CACHING_DISABLED`.
    - Origin request policy: query strings all, headers none, cookies none.
    - Function association `billing-upload-router` at `VIEWER_REQUEST`.
  - `/billing-app/*`: S3 site origin, a response headers policy with
    `applicationContentSecurityPolicy(..., { frameAncestors: "'self'" })` and
    `frameOption: SAMEORIGIN`. Every other path keeps `frame-ancestors 'none'` and `DENY`.
- `billing-upload-router.js` is a `handler(event)` for `cloudfront-js-2.0`.
  - It accepts only
    `^/billing-upload/accounts/[A-Za-z0-9_-]{1,64}/uploads/u\d{14}[0-9a-f]{6}/source\.zip$`,
    which is the companion's `sourceKey` with `ACCT_ID` and `UPLOAD_ID` from `store.js`.
  - It strips `/billing-upload` and returns 403 for anything else. The regex
    already excludes `%`, `..` and `//`.
- The custom resource (`cr.Provider` + `lambda.Function` with the bundle) runs on
  Create/Update. It gets the current policy, replaces the whole statement list
  with the single OAC statement, and calls `PutResourcePolicy` with the returned
  `revisionId`; on Delete it calls `DeleteResourcePolicy`. Its role allows only
  the Get, Put and Delete resource-policy actions on `functionArn`.

- [ ] **Step 1: Look up the IAM action names** for `PutResourcePolicy`,
  `GetResourcePolicy` and `DeleteResourcePolicy` in the Service Authorization
  Reference for Lambda Web Functions. Record them in a code comment.
- [ ] **Step 2: Write the failing router tests** (load the file in `node:vm`):
  - `strips_prefix_for_upload_part`: `/billing-upload/accounts/A1/uploads/u20261027120000abcdef/source.zip` gives `/accounts/A1/uploads/u20261027120000abcdef/source.zip`.
  - `rejects_other_file_in_upload`: `.../uploads/u.../other.zip`.
  - `rejects_versions_path`
  - `rejects_jobs_path`
  - `rejects_encoded_slash`: `%2F` and `%2f`.
  - `rejects_dotdot`
  - `rejects_double_slash`
  - `rejects_bad_account_id`
- [ ] **Step 3: Write the failing `billing-frontend.test.ts`.**
  - `billing_behaviors_precede_api`
  - `oac_lambda_always_amz_auth`
  - `cache_key_includes_authorization`
  - `no_origin_edge_triggers_on_billing`
  - `upload_behavior_has_no_oac_and_router`
  - `billing_app_frame_ancestors_self_only`
  - `other_paths_still_frame_deny`
  - `resource_policy_single_statement_with_source_arn`: the custom resource's
    policy JSON has one statement with principal `cloudfront.amazonaws.com` and a
    `StringEquals aws:SourceArn` distribution ARN.
  - `absent_without_billing_props`
  - `partial_endpoint_context_throws`
  - `bootstrap_context_has_no_billing_behaviors`: only `billingDataBucket` and
    `billingPlatformTable` set; synth succeeds, `TtobakBillingStack` exists and
    FrontendStack has no `/api/billing/*`.
- [ ] **Step 4: Run** `cd infra && npm test -- billing`. Expected: FAIL.
- [ ] **Step 5: Implement**, then run `npx cdk synth` (with and without billing context) and `npm test`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(infra): Billing behaviors and Web Function resource policy`.

### Task 10: Deploy and accept (owner approval required)

**Files:** Create `docs/runbooks/billing-phase1-release.md` (dated evidence, no
tokens or customer data). Modify `docs/README.md` (runbook row).

- [ ] **Step 1: Membership audit.**
  - List users who see an Account today only through a parent: compare companion
    `visible()` with direct membership for every member sub.
  - Have the owner add the needed direct memberships before anyone uses `/billing`.
- [ ] **Step 2: Deploy, with approval per step.**
  1. Merge a PR adding `ttobak:billingDataBucket` and `ttobak:billingPlatformTable` to `infra/cdk.json`.
  2. Deploy with `npx cdk deploy TtobakBillingStack --exclusively`. The release
     coordinator does not deploy this stack.
  3. Merge a second PR adding the three endpoint/function keys from the stack
     outputs to `infra/cdk.json`. The normal deploy-infra run then deploys
     FrontendStack through the coordinator.
  4. Run the frontend release workflow: it preserves `config.json`, re-uploads HTML no-cache, and invalidates.
  - Keep `billingService` false.
- [ ] **Step 3: Acceptance (record results in the runbook).**
  - The endpoint has no other auth type:
    `GetWebFunctionEndpoint` gives `authType: IamAuth`. `GetResourcePolicy` shows
    one statement with the distribution ARN.
  - An unsigned `curl` to the endpoint domain gets 403. A SigV4-signed admin call
    without a Bearer token gets 401 from the app.
  - Two test users in different Accounts:
    - each sees only their own Account at `/billing`;
    - crossing `acct` in `/api/billing/data/kpis.json?acct=&v=` gives 404;
    - a third user with no membership sees the empty state.
  - Upload a small synthetic CUR file through `/billing-upload`. The old worker
    processes it and the new page shows the version.
  - Send `GET /api/billing/data/kpis.json` twice as different users through
    CloudFront; check `X-Cache` is never a hit for the second user.
- [ ] **Step 4: Report** the acceptance to the owner. Flipping `billingService`
  (cut-over §9 step 4) is a separate approval.

---

## Follow-on plans (not in this plan)

- **Phase 2:** move the agent stage to AgentCore Runtime. The worker invokes it
  after ingest, and `rerun-agents` invokes it directly. The worker code moves to
  `services/billing/worker`. If it keeps calling the web-search gateway, add it to
  the aligned SigV4/MCP transport list.
- **Phase 3:** MCP `billing_*` tools. They call `/api/billing/*` through
  CloudFront with the caller's token. Add the MCP resource audience to
  `BILLING_ALLOWED_CLIENT_IDS` handling, apply connector grant checks and
  operation inventory entries, and keep the 32,000-byte result caps.
- **Phase 4:** cut-over.
  - Flip `billingService`; move the worker EventBridge rule into
    `TtobakBillingStack`.
  - `insight.atomai.click` gets a two-week redirect (a CloudFront Function on
    the old distribution), then the old stack, distribution and DNS record are
    removed.
  - Decide KMS encryption for the data bucket, which is SSE-S3 today, and
    confirm platform-table retention.
