# Real note quality evaluation

Evaluate the actual `SummarizeTranscript` prompt and postprocessing path against
reviewed synthetic meetings. Keep DynamoDB and S3 synthetic; invoke only Bedrock
in live mode. Record model/region, request and fixture hashes, raw responses,
rendered notes and criterion-level results. Request export is not an evaluation.

- [ ] Add reference cases for selected sources, numbers/units, negation,
  decisions versus proposals, owners/deadlines and notes-only evidence.
- [ ] Verify the rubric accepts reviewed examples and rejects deliberate errors.
- [ ] Add an executable live/export/grade command without customer-data access.
- [ ] Add a main-only manual CI workflow using the deployed model configuration.
- [ ] Run real model evaluation, inspect all outputs and record results honestly.
- [ ] Fix observed prompt failures without weakening the reference criteria.
- [ ] Complete latest-head PR review, merge and deployment verification.

Default unit tests must not invoke AWS. Missing credentials, missing responses
and incomplete model output are failures, not passing quality evidence.
The small synthetic corpus is a regression check, not a general accuracy estimate.
