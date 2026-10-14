# Prepared HTTP origin release coordinator

Run from a clean checkout matching `GITHUB_SHA` when that variable is set.
The coordinator deploys the exact cloud assembly accepted by its diff and
activation checks, verifies its hash before each deployment, and fails if the
checkout or assembly changes. The Storage-only `retighten` action applies the
same account, revision and assembly checks without rotating origin proofs.

A complete receipt with `recoveredExpiredCutover: true` records restoration after
an expired overlap had already interrupted availability: the run started past the
deadline with CloudFront or any consumer outside the rotating version. Its final
checks establish the recovered state; they do not assert uninterrupted
availability. Incomplete receipts never set it.

The activation gate also requires every consumer function to depend on a
GatewayStack policy granting `secretsmanager:GetSecretValue` on
`HttpOriginSecretArn` to that function's role, and to stay outside a VPC. One
GatewayStack deployment switches all consumers to ARN lookup, so a missing grant
or unreachable endpoint would otherwise fail every origin check closed.

`http_origin_release.py` prepares a coordinated migration of the existing HTTP
origin proof. The tool can be reviewed before the paired infrastructure and
workflow activation. It rejects templates without the required Amazon Resource
Name (ARN)/version
contract before creating or changing a secret or environment. Shipping this
tool alone does not rotate a deployed proof or close the disclosure finding.

The compatible reader release must actually be installed. The coordinator checks
its verified-response protocol marker on existing content-free paths, rejects
missing proofs, captures function code/configuration identities and rechecks
them before mutation. Source ancestry alone is insufficient.

| Phase | Expiry and recovery |
|---|---|
| Before seeded bootstrap | Failed readiness/agreement changes no proof or environment |
| Seeded | The original proof remains current; re-inspect before resuming |
| Rotating before its deadline | Resume the original generation; never extend the previous-proof deadline |
| Less than one hour remains, mixed consumers and old producer | Stop before mutation; insufficient operational headroom for a new consumer transition |
| Expired rotation, mixed consumers and old producer | Stop for operator recovery; automatic continuation could create an outage |
| Expired rotation with all consumers pinned to the dual version, or the producer already switched | Roll forward in dependency order; do not conceal existing availability loss |
| Active | Recheck immutable identities and retired-proof rejection without rotating again |

The planned activation uses this order:

1. Verify live proof agreement across consumers and CloudFront.
2. Validate the synthesized activation contract and bootstrap the same proof.
3. Deploy consumers using an immutable seeded version, preserving current traffic.
4. Publish a bounded dual version, deploy and verify consumers using that version,
   and drain caches for 65 seconds before changing the producer.
5. Verify deployed CloudFront, publish the active version, deploy pinned consumers,
   drain again and check rejection of the retired proof.

The coordinator preserves explicit, exclusive stack deployment order and existing
parameters. It never deploys KnowledgeStack. AWS Cloud Development Kit (CDK)
output is discarded because an
old-template deletion diff can contain the disclosed value. Diagnostics and
receipts use fixed categories and nonsecret identities, not proof values,
complete environments or arbitrary provider messages.

Interrupted seeded or dual states are re-inspected. Lost promotion responses
are reconciled by immutable version identity. Deadlines are never silently
extended. Expired mixed consumer/producer states stop for operator review when
automatic continuation could introduce an outage. Existing availability loss is
not represented as success. Function revision checks are not a distributed lock
against another administrator; use exclusive operational control.
The one-hour minimum is checked again before mutations. It is operational
headroom, not a guarantee that CloudFormation will finish within that hour.

The original immutable version keeps the `TTOBAK_HTTP_RETIRED` staging label.
This prevents its automatic deprecation after later `AWSCURRENT` moves and keeps
future retired-proof rejection probes possible. The label never grants acceptance.

The default inspection action performs no activation mutation:

```bash
python3 scripts/operations/http_origin_release.py check
```

`release` is for the reviewed normal workflow after paired activation is present.
`retighten` preserves the existing StorageStack OAC repair and performs no HTTP
proof rotation. The operator environment needs `boto3<2`; tests use synthetic
clients and make no AWS or model calls.

Tests cover proof disagreement, missing reader readiness, configuration changes,
consumer secret read grants, old-cache cutover, interrupted/resumed rotation, lost
responses, expired windows and their recovery receipts,
idempotent completion and value-free CDK diagnostics. A live completed receipt is
still required to establish the deployed outcome.
