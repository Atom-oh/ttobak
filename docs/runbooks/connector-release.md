# Connector deployment and acceptance

The activation source sets `connectorOAuthEnabled` and
`connectorPrivateBridgeEnabled` in `infra/bin/infra.ts` to true for the two
dedicated clients and private HTTP transport. The edge receives the same private
flag so its challenge requires `openid email profile ttobak.connector/read`;
MCP discovery advertises that baseline and optional `ttobak.connector/write`.
Source configuration is not live provider acceptance. Record the deployed flags,
reviewed revision and acceptance evidence before claiming activation complete.

The [2026-10-21 disabled-staging record](connector-staging-2026-10-21.md) records
the prerequisite four-stack rollout and first-party acceptance with both flags
false, including its artifact identities and remaining private-activation checks.

Private HTTP mode accepts only the dedicated read and read/write client IDs.
Reconfigure each HTTP host using the AuthStack `ReadConnectorClientId` output,
or `WriteConnectorClientId` when personal-note writing is required, and perform a
fresh PKCE login. A first-party HTTP token is rejected in this mode. Installed
stdio keeps its existing client and deployment/profile-bound cache.

## Resource boundaries

AuthStack preserves the existing pool, first-party and companion client identities.
It adds public read and read/write clients with authorization code flow only,
revocation enabled and the already approved MCP callback set. Neither client enters
the Cognito identity pool, API first-party allowlist or WebSocket allowlist.
The read client can reach only the API's current-grant bounded reader/detection
boundary. The write client can reach `/api/mcp` only at the edge.

GatewayStack owns the retained RSA-2048 signing key and exact-key public-reader
policy. Only MCP may sign with RSA-PSS/SHA-256 and invoke the API `live` alias or
QA function. Its DynamoDB permission is transactional `GetItem` on the configured
table with grant metadata attributes and key prefixes; it cannot read content or
write grants. Transaction IAM uses the underlying GetItem action, as specified in
[AWS transaction permissions](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis-iam.html).
Consumer ownership and current-source checks remain required independently.

QA receives an ARM64/Python 3.12 layer with all three dependency versions and wheel
hashes pinned in `infra/assets/connector-crypto/requirements.txt`. CDK installs with
Python 3.12 locally or its Lambda build image, explicitly selecting aarch64 wheels.
An unsuccessful install stops synthesis. Check the deployed layer, import and
real RSA-PSS verification before enabling any producer.

## Deployment sequence

1. Capture current main, deployment activity, stack templates, Lambda code hashes,
   alias versions, feature parameters, grants and public config without credentials.
   Build all Go entry points and both MCP artifacts from the reviewed combined
   revision, preserving already accepted news and current-source consumers.
2. For the prerequisite staging rollout, use a reviewed revision with both
   connector flags false. Synth/diff each changed stack and check resource
   identities, IAM and staged assets. Deploy AuthStack, EdgeAuthStack,
   GatewayStack and FrontendStack individually with `--exclusively`, respecting
   their cross-region references. Never deploy KnowledgeStack implicitly.
   Preserve all deployed `SummaryPrivacy*` parameters with previous parameter
   values; never reset or advance the legacy member cutoff during this release.
3. With issuance still disabled, verify unchanged first-party login, API, WebSocket,
   HTTP MCP and stdio token-cache behavior. Check exact client IDs, disabled OAuth,
   retained key, deployed crypto layer and private rejection without grants.
4. Review the separate activation revision with both flags true and the matching
   edge challenge. Repeat the resource/IAM diff and deploy the same stacks
   individually, preserving previous summary privacy parameters. Use the approved
   loopback PKCE flow for a dedicated synthetic identity. Request the exact
   resource audience and read scope, plus write scope only for the write client.
   Capture provider claims as nonsecret assertions; keep tokens and verifier
   material private.
5. Verify reads and personal-note creation through CloudFront and actual service
   IAM/KMS. Test the same-key replay, conflicting body, human edit and deletion,
   another user's source denial, source changes, expired/revoked/blocked approvals,
   disabled client policy and rejected ID/unbound/unknown-client tokens.
   Verify direct write-client API and both clients' WebSocket denial.
6. Verify rollback of the private flag with existing first-party clients; disable
   dedicated OAuth when retiring that path. Restore the accepted configuration,
   remove synthetic source/session/note fixtures and preserve audit receipts.
   Record exact deployed versions and provider outcomes before main integration.

Reserve an HTTP MCP maintenance window for both activation and flag-off rollback.
EdgeAuthStack and GatewayStack cannot switch atomically: edge-first activation
advertises a read scope unsupported by the first-party client while the old MCP
still rejects dedicated clients; cached first-party sessions may continue until
Gateway changes. Gateway-first activation makes MCP reject first-party clients
while the old edge still advertises first-party scopes. Expect temporary
authentication failures until both stacks converge and HTTP hosts complete their
dedicated login. The reverse split occurs during rollback. Check both deployed
flags, discovery/challenge and positive/negative client probes before ending the
window; a single successful stack update is insufficient.

## Activation and rollback checks

Use disposable identities and synthetic sources. Enable the two exact client
policies through administrator settings and approve writing through the synthetic
user's first-party settings, recording policy and approval generations. Read-only
OAuth is not write approval. A missing or disabled policy must deny private reads.
Keep captured tokens, PKCE verifiers and login callbacks in private files; receipts
record boolean claim assertions, response status, byte counts and fixture IDs.

| Check | Required result |
|---|---|
| Read and write client PKCE | Verified access token has the exact client, issuer, resource audience and requested scopes; read client cannot obtain write scope |
| Both discovery paths and edge 401 | Read scope is advertised/required; write is supported but absent from the baseline challenge |
| Bounded source read and private QA | Synthetic current source is returned/cited through actual service IAM and KMS; no bearer fallback |
| First-party HTTP versus stdio | HTTP MCP rejects the first-party token; installed stdio still reads using its original cache/profile |
| Note create and same-key replay | One fresh personal note ID; replay returns that ID without another note |
| Conflicting body, human edit, deletion | Changed body conflicts; replay preserves human text; replay never recreates a deleted note |
| Foreign source and changed continuation | Foreign source is denied; a continuation for old bytes is rejected after an authorized edit |
| Grant expiry, revoke, block and disabled policy | Existing access tokens gain no stale privilege; re-approval uses the current generation |
| ID, unbound, expired and unknown-client tokens | Rejected before tool execution |
| Direct write-client API and both WebSockets | Denied; no identity-pool or first-party allowlist membership |
| Private flag rollback | Set it false in both EdgeAuthStack and GatewayStack, preserving OAuth while testing; discovery/challenge restore first-party scopes, first-party HTTP succeeds and dedicated HTTP is rejected |
| Restore and cleanup | Restore reviewed flags, verify both client modes again, remove only synthetic source/session/note fixtures and revoke synthetic approvals/tokens; retain bounded audit receipts |

When retiring dedicated issuance, set `connectorOAuthEnabled` false and deploy
AuthStack separately. Disabling OAuth does not itself revoke already issued
access tokens; keep the private flag and current-grant deny checks in the
rollback procedure. Do not alter installed first-party stdio credentials.

For flag-off first-party HTTP testing, resolve the existing SPA client from
AuthStack's `SpaClientId` output and use its original scopes and resource audience.
Do not substitute a dedicated client or rewrite a stdio cache.

Prefer flag rollback with the reviewed API code retained. Rolling API code back
to the older deterministic-note-ID implementation after fresh UUID receipts exist
makes retries for those receipts return 409 for their retained lifetime; the
receipt still prevents duplicate creation and preserves the existing note.
Do not delete receipts or change idempotency keys to bypass that conflict.

The installed stdio adapter continues using `deployment-auth.ts` and its verified,
deployment/profile-bound cache. It must not be replaced by the legacy `auth.ts`.
Native host migration and actual cached-session/refresh behavior remain acceptance
requirements. Never translate an existing first-party cache into dedicated-client
credentials.
