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
into topic records. Until it finishes, numbers not listed below still name
their original record. A retired number is reused only after every reference
to it has been rewritten.

| Former record | Now in |
|---|---|
| ADR-008 Per-user dictionaries for speech recognition | [ADR-005](ADR-005-live-stt-translation-and-dictionary.md) |
| ADR-043 Specialist review protocol | [ADR-002](ADR-002-delivery-pipeline-and-pr-review.md) |
