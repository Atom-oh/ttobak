# Connector delegation version 1

Status: staged local verifier implementation; production transports do not import
these modules. Go, TypeScript and Python verify the same synthetic signatures and
resolve current grants through an injected trusted callback. No AWS client,
private receiver, OAuth issuer or elevated executor is activated here.

The later hosted MCP signer must derive identity/scopes/expiry from its verified
access token and invoke private API/QA consumers with service IAM. Those consumers
must fetch the exact configured key's public material and verify before execution
and before returning read results. Native migration and deployed/provider
acceptance remain separate gates under ADR-053.

The signed payload has this exact JSON field order:

```text
version, protocol, destination, sub, clientId, capabilities,
approvalGeneration, operation, argumentsSHA256, requestId,
issuedAt, expiresAt, upstreamExpiresAt
```

`version` is integer 1; `protocol` is `ttobak.connector`. The destination is a
configured private API or QA bridge. Identity/request IDs use bounded ASCII;
capabilities are sorted, unique, known strings including read. Numeric fields
use safe integers shared by the three languages. All fields are required.

The outer JSON has exactly `keyId,algorithm,payload,signature` in that order.
Payload/signature use canonical unpadded base64url. Both JSON layers must equal
their schema's canonical serialization: no duplicate/unknown fields, alternate
number spelling, escaped identifiers, extra whitespace or reordered fields.
UTF-8 is strict. This avoids trusting a parser's duplicate-field preference.

Only a configured, exact KMS key ARN and its pinned RSA SPKI DER public key are
accepted. Key aliases, key URLs, alternate algorithms and caller key discovery
are unavailable. Keys must be RSA 2048/3072/4096 with exponent 65537. The fixed
algorithm is `RSASSA_PSS_SHA_256`, MGF1 SHA-256 and a 32-byte salt.

KMS Sign must receive `MessageType=DIGEST` and SHA-256 of the **exact payload
bytes**. Do not hash that digest again. Standard Go RSA, Node crypto and Python
cryptography verify the corresponding payload signature locally. Argument
bytes travel separately: `argumentsSHA256` binds their exact UTF-8 bytes, not a
parsed/re-serialized approximation. Typed executor DTO/schema validation is an
additional required boundary; this verifier does not validate argument semantics.

| Bound | Value |
|---|---|
| Entire delegation wire | 8,192 bytes |
| Decoded payload | 4,096 bytes |
| Exact argument bytes | 524,288 bytes |
| Delegation lifetime | At most 60 seconds |
| Issued-at clock allowance | At most 5 seconds ahead |
| Expiry | Strictly after now and at/before upstream token expiry |

The verifier also binds the expected destination, operation and request ID.
It resolves current client maximum/approval separately from the signed envelope,
then rechecks expiry after that lookup. Return only an immutable verified
principal. Lookup failures and invalid signatures have closed diagnostics without
provider causes or raw arguments. No OAuth token is part of this protocol.
Signers must derive upstream expiry and scopes from a verified dedicated-client
access token; accepting caller expiry/scopes would invalidate this boundary.

The planned private Lambda root frame has exactly `protocol`, `operation`, `requestId`,
`delegation` and `arguments`. Its protocol is `ttobak.connector.invoke.v1`.
API Gateway event bodies must not replace that event root. The API consumer must bind
literal method/path and bounded identifiers to the generated inventory before
running existing domain readers, with a result cap of 14,000 encoded bytes.
Private QA must additionally cap arguments at 32,000 bytes and reject duplicate
JSON fields; its question DTO may allow only `question` and optional `meetingId`.
No received OAuth bearer travels in this wire.

The planned initial elevated executor creates a bounded personal note.
It must reject file/account/share arguments, empty content and TTOBAK-origin import
markers, preserve normalized wikilinks and bind the verified subject's partition.
Current enabled write maximum, exact approval generation/expiry/capabilities,
canonical creation and audit receipt must share one transaction. A stable delegation
request ID must prevent a transport replay from creating another note; an unknown
outcome requires inspection before a fresh invocation. Other share/delete/queued
mutations remain unavailable until their own canonical publication is fenced.

`contracts/connector/v1/delegation-fixtures.json` contains synthetic local RSA-PSS
vectors and a public key, with no private key or live user data. Tests cover valid
read/write/share/delete, a read token attempting create, forged signatures,
wrong destinations/body/request, unknown operations/fields, duplicate fields,
upstream expiry, wrong PSS salt, current approval revocation and lookup deadlines.
These tests establish local interoperability, not live KMS or provider acceptance.

The isolated Python verifier checks use exact `cryptography==50.0.2`. The QA Lambda
is Python 3.12 ARM64. Staged GatewayStack now attaches a layer built from pinned
versions and hashes for cryptography, cffi and pycparser; a source requirements
entry alone does not prove the native artifact. Before activation, verify the
deployed layer's imports and RSA-PSS execution. Preserve fail-closed behavior during
producer-first rollout, including every alias that can receive traffic.
No public or unauthenticated probe route is authorized for this purpose.

The KMS Sign reference was rechecked on 2026-10-21. Other primary references
were retained from the archived design and are not deployment evidence:

- [KMS Sign: DIGEST inputs](https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html)
- [KMS RSA-PSS parameters](https://docs.aws.amazon.com/kms/latest/developerguide/asymmetric-key-specs.html)
- [Go RSA verification](https://pkg.go.dev/crypto/rsa)
- [Node crypto verification](https://nodejs.org/api/crypto.html)
- [Cryptography package metadata](https://pypi.org/project/cryptography/)
