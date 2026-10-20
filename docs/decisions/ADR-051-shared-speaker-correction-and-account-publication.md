# ADR-051: Shared Speaker Correction and Account Publication

- Status: Accepted; implementation requires deployment.
- Decision date: 2026-10-20.
- Supersedes: ADR-016's separate meeting-detail link/share controls only.

## Context

Shared readers could open the speaker-name editor, but the API rejected their
save. Meeting detail also exposed separate private-link and team-share buttons,
making the intended team collaboration flow confusing.

## Decision

Allow every currently authorized meeting reader to correct known speaker names.
This narrow exception includes direct read grants and current members of the
published account team. It does not upgrade the share or permit arbitrary
content/notes edits, changing associations, or restarting diarization.

Recheck existing share/publication/membership authorization on each save. Accept
only known speaker labels, bound request/name/map sizes, preserve action-item
identity and completion, and retain conditional publication against the observed
meeting version. Concurrent edits return a conflict rather than being overwritten.

For read grants, only anonymous speaker labels may be replaced in prose. Custom
names can be corrected repeatedly in structured speaker fields, complete
bracket-only transcript attribution lines and exact action-item assignees, without
rewriting ordinary occurrences of those names. Previous mapping values retain
legacy speaker identity when segments are absent. Owner/editor prose replacement
remains available. Partial corrections retain other current speaker names;
compose against the original mapping values once, without accumulating historical
aliases. Action-item updates target JSON text/assignee fields and preserve other
metadata. When account access returns a restricted summary
view, omit content and action items from the write entirely: hidden view fields
are never canonical values. Omit unchanged derived fields too, so attribution-only
corrections do not invalidate a readable summary's safe provenance.

Replace meeting-detail private-link/share controls with a single clearly labeled
link-and-share action. Call `ShareMeetingToAccount` directly: it already links the
meeting, publishes account access and writes discovery/insight/share projections.
Ownership and target-account membership remain required. The API's private-link
operation, recording preparation and existing private meetings remain unchanged.

## Consequences

Shared readers can collaboratively fix names without receiving general editing
rights. Re-diarization controls are shown only to the owner. UI text makes the
scope of speaker changes and account publication explicit.

No migration retroactively publishes existing private links. Account publication
retains ADR-016's non-atomic projection/fan-out limits; failures surface and the
same action can retry. This decision does not claim deployment acceptance.

## Evidence

- `backend/internal/service/meeting.go` and `meeting_speakers.go`
- `backend/internal/service/meeting_speakers_test.go`
- `backend/internal/handler/meeting_speakers_test.go`
- `frontend/src/components/meeting/AccountSection.tsx`
- `frontend/src/components/meeting/SpeakerMapEditor.tsx`
- `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
