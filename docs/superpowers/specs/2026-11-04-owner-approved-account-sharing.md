# Owner-approved Account sharing

Status: Historical design; user-approved behavior, deployment acceptance pending.
Date: 2026-11-04
Reviewed base: `4c6de607`

## Requirement

The meeting owner can share the current completed meeting notes and Insights with
an Account team immediately. Sharing does not invoke a model, replace notes,
clear derivatives, or require a separate sharing-safe regeneration. The user
explicitly confirmed preserving the completed meeting flow and output.

The earlier proposed source-only first-summary default is superseded. It would
change evidence and output rather than share what the user has already completed.

## Decision

The existing owner-only Account share operation records a separate server-managed
disclosure approval for the canonical current output and target Account. Source
privacy remains safe/private/unknown; owner disclosure does not certify source
provenance. The approval stores version, owner, Account, current summary generation,
exact UTF-8 content hash and approval time. Keep existing generation identities,
notes, live draft, action IDs/completion, Insights and analysis state unchanged.

The approval and canonical Account publication use the same conditional write,
pinning the current meeting privacy/source/derived fields and canonical direct
owner membership. Only the verified meeting owner may invoke it. Inherited read
access does not confer Account assignment or publication permissions.

An approval permits protected Account readers only while the meeting is still
published to that Account and owner, summary generation and content match.
Metadata readers rely on the trusted server stamp; full reads additionally check
the current bytes. Ordinary content/live-summary writes and summary publication
remove approval, including when privacy writers are disabled. Sharing again can
approve the new completed result. Safe-source provenance continues to work.
Generation-fenced derivatives remain bound to the approved summary generation.
Legacy approval installs an Insight revision if one is absent, without changing
the summary generation or action state. Old unidentified Account Insight copies
remain inaccessible until current canonical copies are written successfully.

Direct-email and pending grants retain ADR-049's existing policy. Account share
approval does not authorize another audience. No member role, invitation,
hierarchy write permission, authentication, public route, model or indexing
activation changes.

## Rollout and UX

Install Go and Python readers before enabling the new API-only
`SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED=1` writer. The switch defaults to `0` and
requires the existing privacy writer to be enabled. Preserve existing activation
flags, cutoff, models and origin configuration.

Account sharing copy explains that the current notes and Insights are shared.
An owner-capable status distinguishes immediate Account sharing from optional
source-only regeneration. Restricted readers are directed to the owner to update
sharing rather than told regeneration is the only remedy.

## Acceptance

Use synthetic SDK transport tests for unknown/private completed notes with
existing derivatives. Approval/publication must preserve their exact bytes and
generation, advance authorization revision, fence canonical membership and reject
source or membership races. Forged server fields, nonowners, another Account,
changed content/generation and partial recording output must fail closed.

Exercise API status, protected direct/descendant Account reads, Insight rows,
metadata reads, full QA reads, cached/current-source authorization and writer
rollback invalidation. Run Go cmd-inclusive tests/vet/build, frontend checks,
Python QA tests, infra synth/tests and documentation checks. Required PR checks
must cover the exact reviewed head. Deployment requires content review >=85 and
fresh deployed-code/config receipts. Browser acceptance requires an actual
authenticated session; do not infer it from builds.

The empty recording Account picker is an independent bug with its own plan.
