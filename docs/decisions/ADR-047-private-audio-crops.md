# ADR-047: Cropped audio creates a separate private meeting

Status: Accepted; production activation is gated
Date: 2026-09-25

## Decision

An owner can select a range from a completed/error single-file meeting. Create a
new private meeting, retaining saved notes and participants but no attachments,
relations, shares, prior transcripts, human summary edits or derived analyses.
Never replace the source audio. The API bounds the request and resolves the source
key from the owner's canonical meeting; clients cannot supply storage keys.

The source revision check and conditional copy creation share a DynamoDB transaction.
A caller UUID and range identify one copy; retrying a failed event publication uses
the same identity. A private EventBridge event dispatches the production Whisper
worker, whose ECS client token deduplicates dispatch. The worker conditionally
claims the copy once, revalidates source ownership/key, and downloads with IfMatch
against the pinned S3 ETag. FFmpeg produces a mono 16 kHz WAV for the selected range;
only those bytes go to transcription. Publication requires the same worker run and
an existing unmodified destination. Cropped audio uploads cannot trigger ordinary
STT or replace/rediarize a cropped meeting in place.

## Limits and rollout

Source size is at most 2 GiB; selected duration at most six hours; end time at most
24 hours. FFmpeg has a 15-minute conversion timeout, limited local protocols and
container demuxers. Overrun or short output fails rather than silently shortening
the range. The original remains available on failure. There is no automatic Spot
worker restart; a failed/interrupted copy requires another request from the original.

GatewayStack defaults AUDIO_CROP_ENABLED to 0. The opt-in context
`ttobak:audioCropEnabled=true` must follow the worker-image rollout and actual
crop/transcription acceptance. Consumer Lambda, event rule and invocation permission
precede the API producer. This decision adds an authenticated API route through the
existing CloudFront origin; it adds no public compute origin or auth bypass.

Local Go, Python/FFmpeg, browser and CDK tests establish code behavior. Synthetic
media and a stub recognizer do not establish production GPU/model acceptance.
