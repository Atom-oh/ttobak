# HTTP origin proof migration and rotation

This runbook describes the release coordinator and its remaining runtime acceptance.
Source checks alone do not prove a deployed rotation. See
[ADR-050](../decisions/ADR-050-http-origin-proof-rotation.md).

## Activation prerequisite

The compatible reader release must actually be deployed before activation. Its
verified HTTP responses carry `X-Ttobak-Origin-Protocol: 1`; the marker alone does
not establish Secrets Manager permission or successful version binding.

The coordinator reads the currently deployed API alias, latest API, QA and enabled
HTTP MCP configurations, their code hashes/revisions, the API routes/integrations
and CloudFront's HTTP origin. It uses existing content-free probes: a nonexistent
public-document token returns 404, an unknown QA GET returns 405 before user/model
handling, and existing MCP discovery returns 200. Every successful probe requires
the marker; a missing proof must be rejected without it. No JWT is invented, no
meeting is read, and no model is invoked. Function identity, code hash, configuration
revision and alias version are rechecked immediately before mutations. Weighted
aliases and unstable stacks are rejected.

Initial proofs must agree across the live consumers and CloudFront. The old CDK
literal is never used as evidence of deployment. The new proof document requires
16–128 visible ASCII characters without commas; a legacy value outside that
contract stops activation before any mutation and needs a separately reviewed
migration. The compatible reader release preserves such legacy values meanwhile.

## Managed values and permissions

The release bootstraps `/ttobak/http/origin-verify` with the existing live proof.
It is an external Secrets Manager resource retained independently of stack removal;
the coordinator never deletes it. Gateway-owned policies grant only its exact
`GetSecretValue` ARN to the API, QA and HTTP MCP roles, avoiding a reverse dependency
on AiStack. Consumers receive only the ARN and an immutable version identifier:

- `ORIGIN_VERIFY_SECRET_ARN`
- `ORIGIN_VERIFY_SECRET_VERSION_ID`

The legacy environment/read path is removed. Missing ARN/version configuration
denies HTTP access; Go/QA local development still requires an explicit flag outside
Lambda. The existing internal QA WebSocket and SQS paths remain separate.

CloudFront resolves the version's `current` JSON field through a dynamic reference.
`HttpOriginSecretVersionId` is a required parameter in GatewayStack and FrontendStack.
Changing the Gateway version also changes the API version description, ensuring a
new immutable alias version is published for a parameter-only transition. The
CloudFront version parameter forces an actual header update; rotating a secret
alone does not update its resolved origin value.

Initial missing-secret bootstrap remains an explicitly authorized management-role
operation. The CI role does not receive `CreateSecret`: a missing secret stops its
release before it can create one. Bootstrap must preserve the inspected live proof
and complete the original coordinator's agreement/foundation guards.

`ttobak:httpOriginReleaseCiRoleName` names the existing `mall-apne2-mgmt-ci-runner`
role. GatewayStack owns one inline policy granting only `GetSecretValue`,
`PutSecretValue` and `UpdateSecretVersionStage` on the exact
`HttpOriginSecretArn` parameter. It creates no role or secret, changes no trust
policy, and grants no wildcard, List, Delete or CreateSecret action. Omit this
context or set JSON boolean `false` to disable the grant; invalid role names or
types stop synthesis. This grant is intended only for an existing
seeded/rotating/active secret. The coordinator's initial read uses its fixed
secret name; successful CI Get-by-name still requires the policy's actual
deployment and verification under the actual CI caller.

The coordinator reads with `get_secret_value`; version publication uses
`put_secret_value`, and expected-current promotion plus retirement labeling use
`update_secret_version_stage`. Its `create_secret` path is only the missing-secret
bootstrap. Inventory/deployment and named-consumer invocation permissions remain
separate existing prerequisites. Runtime roles receive no rotation permissions.
Permission failures still stop the release. This source grant does not establish
live permissions: independent content/control review, exact-head native/CI,
actual-main integration and root-only reviewed policy deployment remain required.

## Normal release order

The existing main-branch infrastructure workflow runs synth/tests before its deploy
job, builds all original artifacts, then invokes `http_origin_release.py release`.
The coordinator preserves explicit per-stack `--exclusively` deployment order and
excludes KnowledgeStack. Existing parameter values are preserved. The workflow's
always-run StorageStack OAC repair remains separate and does not rotate a proof.

The reviewed assembly digest pins the explicitly diffed stack set, all input file
bytes and Unix permission modes. Deployment must target a member of that reviewed
set with `--exclusively`; a Storage-only review cannot authorize Gateway deployment.
Only the assembly-root `.lock` is transient; `.lock` files inside staged assets
remain inputs. CDK can create `.cache/<asset-id>.zip` while publishing a declared
directory asset. Before and after every deployment, the coordinator validates each
such direct cache file against its manifest-declared, assembly-staged source:
exact member names, bytes, CRC, regular-file types and modes, including hidden files.
Validation streams bounded chunks without extracting files. Unknown or nested cache
paths, symlinks, malformed archives and changed inputs stop the release. There is
no directory-wide cache exemption.

The cache reader supports the pinned CLI's ZIP32/DEFLATE format and rejects unsupported
cache formats. Before deployment, every ZIP directory source in the reviewed stacks'
direct asset manifests must be staged inside the assembly, even without an existing
cache. The guard never traverses CloudFormation stack dependency closure. Storage-only
retighten therefore preserves its original scope; an uncached, unselected Gateway
source does not become a publication prerequisite. Every existing cache is still
validated against all declared manifests and staged sources, including unselected
assets. Unselected templates, manifests and staged input files remain in the digest.
A fresh full-release diff must stage its selected sources; do not disable the guard
or substitute a partial repair assembly for that review. A guard failure after a
successful stack deployment does not roll that deployment back. Re-inspect the actual
state and prepare a fresh source-bound diff before an authorized resume. Local cache
tests establish this guard's behavior, not completed migration or runtime acceptance.

1. Inspect live agreement, verify the installed reader marker and capture stable
   code/configuration identities. Run the stack diff before activation mutations.
2. Bootstrap the same proof if the secret does not exist. Deploy the existing stack
   sequence with consumers pinned to that immutable seeded version. Verify actual
   reads before binding CloudFront to the same proof/version.
3. Publish a dual-proof version using an expected-current-version stage transition.
   Deploy consumers pinned to it. Drain for at least 65 seconds: 60-second cache,
   three-second in-flight lookup and margin. Require actual new-proof acceptance
   before updating CloudFront.
4. Update the explicit CloudFront version and require the distribution to be
   deployed with the expected proof. Verify CloudFront success and direct-origin
   rejection on the existing public probe.
5. Publish an active version without the previous proof, deploy consumers pinned to
   it, drain again and verify current-proof success plus missing/retired-proof
   rejection. Record the checked function versions, code hashes and revisions.

All proof comparisons use fixed-time primitives. The version-1 JSON contract has
`schemaVersion=1`, `current`, `previous`, `rotationStartedAt`, `previousExpiresAt`,
`generation` (32 lowercase hex characters) and `phase` (`seeded`, `rotating`, `active`).
The previous value is accepted only within its explicit window, at most 24 hours.
`retiredVersionId` links the immutable pre-rotation value for rejection checks.
The `TTOBAK_HTTP_RETIRED` staging label preserves that version after it loses
`AWSPREVIOUS`; an unlabeled version can be deleted by Secrets Manager. The label
does not make a consumer accept the retired proof.
Documents are limited to 2,048 UTF-8 bytes. Each runtime cache lasts at most 60
seconds; lookups and diagnostics retain the bounded, value-free reader contract.

## Failure and resume behavior

| State | Behavior |
|---|---|
| Foundation missing, proof disagreement or changed consumer revision | No activation mutation; deploy/verify the reader prerequisite first |
| Seeded migration interrupted | The same original proof remains usable; re-inspect and resume |
| Dual version published, producer not switched | Both proofs remain accepted by transitioned consumers within the original window; resume without extending it |
| Less than one hour remains with mixed old consumers and an old producer | Stop before mutation with `INSUFFICIENT_ROTATION_WINDOW`; do not start another consumer transition |
| Lost stage-promotion response | Re-read the immutable version; never blindly repeat the mutation |
| Producer switched, cleanup interrupted | Current proof remains accepted; finish active-version deployment and rejection checks |
| Expired window with mixed old consumers and an old producer | Stop before further deployment with `EXPIRED_MIXED_CUTOVER_REQUIRES_OPERATOR`; never silently renew or restore an expired proof |
| Expired window with all dual consumers, or already-switched producer | Roll forward using the original state and normal dependency order; existing availability loss is not hidden |

Keep exclusive operational control during the release. Revision checks detect
changes but are not a distributed lock against another administrator. Never edit
secret stages, functions, aliases or origin headers concurrently. An interrupted
release must be investigated before its overlap deadline; bounded expiry cannot
guarantee availability after an indefinitely abandoned rollout.
The one-hour minimum is checked before mutations and provides operational
headroom; it does not guarantee CloudFormation completion within that hour.

## Operator checks and receipts

`check` performs inventory/agreement checks without mutation; it is not a readiness
or rotation receipt. The normal release performs the additional IAM-authorized
content-free probes and transitions:

```bash
python3 scripts/operations/http_origin_release.py check
```

A completed workflow writes `build/verification/http-origin-release.json` and a
redacted summary. Completion is emitted only after all acceptance checks. Incomplete
receipts name a fixed failure category and phase; proof values, headers, complete
environments and provider messages are never logged. CDK child output is discarded
because even a deletion diff can expose the retired plaintext. Inspect CloudFormation
events privately when a fixed deployment-failure category needs diagnosis.

Do not claim the finding closed from an ARN, a clean template or a unit test. Keep
the actual completed receipt tied to the deployed revision. Repository history and
old immutable Lambda/secret versions can retain the retired value; rejection of
that value on active ingress is the required closure evidence. This receipt covers
the named consumers and existing ingress probes, not a general compliance audit.
