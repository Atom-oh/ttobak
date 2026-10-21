# ADR-053: Connector capabilities and private delegation

Date: 2026-10-21

Status: Accepted design; staged policy foundation, activation pending

## Context

Connector access needs an explicit operation policy independent of HTTP verbs
and the existing browser session. Read access must not implicitly enable note
creation, sharing, deletion, asynchronous research or mutation-capable streaming.
Earlier bounded meeting/document/account/project readers remain the producers;
this work does not replace their authorization or source-revision checks.

## Decision

Maintain one versioned operation inventory in
`contracts/connector/v1/operations.json`. Generate matching Go, TypeScript and
Python tables, with a SHA-256 of the complete inventory in each output. Unknown
operations and mismatched destinations fail closed. A route's method is a binding,
not its capability classification. For example, revoking a share requires
`share`, and question detection can be classified as `read` despite using POST.

Capabilities are sorted, unique subsets of `delete`, `read`, `share`, `write`,
always including `read`. Effective privileges intersect the current enabled
client maximum, the current user's approval and the requested delegation.
Elevated requests bind the exact approval generation and cannot outlive its
expiry. Revoked, missing, changed or foreign approvals reject the request rather
than silently reducing its privileges. Generation zero is read-only.

Inventory eligibility is not executor availability. The staged API reader
allowlist names only the eight existing bounded reading families, including
account and project reading. The planned
private protocol will separately bind verified client identity, exact operation
and argument bytes, destination, request identity and expiry. Ordinary browser
authorization, domain ownership and canonical source revalidation remain
independent requirements.

## Current implementation and remaining delivery

This revision supplies policy types, operation tables, deterministic generation,
local RSA-PSS delegation verifiers and shared synthetic signature tests.
The [wire contract](../security/connector-delegation-v1.md) defines exact bytes,
key pinning and lifetime limits. Current grant readers now exist in Go and Python:
client policy and user approval are read together with `TransactGetItems`, with
strict identity, type and configured issuer/audience checks. Malformed existing
approval rows reject rather than becoming a missing read-only approval.

Policy and approval writers atomically publish the expected generation and a
bounded audit receipt. Policy profiles are read or read/write. Grant elevation
checks the enabled client maximum as a DynamoDB String Set; revocation and blocking remain possible after
administrators disable or tighten that policy. Blocking has no TTL, while ordinary
expired approvals and audit receipts retain the documented 30-day cleanup period.
A removal-only revoke cannot clear an existing block: it returns a condition
conflict unless the caller retains the block. Unblocking requires explicit
re-approval through the enabled-policy and capability checks.
SDK transaction retries carry a stable request token; a condition conflict is
separate from an unknown transport outcome that requires generation inspection.
No live signer, approval endpoint, OAuth client, IAM permission
or scheduler is activated by this foundation. The API auth boundary now applies a dedicated
read-client policy when configured.
Its default configuration is empty; without an injected current-grant resolver,
every dedicated client is denied. Existing first-party client authorization remains
in force. A successful dedicated read requires fresh enabled-client grants before
execution and again before releasing its bounded response. The QA HTTP boundary
similarly admits only bounded question detection and denies
dedicated clients without a current-grant resolver. Both boundaries reject
mutation-capable and unbounded routes before dispatch; QA does not decode caller
headers to establish a configured client's identity.

The API Lambda now distinguishes a top-level private invoke frame from API
Gateway payload 1.0. A configured exact KMS signing-key ARN enables a lazy,
bounded public-key lookup and local signature verification; empty configuration
keeps the private consumer unavailable. The bridge binds operation, exact argument
bytes and current grants before calling only the eight bounded API read families,
then verifies again before releasing at most 14,000 body bytes. The router accepts
only its verifier-created in-process principal; no HTTP header or nested request
body selects this path. Existing services still check resource ownership and
source revisions. The API's dedicated HTTP read boundary now receives the same
atomic grant resolver. The private mutation callback remains unavailable.

The private QA consumer now accepts only `qa.readonlyAsk`, with a question and
optional meeting ID. It verifies the same signed delegation against the QA
destination and current grants, then reads through the existing current-source
boundary. One bounded model call runs without tools, sessions or research/job
creation. Current source checks and grant checks precede result delivery.
Model evidence includes verified attachment text and its partial/retained/unavailable
status; transcript, notes and serialized attachment context share the 24,000-byte
limit. Attachment citations correspond to the evidence supplied to the model.
Only the pinned public key is cached; KMS and grant clients use bounded timeouts
and one attempt. Dedicated HTTP detection now receives the same current-grant
reader. Empty signing-key configuration leaves private execution unavailable.
The deployment artifact must contain the pinned ARM64 cryptography wheel before
key configuration is enabled; source installation does not prove deployment.

The MCP HTTP adapter now has an opt-in private transport for the eight bounded
API reads and `qa.readonlyAsk`. Request-scoped verified identity/scopes, atomic
grant reads, exact-byte KMS signatures and service-IAM invocation replace bearer
forwarding in this mode. It rechecks grants and expiry before returning bounded
results, with one SDK attempt and bounded calls. The flag defaults off and a
private failure never falls back to a legacy request. Stdio remains unchanged.

Subsequent reviewed stages must supply private signing infrastructure, approval management endpoints
and the specifically available personal-note executor. Infrastructure must
preserve existing pool/client identities and keep issuance disabled through
deployment verification. Native migration, real provider tokens, audience and
scope checks, current-source reads, expiry/revocation/disable behavior, actual
KMS/IAM execution and rollback require recorded acceptance before activation.
No additional external callback or unauthenticated route is approved here.

## Verification

`scripts/connector/generate_contract.py --check` compares all generated files
against the same inventory. Language tests cover inventory hashes, explicit
capabilities, unknown operations, destination binding, immutable returned policy
and current approval intersection. MCP tests also check the current Go route
inventory. These source tests do not prove deployed provider or consumer behavior.
