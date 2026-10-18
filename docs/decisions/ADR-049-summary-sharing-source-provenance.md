# ADR-049: Summary sharing and source provenance

Status: Accepted (implementation; deployment acceptance remains separate)
Date: 2026-10-13

## Context

Owner-private summaries can consume linked predecessor meetings. The resulting
notes and derivative tasks/insights can retain information that a later recipient
or newly added account member cannot read. Checking collaborator rows only before
generation leaves races, canonical account publication without cache rows, and
later sharing unprotected. Historical summary rows have no reliable provenance.

## Decision

Store summary privacy as a versioned generation identity, exact content hash,
sticky safe/private/unknown state and consumed predecessor identity/hashes.
Capture canonical parent authorization revision, publication and link state before
reading linked sources. Publish only while those observations and every consumed
predecessor's owner/title/content/action bytes still match. Canonical publication
to an account excludes owner-private predecessor evidence even with no members.

Every activated new direct, pending/materialized or account-cache grant fences
the canonical parent and advances its authorization revision atomically with the
grant. Account classification/publication also pins current owner membership.
Private and unknown generated content require explicit owner regeneration before
a new grant. Existing independent direct grants retain their original scope.
Ordinary saved-source regeneration and content/live-draft edits preserve sticky
private/unknown ancestry. Content/live-draft edits invalidate a previous safe
clearance. Saved notes remain independent user input.

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

## Membership and readers

Server-mint privacy markers and AddedAt for genuinely new, materialized and
re-added account memberships. Role updates preserve both. Protected membership
views withhold historical private/unknown derived fields and linked IDs, retain
bounded raw parent-owned notes/transcript access, and expose explicit regeneration
status. Derived reads never report withheld output as successful empty evidence.
Current-source question answering (QA), indexed retrieval and tool history apply
the same visible scope and invalidate earlier private paraphrases when
dependencies change.

Existing unmarked membership eligibility requires valid AddedAt strictly before
an operator-fixed UTC cutoff. Missing/invalid times or activated-policy
configuration cannot establish eligibility. Owner and existing independent
direct-grant access remain separate.

## Activation and limits

Install every reader before enabling marker/provenance/grant writers.
`SUMMARY_PRIVACY_WRITER_ENABLED=1` enables writers; other values leave installation
mode. Stamped memberships remain protected even in reader installation mode.
Activate unmarked-member enforcement with `SUMMARY_PRIVACY_READER_ENABLED=1` and
fixed `SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF` only after verified reader deployment
and old API/worker/queued-work drain. Never move the cutoff forward to grandfather
new unmarked grants. Source code and successful builds do not prove these gates.

Safe provenance records absence of inherited private predecessor/model evidence.
It does not classify sensitive information a user placed in own notes/transcript
or verified documents. Historical disclosure and existing grants are unchanged;
no migration, backfill or revocation is implied. Legacy whole-item meeting writes
are rejected after writer activation because they lack an observed source fence.

## Evidence

Current contracts: [API](../API-SPEC.md), [UI](../DESIGN-SPEC.md),
[architecture](../architecture.md), [QA source contract](../../backend/python/qa/SOURCE_CONTRACT.md).
Implementation: `summary_privacy.go`, `summary_publish.go`, `resummary.go`,
`summary_derivatives.go`, `summary_insights.go` and their production callers.
Tests cover grant/publication races, predecessor mutation, same-content generation
resets, canonical membership replacement, explicit mode persistence, atomic reset,
failure preservation, task continuity and current-source/history restriction.
The [implementation plan](../superpowers/plans/2026-10-12-summary-sharing-privacy.md)
tracks remaining deployment acceptance separately.
