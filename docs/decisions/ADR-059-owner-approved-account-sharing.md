# ADR-059: Owner-approved Account sharing of current meeting output

Status: Proposed (Python reader foundation implemented; companion implementation and deployment acceptance pending)
Date: 2026-11-04
Supersedes: ADR-049's mandatory regeneration before an owner Account publication
of private/unknown generated output. Its provenance, direct-email/pending grants,
protected membership and conditional source publication rules remain in force.

## Implementation scope

The reader foundation PR delivers only Python QA approval validation, source
fingerprints and Insight availability handling. The Go writer/readers, API status,
frontend behavior, CDK prerequisite and ADR-049 supersession are companion
implementation in PR #573. The decision below specifies the complete intended
contract; it does not claim those companion changes exist in this reader revision.
ADR-049 remains the active writer policy until the companion is merged and deployed.

## Context

A newly recorded meeting can acquire unknown provenance through a live-summary
write. Requiring another generation before Account sharing replaces the output
the owner has just reviewed and can clear Insights, tasks and other analyses.
The user explicitly requested sharing the completed meeting flow and output as-is.

## Decision

The verified meeting owner's Account sharing action can approve disclosure of
the current canonical output to one Account. A separate server-managed
`summaryAccountApproval` stores version 1, owner, Account, exact content hash,
current summary generation and UTC approval time. It never changes
safe/private/unknown source provenance or grants access to predecessor meetings.

Mint approval and canonical publication in the same transaction, pinning the
current output, source/privacy fields, authorization revision and direct owner
membership identity. Preserve current notes, live draft, tasks/completion,
Insights and analysis state. Existing summary generation identity, including an
empty legacy identity, remains unchanged. If the canonical Insight revision is
absent, mint one so old unidentified projection rows stay hidden until fresh
canonical copies are published. Every copied Insight must match both current
generation and revision; fanout failure surfaces without exposing stale copies.

Protected Account readers validate canonical publication, owner, audience,
generation, approval time and strict content hash. Full reads additionally verify
the exact UTF-8 content bytes. Metadata reads trust the server stamp and existing
conditional invalidation; cached/indexed evidence never grants access. Include
approval in hidden QA scope/source/history fingerprints. Inherited Account read
grants use this same scope and remain read-only.

Ordinary content/live-summary writes, final summary publication, legacy
whole-item replacement and Account classification/unpublication clear approval,
even with provenance writers disabled. A new explicit owner sharing action can
approve the new completed result. Source-only sharing-safe regeneration remains
an optional output-replacement operation. Direct-email/pending grants retain
their separate safe-source requirement.

## Activation

`SummaryOwnerAccountShareEnabled` defaults to `0` and targets only the API as
`SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED`. Runtime requires both that value and
`SUMMARY_PRIVACY_WRITER_ENABLED` to equal `1`; CloudFormation rejects approval
activation without the privacy writer. The API immutable version description
binds the new parameter. Deploy and verify Go/Python readers with approval disabled
before enabling the new writer; preserve existing flags, fixed cutoff, model and
origin parameters. No historical backfill or automatic sharing is implied.

## Evidence and limits

Model/service and SDK transport tests cover existing output preservation, owner
and audience scope, source/membership fences, stale legacy Insight projections,
generation changes and rollback invalidation. QA tests cover metadata/full reads,
inherited reads, history/source scope and valid large reads. Code and tests do not
prove deployed artifacts or authenticated cross-user browser acceptance.

See [API](../API-SPEC.md), [UI](../DESIGN-SPEC.md),
[rollout](../runbooks/summary-privacy-release.md) and the
[historical plan](../superpowers/plans/2026-11-04-owner-approved-account-sharing.md).
