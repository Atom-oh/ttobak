# Solutions Architect (SA) meeting workflow

Status: local implementation under validation; not deployed. The user selected
preparation → recording → follow-up.

## Resulting behavior

- Save preparation as a private `prep` document; reopening it seeds the recording
  screen without inventing a long-lived recording/draft lifecycle.
- Keep selected customer context and preparation/reference notes through meeting
  creation and recording. Classification never implies team sharing.
- Surface stored meeting insights and account/project associations in detail.
  Legacy insights are labeled as previously extracted, not certified current.
- Browse authorized account insights/research/documents, prior meetings, personal
  KB files and crawled Insights from the same reference panel. Partial failures
  are visible; changing customer cannot show results from an older request.
- Add deliberate source-labeled excerpts or completed Q&A answers with safe links
  and caveats to human notes. These are references, not spoken evidence. Source
  selection prepares a question; it does not claim to restrict the QA tool scope.
- Edit saved notes with compare-and-set conflict handling; preserve drafts on
  errors. Unsaved notes participate in re-summary guards.
- Guarded note editing and private relinking require explicit server capability
  flags. Recording uses one serialized comparison writer for autosave and final
  notes; upload retries never resend an already acknowledged note. Oversized or
  failed final notes remain editable while the captured audio is retained.
  Revision comparisons fence delayed requests, including a same-text revert
  after timeout; final submission locks before flushing pending autosave.
- Produce a private follow-up document from current saved notes/summary/actions,
  link a project, or explicitly share the meeting to its account team. Do not
  automatically publish private sources or generate invented commitments.

## Implementation boundaries

Reuse personal document CRUD for preparation/follow-up and existing authenticated
account/project operations. Add meeting-detail association/insight projection,
optional initial preparation at create, and optional notes CAS on the existing
update route. Preserve legacy callers and the existing recording lifecycle.
Paginate the reused personal KB listing. Do not activate canonical indexing or
modify the pending QA async/provenance workstreams.

Frontend source insertion is an explicit copy into the selected destination;
show that meeting readers can see saved notes. Source links still require their
own current authorization when opened. No source picker performs web search or
model calls automatically. Q&A keeps its current server authorization and tools.

## Acceptance checks

1. Prepare context, save/reopen a private prep document, start capture and verify
   initial notes/account survive creation and later detail reads.
2. Account switching and request failures never render another account's stale
   results or an error as an empty successful collection.
3. Select a KB/Insight/research/meeting reference, prepare a question and append
   a citation-bearing note with the original source link.
4. Save notes, simulate a concurrent edit and retain the local draft on 409;
   re-summary remains disabled while an editor has unsaved changes.
5. Read stored insight provenance, link a project, create a private follow-up
   note and explicitly share to a team. Read-only viewers cannot mutate.
6. Go stdlib unit/wire tests, full Go tests/vet/ARM64 build, frontend lint/build,
   and temporary Playwright desktop/mobile fixtures. No customer data or model
   calls are needed for validation.

## Requested delivery queue

After this SA workflow is complete, continue the user's two draft PRs:
- #242: durable asynchronous REST Q&A; honor its foundation/provenance dependencies.
- #227: canonical indexing activation; verify deployed strict QA and snapshot
  readiness before enabling all-mode. Do not infer rollout from merged source.
