# Real note quality evaluation

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

Evaluate the actual `SummarizeTranscript` prompt and postprocessing path against
reviewed synthetic meetings. Keep DynamoDB and S3 synthetic; invoke only Bedrock
in live mode. Record model/region, request and fixture hashes, raw responses,
rendered notes and criterion-level results. Request export is not an evaluation.

- Recorded at the time: Add reference cases for selected sources, numbers/units, negation,
  decisions versus proposals, owners/deadlines and notes-only evidence.
- Recorded at the time: Verify the rubric accepts reviewed examples and rejects deliberate errors.
- Recorded at the time: Add an executable live/export/grade command without customer-data access.
- Recorded at the time: Add a main-only manual CI workflow using the deployed model configuration.
- Not verified by this record: Run real model evaluation, inspect all outputs and record results honestly.
- Not verified by this record: Fix observed prompt failures without weakening the reference criteria.
- Not verified by this record: Complete latest-head PR review, merge and deployment verification.

Default unit tests must not invoke AWS. Missing credentials, missing responses
and incomplete model output are failures, not passing quality evidence.
The small synthetic corpus is a regression check, not a general accuracy estimate.
