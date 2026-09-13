# Historical AI note and MCP audit

Historical audit dated 2026-09-11, baseline `f935290`. It examined source retention,
edit freshness, authorization and visible failures using synthetic data, not
customer-recording transcription accuracy or semantic model quality. Work was
split across PRs #188 (MCP), #189 (summary/source), #190 (QA), #191 (storage-read
guard first) and the subsequent retrieval-freshness change. Check current source
and deployment history before inferring a PR's rollout status.

## Findings and recorded remediation

| Baseline defect | Remediation intent / evidence |
|---|---|
| Selecting transcript B could still use A's speaker text | Validate segment text against the selected source before attaching evidence |
| Editing A left stale segments that overrode corrected text | Preserve other variants but only use matching selected-source segments |
| Saved user notes were absent from summary/QA input | Supply a distinct source; do not portray notes as spoken agreement |
| Truncated/empty model output could be saved as complete | Validate stop reason, text blocks and content before success writes |
| MCP personal note create/list/read/update paths were disconnected | Use authenticated personal document APIs and preserve IDs on update |
| MCP lagged account hierarchy/filter contracts | Expose parentAccountId/accountIds; require explicit accessible descendants |
| Error-shaped HTTP JSON could count as MCP success | Check HTTP success before returning tool results |
| UTF-8 split across network chunks could corrupt text | Decode streams incrementally; test byte-split responses |
| Failed transcript saves closed the editor or left stale display | Preserve draft/error on failure; update only after success |
| QA omitted inherited account-team access | Union grants and revalidate membership/canonical publication state |
| QA could not read S3-spilled transcripts | Deploy exact bucket/meeting/field guards before narrow transcripts/* read grant |
| Edited/revoked KB hits remained stale through cache | Use index results as candidates, then recheck current access and content |

Primary source: `backend/internal/service/{bedrock,meeting}.go`,
`backend/python/qa/`, `mcp-server/src/{api,index}.ts`, and
`frontend/src/components/meeting/TranscriptSection.tsx`.

## Remaining limits

- Current reads fix content for discovered KB candidates; they do not re-export or
  reindex every meeting edit. A newly added term may still fail candidate discovery.
  A future solution needs versioned export, retry/state visibility and ordering
  protection so an older save cannot replace a newer index version.
- Personal/account Document Hub Markdown is not automatically the QA KB. MCP reads
  current documents directly; do not describe that as automatic RAG indexing.
- Meeting file attachments still lack content extraction for summary grounding;
  this was known before the audit.
- Long-meeting and semantic evaluation needs reviewed fixtures: agreements versus
  proposals, negation, numeric corrections, uncertain owners/deadlines, multipart
  audio and conflicting user notes. Unit tests do not prove semantic accuracy.

Recorded validation used actual MCP stdio with stub HTTP, captured Go model/storage
boundaries, and QA access/pagination/transcript regression cases. Frontend validation
was lint/build, not a real browser or live-model quality test. The audit recorded
15 pre-existing full-lint errors at its baseline; that count is historical and
must not be presented as the current tree's result without rerunning lint.
