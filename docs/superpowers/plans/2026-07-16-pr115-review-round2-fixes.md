# PR #115 Review Round 2 Fixes — pyannote Speaker Diarization

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the second AI Code Review round's findings on PR #115 (`feat/pyannote-speaker-diarization`): all round-1 "머지 전 필수" fixes were confirmed correctly applied, but 3 new code MAJORs (all code-verified) and 2 repo doc-sync MAJORs remain.

**Context:** All 5 findings verified against actual code before writing this plan:
1. **L2 MAJOR-1 (3/4 models independently)** — `_diarize`'s `kwargs = {"num_speakers": num_speakers}` forces pyannote to produce EXACTLY `num_speakers` clusters, not a maximum. `main.go` sets `numSpeakers = len(meeting.Participants)` (registered headcount), but ADR-019's own motivating example is 8 registered participants with only 3 actual speakers — passing 8 as an exact constraint forces pyannote to over-split those 3 speakers into 8 clusters, worsening exactly the bug this feature exists to fix.
2. **L2 MAJOR-2 (1/4, code-verified)** — `_assign_speakers`'s `label_order` is a fresh local list per Whisper container invocation (= per audio part in a multi-part meeting). Part 1's first speaker and part 2's first speaker both become `spk_0`. `mergePartTranscripts` (`merge.go`) calls `RefineTranscript` independently per part and concatenates the results with no cross-part label reconciliation. Before this PR, the multi-part path was dormant (`OUTPUT_KEY` was ignored); this PR's `OUTPUT_KEY` fix activates it, and preserve mode makes the (now colliding) acoustic labels authoritative — worse than the old infer-mode behavior, which at least re-guessed consistently from full-meeting text context.
3. **L2 MAJOR-3 (1/4, code-verified)** — `rawFallbackSegments` (`bedrock.go`) takes one `speaker` string and stamps it on every segment in a fallback chunk, discarding each segment's own `seg.Speaker` acoustic label entirely. This only fires when `RefineTranscript` fails/returns empty for a chunk, but when it does, a chunk with real diarization data gets actively mislabeled with one arbitrary speaker instead of falling back to unlabeled-but-still-per-segment-correct data.
4. **L4 MAJOR (repo rule violation)** — `AGENTS.md`'s generation marker (`claude-md-sha: 6ad04d5e2873`) predates this PR's `CLAUDE.md` edits (the diarization bullet, the whisper test command). Confirmed stale via `check_ai_context.py`. The repo's own `co-agent` tooling requires re-running `/co-agent sync-context` after any `CLAUDE.md` change.
5. **L5 MAJOR (repo rule violation)** — `docs/architecture.md`'s ADR index (both KO and EN sections) lists ADR-018 then jumps straight to ADR-020, omitting ADR-019 entirely — confirmed by direct grep. CLAUDE.md's Auto-Sync Rules require "Architecture changes (new stacks, services, pipelines): Update `docs/architecture.md`", and this PR adds a new pipeline stage (diarization) plus a new ADR.

**Explicitly NOT in this plan** (per the review's own verdict language, these are follow-up, not blocking): pyannote model-bundle integrity/checksums, `NUM_SPEAKERS` upper-bound validation, hardcoded bucket names (pre-existing "Infra hardcoding" Known Issue class), dependency version pinning for `pyannote.audio`/`pyyaml`/`huggingface_hub`, the plan-document machine-specific Go path (cosmetic, already-executed record), INFRA-SPEC's pre-existing A10G VRAM typo (16GB vs 24GB — pre-existing error this PR's diff merely exposed, not introduced).

**Tech Stack:** Python 3.9+ (container), Go (transcribe Lambda, summarize Lambda), Markdown docs.

---

## Task 1: Pass `max_speakers` instead of an exact `num_speakers` constraint

**Files:**
- Modify: `backend/whisper/transcribe.py`
- Test: `backend/whisper/test_transcribe.py`

- [ ] **Step 1: Change `_diarize`'s pyannote kwarg from `num_speakers` to `max_speakers`**

In `backend/whisper/transcribe.py`'s `_diarize`, change:
```python
kwargs = {"num_speakers": num_speakers} if num_speakers else {}
```
to:
```python
kwargs = {"max_speakers": num_speakers} if num_speakers else {}
```
`pyannote.audio`'s `Pipeline.__call__` accepts `min_speakers`/`max_speakers` as a bounding range (auto-detect within the range) versus `num_speakers` (exact count forced) — passing the registered headcount as an upper bound lets pyannote still auto-detect the ACTUAL number of distinct voices (which is what ADR-019's whole feature is trying to get right) while still using the headcount to prevent runaway over-segmentation beyond what's plausible. The parameter name `num_speakers` in `_diarize`'s own signature and in `main.go`'s `NUM_SPEAKERS` env var can stay as-is (renaming across two files/languages for what's now used as a bound, not an exact count, is a larger diff for a purely cosmetic gain) — but add a one-line comment at the kwarg site clarifying the semantic (see Step 2).

- [ ] **Step 2: Document the semantic change at the call site**

Add a comment directly above the kwargs line: `# num_speakers is a registered-participant headcount, not an actual speaker count -- passed as max_speakers (an upper bound pyannote auto-detects within) rather than num_speakers (which would force exactly that many clusters and over-split when fewer people actually spoke).`

- [ ] **Step 3: Update the existing unit test's assertion, if any, and verify**

`backend/whisper/test_transcribe.py` only tests the pure `_assign_speakers` function today, not `_diarize` (which needs the real pyannote pipeline and isn't unit-testable without heavy deps) — so there is no existing assertion on the `num_speakers`/`max_speakers` kwarg name to update. No new test needed for this one-line kwarg-name change (nothing to unit-test in isolation: `_diarize`'s only branch here is `if num_speakers` truthy-or-not, unchanged). Verify via `cd backend/whisper && python3 -m unittest test_transcribe -v` (confirms nothing else broke) and a manual code read confirming the kwarg key matches `pyannote.audio`'s documented parameter name (`max_speakers`, not e.g. `maxSpeakers` or `max_num_speakers`) — check the installed `pyannote.audio` version's pipeline signature if there's any doubt, since this can't be verified by a unit test in this environment (no GPU/model available here).

---

## Task 2: Prevent `spk_N` label collisions across multi-part meetings

**Files:**
- Modify: `backend/cmd/summarize/merge.go`
- Test: `backend/cmd/summarize/merge_test.go`

**Testability note:** `backend/cmd/summarize/` has NO existing test files (confirmed —
`ls backend/cmd/summarize/*_test.go` finds nothing), and `mergePartTranscripts` calls the
package-level `bedrockService *service.BedrockService` (a concrete type, not an interface)
directly — there's no seam to mock Bedrock/S3 for a full test of the function, and
introducing one is a bigger refactor than this fix warrants. Extract JUST the label-rewrite
logic (Step 1) into a small pure function with no AWS dependency, so it's unit-testable in
isolation without touching the untestable surrounding function.

**Design note:** the review's own suggestion (namespace labels as `spk_{part}_{n}`) was checked against the frontend and found to BREAK it: `frontend/src/components/meeting/SpeakerMapEditor.tsx` has `UNMAPPED_PATTERN = /^(spk_\d+|화자[A-Z])$/` (line 13), a content-scanning regex `/(?:spk_\d+|화자[A-Z])/g` (line 28), and `speakerSortKey` which does `parseInt(label.replace('spk_', ''))` (line 16) — all three assume the label is `spk_` followed by ONLY digits. A label like `spk_p0_2` would (a) never match either regex, making it invisible to "has unmapped speakers" detection and the speaker list entirely, and (b) `parseInt` on the non-numeric remainder yields `NaN`, corrupting the sort. Redesigning those three call sites to accept a new format is more invasive than avoiding the problem: this task instead picks a namespacing scheme that stays PURELY NUMERIC, so no frontend change is needed at all. A full re-architecture of the diarization boundary (one diarization pass over concatenated audio, or cross-part voice-embedding reconciliation) remains explicitly out of scope/follow-up, same as the original review's own framing.

- [ ] **Step 1: Namespace preserve-mode speaker labels by part index during merge, using a purely numeric offset**

In `backend/cmd/summarize/merge.go`, add a pure helper (no AWS/service dependency, so it's
directly unit-testable):

```go
// maxSpeakersPerPart bounds n before offsetting -- without this, an n that
// itself already exceeds the offset step (implausible from _assign_speakers'
// normalization in practice, but not something this function can assume)
// would let one part's namespaced range collide with another's. Enforcing
// the bound makes collision-freedom a property of this function alone, not
// a fact about pyannote's typical output that could change.
const maxSpeakersPerPart = 1_000_000

// namespaceSpeakerLabel rewrites an acoustic spk_N label to be unique across
// parts by offsetting N by partIndex*maxSpeakersPerPart. Stays within
// spk_\d+ so the frontend's SpeakerMapEditor (UNMAPPED_PATTERN,
// speakerSortKey's parseInt) needs no changes. Non-spk_ labels (or already
// part-0, which needs no offset) pass through unchanged.
func namespaceSpeakerLabel(label string, partIndex int) string {
	if !strings.HasPrefix(label, "spk_") {
		return label
	}
	n, err := strconv.Atoi(strings.TrimPrefix(label, "spk_"))
	if err != nil {
		return label // not a spk_N label (e.g. a Korean 화자 fallback) -- leave as-is
	}
	// Clamp BEFORE computing the offset, and apply the offset uniformly
	// (including partIndex==0 -- an unclamped, un-namespaced part-0 label
	// could otherwise land inside part 1+'s offset range and collide with
	// it). Each part's output is confined to the disjoint interval
	// [partIndex*maxSpeakersPerPart, (partIndex+1)*maxSpeakersPerPart - 1]
	// regardless of the input n, so collision-freedom holds for every
	// partIndex >= 0 and every input, not just "typical" ones.
	// _assign_speakers only ever produces sequential labels starting at 0
	// (see its label_order logic), so n reaching this clamp in practice
	// would mean something already went wrong upstream; the clamp exists so
	// that OUT-OF-RANGE input degrades to a shared bucket within its own
	// part (still never colliding with another part) instead of this
	// function's own collision-freedom guarantee depending on an
	// assumption about pyannote's typical output.
	//
	// Accepted bound: partIndex*maxSpeakersPerPart can overflow int only
	// for partIndex in the billions -- partCount comes from actual
	// multi-file audio uploads (bounded by upload chunking, realistically
	// under a few hundred parts for even a very long recording), so this
	// is not reachable in practice. Not guarded further -- doing so would
	// add real complexity (bignum or a saturating-add) for a case this
	// pipeline's own upstream constraints already rule out.
	switch {
	case n < 0:
		n = 0
	case n >= maxSpeakersPerPart:
		n = maxSpeakersPerPart - 1
	}
	return fmt.Sprintf("spk_%d", partIndex*maxSpeakersPerPart+n)
}
```

In `mergePartTranscripts`'s loop, the loop already has `part.index` and `whisperSegments`
(the raw per-part Whisper output, returned by `downloadAndParseTranscript` a few lines
above the `RefineTranscript` call) in scope. Detect whether THIS part used acoustic
(preserve-mode) diarization by checking `whisperSegments` for any non-empty `Speaker`
field — same check as `service.hasAcousticSpeakers`, just inlined here since that function
is unexported in a different package (`service`) and this file is `package main`; not
worth exporting a one-line helper across a package boundary for a single call site. Only
when that's true, call `namespaceSpeakerLabel(rs.Speaker, part.index)` on each returned
`refinedSegs[i].Speaker` before appending to `segments`. Skip entirely for infer-mode parts
(no acoustic `Speaker` data) — infer-mode's per-chunk labels already have their own
documented drift caveat and this task doesn't change that behavior. Cost of this fix: the
SAME real-world speaker gets a different label per part if they spoke in multiple parts (a
real limitation, but strictly better than the CURRENT bug of two DIFFERENT people silently
sharing one label — manually re-labeling two now-distinct `spk_N` entries as the same
person via the existing `SpeakerMapEditor` is a correctable UX gap; two different people
silently merged with no signal anything is wrong is not).

- [ ] **Step 2: No frontend changes needed — confirm via the SpeakerMapEditor code path, don't just assume**

Since Step 1's scheme stays within `spk_\d+`, `SpeakerMapEditor.tsx` needs no changes. Re-read `UNMAPPED_PATTERN`, the content-scan regex, and `speakerSortKey` (lines 13-18) once more against a concrete example (`spk_1000000`, `spk_2000003`) to confirm they behave correctly (they do: `\d+` matches any digit run, `parseInt('1000')` is a valid number) before moving on — this step is a confirmation checkpoint, not new code.

- [ ] **Step 3: Add a regression test for the pure helper**

In new `backend/cmd/summarize/merge_test.go`, test `namespaceSpeakerLabel` directly (no
mocking needed — it's pure): `namespaceSpeakerLabel("spk_2", 0)` → `"spk_2"` (part 0's
offset is `0*1_000_000`, so small indices happen to pass through numerically, not because
part 0 is special-cased); `namespaceSpeakerLabel("spk_0", 1)` → `"spk_1000000"`;
`namespaceSpeakerLabel("spk_3", 2)` → `"spk_2000003"`; a non-`spk_` input (e.g. `"화자A"`)
passes through unchanged for any partIndex; an out-of-range input
(`namespaceSpeakerLabel("spk_5000000", 0)`) clamps to `"spk_999999"`, still inside part 0's
own range and never colliding with part 1's `[1_000_000, 1_999_999]`; a negative-index
input (`namespaceSpeakerLabel("spk_-1", 1)`) clamps to `"spk_1000000"` rather than
underflowing into part 0's range. This directly proves the collision Task 2 exists to fix
is closed (part 0's `spk_0` and part 1's `spk_0` after namespacing are `spk_0` and
`spk_1000000` — never equal, for ANY partIndex and ANY input n) without needing to mock
`RefineTranscript`/S3/Bedrock at all.

- [ ] **Step 4: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`.

---

## Task 3: Preserve acoustic speaker labels in the refine fallback path

**Files:**
- Modify: `backend/internal/service/bedrock.go`
- Test: `backend/internal/service/bedrock_test.go`

- [ ] **Step 1: Make `rawFallbackSegments` prefer each segment's own acoustic label**

In `backend/internal/service/bedrock.go`'s `rawFallbackSegments(chunk []WhisperSegment, speaker string) []RefinedSegment`, change the per-segment loop: for each `seg`, use `seg.Speaker` when it's non-empty (the acoustic label already assigned by diarization — authoritative, per ADR-019's preserve-mode design) instead of the single `speaker` parameter; only fall back to the passed-in `speaker` (or the existing `"화자A"` default) when `seg.Speaker` is empty (older transcripts, diarization-disabled runs). This is a per-segment decision, not a per-chunk one — a chunk can legitimately contain segments from multiple acoustic speakers, and the current code collapses them all into one label even when the underlying data to do better already exists on each segment.

- [ ] **Step 2: Add a regression test**

In `backend/internal/service/bedrock_test.go`, add a test for `rawFallbackSegments` (check if it's already unit-tested — if not, this is straightforward: it's a pure function). Construct a `[]WhisperSegment` with 2 segments carrying DIFFERENT non-empty `Speaker` values (e.g. `spk_0` and `spk_1`), call `rawFallbackSegments(chunk, "spk_0")` (some fallback default), and assert the two returned `RefinedSegment`s keep their respective ORIGINAL acoustic labels (`spk_0`, `spk_1`), not both collapsed to the passed-in `"spk_0"`. Add a second case: a segment with EMPTY `Speaker` still falls back to the passed-in default, preserving today's behavior for non-acoustic data.

- [ ] **Step 3: Verify**

`cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`.

---

## Task 4: Regenerate AGENTS.md via the repo's own sync-context tooling

**Files:**
- Modify: `AGENTS.md`
- Modify: `.kiro/steering/project-context.md` (if the sync tool touches it; check)

**Design note:** this is a doc-sync-tool run, not hand-authored content — `AGENTS.md` carries a generation marker (`DO NOT EDIT — edit CLAUDE.md then run /co-agent sync-context`) that this task must respect. Do not hand-edit the file; run the actual sync process.

- [ ] **Step 1: Run the sync**

Invoke the `co-agent:sync-context` skill/command (per its own documented steps: distill `CLAUDE.md` into a lean reviewer-oriented `AGENTS.md`, emit the generation marker via `check_ai_context.py --emit-marker`, write the Kiro steering bridge if one doesn't already exist unmanaged). This should be run as the actual skill invocation, not hand-copied — the distillation logic (what to include/omit) lives in that skill, not duplicated here.

- [ ] **Step 2: Verify**

`python3 "$SK/check_ai_context.py" /home/atomoh/ttobak` (where `$SK` is the co-agent scripts dir) must report no stale/missing-marker issues for `AGENTS.md`. Confirm the regenerated file actually mentions the diarization test command and the ADR-019 STT bullet (spot-check, since the distillation is lossy by design — confirm it captured THESE specific additions, not just that the marker/hash updated).

---

## Task 5: Add ADR-019 to `docs/architecture.md`'s ADR index (KO + EN)

**Files:**
- Modify: `docs/architecture.md`

- [ ] **Step 1: Insert the missing ADR-019 line in both language sections**

Find the Korean ADR list (currently jumps `ADR-018` → `ADR-020` around line 143-144) and the English list (same jump around line 235-236). Insert an ADR-019 line between them in EACH section, matching the exact format of its neighbors (`- [ADR-019: <title>](decisions/ADR-019-acoustic-speaker-diarization-pyannote.md) (승인됨/Accepted)`) — pull the title from the actual `ADR-019-acoustic-speaker-diarization-pyannote.md` file's own heading for both languages rather than inventing new wording.

- [ ] **Step 2: Add the diarization stage to the batch STT pipeline diagram, if one exists**

Check whether `architecture.md` has an ASCII/mermaid diagram of the batch STT pipeline (Whisper → refine → summarize). If so, add a diarization step between transcription and refine (matching ADR-019's own architecture diagram in the ADR file) so the two documents' pipeline descriptions stay consistent. If no such diagram exists in `architecture.md` (only prose), skip this step — don't introduce a new diagram format the file doesn't already use.

- [ ] **Step 3: Verify**

No automated check for doc content — visually diff against ADR-019's own wording to confirm consistency, and confirm the numbering/format matches surrounding entries exactly (don't restructure the list).

---

## Final verification (all tasks)

- [ ] `cd backend/whisper && python3 -m unittest test_transcribe -v`
- [ ] `cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`
- [ ] `python3 "$SK/check_ai_context.py" /home/atomoh/ttobak` reports clean
- [ ] Re-read the diff against all 5 confirmed findings and confirm each is concretely addressed.
