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
