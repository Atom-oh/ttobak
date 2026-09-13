# Historical Transcribe language-mode benchmark

Historical run: 2026-04-22 23:52. Evaluator: Claude Sonnet 4.6. Three recordings
compared AWS Transcribe A (LanguageCode=ko-KR) with B
(IdentifyMultipleLanguages=true, ko-KR/en-US). These labels belong to this experiment,
not the product's current A/B transcript/source-selection contract.

| Recording | A characters | B characters | A rubric score | B rubric score |
|---|---|---|---|---|
| Hana Bank R&D network, 08417f6d-a7a4-410f-86fa-2d33de73902c | 33,705 | 33,705 | 5.0/10 | 5.0/10 |
| Hana financial technology research, 84c56bfd-b9f9-40cb-ba88-85d2eddb1fc6 | 37,445 | 37,441 | 5.8/10 | 6.1/10 |
| Mobile recording, 6efb247f-d422-4f7c-aa96-e6f875e55190 | 36,729 | 36,729 | 5.6/10 | 5.6/10 |

The first and third comparisons were effectively identical; the second showed a
small rubric preference for B. Shared weaknesses were fragmented sentences,
inconsistent technical names/acronyms, missing/unclear context and no usable speaker
labels in the evaluated text. Examples involved EKS, SageMaker, Red Hat, GPU,
on-premises and PoC terminology. The old prose inferred intended words from
context; those guesses are not independently verified reference transcripts.

The report proposed vocabulary support, acoustic speaker labels, better segmentation
and domain evaluation. Its suggested language-percentage thresholds and claims
about provider internals were hypotheses, not measured API guarantees. Scores were
subjective LLM assessments, not WER/CER/DER or a statistical quality comparison.

Current production batch selection is documented in ADR-009/035. For a new comparison,
use reviewed ground truth, identical audio and recorded configuration. Preserve
aggregate metrics rather than copying raw meeting transcripts into review context.
