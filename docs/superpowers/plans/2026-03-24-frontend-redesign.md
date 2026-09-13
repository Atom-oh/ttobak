# Historical implementation record: Frontend redesign

- Original plan date: 2026-03-24.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

The plan sought a consistent meeting-reading and recording experience based on `design_sample/meeting-note-pc.html`. It proposed shared theme tokens and animations, extracted meeting/recording/Q&A components, a floating audio player, and attachment-gallery polish.

The summary/action-item grid was inside the main content column; the existing desktop Q&A panel remained separate. Transcript content stayed full-width below it, with a single-column mobile layout. Shared Q&A message, suggestion, and empty-state components were intended to remove duplicate rendering and make detected questions and answer progress more visible.

Recording-page decomposition kept orchestration in the page while extracting configuration and post-recording feedback. The plan contradicted itself on a target below 300 lines versus a realistic 400-500; those estimates were readability goals, not architectural limits.

## Risks and intended validation

Intended checks were frontend lint/static build and desktop/mobile, dark-mode, player, and Q&A inspection. Audio URLs needed backend authorization, not construction from a guessed S3 bucket. Visual extraction was not meant to change recording/upload state behavior.

All task boxes were unchecked. The document recorded expected build results, not an executed validation report.

## Current references

- [Meeting detail](../../../frontend/src/app/meeting/[id]/MeetingDetailClient.tsx), [audio player](../../../frontend/src/components/AudioPlayer.tsx), [recording page](../../../frontend/src/app/record/page.tsx), [Q&A message](../../../frontend/src/components/qa/QAChatMessage.tsx).
- [ADR-027](../../decisions/ADR-027-cloudfront-signed-media-urls.md) defines current signed media delivery; [ADR-024](../../decisions/ADR-024-mac-app-native-streaming-upload-and-system-audio-captions.md) and [ADR-030](../../decisions/ADR-030-mobile-live-captions-never-sacrifice-recording.md) supersede early recording assumptions.
