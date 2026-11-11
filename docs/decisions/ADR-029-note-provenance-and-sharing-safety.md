# ADR-029: Note provenance and sharing safety

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

Generated notes carry a provenance state that decides whether they may be shared. The sections below keep, in order, the summary sharing source provenance record, the owner-approved account sharing record and the shareable-by-default provenance record. Where a later section changed an earlier rule, the earlier section has been corrected to say so.

## Summary sharing and source provenance

### Status notes

Status: Accepted (implementation; deployment acceptance remains separate)
Date: 2026-10-13

Partially superseded by the owner approval section:
explicit owner Account disclosure may share current output without regeneration.
Source provenance and direct-email/pending grant policy remain unchanged.

Partially superseded by the shareable-by-default section:
linked predecessors are no longer note evidence, and human content/liveSummary
edits keep `safe` provenance. Sticky `private`/`unknown` and the owner reset remain.

Amended 2026-11-06 (see the amendment below): account-team publication now
shares current derived content with every member; protected membership views
no longer withhold it.

### Context

Owner-private summaries can consume linked predecessor meetings. The resulting
notes and derivative tasks/insights can retain information that a later recipient
or newly added account member cannot read. Checking collaborator rows only before
generation leaves races, canonical account publication without cache rows, and
later sharing unprotected. Historical summary rows have no reliable provenance.

### Decision

Store summary privacy as a versioned generation identity, exact content hash,
sticky safe/private/unknown state .
Capture canonical parent authorization revision, publication and link state before
reading linked sources. Publish only while the captured parent observations and source snapshot still match. Linked predecessor meetings are navigation only and are never generation evidence or publication inputs.

Every activated new direct, pending/materialized or account-cache grant fences
the canonical parent and advances its authorization revision atomically with the
grant. Account classification/publication also pins current owner membership.
Private and unknown generated content require explicit owner regeneration before a new direct or pending grant. Owner Account disclosure follows the owner approval section. Existing independent direct grants retain their original scope.
Ordinary saved-source regeneration and content/live-draft edits preserve sticky
private/unknown ancestry. Owner and editor content and live-draft edits keep safe provenance under a new generation identity, keep private and unknown sticky, and clear any Account approval. Saved notes remain independent user input.

An explicit owner sharing-safe resummary uses selected saved transcript, saved
notes and verified current DOCUMENT extraction, excluding prior generated content,
predecessors and old image/preview summaries. Persist the mode through asynchronous
delivery. Success atomically publishes safe provenance and clears generated
tasks/insights/tags/sentiment/live draft and action-analysis/simulator state.
Source/edit-grant compare-and-swap (CAS) protects concurrent human edits; failure
preserves old content and provenance. The operation does not rerun speech-to-text
(STT).

Task continuity retains at most 500 hashed task identities with IDs/completion,
without old prose, assignee or deadline evidence. Action publication pins run,
source generation/content and prior tasks. Other model derivatives publish
against their captured source snapshot. Insight copies also bind current canonical
publication, insight bytes, generation and insight revision. Simulator claims pin
generation; reset removes the singleton so existing Go/Python run-ID conditions
reject late writes. Activated batch summarizers stop fixed-object knowledge base
(KB) exports; the canonical index worker owns immutable revision/source-checked
publication.

### Membership and readers

Server-mint privacy markers and AddedAt for genuinely new, materialized and
re-added account memberships. Role updates preserve both. Protected membership views are dormant: the membership guard returns false for every member, so account readers see current derived output (2026-11-06 amendment). Derived reads never report withheld output as successful empty evidence.
Current-source question answering (QA), indexed retrieval and tool history apply
the same visible scope and invalidate earlier private paraphrases when
dependencies change.

The legacy membership cutoff no longer changes account read behavior: no live path consults it. Missing/invalid times or activated-policy
configuration cannot establish eligibility. Owner and existing independent
direct-grant access remain separate.

### Activation and limits

Install every reader before enabling marker/provenance/grant writers.
`SUMMARY_PRIVACY_WRITER_ENABLED=1` enables writers; other values leave installation
mode. Membership stamps are still written where implemented, but no read path currently enforces them.
`SUMMARY_PRIVACY_READER_ENABLED` and `SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF` are still configured but no live read path consults them; keep the cutoff value fixed in deployment records. Never move the cutoff forward to grandfather
new unmarked grants. Source code and successful builds do not prove these gates.

Safe provenance records absence of inherited private predecessor/model evidence.
It does not classify sensitive information a user placed in own notes/transcript
or verified documents. Historical disclosure and existing grants are unchanged;
no migration, backfill or revocation is implied. Legacy whole-item meeting writes
are rejected after writer activation because they lack an observed source fence.

### 2026-11-06 amendment: published meetings show their summary to members

Product decision: a meeting published to an account team shows its current
summary, action items, insights, tags and live summary to every member who can
read it, including inherited (descendant-account) readers, exactly as the owner
sees them. The Go `accountMemberNeedsSummaryGuard` and Python
`enforce_summary_guard` predicates therefore return false for every member, so
protected membership views and `SHARING_REGEN_REQUIRED` restrictions no longer
apply to account readers.

Unchanged: only meetings currently published to the account are readable
(link-only classification grants nothing); live membership, publication and
insight generation/revision freshness checks still apply; direct-email and
pending grants keep the safe-source rule (`canCreateGrant`); provenance is
still recorded. Accepted consequence: a summary generated from the owner's
private linked meetings or documents becomes visible to the team when the
meeting is published. Owners must not publish meetings whose summary should
stay private.

### Evidence

Current contracts: [API](../API-SPEC.md), [UI](../DESIGN-SPEC.md),
[architecture](../architecture.md), [QA source contract](../../backend/python/qa/SOURCE_CONTRACT.md).
Implementation: `summary_privacy.go`, `summary_publish.go`, `resummary.go`,
`summary_derivatives.go`, `summary_insights.go` and their production callers.
Tests cover grant/publication races, predecessor mutation, same-content generation
resets, canonical membership replacement, explicit mode persistence, atomic reset,
failure preservation, task continuity and current-source/history restriction.
The [implementation plan](../superpowers/plans/2026-10-12-summary-sharing-privacy.md)
tracks remaining deployment acceptance separately.

## Owner-approved Account sharing of current meeting output

### Status notes

- Status: Accepted (implementation; deployment acceptance remains separate).
- Date: 2026-11-04.
- Supersedes the source provenance section's mandatory regeneration before an
  owner Account publication of private or unknown generated output. Its
  provenance, direct-email and pending grant, and conditional source
  publication rules remain in force.
- The 2026-11-06 amendment to the source provenance section removes protected
  membership views: published meetings show current output to all members, so
  approval no longer gates reads.

### Context

A newly recorded meeting can acquire unknown provenance through a live-summary
write. Requiring another generation before Account sharing replaces the output
the owner has just reviewed and can clear Insights, tasks and other analyses.
The user explicitly requested sharing the completed meeting flow and output as-is.

### Decision

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

Account readers validate canonical publication, owner, audience,
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

### Activation

`SummaryOwnerAccountShareEnabled` defaults to `0` and targets only the API as
`SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED`. Runtime requires both that value and
`SUMMARY_PRIVACY_WRITER_ENABLED` to equal `1`; CloudFormation rejects approval
activation without the privacy writer. The API immutable version description
binds the new parameter. Deploy and verify Go/Python readers with approval disabled
before enabling the new writer; preserve existing flags, fixed cutoff, model and
origin parameters. No historical backfill or automatic sharing is implied.

### Evidence and limits

Model/service and SDK transport tests cover existing output preservation, owner
and audience scope, source/membership fences, stale legacy Insight projections,
generation changes and rollback invalidation. QA tests cover metadata/full reads,
inherited reads, history/source scope and valid large reads. Code and tests do not
prove deployed artifacts or authenticated cross-user browser acceptance.

See [API](../API-SPEC.md), [UI](../DESIGN-SPEC.md),
[rollout](../runbooks/summary-privacy-release.md) and the
[historical plan](../superpowers/plans/2026-11-04-owner-approved-account-sharing.md).

## Shareable-by-default note provenance

### Status notes

- Status: Accepted; implementation in `backend/internal/repository/summary_privacy.go`
  and `backend/internal/service/`.
- Decision date: 2026-11-10. Human edits keep `safe` was decided by the product
  owner; the predecessor, image-evidence, first-draft and owner-only-reset rules
  were design choices approved with the spec.
- Design: [Shareable bilingual notes](../superpowers/specs/2026-11-10-shareable-bilingual-notes-design.md).
- Related: The source provenance section (partially superseded here), the owner approval section.

### Context

The source provenance section let owner-private summaries consume linked predecessor meetings and made
any human edit of a safe summary fall back to `unknown`. Notes therefore became
"private" through ordinary use and needed an explicit sharing-safe regeneration
before an account could receive them.

### Decision

- **No predecessor evidence.** Linked meetings are navigation only. No batch,
  retry or saved-source generation reads or folds predecessor content.
- **Human edits keep `safe`.** Owner/editor `content` or `liveSummary` writes keep
  a `safe` state. `private` and `unknown` stay sticky; a legacy row with no
  provenance but existing derivatives stays `unknown`
  (`editedSummaryPrivacyState`).
- **First live draft is safe.** A row with no provenance and no generated
  derivatives has nothing that could have consumed private sources, so its first
  write stamps `safe`.
- **Own image analyses are safe evidence.** Sharing-safe preparation keeps this
  meeting's own image analyses. Verified at revision `d53c1471`:
  `BedrockService.AnalyzeImage` reads only the S3 image object, and
  `invokeImageModel` sends only fixed prompts plus that image.
  `ProcessImageAttachment` and `backend/cmd/process-image` additionally read only
  the attachment row. No other meeting content enters the prompt.
- **Language.** Final notes always use the Korean directive. `noteLanguage` stays
  stored and accepted but no longer affects generation.
- **English note follows the same evidence rule.** The English note has separate
  run state (`ANALYSIS#summaryEn`) and `contentEn` fields, is built from the same
  safe evidence (selected transcript, saved notes, verified documents, this
  meeting's image analyses) and is never derived from the Korean note.
- **Reset stays owner-only.** `{sharingSafe:true}` remains the derivative-clearing reset (`RequestSharingSafe`), owner-only, and returns invalid input unless `SUMMARY_PRIVACY_WRITER_ENABLED=1`.

### Consequences

- Ordinary edits no longer force regeneration before sharing.
- Existing `unknown`/`private` rows are not declassified by edits; the owner reset
  is the only way back to `safe`.
- Predecessor identity/hash tracking from the source provenance section no longer applies to new runs.

### Supersession

Partially supersedes the source provenance section: predecessor evidence and the edit-to-`unknown`
rule. Account publication, grant fencing and sticky `private`/`unknown` policy
remain as recorded there.
