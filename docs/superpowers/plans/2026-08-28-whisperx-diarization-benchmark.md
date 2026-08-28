# WhisperX Diarization Benchmark (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up an isolated WhisperX (pyannote 4.x) transcription+diarization engine — new container image, new ECS task definition, model-staging script, and a manual benchmark runbook — so its speaker-diarization quality can be compared against the existing faster-whisper + pyannote 3.1 engine on real meeting audio, **with zero changes to any production code path**.

**Architecture:** A second, self-contained Python entry point (`transcribe_whisperx.py`) plus a shared helper module (`whisper_common.py`) live next to the existing `transcribe.py`. A second Dockerfile builds a separate image into a new ECR repo; a second ECS task definition reuses the existing cluster/ASG/capacity-provider/IAM roles. Nothing invokes the new task automatically — an operator runs it by hand via `aws ecs run-task` with `OUTPUT_KEY` overridden to benchmark-only S3 keys.

**Tech Stack:** Python 3.12 (stdlib unittest), whisperx 3.8.x (pulls torch 2.8/cu128, pyannote.audio 4.x, faster-whisper, ctranslate2), CDK TypeScript (jest), Docker, bash.

**Spec:** `docs/superpowers/specs/2026-08-28-whisperx-diarization-benchmark-design.md`

## Global Constraints

- **Zero production behavior change**: `transcribe.py`, the existing `Dockerfile`, `cmd/transcribe` (Go), and every existing CDK resource stay byte-identical except the one-line spec fix in Task 1. Only *additive* CDK resources are allowed.
- Output JSON contract (consumed by `cmd/summarize`): `results.transcripts[0].transcript`; `whisper_metadata.{engine, language, language_probability, duration_seconds, transcription_duration_seconds, segments, diarization}`; each segment `{start, end, text, speaker}` with `speaker` = `spk_N` normalized in first-appearance order (omitted when diarization unavailable).
- Same env-var contract as the legacy container: `MEETING_ID`, `USER_ID`, `AUDIO_KEY`, `OUTPUT_KEY`, `NUM_SPEAKERS`, `INITIAL_PROMPT`, `BUCKET_NAME`, `TABLE_NAME`, `VOCAB_KEY`, `MODEL_S3_KEY`.
- New engine string: `"whisperx-large-v3-gpu"` (legacy is `"whisper-large-v3-gpu"`).
- Diarization and alignment are **best-effort**: any failure logs and degrades (unlabeled segments / segment-level timestamps), never aborts transcription. On fatal error the container sets the meeting's DynamoDB `status=error` exactly like `transcribe.py` does. [As shipped: this is gated by `should_mark_meeting_error`, not unconditional — the real meeting row is only ever marked `error` for an explicit real-pipeline `OUTPUT_KEY` (exactly `transcripts/{meetingId}.json` or its `_part_NNN.json` variant); benchmark keys (`bench-transcripts/...`) and an empty/unset `OUTPUT_KEY` never mark the real meeting — an empty key is in fact now rejected outright by `validate_output_key` before any work happens, precisely so it can't reach this fatal-error path at all.]
- Python tests use stdlib `unittest` + `unittest.mock` only, stubbing heavy deps (`whisperx`, `boto3`) before import — follow `backend/whisper/test_transcribe.py`'s existing pattern exactly.
- Non-ASCII in tool/JSON parameters written as literal UTF-8 (project rule).
- Deploy note: merging files under `backend/whisper/**` will trigger `deploy-whisper.yml`, which rebuilds only the *legacy* Dockerfile (it copies just `transcribe.py`) — a harmless no-op rebuild. The new image is built/pushed manually per the runbook (Task 7); CI is not touched in Phase 1. [As shipped: during review, `deploy-whisper.yml`'s paths filter was narrowed to `transcribe.py`+`Dockerfile` specifically, so new whisperx files (`transcribe_whisperx.py`, `Dockerfile.whisperx`, etc.) do NOT trigger the legacy rebuild at all — this is stricter than the "harmless no-op" behavior described above, not a contradiction of the "CI not touched" claim.]

## File Structure

```
backend/whisper/
  whisper_common.py               (new) engine-neutral helpers: speaker assignment/normalization,
                                        S3 audio discovery, vocab prompt, tar stream-extract,
                                        transcript upload, error-status write  — DI-style (clients passed in)
  transcribe_whisperx.py          (new) WhisperX engine entry point (imports whisper_common)
  test_whisper_common.py          (new) unit tests for the pure/DI helpers
  test_transcribe_whisperx.py     (new) unit tests for the pure result-building logic
  Dockerfile.whisperx             (new) separate image (cu128 / torch 2.8 / pyannote 4.x)
  upload-whisperx-diarization-model.sh  (new) one-time operator staging of the pyannote 4.x pipeline to S3
infra/lib/whisper-stack.ts        (modify, additive only) new ECR repo + new task definition + outputs
infra/test/whisper-stack.test.ts  (new) jest assertions for the additive resources
docs/runbooks/whisperx-benchmark.md  (new) manual benchmark procedure incl. VRAM/CPU measurement
docs/superpowers/specs/2026-08-28-whisperx-diarization-benchmark-design.md  (modify: one wording fix)
infra/lib/storage-stack.ts             [Added during review] bench-transcripts/ lifecycle rule
infra/test/storage-stack.test.ts       [Added during review] jest assertions for the lifecycle rule
docs/architecture.md                   [Added during review] benchmark pipeline noted in architecture docs
.github/workflows/deploy-whisper.yml   [Added during review] paths filter narrowed to transcribe.py+Dockerfile
```

---

### Task 1: Branch sync + spec wording fix

**Files:**
- Modify: `docs/superpowers/specs/2026-08-28-whisperx-diarization-benchmark-design.md`

**Interfaces:**
- Produces: an implementation branch based on latest `origin/main` (which includes the `#165` root-volume fix that also touched `whisper-stack.ts` and `transcribe.py` — rebasing now avoids conflicts later).

- [ ] **Step 1: Create/enter an isolated worktree** (superpowers:using-git-worktrees) based on `docs/whisperx-diarization-benchmark-design`, so unrelated dirty files in the main tree are untouched.

- [ ] **Step 2: Rebase onto latest main**

```bash
git fetch origin
git rebase origin/main
```
Expected: clean rebase (both branch commits are docs-only).

- [ ] **Step 3: Fix the spec's internal contradiction.** The spec's "New files (existing files untouched)" section says "Both `transcribe.py` and `transcribe_whisperx.py` import this". In Phase 1 only `transcribe_whisperx.py` imports `whisper_common`; migrating `transcribe.py` onto it is Phase 2 (keeps the production image byte-identical during the benchmark). Edit the `whisper_common.py` bullet's sentence "Both `transcribe.py` and `transcribe_whisperx.py` import this — avoids duplicating..." to:

> In Phase 1 only `transcribe_whisperx.py` imports this (the production `transcribe.py` keeps its private copies so the legacy image stays byte-identical); migrating `transcribe.py` onto the shared module is part of the Phase 2 cutover. The helper implementations are copied verbatim from `transcribe.py` so the two stay behaviorally identical until then.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-08-28-whisperx-diarization-benchmark-design.md
git commit -m "docs(spec): whisper_common is whisperx-only in Phase 1 (transcribe.py migration is Phase 2)"
```

---

### Task 2: `whisper_common.py` — speaker assignment & normalization (pure functions)

**Files:**
- Create: `backend/whisper/whisper_common.py`
- Test: `backend/whisper/test_whisper_common.py`

**Interfaces:**
- Produces (used by Task 4):
  - `normalize_speaker_labels(segments: list[dict]) -> list[dict]` — rewrites each segment's raw `speaker` to `spk_N` by first appearance; segments without a `speaker` key are left untouched. Mutates and returns.
  - `raw_speaker_by_overlap(start: float, end: float, turns: list[tuple[float, float, str]]) -> str | None` — max-time-overlap turn label, midpoint-distance fallback on zero overlap, `None` if `turns` empty.
  - `assign_speakers(segments: list[dict], turns: list[tuple], use_words: bool = True) -> list[dict]` — sets raw labels (word-majority when the segment has timed `words` and `use_words`, else segment overlap) then applies `normalize_speaker_labels`. With empty `turns`, returns segments unchanged (no `speaker` keys added).

- [ ] **Step 1: Write the failing tests**

```python
"""Unit tests for whisper_common's pure speaker helpers.

stdlib unittest only; whisper_common's S3/Dynamo helpers take injected
clients, and its speaker functions import nothing heavy, so no stubbing is
needed to import the module itself.
"""

import unittest

import whisper_common


class TestNormalizeSpeakerLabels(unittest.TestCase):
    def test_first_appearance_order(self):
        segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a', 'speaker': 'SPEAKER_07'},
            {'start': 1.0, 'end': 2.0, 'text': 'b', 'speaker': 'SPEAKER_02'},
            {'start': 2.0, 'end': 3.0, 'text': 'c', 'speaker': 'SPEAKER_07'},
        ]
        result = whisper_common.normalize_speaker_labels(segments)
        self.assertEqual([s['speaker'] for s in result], ['spk_0', 'spk_1', 'spk_0'])

    def test_segments_without_speaker_untouched(self):
        segments = [{'start': 0.0, 'end': 1.0, 'text': 'a'}]
        result = whisper_common.normalize_speaker_labels(segments)
        self.assertNotIn('speaker', result[0])


class TestRawSpeakerByOverlap(unittest.TestCase):
    def test_max_overlap_wins(self):
        turns = [(0.0, 5.0, 'A'), (5.0, 10.0, 'B')]
        self.assertEqual(whisper_common.raw_speaker_by_overlap(4.0, 9.0, turns), 'B')

    def test_zero_overlap_falls_back_to_nearest_midpoint(self):
        turns = [(0.0, 4.8, 'A'), (5.2, 10.0, 'B')]
        # midpoint 4.95: A's midpoint 2.4 (dist 2.55) beats B's 7.6 (dist 2.65)
        self.assertEqual(whisper_common.raw_speaker_by_overlap(4.9, 5.0, turns), 'A')

    def test_empty_turns_returns_none(self):
        self.assertIsNone(whisper_common.raw_speaker_by_overlap(0.0, 1.0, []))


class TestAssignSpeakers(unittest.TestCase):
    def test_empty_turns_leaves_segments_unlabeled(self):
        segments = [{'start': 0.0, 'end': 2.0, 'text': 'hi'}]
        result = whisper_common.assign_speakers(segments, [])
        self.assertNotIn('speaker', result[0])

    def test_segment_overlap_path(self):
        segments = [
            {'start': 0.0, 'end': 5.0, 'text': 'a'},
            {'start': 5.0, 'end': 10.0, 'text': 'b'},
        ]
        turns = [(0.0, 5.0, 'SPEAKER_01'), (5.0, 10.0, 'SPEAKER_00')]
        result = whisper_common.assign_speakers(segments, turns)
        # first-appearance normalization: SPEAKER_01 -> spk_0
        self.assertEqual(result[0]['speaker'], 'spk_0')
        self.assertEqual(result[1]['speaker'], 'spk_1')

    def test_word_majority_path(self):
        # 2 of 3 words fall in SPEAKER_00's turn: majority wins even though
        # the segment's own span overlaps SPEAKER_01 more.
        segments = [{
            'start': 0.0, 'end': 10.0, 'text': 'a b c',
            'words': [
                {'start': 0.5, 'end': 1.0, 'word': 'a'},
                {'start': 1.5, 'end': 2.0, 'word': 'b'},
                {'start': 8.0, 'end': 8.5, 'word': 'c'},
            ],
        }]
        turns = [(0.0, 3.0, 'SPEAKER_00'), (3.0, 10.0, 'SPEAKER_01')]
        result = whisper_common.assign_speakers(segments, turns)
        self.assertEqual(result[0]['speaker'], 'spk_0')

    def test_segment_without_words_falls_back_to_overlap(self):
        segments = [{'start': 3.0, 'end': 10.0, 'text': 'x', 'words': []}]
        turns = [(0.0, 3.0, 'SPEAKER_00'), (3.0, 10.0, 'SPEAKER_01')]
        result = whisper_common.assign_speakers(segments, turns)
        self.assertEqual(result[0]['speaker'], 'spk_0')  # only label seen -> spk_0

    def test_words_without_timestamps_fall_back_to_overlap(self):
        # whisperx emits words without start/end when alignment partially fails
        segments = [{'start': 3.0, 'end': 10.0, 'text': 'x',
                     'words': [{'word': 'x'}]}]
        turns = [(0.0, 3.0, 'SPEAKER_00'), (3.0, 10.0, 'SPEAKER_01')]
        result = whisper_common.assign_speakers(segments, turns)
        self.assertEqual(result[0]['speaker'], 'spk_0')


if __name__ == '__main__':
    unittest.main()
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend/whisper && python3 -m unittest test_whisper_common -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'whisper_common'`

- [ ] **Step 3: Implement the speaker helpers**

```python
"""Engine-neutral helpers shared by Whisper batch-transcription entry points.

Phase 1 (WhisperX benchmark): only transcribe_whisperx.py imports this.
transcribe.py keeps its own private copies until the Phase 2 cutover so the
production image stays byte-identical during the benchmark. Implementations
are copied from transcribe.py where they overlap, so the two engines stay
behaviorally identical until Phase 2 unifies them.

Speaker helpers are pure (no boto3/torch imports); AWS helpers take injected
clients so unit tests can pass MagicMocks without patching module globals.
"""

from __future__ import annotations


def normalize_speaker_labels(segments: list[dict]) -> list[dict]:
    """Rewrites raw diarization labels ("SPEAKER_00") to spk_N in
    first-appearance order — the convention RefineTranscript and the
    frontend already rely on (same behavior as transcribe.py's
    _assign_speakers normalization). Segments lacking a speaker key are
    left untouched."""
    order: list[str] = []
    for seg in segments:
        raw = seg.get("speaker")
        if raw is None:
            continue
        if raw not in order:
            order.append(raw)
        seg["speaker"] = f"spk_{order.index(raw)}"
    return segments


def raw_speaker_by_overlap(start: float, end: float, turns: list[tuple]) -> str | None:
    """Label of the diarization turn with maximum time overlap against
    [start, end]; zero-overlap spans fall back to the turn whose midpoint
    is closest (same rule as transcribe.py's _assign_speakers)."""
    if not turns:
        return None
    best_label, best_overlap = None, 0.0
    for turn_start, turn_end, label in turns:
        overlap = min(end, turn_end) - max(start, turn_start)
        if overlap > best_overlap:
            best_label, best_overlap = label, overlap
    if best_label is None:
        mid = (start + end) / 2
        best_label = min(turns, key=lambda t: abs((t[0] + t[1]) / 2 - mid))[2]
    return best_label


def _raw_speaker_by_word_majority(seg: dict, turns: list[tuple]) -> str | None:
    """Majority vote over the segment's timed words (each word assigned by
    its own midpoint overlap). Returns None when the segment has no usable
    timed words, signalling the caller to fall back to segment overlap."""
    votes: dict[str, int] = {}
    for word in seg.get("words") or []:
        w_start, w_end = word.get("start"), word.get("end")
        if w_start is None or w_end is None:
            continue
        label = raw_speaker_by_overlap(w_start, w_end, turns)
        if label is not None:
            votes[label] = votes.get(label, 0) + 1
    if not votes:
        return None
    return max(votes, key=votes.get)


def assign_speakers(segments: list[dict], turns: list[tuple], use_words: bool = True) -> list[dict]:
    """Assigns each segment a diarization label (word-majority when timed
    words are available and use_words is True, else max segment overlap)
    and normalizes labels to spk_N. Empty turns -> segments unchanged,
    matching the legacy engine's behavior."""
    if not turns:
        return segments
    for seg in segments:
        label = None
        if use_words:
            label = _raw_speaker_by_word_majority(seg, turns)
        if label is None:
            label = raw_speaker_by_overlap(seg["start"], seg["end"], turns)
        if label is not None:
            seg["speaker"] = label
    return normalize_speaker_labels(segments)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend/whisper && python3 -m unittest test_whisper_common -v`
Expected: all PASS. Also run the legacy suite to confirm nothing broke: `python3 -m unittest test_transcribe -v` (PASS).

- [ ] **Step 5: Commit**

```bash
git add backend/whisper/whisper_common.py backend/whisper/test_whisper_common.py
git commit -m "feat(whisper): whisper_common speaker assignment/normalization helpers"
```

---

### Task 3: `whisper_common.py` — AWS helpers (dependency-injected)

**Files:**
- Modify: `backend/whisper/whisper_common.py` (append)
- Test: `backend/whisper/test_whisper_common.py` (append)

**Interfaces:**
- Produces (used by Task 4; all copied from `transcribe.py` with clients/config as parameters instead of module globals):
  - `audio_key_exists(s3, bucket: str, key: str) -> bool`
  - `find_audio_key(s3, bucket: str, user_id: str, meeting_id: str) -> str | None`
  - `load_custom_vocab_prompt(s3, bucket: str, vocab_key: str) -> str`
  - `stream_extract_tar(s3, bucket: str, key: str, dest_dir: str) -> None` — S3 StreamingBody → `tarfile.open(mode="r|gz")` → `extractall(dest_dir, filter="data")`
  - `upload_transcript(s3, bucket: str, output_key: str, result: dict) -> None` — `json.dumps(ensure_ascii=False, indent=2)`, `ContentType: application/json`
  - `mark_meeting_error(table, user_id: str, meeting_id: str) -> None` — best-effort status=error write, swallows its own exceptions
  - Module constants `MIN_AUDIO_SIZE_BYTES = 1024`, `SKIP_KEY_SUBSTRINGS = ("recording_progress", "checkpoint")`

- [ ] **Step 1: Write the failing tests** (append to `test_whisper_common.py`)

```python
import json
from unittest import mock

from botocore.exceptions import ClientError


class TestAudioKeyExists(unittest.TestCase):
    def test_true_on_head_success(self):
        s3 = mock.MagicMock()
        self.assertTrue(whisper_common.audio_key_exists(s3, 'b', 'k'))

    def test_false_on_404(self):
        s3 = mock.MagicMock()
        s3.head_object.side_effect = ClientError(
            {'Error': {'Code': '404'}}, 'HeadObject')
        self.assertFalse(whisper_common.audio_key_exists(s3, 'b', 'k'))

    def test_reraises_non_404(self):
        s3 = mock.MagicMock()
        s3.head_object.side_effect = ClientError(
            {'Error': {'Code': 'AccessDenied'}}, 'HeadObject')
        with self.assertRaises(ClientError):
            whisper_common.audio_key_exists(s3, 'b', 'k')


class TestFindAudioKey(unittest.TestCase):
    def test_picks_latest_valid_candidate(self):
        import datetime
        s3 = mock.MagicMock()
        s3.get_paginator.return_value.paginate.return_value = [{
            'Contents': [
                {'Key': 'audio/u/m/recording_progress.json', 'Size': 5000,
                 'LastModified': datetime.datetime(2026, 1, 3)},
                {'Key': 'audio/u/m/tiny.webm', 'Size': 10,
                 'LastModified': datetime.datetime(2026, 1, 2)},
                {'Key': 'audio/u/m/old.webm', 'Size': 5000,
                 'LastModified': datetime.datetime(2026, 1, 1)},
                {'Key': 'audio/u/m/new.webm', 'Size': 5000,
                 'LastModified': datetime.datetime(2026, 1, 2)},
            ],
        }]
        self.assertEqual(
            whisper_common.find_audio_key(s3, 'b', 'u', 'm'), 'audio/u/m/new.webm')

    def test_none_when_no_candidates(self):
        s3 = mock.MagicMock()
        s3.get_paginator.return_value.paginate.return_value = [{}]
        self.assertIsNone(whisper_common.find_audio_key(s3, 'b', 'u', 'm'))


class TestUploadTranscript(unittest.TestCase):
    def test_puts_pretty_utf8_json(self):
        s3 = mock.MagicMock()
        whisper_common.upload_transcript(s3, 'b', 'transcripts/m.json', {'k': '가'})
        kwargs = s3.put_object.call_args.kwargs
        self.assertEqual(kwargs['Bucket'], 'b')
        self.assertEqual(kwargs['Key'], 'transcripts/m.json')
        self.assertEqual(kwargs['ContentType'], 'application/json')
        self.assertIn('가', kwargs['Body'].decode('utf-8'))


class TestMarkMeetingError(unittest.TestCase):
    def test_swallows_dynamo_failure(self):
        table = mock.MagicMock()
        table.update_item.side_effect = RuntimeError('boom')
        whisper_common.mark_meeting_error(table, 'u', 'm')  # must not raise

    def test_writes_error_status(self):
        table = mock.MagicMock()
        whisper_common.mark_meeting_error(table, 'u', 'm')
        kwargs = table.update_item.call_args.kwargs
        self.assertEqual(kwargs['Key'], {'PK': 'USER#u', 'SK': 'MEETING#m'})
        self.assertEqual(kwargs['ExpressionAttributeValues'], {':s': 'error'})
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `cd backend/whisper && python3 -m unittest test_whisper_common -v`
Expected: new tests FAIL with `AttributeError` (functions not defined); Task 2 tests still PASS.

- [ ] **Step 3: Implement** (append to `whisper_common.py`; bodies copied from `transcribe.py` lines 190–221 / 172–187 / 41–49 / 328–333 / 347–352, parameterized)

```python
import json
import tarfile

from botocore.exceptions import ClientError

# Audio discovery filters — exclude empty/placeholder uploads and
# progress/checkpoint sidecars. (Values mirror transcribe.py.)
MIN_AUDIO_SIZE_BYTES = 1024
SKIP_KEY_SUBSTRINGS = ("recording_progress", "checkpoint")


def audio_key_exists(s3, bucket: str, key: str) -> bool:
    """True if the key exists. False only on 404; auth/throttle errors re-raise."""
    try:
        s3.head_object(Bucket=bucket, Key=key)
        return True
    except ClientError as e:
        code = e.response.get("Error", {}).get("Code", "")
        if code in ("404", "NoSuchKey", "NotFound"):
            return False
        raise


def find_audio_key(s3, bucket: str, user_id: str, meeting_id: str) -> str | None:
    """Most recently modified valid audio object under the meeting's prefix
    (paginated; re-recordings supersede older uploads)."""
    prefix = f"audio/{user_id}/{meeting_id}/"
    paginator = s3.get_paginator("list_objects_v2")
    candidates = []
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            if any(p in key for p in SKIP_KEY_SUBSTRINGS):
                continue
            if obj["Size"] < MIN_AUDIO_SIZE_BYTES:
                continue
            candidates.append(obj)
    if not candidates:
        return None
    return max(candidates, key=lambda o: o["LastModified"])["Key"]


def load_custom_vocab_prompt(s3, bucket: str, vocab_key: str) -> str:
    """TSV custom-vocabulary file -> space-joined display terms. Empty string
    on any failure (vocab is always optional)."""
    try:
        resp = s3.get_object(Bucket=bucket, Key=vocab_key)
        lines = resp["Body"].read().decode("utf-8").strip().split("\n")
        terms = []
        for line in lines[1:]:
            cols = line.split("\t")
            display = cols[2].strip() if len(cols) >= 3 else cols[0].strip()
            if display:
                terms.append(display)
        print(f"Custom vocab loaded: {len(terms)} terms")
        return " ".join(terms)
    except Exception as e:
        print(f"Custom vocab not available: {e}")
        return ""


def stream_extract_tar(s3, bucket: str, key: str, dest_dir: str) -> None:
    """Stream-extracts s3://bucket/key (a .tar.gz) into dest_dir without
    landing the archive on disk (root-volume headroom; see whisper-stack.ts
    blockDevices comment). filter="data" rejects path-escape members."""
    import os
    os.makedirs(dest_dir, exist_ok=True)
    obj = s3.get_object(Bucket=bucket, Key=key)
    with tarfile.open(fileobj=obj["Body"], mode="r|gz") as tar:
        tar.extractall(dest_dir, filter="data")


def upload_transcript(s3, bucket: str, output_key: str, result: dict) -> None:
    s3.put_object(
        Bucket=bucket,
        Key=output_key,
        Body=json.dumps(result, ensure_ascii=False, indent=2).encode("utf-8"),
        ContentType="application/json",
    )


def mark_meeting_error(table, user_id: str, meeting_id: str) -> None:
    """Best-effort status=error write on fatal failure; never raises."""
    try:
        table.update_item(
            Key={"PK": f"USER#{user_id}", "SK": f"MEETING#{meeting_id}"},
            UpdateExpression="SET #s = :s",
            ExpressionAttributeNames={"#s": "status"},
            ExpressionAttributeValues={":s": "error"},
        )
    except Exception:
        pass
```

Note: `boto3` is not imported by this module at all — callers create clients. `botocore` must be importable in the test env (it already is: `test_transcribe.py` imports boto3's machinery via the `pip install 'boto3<2'` convention in CLAUDE.md).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend/whisper && python3 -m unittest test_whisper_common -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/whisper/whisper_common.py backend/whisper/test_whisper_common.py
git commit -m "feat(whisper): DI-style S3/Dynamo helpers in whisper_common"
```

---

### Task 4: `transcribe_whisperx.py` — WhisperX engine entry point

**Files:**
- Create: `backend/whisper/transcribe_whisperx.py`
- Test: `backend/whisper/test_transcribe_whisperx.py`

**Interfaces:**
- Consumes: everything from Tasks 2–3 (`whisper_common.*`).
- Produces: the container entry point Task 5's Dockerfile runs. Pure function `build_result(segments, language, language_probability, duration_seconds, transcription_seconds, diarization_enabled, num_speakers_detected, alignment_enabled) -> dict` (unit-tested).
- New env vars (defaults matter — Task 6's CDK sets them): `WHISPERX_DIARIZATION_S3_KEY` (default `models/whisperx-diarization-4.x.tar.gz`), `WHISPERX_BATCH_SIZE` (default `8`).

- [ ] **Step 1: Write the failing test**

```python
"""Unit tests for transcribe_whisperx's pure result-building logic.

Same stubbing convention as test_transcribe.py: whisperx/torch are
container-only deps, and the module reads required env vars at import time,
so both are stubbed before import."""

import os
import sys
import types
import unittest
from unittest import mock

os.environ['BUCKET_NAME'] = 'test-bucket'
os.environ['TABLE_NAME'] = 'test-table'

for name in ('whisperx', 'torch'):
    if name not in sys.modules:
        sys.modules[name] = types.ModuleType(name)

with mock.patch('boto3.client'), mock.patch('boto3.resource'):
    import transcribe_whisperx


class TestBuildResult(unittest.TestCase):
    def test_output_schema_matches_summarize_contract(self):
        segments = [
            {'start': 0.0, 'end': 2.0, 'text': '안녕하세요', 'speaker': 'spk_0',
             'words': [{'word': '안녕하세요', 'start': 0.1, 'end': 1.9}]},
            {'start': 2.0, 'end': 4.0, 'text': '반갑습니다'},
        ]
        result = transcribe_whisperx.build_result(
            segments=segments, language='ko', language_probability=0.0,
            duration_seconds=4.0, transcription_seconds=1.5,
            diarization_enabled=True, num_speakers_detected=1,
            alignment_enabled=True)

        self.assertEqual(result['status'], 'COMPLETED')
        self.assertEqual(result['results']['transcripts'][0]['transcript'],
                         '안녕하세요 반갑습니다')
        meta = result['whisper_metadata']
        self.assertEqual(meta['engine'], 'whisperx-large-v3-gpu')
        self.assertEqual(meta['language'], 'ko')
        self.assertEqual(meta['duration_seconds'], 4.0)
        self.assertEqual(meta['diarization'],
                         {'enabled': True, 'num_speakers_detected': 1})
        self.assertTrue(meta['alignment_enabled'])
        # words are an internal alignment artifact -- stripped from output,
        # and start/end rounded like the legacy engine's segments
        self.assertEqual(meta['segments'][0],
                         {'start': 0.0, 'end': 2.0, 'text': '안녕하세요',
                          'speaker': 'spk_0'})
        self.assertEqual(meta['segments'][1],
                         {'start': 2.0, 'end': 4.0, 'text': '반갑습니다'})


if __name__ == '__main__':
    unittest.main()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend/whisper && python3 -m unittest test_transcribe_whisperx -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'transcribe_whisperx'`

- [ ] **Step 3: Write the full entry point**

```python
"""WhisperX batch transcription + pyannote 4.x diarization entry point.

Benchmark twin of transcribe.py (Phase 1, ADR-019 follow-up): same env-var
and output-JSON contract, different engine. Never wired into cmd/transcribe;
operators invoke it via `aws ecs run-task` with OUTPUT_KEY overridden (see
docs/runbooks/whisperx-benchmark.md).

Differences from transcribe.py:
- whisperx VAD-batched inference instead of sequential faster-whisper
- optional wav2vec2 forced alignment (word timestamps) -- best-effort, Korean
  availability unconfirmed; degrades to segment-level timestamps
- pyannote.audio 4.x diarization pipeline (staged to S3 by
  upload-whisperx-diarization-model.sh) with word-majority speaker
  assignment when aligned words exist
"""

from __future__ import annotations

import os
import sys
import time

import boto3

import whisper_common as common

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
BUCKET = os.environ["BUCKET_NAME"]
TABLE = os.environ["TABLE_NAME"]
VOCAB_KEY = os.environ.get("VOCAB_KEY", "config/custom-vocabulary.txt")
MODEL_S3_KEY = os.environ.get("MODEL_S3_KEY", "models/faster-whisper-large-v3.tar.gz")
MODEL_LOCAL_DIR = "/tmp/whisper-model"
DIARIZATION_S3_KEY = os.environ.get(
    "WHISPERX_DIARIZATION_S3_KEY", "models/whisperx-diarization-4.x.tar.gz")
DIARIZATION_LOCAL_DIR = "/tmp/whisperx-diarization-model"
BATCH_SIZE = int(os.environ.get("WHISPERX_BATCH_SIZE", "8"))
SAMPLE_RATE = 16000  # whisperx.load_audio's fixed output rate

s3 = boto3.client("s3", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)


def _ensure_model() -> str:
    if os.path.exists(os.path.join(MODEL_LOCAL_DIR, "model.bin")):
        print("Whisper model already cached locally")
        return MODEL_LOCAL_DIR
    print(f"Downloading whisper model from s3://{BUCKET}/{MODEL_S3_KEY}")
    start = time.time()
    common.stream_extract_tar(s3, BUCKET, MODEL_S3_KEY, MODEL_LOCAL_DIR)
    print(f"Whisper model ready ({time.time() - start:.0f}s)")
    return MODEL_LOCAL_DIR


def _ensure_diarization_config() -> str | None:
    """Local pipeline config.yaml path, or None (diarization is best-effort:
    transcription must never fail because the bundle isn't staged yet)."""
    config_path = os.path.join(DIARIZATION_LOCAL_DIR, "pipeline", "config.yaml")
    if os.path.exists(config_path):
        print("Diarization model already cached locally")
        return config_path
    print(f"Downloading diarization model from s3://{BUCKET}/{DIARIZATION_S3_KEY}")
    try:
        start = time.time()
        common.stream_extract_tar(s3, BUCKET, DIARIZATION_S3_KEY, DIARIZATION_LOCAL_DIR)
        print(f"Diarization model ready ({time.time() - start:.0f}s)")
        return config_path
    except Exception as e:
        print(f"Diarization model unavailable, skipping diarization: {e}")
        return None


def _diarize(config_path: str, audio_path: str, num_speakers: int | None) -> list[tuple]:
    """pyannote 4.x diarization -> [(start, end, label)]. [] on any failure
    (caller falls back to unlabeled segments). NUM_SPEAKERS is a
    max_speakers upper bound, not an exact count (same as transcribe.py)."""
    try:
        import subprocess
        import torch
        from pyannote.audio import Pipeline

        wav_path = "/tmp/audio-16k-mono.wav"
        subprocess.run(
            ["ffmpeg", "-y", "-i", audio_path, "-ar", "16000", "-ac", "1", wav_path],
            check=True, capture_output=True,
        )
        pipeline = Pipeline.from_pretrained(config_path)
        pipeline.to(torch.device("cuda"))
        kwargs = {"max_speakers": num_speakers} if num_speakers else {}
        diarization = pipeline(wav_path, **kwargs)
        return [
            (turn.start, turn.end, label)
            for turn, _, label in diarization.itertracks(yield_label=True)
        ]
    except Exception as e:
        print(f"Diarization failed, falling back to unlabeled segments: {e}")
        return []


def _try_align(segments: list[dict], audio, language: str) -> tuple[list[dict], bool]:
    """Best-effort wav2vec2 forced alignment for word timestamps. Korean
    model availability in whisperx's registry is unconfirmed (see design
    spec) -- on ANY failure return the input segments unchanged and False."""
    try:
        import whisperx
        align_model, metadata = whisperx.load_align_model(
            language_code=language, device="cuda")
        aligned = whisperx.align(segments, align_model, metadata, audio, "cuda")
        print("Alignment succeeded (word-level timestamps available)")
        return aligned["segments"], True
    except Exception as e:
        print(f"Alignment unavailable, using segment-level timestamps: {e}")
        return segments, False


def build_result(segments: list[dict], language: str, language_probability: float,
                 duration_seconds: float, transcription_seconds: float,
                 diarization_enabled: bool, num_speakers_detected: int,
                 alignment_enabled: bool) -> dict:
    """Pure: renders the exact transcript JSON cmd/summarize consumes
    (same shape as transcribe.py's output; engine string differs, plus an
    additive alignment_enabled flag Go ignores)."""
    out_segments = []
    for seg in segments:
        out = {
            "start": round(seg["start"], 2),
            "end": round(seg["end"], 2),
            "text": seg["text"].strip(),
        }
        if "speaker" in seg:
            out["speaker"] = seg["speaker"]
        out_segments.append(out)
    transcript_text = " ".join(s["text"] for s in out_segments)
    return {
        "results": {"transcripts": [{"transcript": transcript_text}]},
        "status": "COMPLETED",
        "whisper_metadata": {
            "engine": "whisperx-large-v3-gpu",
            "language": language,
            "language_probability": round(language_probability, 3),
            "duration_seconds": round(duration_seconds, 1),
            "transcription_duration_seconds": round(transcription_seconds, 1),
            "segments": out_segments,
            "diarization": {
                "enabled": diarization_enabled,
                "num_speakers_detected": num_speakers_detected,
            },
            "alignment_enabled": alignment_enabled,
        },
    }


def main():
    meeting_id = os.environ["MEETING_ID"]
    user_id = os.environ["USER_ID"]
    audio_key = os.environ.get("AUDIO_KEY")
    if audio_key and not common.audio_key_exists(s3, BUCKET, audio_key):
        print(f"AUDIO_KEY {audio_key!r} not found in S3; falling back to prefix scan")
        audio_key = None
    if not audio_key:
        audio_key = common.find_audio_key(s3, BUCKET, user_id, meeting_id)
    if not audio_key:
        raise RuntimeError(f"No audio file found for meeting {meeting_id}")

    basename = audio_key.rsplit("/", 1)[-1]
    ext = basename.rsplit(".", 1)[-1] if "." in basename else "bin"
    local_path = f"/tmp/audio.{ext}"
    print(f"Downloading s3://{BUCKET}/{audio_key}")
    s3.download_file(BUCKET, audio_key, local_path)

    vocab_prompt = common.load_custom_vocab_prompt(s3, BUCKET, VOCAB_KEY)
    env_prompt = os.environ.get("INITIAL_PROMPT", "").strip()
    if env_prompt:
        vocab_prompt = f"{vocab_prompt} {env_prompt}".strip()

    model_dir = _ensure_model()

    import whisperx
    print(f"Loading WhisperX large-v3 (GPU float16, batch_size={BATCH_SIZE})...")
    asr_options = {"initial_prompt": vocab_prompt} if vocab_prompt else None
    model = whisperx.load_model(
        model_dir, device="cuda", compute_type="float16",
        language="ko", asr_options=asr_options)

    print("Transcribing (batched)...")
    start = time.time()
    audio = whisperx.load_audio(local_path)
    tx = model.transcribe(audio, batch_size=BATCH_SIZE)
    segments = [
        {"start": seg["start"], "end": seg["end"], "text": seg["text"]}
        for seg in tx["segments"]
    ]
    elapsed = time.time() - start
    language = tx.get("language", "ko")
    duration_seconds = len(audio) / SAMPLE_RATE
    print(f"Done: {len(segments)} segments in {elapsed:.1f}s")

    segments, alignment_enabled = _try_align(segments, audio, language)

    num_speakers_env = os.environ.get("NUM_SPEAKERS", "").strip()
    num_speakers = int(num_speakers_env) if num_speakers_env.isdigit() else None

    diarization_config = _ensure_diarization_config()
    num_speakers_detected = 0
    if diarization_config and segments:
        print("Diarizing (pyannote 4.x)...")
        diarize_start = time.time()
        turns = _diarize(diarization_config, local_path, num_speakers)
        if turns:
            segments = common.assign_speakers(segments, turns)
            num_speakers_detected = len({t[2] for t in turns})
            print(f"Diarization done in {time.time() - diarize_start:.1f}s, "
                  f"{num_speakers_detected} speaker(s) detected")
        else:
            print("Diarization produced no turns; segments left unlabeled")

    result = build_result(
        segments=segments, language=language,
        # whisperx's batched pipeline doesn't expose a language probability;
        # 0.0 keeps the field present for schema parity without faking confidence.
        language_probability=0.0,
        duration_seconds=duration_seconds, transcription_seconds=elapsed,
        diarization_enabled=bool(diarization_config),
        num_speakers_detected=num_speakers_detected,
        alignment_enabled=alignment_enabled)

    output_key = os.environ.get("OUTPUT_KEY", "").strip() or f"transcripts/{meeting_id}.json"
    common.upload_transcript(s3, BUCKET, output_key, result)
    print(f"Uploaded s3://{BUCKET}/{output_key}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        meeting_id = os.environ.get("MEETING_ID", "")
        user_id = os.environ.get("USER_ID", "")
        if meeting_id and user_id:
            common.mark_meeting_error(table, user_id, meeting_id)
        sys.exit(1)
```

- [ ] **Step 4: Run tests**

Run: `cd backend/whisper && python3 -m unittest test_transcribe_whisperx test_whisper_common test_transcribe -v`
Expected: all PASS (all three suites).

- [ ] **Step 5: Commit**

```bash
git add backend/whisper/transcribe_whisperx.py backend/whisper/test_transcribe_whisperx.py
git commit -m "feat(whisper): WhisperX benchmark engine entry point (pyannote 4.x)"
```

---

### Task 5: `Dockerfile.whisperx`

**Files:**
- Create: `backend/whisper/Dockerfile.whisperx`

**Interfaces:**
- Consumes: `transcribe_whisperx.py`, `whisper_common.py` (Task 2–4).
- Produces: the image the runbook (Task 7) builds and pushes to the `ttobak-whisperx` ECR repo (Task 6).

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# WhisperX benchmark image (Phase 1) -- separate from ./Dockerfile so the
# production image stays untouched while pyannote 4.x is evaluated.
# cu128 matches the DLC-validated combo (whisperx 3.8.x / torch 2.8 / CUDA 12.8).
FROM nvidia/cuda:12.8.1-cudnn-runtime-ubuntu24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg && \
    rm -rf /var/lib/apt/lists/*

# whisperx pins its own torch/pyannote.audio(>=4)/faster-whisper/ctranslate2
# versions -- don't pin them separately here, that's the version-skew trap
# the separate image exists to avoid. The extra index serves cu128 wheels.
RUN pip3 install --no-cache-dir --break-system-packages \
    --extra-index-url https://download.pytorch.org/whl/cu128 \
    "whisperx>=3.8,<3.9" boto3

# Whisper CT2 weights + diarization pipeline are loaded from S3 at runtime
# (same pattern as the legacy image; see whisper_common.stream_extract_tar).
COPY whisper_common.py transcribe_whisperx.py /app/
WORKDIR /app

ENTRYPOINT ["python3", "transcribe_whisperx.py"]
```

- [ ] **Step 2: Sanity-check the build.** This host is ARM64; the image targets x86_64 GPU instances. If buildx emulation is available, a syntax/dependency-resolution smoke test is:

```bash
cd backend/whisper && docker build --platform linux/amd64 -f Dockerfile.whisperx -t ttobak-whisperx:local . 2>&1 | tail -20
```
If emulated pip-install of torch is impractically slow or fails on this host, that's acceptable — the authoritative build happens on the x86 runner path documented in the runbook (Task 7). In that case verify at minimum that `docker build` parses the file (fails only at the pip step, not at parse).

- [ ] **Step 3: Commit**

```bash
git add backend/whisper/Dockerfile.whisperx
git commit -m "feat(whisper): Dockerfile.whisperx benchmark image (cu128/pyannote4)"
```

---

### Task 6: `upload-whisperx-diarization-model.sh`

**Files:**
- Create: `backend/whisper/upload-whisperx-diarization-model.sh`

**Interfaces:**
- Produces: `s3://<bucket>/models/whisperx-diarization-4.x.tar.gz` containing `pipeline/config.yaml` (rewritten to local paths) + model checkpoints — the layout `_ensure_diarization_config` expects.

- [ ] **Step 1: Confirm the pyannote 4.x pipeline repo ID.** The spec deliberately left this unverified. Check https://huggingface.co/pyannote (WebFetch or browser) for the pyannote.audio 4.x flagship diarization pipeline repo (expected name family: `pyannote/speaker-diarization-community-1`; do NOT trust this guess — confirm on HF, including which sub-model repos its `config.yaml` references and their gating terms). Record the confirmed ID in the script's `PIPELINE_REPO` default and in a comment.

- [ ] **Step 2: Write the script** (modeled on `upload-diarization-model.sh`, but generic: it parses the downloaded pipeline `config.yaml` for referenced checkpoints instead of hardcoding sub-repo names, since the 4.x pipeline's internal layout is part of what Step 1 confirms)

```bash
#!/bin/bash
set -euo pipefail

BUCKET="${1:-ttobak-assets-180294183052}"
REGION="${2:-ap-northeast-2}"
# CONFIRMED-ON-HF value from Step 1 goes here:
PIPELINE_REPO="${PIPELINE_REPO:-pyannote/speaker-diarization-community-1}"
S3_KEY="models/whisperx-diarization-4.x.tar.gz"

if [ -z "${HF_TOKEN:-}" ]; then
  echo "ERROR: HF_TOKEN is not set. Accept the gated-model terms for" >&2
  echo "  ${PIPELINE_REPO} (and any sub-model repos its config references)" >&2
  echo "  on huggingface.co, then export HF_TOKEN=<your token> and re-run." >&2
  exit 1
fi

echo "Downloading ${PIPELINE_REPO} (pyannote.audio 4.x pipeline)..."
pip3 install -q huggingface_hub pyyaml

STAGE_DIR=$(python3 - <<'EOF'
import os, re, shutil, tempfile
import yaml
from huggingface_hub import snapshot_download

token = os.environ["HF_TOKEN"]
repo = os.environ["PIPELINE_REPO"]
stage = tempfile.mkdtemp(prefix="whisperx-diar-stage-")

pipeline_dir = snapshot_download(repo, token=token)
shutil.copytree(pipeline_dir, os.path.join(stage, "pipeline"), dirs_exist_ok=True)

config_path = os.path.join(stage, "pipeline", "config.yaml")
with open(config_path) as f:
    config = yaml.safe_load(f)

# Stage every HF repo the pipeline config references (segmentation/embedding
# checkpoints appear as "org/name" or "org/name@rev" strings) and rewrite the
# config to local paths so the container needs no runtime HF access.
def rewrite(node):
    if isinstance(node, dict):
        return {k: rewrite(v) for k, v in node.items()}
    if isinstance(node, list):
        return [rewrite(v) for v in node]
    if isinstance(node, str) and re.fullmatch(r"[\w.-]+/[\w.-]+(@[\w.-]+)?", node):
        ref = node.split("@")[0]
        local_name = ref.replace("/", "__")
        local_dir = os.path.join(stage, local_name)
        if not os.path.isdir(local_dir):
            print(f"  staging referenced repo: {ref}")
            src = snapshot_download(ref, token=token)
            shutil.copytree(src, local_dir, dirs_exist_ok=True)
        # container extracts the archive into DIARIZATION_LOCAL_DIR; config
        # lives at <root>/pipeline/config.yaml, referenced repos at <root>/<name>
        return os.path.join("..", local_name)
    return node

with open(config_path, "w") as f:
    yaml.safe_dump(rewrite(config), f, sort_keys=False)

print(stage)
EOF
)
STAGE_DIR=$(echo "$STAGE_DIR" | tail -1)

echo "Staged at: ${STAGE_DIR}"
echo "Compressing..."
tar -czf /tmp/whisperx-diarization.tar.gz -C "$STAGE_DIR" .
du -sh /tmp/whisperx-diarization.tar.gz

echo "Uploading to s3://${BUCKET}/${S3_KEY}"
aws s3 cp /tmp/whisperx-diarization.tar.gz "s3://${BUCKET}/${S3_KEY}" --region "${REGION}"
rm /tmp/whisperx-diarization.tar.gz
echo "Done."
```

Export `PIPELINE_REPO` before the heredoc runs: add `export PIPELINE_REPO HF_TOKEN` right after the default assignment (the Python block reads both from the environment).

Known follow-up risk (accepted, same as the 3.1 script's NOTE comment): pyannote config schemas shift between versions — the relative-path rewrite (`../<name>`) must be validated by actually loading the staged bundle once (`Pipeline.from_pretrained(<stage>/pipeline/config.yaml)`) during the runbook's first benchmark run; if 4.x resolves paths differently, fix the rewrite then.

- [ ] **Step 3: Syntax check**

```bash
bash -n backend/whisper/upload-whisperx-diarization-model.sh && echo OK
```
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
chmod +x backend/whisper/upload-whisperx-diarization-model.sh
git add backend/whisper/upload-whisperx-diarization-model.sh
git commit -m "feat(whisper): pyannote 4.x pipeline staging script for whisperx benchmark"
```

---

### Task 7: CDK — additive WhisperX ECR repo + task definition

**Files:**
- Modify: `infra/lib/whisper-stack.ts` (append only — do not touch existing resources)
- Test: `infra/test/whisper-stack.test.ts` (new)

**Interfaces:**
- Consumes: the existing `executionRole`, `taskRole`, cluster/ASG (already in the stack's scope). [As shipped: dedicated `ttobak-whisperx-task-role`, not the reused legacy `taskRole` — round-10 review found reusing it gave every bench run full bucket read/write + table read/write; round-11 further scoped its S3 write grant down to `bench-transcripts/*` only, dropping the `transcripts/*` write it originally also carried.]
- Produces: exported consts `WHISPERX_TASK_FAMILY = 'ttobak-whisperx'`, `WHISPERX_CONTAINER_NAME = 'whisperx'`; public props `whisperxTaskDefinition`, `whisperxEcrRepository`; CFN outputs `WhisperXTaskDefArn` (export `TtobakWhisperXTaskDefArn`), `WhisperXEcrRepoUri` (export `TtobakWhisperXEcrUri`). The runbook (Task 8) uses these names verbatim.

- [ ] **Step 1: Write the failing jest test**

```typescript
import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { WhisperStack } from '../lib/whisper-stack';

// Vpc.fromLookup returns a dummy VPC in tests when the lookup context is
// absent, as long as the stack env is concrete.
const env = { account: '123456789012', region: 'ap-northeast-2' };

function synth(): Template {
  const app = new cdk.App();
  const deps = new cdk.Stack(app, 'Deps', { env });
  const stack = new WhisperStack(app, 'TestWhisperStack', {
    env,
    vpcId: 'vpc-12345',
    bucket: new s3.Bucket(deps, 'B'),
    table: new dynamodb.Table(deps, 'T', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    }),
  });
  return Template.fromStack(stack);
}

describe('WhisperStack whisperx benchmark additions', () => {
  test('has a second ECR repo for the whisperx image', () => {
    const template = synth();
    template.resourceCountIs('AWS::ECR::Repository', 2);
    template.hasResourceProperties('AWS::ECR::Repository', {
      RepositoryName: 'ttobak-whisperx',
    });
  });

  test('has a whisperx task definition alongside the legacy one', () => {
    const template = synth();
    template.resourceCountIs('AWS::ECS::TaskDefinition', 2);
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Family: 'ttobak-whisperx',
      ContainerDefinitions: Match.arrayWith([
        Match.objectLike({
          Name: 'whisperx',
          Environment: Match.arrayWith([
            Match.objectLike({ Name: 'WHISPERX_DIARIZATION_S3_KEY' }),
            Match.objectLike({ Name: 'WHISPERX_BATCH_SIZE' }),
          ]),
        }),
      ]),
    });
  });

  test('legacy task definition family is untouched', () => {
    const template = synth();
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Family: 'ttobak-whisper',
    });
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd infra && npx jest test/whisper-stack.test.ts`
Expected: FAIL (1 ECR repo / 1 task def found; whisperx properties missing).

- [ ] **Step 3: Implement in `whisper-stack.ts`.** Add exports next to the existing consts:

```typescript
export const WHISPERX_TASK_FAMILY = 'ttobak-whisperx';
export const WHISPERX_CONTAINER_NAME = 'whisperx';
```

Add public props next to the existing ones:

```typescript
public readonly whisperxTaskDefinition: ecs.Ec2TaskDefinition;
public readonly whisperxEcrRepository: ecr.Repository;
```

Append after the existing container/outputs block (reusing the in-scope `executionRole`/`taskRole`):

```typescript
    // --- WhisperX benchmark engine (Phase 1, ADR-019 follow-up) ---
    // Additive twin of the resources above: separate image + task def so
    // pyannote 4.x can be benchmarked against the production engine without
    // touching it. Shares the same cluster/ASG/capacity provider (GPU pool
    // is not split) and the same IAM roles. Nothing invokes this task
    // definition automatically -- operators run it by hand, see
    // docs/runbooks/whisperx-benchmark.md.
    this.whisperxEcrRepository = new ecr.Repository(this, 'WhisperXRepo', {
      repositoryName: 'ttobak-whisperx',
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      lifecycleRules: [{ maxImageCount: 5 }],
    });

    this.whisperxTaskDefinition = new ecs.Ec2TaskDefinition(this, 'WhisperXTaskDef', {
      family: WHISPERX_TASK_FAMILY,
      executionRole,
      taskRole,
      networkMode: ecs.NetworkMode.HOST,
    });

    this.whisperxTaskDefinition.addContainer('whisperx', {
      containerName: WHISPERX_CONTAINER_NAME,
      image: ecs.ContainerImage.fromEcrRepository(this.whisperxEcrRepository, 'latest'),
      memoryLimitMiB: 12288,
      gpuCount: 1,
      environment: {
        BUCKET_NAME: props.bucket.bucketName,
        TABLE_NAME: props.table.tableName,
        AWS_REGION: cdk.Aws.REGION,
        VOCAB_KEY: 'config/custom-vocabulary.txt',
        // CT2 large-v3 weights are engine-compatible -- reuse the staged archive.
        MODEL_S3_KEY: 'models/faster-whisper-large-v3.tar.gz',
        WHISPERX_DIARIZATION_S3_KEY: 'models/whisperx-diarization-4.x.tar.gz',
        // Conservative default: whisperx's own default of 16 raises peak
        // VRAM on the shared 24GB A10G (see design spec's sizing risk).
        WHISPERX_BATCH_SIZE: '8',
      },
      logging: ecs.LogDrivers.awsLogs({ streamPrefix: 'whisperx' }),
      essential: true,
    });

    new cdk.CfnOutput(this, 'WhisperXTaskDefArn', {
      value: this.whisperxTaskDefinition.taskDefinitionArn,
      exportName: 'TtobakWhisperXTaskDefArn',
    });
    new cdk.CfnOutput(this, 'WhisperXEcrRepoUri', {
      value: this.whisperxEcrRepository.repositoryUri,
      exportName: 'TtobakWhisperXEcrUri',
    });
```

- [ ] **Step 4: Run the new test, then the whole suite + synth**

```bash
cd infra && npx jest test/whisper-stack.test.ts   # PASS
npm test                                          # all suites PASS
npx cdk synth TtobakWhisperStack > /dev/null && echo SYNTH-OK
```
Expected: PASS / PASS / `SYNTH-OK`.

- [ ] **Step 5: Update `docs/INFRA-SPEC.md`** (Auto-Sync rule: infra change → INFRA-SPEC). Add a short entry under the Whisper stack section: second ECR repo `ttobak-whisperx` + task definition family `ttobak-whisperx` (benchmark-only, never invoked automatically; see `docs/runbooks/whisperx-benchmark.md`).

- [ ] **Step 6: Commit**

```bash
git add infra/lib/whisper-stack.ts infra/test/whisper-stack.test.ts docs/INFRA-SPEC.md
git commit -m "feat(infra): additive whisperx benchmark ECR repo + ECS task definition"
```

---

### Task 8: Benchmark runbook

**Files:**
- Create: `docs/runbooks/whisperx-benchmark.md`

**Interfaces:**
- Consumes: everything above — env var names, task family `ttobak-whisperx`, container name `whisperx`, cluster `ttobak-whisper`, capacity provider `ttobak-whisper-spot`, output-key convention.

- [ ] **Step 1: Write the runbook** with these sections (concrete commands, account 180294183052 / region ap-northeast-2):

1. **One-time setup**
   - Build & push the image (x86_64 host or CI runner `ttobak-x86`; this repo's dev host is ARM):
     ```bash
     aws ecr get-login-password --region ap-northeast-2 \
       | docker login --username AWS --password-stdin 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com
     cd backend/whisper
     docker build --platform linux/amd64 -f Dockerfile.whisperx \
       -t 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest .
     docker push 180294183052.dkr.ecr.ap-northeast-2.amazonaws.com/ttobak-whisperx:latest
     ```
   - Stage the diarization bundle: `HF_TOKEN=... ./upload-whisperx-diarization-model.sh`
   - Deploy the stack: `cd infra && npx cdk deploy TtobakWhisperStack --exclusively` (never `--all`; per CLAUDE.md).
2. **Selecting meetings**: pick 3–5 `done` meetings with varied speaker counts/lengths; note each meeting's `userId`, `meetingId`, audio S3 key (from the meeting's `AudioKeys`).
3. **Running a benchmark pair** — for each meeting run BOTH task defs with the same audio, output to bench-only keys (never the real `transcripts/{meetingId}.json`):
   ```bash
   for TD in ttobak-whisper ttobak-whisperx; do
     SUFFIX=$([ "$TD" = ttobak-whisper ] && echo legacy || echo whisperx)
     CONTAINER=$([ "$TD" = ttobak-whisper ] && echo whisper || echo whisperx)
     aws ecs run-task --cluster ttobak-whisper --task-definition "$TD" --count 1 \
       --capacity-provider-strategy capacityProvider=ttobak-whisper-spot,weight=1 \
       --overrides "{\"containerOverrides\":[{\"name\":\"$CONTAINER\",\"environment\":[
         {\"name\":\"MEETING_ID\",\"value\":\"$MEETING_ID\"},
         {\"name\":\"USER_ID\",\"value\":\"$USER_ID\"},
         {\"name\":\"AUDIO_KEY\",\"value\":\"$AUDIO_KEY\"},
         {\"name\":\"OUTPUT_KEY\",\"value\":\"transcripts/${MEETING_ID}_bench_${SUFFIX}.json\"}]}]}"
   done
   ```
   Note: `OUTPUT_KEY` under `transcripts/` WILL trigger the summarize EventBridge rule for the bench key — check whether `cmd/summarize` looks up the meeting by the key's meetingId segment; the `_bench_*` suffix makes the key parse as a different meeting id, so summarize will fail its meeting lookup and log an error. That's noisy but harmless; alternatively write bench output under `bench-transcripts/` (no EventBridge rule) — **runbook must state which and why (recommend `bench-transcripts/`; verify the transcripts EventBridge rule's prefix filter in `infra/lib/gateway-stack.ts` before choosing)**.
4. **Resource measurement per WhisperX run** (resolves the spec's VRAM sizing unknown):
   - Find the container instance: `aws ecs list-tasks --cluster ttobak-whisper` → `describe-tasks` → `containerInstanceArn` → EC2 instance id.
   - Sample GPU: `aws ssm send-command --instance-ids $IID --document-name AWS-RunShellScript --parameters 'commands=["for i in $(seq 60); do nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader; sleep 5; done"]'` then fetch output; record peak `memory.used`.
   - Container CPU/mem: same SSM channel, `docker stats --no-stream`.
5. **Comparing outputs** (jq, per meeting):
   ```bash
   for S in legacy whisperx; do
     aws s3 cp "s3://ttobak-assets-180294183052/<bench prefix>/${MEETING_ID}_bench_${S}.json" "/tmp/${S}.json"
     echo "== $S: speakers =="; jq '[.whisper_metadata.segments[].speaker] | unique' "/tmp/${S}.json"
     echo "== $S: turn timeline =="
     jq -r '.whisper_metadata.segments[] | "\(.start)\t\(.end)\t\(.speaker // "-")\t\(.text)"' "/tmp/${S}.json" | head -80
   done
   ```
   Judge qualitatively: detected speaker count vs known participants, turn-boundary placement on known multi-speaker stretches, over/under-splitting.
6. **Recording results**: table per meeting (duration, participants, legacy speakers detected, whisperx speakers detected, peak VRAM, wall-clock, qualitative verdict) — results feed the Phase 2 go/no-go and its ADR.
7. **Cleanup**: delete bench S3 objects; ASG scales itself back to 0.

- [ ] **Step 2: Verify the EventBridge question in section 3 while writing it** — grep the transcripts rule:

```bash
grep -n "transcripts" infra/lib/gateway-stack.ts | head
```
Set the runbook's bench prefix accordingly (if the rule matches prefix `transcripts/`, use `bench-transcripts/`).

- [ ] **Step 3: Commit**

```bash
git add docs/runbooks/whisperx-benchmark.md
git commit -m "docs(runbook): whisperx diarization benchmark procedure"
```

---

### Task 9: Final verification + PR

- [ ] **Step 1: Full test pass**

```bash
cd backend/whisper && python3 -m unittest test_transcribe test_whisper_common test_transcribe_whisperx -v
cd ../../infra && npm test && npx cdk synth > /dev/null && echo SYNTH-OK
```
Expected: all PASS, `SYNTH-OK`.

- [ ] **Step 2: Confirm zero production diffs.** `git diff origin/main -- backend/whisper/transcribe.py backend/whisper/Dockerfile backend/cmd backend/internal frontend/` must be empty except files this plan created/modified. `git diff origin/main -- infra/lib/whisper-stack.ts` must show only appended whisperx resources.

- [ ] **Step 3: Push and open PR**

```bash
git push -u origin HEAD
gh pr create --title "feat(whisper): WhisperX diarization benchmark engine (Phase 1, no product changes)" \
  --body "$(cat <<'EOF'
Implements docs/superpowers/specs/2026-08-28-whisperx-diarization-benchmark-design.md (Phase 1).

- New isolated WhisperX engine: transcribe_whisperx.py + whisper_common.py + Dockerfile.whisperx
- Additive CDK: ttobak-whisperx ECR repo + ECS task definition (same cluster/ASG/roles); nothing invokes it automatically
- pyannote 4.x pipeline staging script (operator-run)
- Benchmark runbook: docs/runbooks/whisperx-benchmark.md (incl. VRAM/CPU measurement)

Zero production behavior change: transcribe.py, the legacy Dockerfile, cmd/transcribe, and all existing CDK resources are untouched. Phase 2 (engine cutover) is a separate spec.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Note post-merge operator steps in the PR** (already in the runbook): image build/push, model staging, `cdk deploy TtobakWhisperStack --exclusively`.
