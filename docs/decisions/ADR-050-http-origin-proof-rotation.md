# ADR-050: Versioned HTTP origin proof rotation

- Date: 2026-10-12
- Status: Accepted; staged implementation
- Scope: The existing CloudFront HTTP origin boundary

## Context

The HTTP origin proof is a tracked configuration value. Removing that occurrence
does not revoke repository history, and changing a secret without coordinating
CloudFront and cached consumers can interrupt traffic. Source ancestry does not
prove that a compatible consumer was deployed.

## Decision

Use a Secrets Manager JSON proof document with a generation, current value and
an explicitly expiring previous value. Bind activated consumers and the
CloudFront origin header to explicit immutable secret versions. Keep the same
proof during the initial environment migration, then deploy and verify consumers
using the dual-proof version before changing the producer.

Before activation mutations, invoke existing content-free HTTP paths through
AWS Identity and Access Management (IAM). Require the verified HTTP protocol
marker, the expected response, and rejection without an origin proof. Capture
and recheck function identity, code hash, configuration revision and alias
version. The marker alone does not establish Secrets Manager permission or
successful version binding.

Drain caches for at least 65 seconds after each consumer version transition:
60-second cache, three-second in-flight lookup and margin. Verify new-proof
acceptance before CloudFront cutover. After the distribution is deployed,
deploy consumers using the active version, drain again and verify rejection of
the retired proof. Preserve bounded, resumable state after failures; never
extend a previous-proof deadline silently or claim completion without checks.

The preparatory reader release retains exact comparison of every nonempty
legacy value, including values outside the new grammar. Observable duplicate
or conflicting header representations are rejected. Activated ARN mode uses
the strict document/header contract. Missing configuration never grants HTTP
access; explicit local development is restricted to execution outside Lambda.

## Consequences

The reader foundation does not rotate a deployed proof. A separately reviewed
release coordinator must perform the environment migration, version transitions
and redacted runtime receipt before the disclosure finding can close.

No application route is added. Existing public-document and static MCP discovery
exceptions remain unchanged. The WebSocket secret and its authorizer retain
their separate contract. Lookup diagnostics contain fixed categories only.

See the [migration contract](../runbooks/http-origin-secret-migration.md).
