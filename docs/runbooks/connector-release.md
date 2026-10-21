# Connector deployment and acceptance

The two connector clients, private consumers and signing infrastructure are staged.
`connectorOAuthEnabled` and `connectorPrivateBridgeEnabled` in `infra/bin/infra.ts`
remain false until separately reviewed activation. A passing source test is not
live provider acceptance.

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

Before synth or gateway tests, provide Python 3.12 with pip (`python3.12` or
`python3`), or Docker capable of running the Lambda Python 3.12 ARM64 build image
with volume copy. The bundler needs the package index for the three pinned wheels.
Local pip can use an existing wheel directory with `PIP_FIND_LINKS` and
`PIP_NO_INDEX=1`; hashes remain mandatory. A failed local install does not fall
back to Docker. PR tests and the separate deployment `infra-tests` job need these
prerequisites before the later deploy job's Python setup.

## Deployment sequence

1. Capture current main, deployment activity, stack templates, Lambda code hashes,
   alias versions, feature parameters, grants and public config without credentials.
   Build all Go entry points and both MCP artifacts from the reviewed combined
   revision, preserving already accepted news and current-source consumers.
2. Obtain the required independent review, synth/diff each changed stack and check
   resource identities, IAM and staged assets. Deploy AuthStack, EdgeAuthStack,
   GatewayStack and FrontendStack individually with `--exclusively`, respecting
   their cross-region references. Never deploy KnowledgeStack implicitly.
3. With issuance still disabled, verify unchanged first-party login, API, WebSocket,
   HTTP MCP and stdio token-cache behavior. Check exact client IDs, disabled OAuth,
   retained key, deployed crypto layer and private rejection without grants.
4. Review the separate activation revision. Pair the EdgeAuth private-bridge flag
   with the MCP mode and protected-resource discovery scopes: active challenges
   require `ttobak.connector/read`; staged and flag-off challenges retain
   `openid email profile`. Both source flags remain false in this staging revision.
   Verify the complete deployment sequence before consumer acceptance.
   Use the approved loopback PKCE flow
   for a dedicated synthetic identity. Request the exact resource audience and
   read scope, plus write scope only for the write client. Capture provider claims
   as nonsecret assertions; keep tokens and verifier material private.
5. Verify reads and personal-note creation through CloudFront and actual service
   IAM/KMS. Test the same-key replay, conflicting body, human edit and deletion,
   another user's source denial, source changes, expired/revoked/blocked approvals,
   disabled client policy and rejected ID/unbound/unknown-client tokens.
   Verify direct write-client API and both clients' WebSocket denial.
6. Verify rollback of the private flag with existing first-party clients. For
   retirement, disable dedicated client policies, revoke remaining write approvals,
   and disable dedicated OAuth. Revoke issued refresh tokens or separately review
   deletion of the dedicated clients; OAuth-off alone is not credential revocation.
   Locally verified issued access/ID tokens can remain valid until expiry, so keep
   grant and transport denial in place. Restore the accepted configuration,
   remove synthetic source/session/note fixtures and preserve audit receipts.
   Record exact deployed versions and provider outcomes before main integration.

The installed stdio adapter continues using `deployment-auth.ts` and its verified,
deployment/profile-bound cache. It must not be replaced by the legacy `auth.ts`.
Native host migration and actual cached-session/refresh behavior remain acceptance
requirements. Never translate an existing first-party cache into dedicated-client
credentials.
