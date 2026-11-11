# Decision records

Decisions are kept as at most 30 topic records, numbered ADR-001 to ADR-030.
Each record states the rules in force today and is rewritten in place when a
decision changes; version control keeps the earlier text.

- A new decision goes into the record that owns its topic. Add a record only
  for a topic no existing record covers, using an unused number up to 030.
- Do not append dated amendments or per-PR notes; edit the affected rules.
- When code and a record disagree, fix the record or the code in the same
  change and say which one was wrong.

## Consolidation in progress (2026-11-11)

The earlier one-record-per-decision set (ADR-001 to ADR-073) is being merged
into topic records. A record rewritten in place keeps its number and is
listed below with its new scope; other numbers still name their original
record. A retired number is reused only after every reference to it has been
rewritten.

| Record | Change |
|---|---|
| [ADR-005](ADR-005-speech-languages-and-live-translation.md) | Rewritten as speech languages and live translation; was Korean and English detection in Transcribe |
| [ADR-008](ADR-008-custom-dictionary-for-stt-accuracy.md) | Rewritten in place as the dictionary record; was per-user dictionaries |
| [ADR-002](ADR-002-delivery-pipeline-and-pr-review.md) | Delivery pipeline and AI PR review; absorbs the former ADR-043 (specialist review protocol) |
| [ADR-009](ADR-009-batch-stt-and-diarization.md) | Batch STT and diarization; absorbs the former ADR-019 (acoustic diarization) and ADR-035 (pyannote 4 and ASR pins) |
| [ADR-011](ADR-011-interactive-deep-research.md) | Rewritten in place |
| [ADR-014](ADR-014-meeting-audio-files-and-lookup.md) | Meeting audio files and lookup; absorbs the former ADR-012 (GSI3 lookup), ADR-047 (private audio crops) and ADR-062 (seekable playback copies) |
| [ADR-007](ADR-007-sign-up-identity-and-admin.md) | Sign-up, identity attributes and user administration; absorbs the former ADR-032 (admin panel) and ADR-057 (client write attributes) |
| [ADR-006](ADR-006-browser-and-mobile-recording.md) | Browser and mobile recording; absorbs the former ADR-001 (system audio capture), ADR-030 (mobile live captions) and ADR-048 (browser recording backups) |
| [ADR-024](ADR-024-mac-native-capture-and-upload.md) | Mac native capture and upload; absorbs the Mac parts of the former ADR-006 and the former ADR-046 (Core Audio tap and microphone) |
| [ADR-013](ADR-013-summary-generation-and-publication.md) | Summary generation, recovery and publication; absorbs the former ADR-031 (summarize timeout resilience and STT recovery), ADR-037 (immutable spills) and ADR-040 (guarded summary publication) |
| [ADR-016](ADR-016-meeting-account-sharing.md) | Meeting-account linking, publication and team management; absorbs the former ADR-023 (share-origin provenance) and ADR-051 (shared speaker correction and account publication) |
