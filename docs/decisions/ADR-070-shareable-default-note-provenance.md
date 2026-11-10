# ADR-070: Shareable-by-default note provenance

- Status: Accepted; implementation in `backend/internal/repository/summary_privacy.go`
  and `backend/internal/service/` (PR 1 of the shareable notes work).
- Decision date: 2026-11-10. Human edits keep `safe` was decided by the product
  owner; the predecessor, image-evidence, first-draft and owner-only-reset rules
  were design choices approved with the spec.
- Design: [Shareable bilingual notes](../superpowers/specs/2026-11-10-shareable-bilingual-notes-design.md).
- Related: ADR-049 (partially superseded here), ADR-059.

## Context

ADR-049 let owner-private summaries consume linked predecessor meetings and made
any human edit of a safe summary fall back to `unknown`. Notes therefore became
"private" through ordinary use and needed an explicit sharing-safe regeneration
before an account could receive them.

## Decision

- **No predecessor evidence.** Linked meetings are navigation only. No batch,
  retry or saved-source generation reads or folds predecessor content.
- **Human edits keep `safe`.** Owner/editor `content` or `liveSummary` writes keep
  a `safe` state. `private` and `unknown` stay sticky; a legacy row with no
  provenance but existing derivatives stays `unknown`
  (`editedSummaryPrivacyState`).
- **First live draft is safe.** A row with no provenance and no generated
  derivatives has nothing that could have consumed private sources, so its first
  write stamps `safe`.
- **Own image analyses are safe evidence.** Sharing-safe preparation keeps the
  owner's own image analyses. Verified at revision `d53c1471`:
  `BedrockService.AnalyzeImage` reads only the S3 image object, and
  `invokeImageModel` sends only fixed prompts plus that image.
  `ProcessImageAttachment` and `backend/cmd/process-image` additionally read only
  the attachment row. No other meeting content enters the prompt.
- **Language.** Final notes always use the Korean directive. `noteLanguage` stays
  stored and accepted but no longer affects generation.
- **Reset stays owner-only.** `{sharingSafe:true}` remains the derivative-clearing
  reset (`RequestSharingSafe`), owner-only and unchanged.

## Consequences

- Ordinary edits no longer force regeneration before sharing.
- Existing `unknown`/`private` rows are not declassified by edits; the owner reset
  is the only way back to `safe`.
- Predecessor identity/hash tracking from ADR-049 no longer applies to new runs.

## Supersession

Partially supersedes ADR-049: predecessor evidence and the edit-to-`unknown`
rule. Account publication, grant fencing and sticky `private`/`unknown` policy
remain as recorded there.
