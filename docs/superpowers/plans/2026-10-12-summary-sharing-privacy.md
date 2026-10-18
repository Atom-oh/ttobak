# Summary-sharing privacy remediation

Status: active implementation plan, not deployed acceptance. Historical evidence
is the R1 finding at `640d297`; no live grant migration, revocation, model call or
deployment is authorized by this work.

## Required behavior

1. Keep owner-private linked-predecessor context. Capture canonical parent ACL,
   link set and authorization revision before reading predecessors or invoking a
   model. Pin every consumed predecessor's owner, title, content and action-item
   bytes in the publication transaction. These source fields are inline today;
   do not broaden transcript S3-reference permissions.
2. Every new direct, pending/materialized, or account-publication grant must
   atomically fence the parent and advance its authorization revision. Account
   publication itself blocks private context, including accounts with no members
   and readers authorized without a cached Share row.
3. Private and unknown legacy derivation remain sticky through ordinary saved
   resummary and human edits. A missing marker is never proof of sharing safety.
   Existing explicit grants remain valid; this does not remediate past disclosure.
4. Add an explicit owner sharing-safe regeneration mode. It uses current selected
   transcript, saved notes and verified DOCUMENT evidence, excludes prior
   generated/private content, and preserves concurrent human edits through CAS.
   Success atomically publishes a safe generation and clears old model-derived
   fields. Failures leave old content and provenance intact.
5. Preserve matching task IDs/completion using bounded hash-to-ID/completion
   continuity only. Never retain or feed old private task text, assignee or
   deadline into the new extraction prompt.
6. Guard tag/sentiment/insight/action publication against its captured source
   generation. Account insight copies must match current canonical generation
   and insight revision on both write and read; stale copies cannot reappear
   after a successful reset.
7. Preserve account onboarding. Mark genuinely new/materialized/re-added
   memberships. Role updates retain server-minted AddedAt and the marker.
   Go/Python/current-source/history/metadata/insight consumers restrict
   historical private/unknown derived fields for new members. Existing
   memberships require a valid pre-cutover AddedAt to retain legacy access;
   an absent marker alone establishes nothing.
8. Retain raw parent-owned transcript/notes access where a bounded projection is
   practical. Return explicit sharing-safe-regeneration status otherwise. Never
   turn a restricted read into successful empty derived evidence or retain an
   earlier private paraphrase through tool history.

## Reviewable delivery stages

- Reader foundation: typed provenance/membership contract, shared Go/Python
  read policy, restricted projections/status, history invalidation and insight
  revision checks. Writer activation remains a separate gate.
- Publication and grant writers: authorization revision transactions, linked
  source capture, provenance inheritance/invalidation, guarded derivatives and
  owner regeneration API. Audit every writer and transaction cancellation index.
- UX and rollout: owner confirmation/recovery flow, current API/QA contracts,
  a fixed cutover epoch, reader-before-writer deployment and old-writer drain.

Keep each complete review patch below 3,000 changed lines and 95 KB. A reader or
writer stage alone is not an R1 completion claim. Existing R5/R6 worktrees are
independent and must be integrated without overwriting their changes.

## Activation evidence required outside this implementation

Deploy every relevant reader before new membership markers can be written.
`SUMMARY_PRIVACY_READER_ENABLED=0` is the installation stage for unmarked
memberships only; stamped memberships are always protected. This inactive mode
does not establish legacy eligibility or qualify R1 as fixed. Final activation
uses `SUMMARY_PRIVACY_READER_ENABLED=1` with a fixed
`SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF` in UTC.
Use a fixed UTC legacy-member cutoff, coordinate it across artifacts, and verify
old API/worker versions and queued/in-flight old writes are drained before the
cutoff is treated as authoritative. Do not infer this from source code, a timer,
or a package build. Do not later move the cutoff forward to grandfather new
unmarked grants. Missing/invalid AddedAt or policy configuration cannot establish
legacy eligibility. Document existing legacy access separately from protected
new grants; no backfill or past-disclosure cleanup is implied.

## Local reader-stage validation (2026-10-13)

Reader-stage code is locally implemented and tested, not published or deployed.
Go meeting/detail/list, bounded reads, action/simulator/knowledge consumers and
account insights use the restricted view/status; Python handler, canonical source,
indexed retrieval and strict account/history consumers use the corresponding
policy. Protected source snapshots retain raw parent-owned notes/transcript,
withhold old derivation and change the user-scoped revision. Account insight copies
bind generation/revision and recheck current publication/membership before return.
Restrictions are explicit; they do not attest successful empty derived evidence.

Eligibility preserves RFC3339 nanoseconds in both artifacts, stamped memberships
remain protected in installation mode, and malformed/missing cutoff eligibility
fails closed once activated. Metadata checks require canonical lowercase hashes.
Legacy live draft context counts as derivation. Synthetic tests cover independent
existing direct grants, raw-source projection, withheld-vector denial, insight
revision races and sharing-safe snapshots excluding predecessor reads.

At this reader-stage checkpoint, snapshot/provenance/grant helpers were preparatory
and uncalled. The subsequent local writer integration below supersedes that
implementation status; neither checkpoint proves deployment.

## Local writer integration (2026-10-13)

Implemented in the local worktree, with activation disabled by default:

- `SummaryPrivacyWritesEnabled` accepts exactly `SUMMARY_PRIVACY_WRITER_ENABLED=1`.
  Marker writes, new-grant protection, source/provenance publication and owner
  regeneration must follow reader installation and old-writer drain.
- Direct grants, queued meeting invitations and materialization atomically fence
  the canonical parent and advance authorization revision. Account publication
  also pins the owner's current membership. Account cache grants require current
  canonical publication, fence the parent, and distinguish parent CAS rejection
  from an existing direct-grant clobber guard.
- New/materialized/re-added memberships mint the marker and AddedAt; role changes
  preserve those fields. Existing independent direct grants retain their scope.
- Batch `SummarizeTranscript` captures parent privacy/ACL/link state before binding
  authorized predecessors, then pins exact predecessor title/content/tasks through
  `SaveMeetingSummary`. Canonical account publication itself prevents predecessor
  consumption. Ordinary saved resummary keeps private/unknown ancestry.
- Owner-only `{sharingSafe:true}` is persisted through saved-source delivery and
  source/edit-grant CAS. It excludes prior generated and predecessor material,
  permits only selected transcript, saved notes and verified current DOCUMENT
  evidence, and atomically resets derivative/action/simulator state. Failure keeps
  old content/provenance and concurrent human edits. No STT is rerun.
- Action tasks queue and complete against captured generation/content/prior tasks.
  Hash-only ID/completion continuity cannot feed old prose to extraction. Tag,
  sentiment and insight model calls now use immutable snapshots and conditional
  publication. Insight copies check current canonical bytes/generation/revision in
  one bounded replacement transaction; readers independently revalidate them.
- Simulator claims fence canonical generation. Safe reset deletes its singleton,
  so existing Go/Python run-ID CAS prevents late workers from restoring results.
  Canonical index publication already uses immutable revision-bound objects and
  source CAS. Activated summarizers stop legacy fixed-object KB exports.
- Content/live-draft edits invalidate safe clearance while preserving sticky
  private/unknown ancestry. Saved notes stay independent. Legacy whole-item writes
  are rejected after writer activation; production callers use field updates.
- The owner UI presents confirmation, blocks unsaved/active work and clears old
  displayed derivatives after loading a verified sharing-safe result. Restricted
  readers receive explicit status and retain raw own-source access.

The complete local source tree remains authoritative. Review artifacts must each
remain below 95 KB; applying all ordered artifacts reconstructs the validated
worktree. A partial artifact or reader-only merge does not establish R1 completion.

## Remaining deployment acceptance

No migration, grant revocation, model invocation, writer activation or deployment
was performed by this local work. Deploy all readers first, verify fixed-cutoff
configuration and old API/worker/queued-work drain, then activate coordinated
writers/readers. Verify deployed modes independently and exercise authorized
owner/new-member/current-source/history consumers. Never advance the cutoff to
silently grandfather new unmarked grants. Historical disclosure is unchanged.

## Current-main delivery preparation (2026-10-15)

The ordered local stages are integrated onto source `84f9bdeb`, in an isolated
delivery clone. This supersedes the older local package base for delivery only;
no source-only preparation is public approval or activation.

The integration preserves current transcript quality/untimed output, UUID source
revisions, initial/resumed claim handling and human-content CAS/preservation.
Private predecessor binding follows the current claim and human-content checks.
Derivative snapshots also pin quality/revision fields. The existing PDF source
recheck still blocks a changed complete view before storage. QA keeps current
origin-proof registration and source/history contracts.

Each cumulative local stage runs full Go and QA checks. The final privacy-only
UI runs frontend lint/build; existing auth/MCP documentation and controls are
preserved. Membership/provenance/grant writers and unmarked-reader activation
remain off by default. Root review, exact-HEAD CI, explicit payload approval and
deployment acceptance remain separate gates.
