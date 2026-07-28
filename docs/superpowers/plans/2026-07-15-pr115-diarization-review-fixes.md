# PR #115 Review Fixes — pyannote Speaker Diarization

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the review-confirmed MUST-FIX findings on PR #115 (`feat/pyannote-speaker-diarization`) — the three the review explicitly calls out as blocking ("머지 전 필수"), plus the doc-sync MAJOR the review's own rules make mandatory. Leave the PLAUSIBLE-but-unconfirmed NUM_SPEAKERS/disk-sizing findings as follow-up, not blocking, per the review's own "설계·문서·테스트 품질은 높으므로 위 필수 3건(+스모크 테스트)만 반영하면 재리뷰 없이 머지 가능한 수준" verdict.

**Context:** The review confirmed 3 MUST-FIX MAJORs (L2×2, L3×1) plus an L5 MAJOR for a missed doc-sync rule:
1. **L2 MAJOR (CONFIRMED, 3/4 models)** — `_to_mono16k_wav(local_path)` in `main()` runs `subprocess.run(..., check=True)` OUTSIDE any try/except, before `s3.put_object`. An ffmpeg failure on a bad/unusual codec raises and aborts `main()` entirely — the transcript that Whisper already successfully produced is never uploaded, and the meeting flips to `error`. This directly contradicts ADR-019's "diarization is best-effort, never fails transcription" contract.
2. **L2 MAJOR (CONFIRMED via base code)** — `duration_seconds` in the output JSON is `elapsed` (Whisper's wall-clock processing time, NOT the audio's actual duration). `backend/cmd/summarize/main.go`'s `mergePartTranscripts` treats this field as "authoritative audio duration" to compute multi-part timestamp offsets (its own doc comment says so). Before this PR, `transcribe.py` ignored `OUTPUT_KEY` so the Whisper multi-part path never actually wrote per-part files — this PR's (correct) `OUTPUT_KEY` fix activates that dormant path, and Whisper's faster-than-realtime processing means part-2-onward timestamps would be badly compressed/overlapping.
3. **L3 MAJOR (CONFIRMED, 3/4 models)** — `_ensure_diarization_model()`'s `tar.extractall(DIARIZATION_LOCAL_DIR)` has no member-path validation (CVE-2007-4559 pattern: a crafted tarball with `../`-escaping member names can write outside the target dir). Trust boundary is the same-account assets bucket, but the container runs as root and processes audio/transcript data, so a compromised bucket-write path escalates to code execution on the STT pipeline.
4. **L5 MAJOR (doc-sync rule violation)** — `docs/INFRA-SPEC.md`'s Whisper task env var list doesn't mention the new `DIARIZATION_S3_KEY`, even though `infra/lib/whisper-stack.ts` changed and CLAUDE.md's own Auto-Sync Rules require an infra change to update INFRA-SPEC.md.

Also fold in the review's specific mitigation for the PLAUSIBLE "config.yaml relative path may not resolve" finding — not because it's confirmed broken, but because the fix (rewrite to absolute paths) is strictly safer than the status quo and costs nothing: it removes an entire failure mode (silent, undetectable diarization disablement) rather than requiring a runtime smoke test to rule it out.

**Tech Stack:** Python 3.9+ (container-only heavy deps: torch, pyannote.audio, faster-whisper), Go (summarize Lambda), bash (upload script), CDK docs.

---

## Task 1: Make `_to_mono16k_wav` failures fall back to unlabeled segments, never abort transcription

**Files:**
- Modify: `backend/whisper/transcribe.py`
- Test: `backend/whisper/test_transcribe.py`

- [ ] **Step 1: Move the wav conversion inside the diarization best-effort block**

In `backend/whisper/transcribe.py`'s `main()`, the diarization block currently is:

```python
    diarization_config = _ensure_diarization_model()
    num_speakers_detected = 0
    if diarization_config and all_segments:
        print("Diarizing...")
        diarize_start = time.time()
        wav_path = _to_mono16k_wav(local_path)
        turns = _diarize(diarization_config, wav_path, num_speakers)
        ...
```

Wrap the `_to_mono16k_wav` call (and, for symmetry, the whole diarize-and-assign sequence) in a `try/except Exception`, matching the existing "log and fall back to unlabeled" pattern already used inside `_ensure_diarization_model` and `_diarize`. Since `_diarize` already swallows its own exceptions and returns `[]` on failure, the *only* uncaught raise in this block is `_to_mono16k_wav`'s `subprocess.run(check=True)` — wrap specifically that call (or the whole block, either is fine, but the try/except must cover at minimum the `_to_mono16k_wav` call and everything downstream of it that assumes `wav_path` exists):

```python
    diarization_config = _ensure_diarization_model()
    num_speakers_detected = 0
    if diarization_config and all_segments:
        print("Diarizing...")
        diarize_start = time.time()
        try:
            wav_path = _to_mono16k_wav(local_path)
            turns = _diarize(diarization_config, wav_path, num_speakers)
        except Exception as e:
            print(f"Audio conversion for diarization failed, skipping diarization: {e}")
            turns = []
        if turns:
            all_segments = _assign_speakers(all_segments, turns)
            num_speakers_detected = len({t[2] for t in turns})
            print(f"Diarization done in {time.time() - diarize_start:.1f}s, "
                  f"{num_speakers_detected} speaker(s) detected")
        else:
            print("Diarization produced no turns; segments left unlabeled")
    elif not diarization_config:
        print("Diarization model unavailable; segments left unlabeled "
              "(summarize Lambda will infer speakers from text)")
```

- [ ] **Step 2: Add a regression test**

`backend/whisper/test_transcribe.py` currently only tests the pure `_assign_speakers` function (no heavy imports needed). This fix is inside `main()`, which has heavy imports (`faster_whisper`, `boto3`) already stubbed at module-import time in this test file — check the existing stubbing pattern (`sys.modules['faster_whisper']` injection + `mock.patch('boto3.client')`/`mock.patch('boto3.resource')`) and extend it minimally: rather than testing all of `main()` (which needs S3/model mocking beyond this fix's scope), extract the try/except-wrapped block's *logic* into a small pure helper if that's a smaller diff than mocking `main()` end-to-end — e.g. a helper `_safe_diarize(config_path, local_path, num_speakers) -> list` that wraps `_to_mono16k_wav` + `_diarize` in the try/except and returns `[]` on any failure, called from `main()`. This keeps the fix testable without mocking ffmpeg/subprocess inside a giant `main()` test. Test: `_safe_diarize` returns `[]` (not a raised exception) when `_to_mono16k_wav` (patched via `unittest.mock.patch`) raises `subprocess.CalledProcessError`.

- [ ] **Step 3: Verify**

`cd backend/whisper && python3 -m unittest test_transcribe -v` — all pass, including the new failure-path test.

---

## Task 2: Make `duration_seconds` actually mean audio duration; stop conflating it with processing time

**Design note (revised after plan-gate review):** the first draft of this task added a
NEW field (`audio_duration_seconds`) and left the existing `duration_seconds` meaning
"processing time," with the Go side preferring the new field and falling back to the old
one. Plan-gate review (Codex, code-verified) correctly rejected this: the fallback path
still lets old-meaning `duration_seconds` be read as audio duration whenever the new field
is absent — which is exactly the bug this task exists to fix, just deferred to "whenever an
old-format transcript is encountered" instead of fixed outright. A grep across the whole
repo confirms `duration_seconds`/`DurationSeconds` has exactly ONE producer
(`backend/whisper/transcribe.py`) and ONE consumer (`backend/cmd/summarize/main.go` /
`merge.go`, both in this same PR's blast radius) — there is no third party whose existing
integration this field's meaning is contractually pinned to. Repurposing it directly, with
a clearly-named new field for what it *used to* mean, is safe and fixes the bug outright
instead of through an indirect, incomplete fallback.

**Files:**
- Modify: `backend/whisper/transcribe.py`
- Modify: `backend/cmd/summarize/main.go`
- Modify: `backend/cmd/summarize/merge.go`
- Test: `backend/whisper/test_transcribe.py`

- [ ] **Step 1: Make `duration_seconds` mean audio duration; add `transcription_duration_seconds` for the old meaning**

`faster_whisper`'s `model.transcribe(...)` already returns `info` (used for
`info.language`/`info.language_probability`), and `info.duration` is the actual decoded
audio length in seconds (a documented, stable `faster_whisper` `TranscriptionInfo` field).
In `backend/whisper/transcribe.py`'s `main()`, change the `whisper_metadata` dict's
`"duration_seconds": round(elapsed, 1)` to `"duration_seconds": round(info.duration, 1)`,
and add a new `"transcription_duration_seconds": round(elapsed, 1)` alongside it (so the
previously-tracked wall-clock timing metric isn't silently lost, just correctly renamed
to what it actually measures — plan-gate verification round confirmed `elapsed` at this
point in `main()` is captured right after Whisper transcription completes and BEFORE
diarization begins, so "transcription" is the accurate name, not "processing" (which would
wrongly imply it includes the diarization pass that follows). This field alone doesn't
capture the diarization-specific timing ADR-019's "GPU time increases by 1-3 minutes" note
refers to — that's a separate, pre-existing `time.time() - diarize_start` log line already
in the code, not part of this task's output JSON).

- [ ] **Step 2: Update the Go side to match — no fallback needed, this is the field's ACTUAL meaning now**

In `backend/cmd/summarize/main.go`: no field rename needed on the Go struct — `WhisperMetadata.DurationSeconds` (`json:"duration_seconds"`) now correctly receives audio duration directly from the container, with zero Go-side changes required to the actual value flow. This is deliberately the smallest fix: the bug was entirely in what the Python side computed and labeled, not in how Go consumed it.

Update the doc comment above `downloadAndParseTranscript` (~line 683, currently says "the
authoritative audio duration in seconds from `whisper_metadata.duration_seconds`") — this
claim is NOW accurate (it wasn't, before this fix); tighten the wording to state plainly
that the field is audio duration in seconds as of this fix, and that transcripts written by
a pre-fix container image have `duration_seconds` populated with processing time instead —
callers reading OLD transcripts (already-transcribed meetings, not re-processed) may see a
value that doesn't represent true audio length. This is a data-generation-time distinction,
not something the Go code can detect or correct after the fact (there's no reliable way to
tell, from the JSON alone, whether a given transcript predates this fix) — document it as a
known caveat on historical data, matching this plan's existing pattern (see PR #114's
Share-provenance historical-data caveat) rather than attempting a runtime heuristic.

In `backend/cmd/summarize/merge.go` (~line 109-119), update the comment chain describing
the duration fallback order — it currently says `whisper_metadata.duration_seconds
(preferred — true audio length including trailing silence; ADR-014 §5.2 mandates this)`,
which was aspirational before this fix and is now actually true; no wording change is
strictly required here since the comment already describes the INTENDED behavior this fix
delivers, but skim it once to confirm nothing else in that comment block assumes the old
(processing-time) meaning.

- [ ] **Step 3: Add a Python regression test**

In `backend/whisper/test_transcribe.py`, this fix's actual logic (which `info` attribute
feeds which output key) lives inside `main()`, which the existing test file doesn't
exercise end-to-end (it stubs heavy imports but only tests the pure `_assign_speakers`
function). Rather than mocking all of `main()`, verify this fix via the SAME extraction
approach as Task 1 Step 2 if that task already pulled diarization logic into a small
testable helper — if not, this step's correctness is adequately covered by Task 1's test
file changes plus a manual read-through, since the change here is a one-line dict-key
swap with no branching logic to unit-test in isolation (unlike Task 1's try/except, there's
no failure path here to regression-test against).

- [ ] **Step 4: Verify**

`cd backend/whisper && python3 -m unittest test_transcribe -v` and `cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`.

---

## Task 3: Harden tar extraction against path-traversal + rewrite config to absolute paths

**Files:**
- Modify: `backend/whisper/transcribe.py`
- Modify: `backend/whisper/upload-diarization-model.sh`

- [ ] **Step 1: Filter the diarization model tarball extraction**

In `_ensure_diarization_model()`, the container's Python is 3.12+ (Ubuntu 24.04-based CUDA image per the Dockerfile — confirm the base image's Python version if uncertain, but this is expected to be well above 3.9). Python 3.12+'s `tarfile.extractall` supports the `filter` kwarg (PEP 706); use the built-in safe filter:

```python
        import tarfile
        with tarfile.open(archive) as tar:
            tar.extractall(DIARIZATION_LOCAL_DIR, filter="data")
```

If the container's Python turns out to be older than 3.12 (check `python3 --version` in the Dockerfile's base image, or add a runtime check), `filter="data"` on Python <3.12 raises if `tarfile` doesn't support the kwarg at all (it does from 3.8.17/3.9.17/3.10.12/3.11.4 as a backport, per PEP 706) — since this repo's Dockerfile installs `python3` via `apt-get` on Ubuntu 24.04, it will be 3.12, so the plain `filter="data"` kwarg is safe without a version-guard. Double check by reading `backend/whisper/Dockerfile`'s base image tag before implementing, and note the confirmed Python version in the commit message.

- [ ] **Step 2: Rewrite `upload-diarization-model.sh`'s config to use absolute container-runtime paths**

The review flags (as PLAUSIBLE, not confirmed) that `Pipeline.from_pretrained(config_path)` might resolve the config's `../segmentation/{weights}`-style relative paths against the process CWD (`/app`, per the Dockerfile's `WORKDIR`) rather than the config file's own directory — which would silently disable diarization (caught only by absence of "Diarization done" in logs). Since the fix is strictly safer regardless of which resolution behavior pyannote actually uses, change the rewrite in `upload-diarization-model.sh` to write **absolute paths matching the container's runtime extraction location** (`DIARIZATION_LOCAL_DIR = "/tmp/diarization-model"` in `transcribe.py`, with the tar's internal layout being `pipeline/`, `segmentation/`, `embedding/` — so the absolute path at runtime is `/tmp/diarization-model/segmentation/{weights}` etc.), instead of the current `../segmentation/{weights}` relative form:

```python
params['segmentation'] = f'/tmp/diarization-model/segmentation/{seg_weights}'
params['embedding'] = f'/tmp/diarization-model/embedding/{emb_weights}'
```

This couples the upload script to `transcribe.py`'s `DIARIZATION_LOCAL_DIR` constant — add a comment in `upload-diarization-model.sh` noting that if `DIARIZATION_LOCAL_DIR` in `transcribe.py` ever changes, this script's hardcoded `/tmp/diarization-model` prefix must change with it (there's no shared config between the bash script and the Python file, so this is a manual-sync risk worth flagging rather than silently coupling).

- [ ] **Step 3: Verify**

No automated test exercises the upload script (it's an operator-run bootstrap, not part of CI) — verify by reading the diff for correctness (path construction matches `DIARIZATION_LOCAL_DIR` exactly) rather than running it (it needs `HF_TOKEN` + network + gated model access, out of scope for this fix session). Confirm `backend/whisper/test_transcribe.py` still passes unaffected (`cd backend/whisper && python3 -m unittest test_transcribe -v`) since this task doesn't touch any code path the tests cover.

---

## Task 4: Sync `docs/INFRA-SPEC.md` for `DIARIZATION_S3_KEY`

**Files:**
- Modify: `docs/INFRA-SPEC.md`

- [ ] **Step 1: Add the new env var to the Whisper task's documented env list**

Find the section in `docs/INFRA-SPEC.md` describing the Whisper ECS task's environment variables (the review cites it as listing `BUCKET_NAME, TABLE_NAME, AWS_REGION, VOCAB_KEY, MODEL_S3_KEY`). Add `DIARIZATION_S3_KEY` (default `models/pyannote-diarization-3.1.tar.gz`) to that list, with a one-line description matching the style of the existing entries (e.g. "S3 key for the bundled pyannote diarization model, downloaded once and cached at `/tmp/diarization-model` — see ADR-019").

- [ ] **Step 2: Verify**

No automated check for doc content — visually confirm the new line matches the existing table/list formatting exactly (don't restructure the surrounding section).

---

## Final verification (all tasks)

- [ ] `cd backend/whisper && python3 -m unittest test_transcribe -v`
- [ ] `cd backend && /home/atomoh/go-sdk/go/bin/go build ./... && /home/atomoh/go-sdk/go/bin/go test ./...`
- [ ] `cd infra && npx cdk synth TtobakWhisperStack` (confirm the doc-only change in Task 4 didn't require a CDK change — it shouldn't, `DIARIZATION_S3_KEY` was already added to `infra/lib/whisper-stack.ts` in the original PR)
- [ ] Re-read the diff against the 3 "머지 전 필수" findings + the L5 doc-sync MAJOR and confirm each is concretely addressed. The NUM_SPEAKERS-exact-vs-max_speakers MAJOR (2/4 models, PLAUSIBLE) and the disk-sizing MAJOR (2/4, PLAUSIBLE) are explicitly NOT in this plan's scope — they need product/ops judgment (what speaker-count semantics to use; actual instance disk headroom measurement) beyond what a code fix alone resolves cleanly, and the review's own verdict says the 3+1 fixes here are sufficient for merge without re-review.
