# Retroactive Meeting Merge Design

> Historical design record. Original date: 2026-04-21. Original status: Approved;
> branch was TBD. Approval of this proposal did not establish implementation.

## Problem and chosen approach

Interrupted recordings or multiple devices could produce several completed meeting
records for one conversation. The proposal would combine those records while
retaining independent audio and transcripts. A unified summary/action list would
sit outside per-source tabs; audio concatenation was deliberately excluded.

## Proposed model and workflow

An `AudioSource` collection would own each source's audio key, A/B transcripts,
selection, segments, speaker map, provider, duration, label, and creation time.
The meeting would retain shared notes, participants, tags, attachments, and unified
outputs; `MergedFrom` would record provenance. Lazy migration would synthesize a
first source from legacy flat fields on read and retain transitional response fields.

The proposed merge endpoint required distinct, same-owner, completed meetings. It
would attach source data, re-key attachment records, union participants/tags/notes,
combine share grants using the more permissive permission, delete source records,
and asynchronously regenerate the summary and KB export. Large inputs would use
per-source summaries before a combined synthesis. Source tabs, a searchable picker,
and an explicit deletion confirmation supported desktop and mobile use.

## Tradeoffs and unresolved risks

Independent audio avoided codec conversion and preserved different device captures.
The cost was migration complexity, reconciliation of conflicting transcripts and
permissions, and destructive source deletion with no undo.

The original claims that batched transactions were globally atomic and that a
mid-merge failure caused only harmless duplication were not established. Multiple
transactions, S3/record changes, source deletion, and summary failure require a
reviewed recovery strategy. Automatically unioning shares can broaden access to
formerly separate content; same-owner checks alone do not justify that exposure.
These unresolved design risks were not accepted security-policy exceptions.

Cross-user merging, automatic candidate suggestions, audio editing, and undo were
excluded. Original error/confirmation examples described intended behavior, not
available API contracts.

## Current disposition

The current [Meeting model](../../../backend/internal/model/meeting.go) and
[API router](../../../backend/cmd/api/main.go) do not implement this `AudioSources`/
`MergedFrom` migration or merge endpoint. [ADR-014](../../decisions/ADR-014-multi-file-audio-and-linked-meetings.md)
instead covers multiple audio inputs within one meeting and separate follow-up
links. Neither feature proves retroactive record merging or authorizes deleting
source meetings or unioning their permissions.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [project security guidance](../../../CLAUDE.md).
