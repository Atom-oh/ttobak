# CloudFront WebSocket runtime

This is an operational procedure, not evidence that a particular revision is
deployed. The browser endpoint is the current site's `/ws` path; on the primary
site it resolves to `wss://ttobak.atomai.click/ws`.

## Contract

FrontendStack publishes `wsUrl: "/ws"` alongside public Cognito IDs in config.json.
Chat and Live QA accept only that same-site path. The CloudFront behavior disables
caching, forwards viewer headers except Host, and rewrites `/ws` to `/production`.
It injects `x-origin-verify` from a Secrets Manager dynamic reference. A viewer's
header with the same name is overwritten by CloudFront.

The existing `$connect` Lambda authorizer requires the origin proof plus a
verified Cognito JWT. Lambda env contains only `WS_ORIGIN_SECRET_ARN`; neither the
proof nor its ARN belongs in public config. The generated proof is 64 alphanumeric
characters. Secret reads have a three-second deadline and a 60-second cache;
missing configuration, malformed/duplicate headers and refresh errors fail closed.
The JWT decision is not cached by this helper. The returned policy covers the
specific connect ARN, not all API stages.
`AuthorizerResultTtlInSeconds` applies only to HTTP API Lambda authorizers, not
WebSocket authorizers; do not add an unsupported result-cache setting here.

Chat bounds the handshake to 10 seconds and each answer to 65 seconds, preserves
partial text with a failure notice, and releases input on timeout/error/close.
It does not automatically replay QA over WebSocket or REST after a send attempt.
Unknown requests get a fresh conversation session; old socket/REST completions
cannot update a newer request or a new chat. Chat sockets are closed at terminal
completion; Live QA retains its existing reconnect/watchdog behavior.

Local development uses REST by default: the environment fallback has no `wsUrl`
and this repository does not provision a local `/ws` proxy. The URL validator's
loopback support only applies when a developer explicitly supplies both.

The websocket and QA workers retain their existing IAM-signed callback path.
No connections table, new unauthenticated application route or direct browser
execute-api fallback is introduced. Existing legacy HTTP origin configuration and
pre-existing broad management permissions are not claimed fixed by this change.

## Ordered rollout

1. Run Go tests/vet, the ws-authorizer race tests, frontend lint/build, infra tests
   and offline synth. Build the changed bootstrap with Linux/ARM64 and
   `-tags lambda.norpc`; the test/deploy loops now include all eight zip targets.
2. Deploy GatewayStack with `--exclusively`. Confirm the secret/read policy exists,
   ws-authorizer has the expected bootstrap hash and ARN-only environment, and
   `$connect` requires token plus origin-header identities.
3. Deploy FrontendStack with `--exclusively`. Wait for CloudFront propagation.
   Verify `/ws` behavior, exact `/production` rewrite, no-cache policy and dynamic
   origin header without printing the header value.
4. Confirm config.json contains the relative `/ws` endpoint and no secret fields.
   Deploy frontend assets using the existing config-preserving workflow; apply the
   HTML refresh requirement in the [deployment runbook](deployment.md).
5. Refresh the browser before acceptance. An already-open page can retain its old
   cached runtime config and continue using REST.

The explicit race command is:

```bash
(cd backend && /usr/local/go/bin/go test -race ./cmd/ws-authorizer -count=1)
```

## Real acceptance after deployment

- Use the existing authenticated synthetic/demo browser session. Through the
  CloudFront endpoint, verify a successful WebSocket upgrade and actual
  `answer_delta` / `answer_complete` frames in both Chat and Live QA. Check source
  details, failure messages and no duplicate background request after fallback.
- With a valid JWT but no origin proof, the direct API Gateway WS endpoint must
  reject the connection. A bad JWT through CloudFront must also be rejected.
  Keep tokens in the authenticated client or protected test input; do not print
  URLs containing tokens or copy proofs into shell history.
- Record deployed source/bootstrap hashes, distribution state, positive/negative
  connection outcomes and completion evidence. Unit mocks/synth are not live proof.
- Check the distribution's standard logging, every behavior's real-time binding,
  and paginated CloudWatch delivery sources for standard logging v2. The current
  stack declares none. Before enabling query-bearing logging, provide reviewed
  exclusion/redaction of the `token` query value; do not disable existing audit
  logging merely because a review speculates that it is enabled.

## Coordinated secret rotation

There is no rotation automation. Keep the same Secret ARN and coordinate a
maintenance window for new connections. Update its AWSCURRENT value to a fresh
64-character alphanumeric proof without logging it. Then explicitly update the
CloudFront Distribution resource: for example, change its non-secret Comment
revision in FrontendStack and deploy that stack exclusively. This forces
CloudFormation to resolve the current secret reference during the resource update.
An unchanged deployment, or changing the secret alone, does not guarantee refresh.

Wait for CloudFront's deployment to finish and for the authorizer's 60-second
secret cache to expire, then repeat the positive/negative connection checks.
During transition, mismatched edge/cache versions can reject new connections;
never add an empty-secret or stale-secret fallback to avoid that rejection.
`$connect` authorization does not retroactively revoke existing connections:
close known test sockets and account for legacy sessions during cutover.
Restrict access to Secrets Manager and CloudFront distribution configuration,
which contains the resolved private origin header.

Relevant primary documentation:

- [CloudFront WebSockets](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/distribution-working-with.websockets.html)
- [CloudFront custom origin headers](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/add-origin-custom-headers.html)
- [Secrets Manager dynamic references](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references-secretsmanager.html)
- [API Gateway v2 Authorizer properties](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-apigatewayv2-authorizer.html)
