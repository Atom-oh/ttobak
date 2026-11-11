# Shareable-by-default and bilingual meeting notes

Date: 2026-11-10. Status: Historical design record, approved by the user on 2026-11-10.

## Intent

Users think about meeting notes in two states only: not shared (private) or
shared with an account team. Today a third, hidden state exists: generated notes
can be "private" or "unknown" under ADR-049, and the owner must run a separate
the sharing-safe regeneration action before an account or email
share is allowed. That step should disappear for normal use.

Separately, Korean notes sometimes read ambiguously, or with a different meaning
than the speaker intended. The owner wants to read the same meeting again in
English to cross-check meaning. A translation of the Korean note would inherit
its ambiguity, so the English note is written independently from the source
transcript.

## Goals

1. Every newly generated final note (`Meeting.Content`) is sharing-safe at
   publication: it consumes no owner-private predecessor evidence, so sharing is
   only a matter of choosing an account or recipient.
2. Final notes default to Korean only.
3. On demand, an owner or editor can generate an English note for a meeting. It
   is written from the selected transcript, saved notes and verified DOCUMENT
   text, not translated from the Korean note.
4. The English note is stored alongside the Korean note. Everyone who can read
   the meeting's notes (owner, editors, email-share recipients, account members)
   can switch between Korean and English on the meeting page.
5. The English note is shareable under the same rules as the Korean note.

## Non-goals

- Linked-predecessor context in generated notes. It is dropped from all
  generation paths in v1. A later version may include a predecessor that is
  already readable by exactly the same audience (for example, published to the
  same account); not in scope here.
- English action items, insights, tags or sentiment. All derivatives stay tied
  to the Korean note. The English note has no task list of its own.
- Persisted notes in languages other than Korean and English.
- English notes in MCP reads, QA source context, the canonical KB index or
  exports (PDF/Notion/Obsidian). These keep reading `Content` (Korean). Builder
  may add a language parameter later; not v1.
- Per-user default language settings.
- Replacing the existing non-persisted page translation
  (`MeetingTranslation.tsx`, `POST /api/meetings/{id}/translation`). It stays.

## Anti-goals

- The English note must not become a translation of the Korean note.
- No path may publish a note marked safe that consumed private/unknown
  generated content or predecessor content. Removing the user-facing step must
  not weaken ADR-049's guarantees for existing private/unknown rows.
- Generating or regenerating the English note must not reset or change Korean
  content, action items (IDs, completion), insights, tags, sentiment, simulator
  state or their provenance.
- No new unauthenticated route; no new public origin.

## Current state (verified 2026-11-10 against origin/main dde84cb8)

- Deployed Lambda env: `ttobak-api` writer=1 reader=1 and
  `SUMMARY_OWNER_ACCOUNT_SHARE_ENABLED=1`, `ttobak-summarize` writer=1,
  `ttobak-qa` reader=1 (`aws lambda get-function-configuration`).
- Account-team sharing already works without regeneration: ADR-059 lets the
  owner approve current output for one account, and the 2026-11-06 ADR-049
  amendment shows published summaries to every member. The remaining gate is
  direct-email and pending grants, which still require `CanCreateGrant`
  (safe provenance). `ResummaryControls.tsx` tells users that individual invitations need
  sharing-safe notes.
- The first live-summary write on a new meeting stamps `unknown` (nil
  provenance maps to `unknown` in `UpdateMeetingFieldsIfMatch`), and batch
  publication keeps that sticky state. So most live-recorded meetings cannot be
  shared by email without the sharing-safe step. The live summary itself comes
  from the client's live transcript via `handler/summarize_live.go` and the
  client's own update; it consumes no predecessor content.
- Image analyses (`BedrockService.AnalyzeImage`, `ProcessImageAttachment`)
  read only the image object and the attachment row: no other meeting content.
- Batch summary binds up to three linked predecessors
  (`BindLinkedSummarySources`, `repository/summary_privacy.go`) when the meeting
  is not account-published and has no non-owner direct share. Any consumed
  predecessor marks the result `private` (`summaryPublicationPrivacy`).
- Owner/editor content edits downgrade `safe` to `unknown`
  (`UpdateMeetingFieldsIfMatch`, `repository/dynamodb.go`), which blocks a new
  grant until regeneration. Regeneration overwrites the edit.
- Owner sharing-safe resummary: `POST /api/meetings/{id}/resummary` with
  `{sharingSafe:true}` (`handler/resummary.go`, `service/resummary.go`); it
  excludes prior generated content and predecessors and clears derivatives.
- `noteLanguage` is `""` (auto) / `ko` / `en`, per meeting, set from
  `ResummaryControls.tsx`; the prompt rule is `noteLanguageDirective`
  (`service/bedrock.go`). Auto means "Korean unless the transcript is entirely
  English". There is no create-time or per-user default.
- `content` is a non-spillable sized field counted against the ~300KB inline
  item budget (`nonSpillableSizedFields`, `repository/dynamodb.go`).

## Decided behavior

### Shareable by default

- Batch summarize (initial and retried runs) and ordinary saved-source
  resummary never bind linked predecessors.
- Prior generated content (`Content`, live draft) is used as evidence only when
  its current privacy state is `safe`. Otherwise it is excluded, as the
  sharing-safe resummary does today.
- When nothing private/unknown was consumed, publication stamps `safe`, so
  account selection or an email share succeeds without a regeneration step.
- Existing private/unknown rows keep their sticky state and remain blocked for
  new grants. The owner keeps a regeneration action for them, relabeled as an
  ordinary "regenerate notes" action with a short explanation that the old note cannot
  be shared as is. The separate "sharing-safe" concept and wording leave the UI.

- Who may reset an existing private/unknown row (decided by the designer; the
  user can override): only the owner, through the existing derivative-clearing
  sharing-safe path (it clears tasks, insights, tags, sentiment, live draft,
  action analysis and simulator state as today). An editor's ordinary resummary
  or a batch retry on a private/unknown row keeps the sticky state. On a `safe`
  row, ordinary resummary by owner or editor stays `safe`, so v1 needs no second
  user-visible mode; the internal sharing-safe mode remains for the reset.
- Image/preview analyses (designer's call): analyses of this meeting's own
  uploaded images count as safe evidence for both the Korean and English notes,
  provided the builder verifies that `cmd/process-image` consumes only the
  image and this meeting's own data. If verification shows any other meeting's
  content enters that prompt, keep today's exclusion and report it. Pre-existing
  analyses are covered by the same verification, because the producer is the
  same code; record the verified revision in the new ADR.
- Linked meetings (designer's call): `linkedMeetingIds` and the link UI stay as
  navigation only. UI and docs wording that promises carried-over context is
  updated. Existing rows marked private only because of a predecessor stay
  blocked until the owner resets them.

- Live drafts: a live-summary write on a row with nil provenance and no
  generated derivatives stamps `safe` (it is client-authored from this
  meeting's own live transcript), instead of `unknown`.

### Human edits (decided by the user, 2026-11-10)

An owner/editor edit of a `safe` note stays `safe` (new generation ID, new
content hash). Rationale: ADR-049 already states safe provenance does not
classify information a user types; saved notes are treated the same way. The
editor is an authorized participant making a deliberate disclosure, not model
output inheriting hidden evidence. The same rule applies to live-draft
(`liveSummary`) edits of a `safe` row. Edits of `private`/`unknown` notes remain
sticky. A new ADR records this and the predecessor removal; ADR-049 gains a
"superseded in part by" pointer rather than rewritten history.

### Korean default

- The Korean note is always written in Korean (`noteLanguageDirective(ko)`
  behavior), including for all-English transcripts.
- The per-meeting language selector is removed from the UI. Stored `en`/`""`
  values on existing meetings do not change existing content; the next Korean
  generation is Korean. The API keeps accepting the field for compatibility
  (builder may make it a no-op); clients stop sending it.

### English note

- Trigger: an owner or editor presses the "generate English notes" action on a done meeting with
  a usable transcript. Not generated automatically.
- Source: selected transcript (A/B per existing selection), saved notes,
  verified current DOCUMENT extraction and the same image analyses the Korean
  note may use, with the English directive (same
  structure, English headings, markers and speaker labels unchanged). No Korean
  note, no predecessors, no prior English note as evidence.
- Always published `safe` with its own generation ID and content hash.
- Run state follows the saved-source resummary pattern: its own analysis row
  with run/source hash/numeric lease CAS, async execution in the existing
  summarize Lambda, retry after failure, failure retains the previous English
  note. Edit grant and source bytes are revalidated before publication.
- Freshness: record the source revisions used. When the transcript selection,
  transcript revision, notes revision or verified documents change, the UI marks
  the English note as outdated and offers regeneration. It is never silently
  regenerated.
- Readers: meeting detail returns the English note, its status and freshness to
  anyone who can read the Korean note, under the same ADR-049 reader projection
  (an account member sees it only if its own state is `safe`).
- Editing: v1 is read-only with regeneration (builder's call, see below).
- Deletion: deleting the meeting deletes the English note and its run state in
  the same transaction as the existing summary state.

### Meeting page

- A Korean/English segmented toggle appears above the final note when an
  English note exists; otherwise only the "generate English notes" action shows (for
  owner/editor). Recipients without edit rights see the toggle only when an
  English note exists.
- Status: generating, failed (with retry), outdated.

## Constraints

- ADR-049/013/028 guarantees, CAS rules and reader projections remain.
- Inline item budget: the English field counts against the ~300KB item budget
  like `content`, or gets its own overflow; publication must fail cleanly
  rather than exceed the item limit.
- Reader-compatible code (API detail, account projection, QA if it reads full
  items) deploys before the writer.
- Docs to update: `docs/API-SPEC.md`, `docs/DESIGN-SPEC.md`,
  `docs/architecture.md`, a new ADR amending ADR-049 (predecessor removal,
  safe-edit rule, image-analysis evidence) with a pointer added to ADR-049.

## Left to the builder

- Field and row names (for example `contentEn`, `ANALYSIS#summaryEn`), route
  shape (for example `POST/GET /api/meetings/{id}/notes/en`), and whether the
  English run reuses `ResummaryService` with a target/language parameter or a
  sibling service.
- Storage choice for size (inline non-spillable vs dedicated spill key).
- Whether `noteLanguage` becomes a server no-op or is retained internally.
- Whether to make the English note editable in a later step.
- Exact Korean UI copy, within the wording rules above.
- Model choice: the same summary model as the Korean note
  (`BEDROCK_SUMMARY_MODEL_ID`) unless a reason appears.
