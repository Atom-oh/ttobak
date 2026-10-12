# HTTP origin proof migration

## Current implementation and release boundary

The Go application programming interface (API), Python question answering (QA)
and hosted Model Context Protocol (MCP) consumers verify the Hypertext Transfer
Protocol (HTTP) origin. They support the Amazon Resource Name (ARN) configured in
`ORIGIN_VERIFY_SECRET_ARN` and the proof document below. This is the consumer
foundation for a coordinated migration. The checked-in Cloud Development Kit
(CDK) configuration still supplies the legacy environment proof; no new secret,
permission grant, CloudFront cutover or rotation is performed by this foundation.
The original plaintext-disclosure finding remains open until the later migration
and a verified rotation receipt.

ARN configuration takes precedence over the legacy `ORIGIN_VERIFY_SECRET`.
If the ARN read fails, the consumer denies access instead of trying the legacy
value. The legacy path preserves every nonempty existing value exactly, including
Unicode, commas and values outside the new proof length range. It rejects
observable duplicate/conflicting representations but does not reinterpret a
single legacy value. It must be removed with the final environment migration.
Do not point a production consumer at a new random proof before CloudFront and
the other consumers can use it.

Missing configuration denies HTTP requests. Local Go/QA development can explicitly
set `TTOBAK_ORIGIN_LOCAL_DEV=1` only outside Lambda: the application binding also
requires `AWS_LAMBDA_FUNCTION_NAME` to be absent. The HTTP MCP Lambda wrapper has
no local bypass; the existing local MCP HTTP transport is a separate entry point.
QA's existing WebSocket invocation, authorized through Identity and Access
Management (IAM), and its source-validated Simple Queue Service (SQS) worker
branches remain separate from the HTTP origin check.

Verified HTTP responses carry the constant `X-Ttobak-Origin-Protocol: 1` marker,
including normal authentication, unknown-route and configuration error responses.
Rejected origin proofs and internal QA transports do not carry it. The marker
identifies the installed reader contract; it does not prove Secrets Manager
permissions or an ARN cutover. Before activation mutations, the release must
verify the marker on the existing content-free paths and pin the consumers'
code hash, configuration revision and alias version against concurrent changes.

## Version 1 proof document

Secrets Manager `SecretString` contains a JavaScript Object Notation (JSON) object
of at most 2,048 bytes in UTF-8 Unicode encoding.
The generation is nonsecret metadata; proof values must never be printed.

| Field | Contract |
|---|---|
| `schemaVersion` | Integer `1` |
| `current` | 16–128 visible ASCII characters, excluding commas |
| `previous` | Empty for `seeded`/`active`; a different valid proof for `rotating` |
| `rotationStartedAt` | Nonnegative Unix seconds; positive during rotation |
| `previousExpiresAt` | Zero for `seeded`/`active`; strictly after rotation start and at most 24 hours later during rotation |
| `generation` | Exactly 32 lowercase hexadecimal characters |
| `phase` | `seeded`, `rotating` or `active` |

`current` is accepted after a valid secret read. `previous` is accepted only while
`rotationStartedAt <= now < previousExpiresAt`. An expired previous proof does
not invalidate the current proof. The time check runs on every request, including
when the document is cached.

In ARN mode, all consumers reject missing, duplicate, conflicting, comma-combined,
whitespace or oversized proof headers and compare values in constant time. API Gateway
payload 2.0 comma-combined duplicates are rejected. Go and Python additionally
reject conflicting multi-value representations supplied by their adapters.
The wrapper never forwards the origin proof to the MCP application server.

The optional nonsecret `ORIGIN_VERIFY_SECRET_VERSION_ID` pins an immutable
Secrets Manager version. Activation must configure and verify a version pin on
every consumer; an ARN alone does not establish the intended source version.

Secret documents are cached for at most 60 seconds. Go and MCP bound lookup
waiting to three seconds and disable software development kit (SDK) retries.
QA bounds lock waiting to one second and uses one-second SDK connect/read timeouts
with one total attempt.
Expired cache entries never authorize a request after a failed refresh. Rejection
responses use fixed messages and do not include AWS errors, proof values, request
headers, secret identifiers or generated content.

The Secrets Manager JavaScript SDK is an exact build dependency included in the
Lambda bundle. It does not become a runtime dependency of the downloadable stdio
bundle or change that bundle's Node.js compatibility.

## Required coordinated follow-up

The final migration must perform and verify all of these steps through the normal
reviewed release workflow:

1. Read the proof from the **currently deployed API configuration**, verify the
   other consumers and CloudFront agree, and bootstrap Secrets Manager with that
   same proof. Never assume the tracked CDK value matches deployment.
2. Install exact secret-read permissions and ARN-only consumer configuration,
   preserving the currently accepted proof.
3. Configure CloudFront with a Secrets Manager reference to the same proof and
   verify the distribution has deployed.
4. Generate a new proof and explicit bounded overlap metadata. Deploy and verify
   consumers pinned to the immutable dual-proof version, then drain old caches
   for at least 65 seconds (60-second TTL, three-second lookup and margin).
   Require actual new-proof acceptance before the producer changes. Force a
   CloudFront secret-version update; changing a secret alone does not update its resolved
   origin header. Retain enough time to finish or resume the cutover safely.
5. Remove the previous proof only after CloudFront is deployed with the new proof.
   Deploy consumers pinned to the immutable active version and drain caches again
   before verifying retirement. Verify both successful CloudFront
   requests and rejection of direct-origin and retired-proof requests.
6. Remove the tracked literal and legacy environment/read path, and record a
   redacted receipt of the deployed revision and completed rotation.

Failure or abort handling must preserve a resumable generation/deadline and must
not discard a proof while CloudFront might still send it. A configured ARN, clean
template or successful unit test is not a rotation receipt. Repository history
retains the old disclosed value, so removing its current-file occurrence alone
does not close the finding.

## Verification

Go middleware tests, QA handler/origin tests and MCP tests cover bounded overlap,
per-request expiry during caching, failed refresh, malformed/duplicate headers,
legacy compatibility and denial without configuration. Actual SDK requests to
synthetic Secrets Manager responses run against both the TypeScript output and
the distributed Lambda bundle. The existing QA HTTP tests explicitly stub the
origin boundary; separate tests verify HTTP denial and unchanged internal routing.
These checks make no authenticated production requests and establish no deployed
secret state.
