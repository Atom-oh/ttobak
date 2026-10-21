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

This revision supplies policy types, operation tables, deterministic generation
and cross-language tests. No token verifier, signer, grant store, private
consumer, approval endpoint, OAuth client, IAM permission or scheduler is
activated by this foundation. Existing transports do not import the policy.

Subsequent reviewed stages must supply dedicated-client ingress, private signing
and current grant resolution, bounded API/QA consumers, atomic approval/audit
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
