"""WhisperX batch transcription + pyannote 4.x diarization entry point.

Benchmark twin of transcribe.py (Phase 1, ADR-019 follow-up): same env-var
and output-JSON contract, different engine. Never wired into cmd/transcribe;
operators invoke it via `aws ecs run-task` with OUTPUT_KEY REQUIRED and
explicitly set (see docs/runbooks/whisperx-benchmark.md). Unlike
transcribe.py, an unset/empty OUTPUT_KEY is NOT accepted and does NOT
default into the production transcripts/{meeting_id}.json key -- a single
forgotten env var must never write into the real pipeline's namespace or
mark a real meeting errored (see validate_output_key /
should_mark_meeting_error).

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
import re
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
        if not os.path.isfile(config_path):
            print(f"Diarization bundle extracted but {config_path} is missing, "
                  f"skipping diarization")
            return None
        print(f"Diarization model ready ({time.time() - start:.0f}s)")
        return config_path
    except Exception as e:
        print(f"Diarization model unavailable, skipping diarization: {e}")
        return None


def _log_gpu_memory(stage: str) -> None:
    """Prints one GPU memory sample to the task log so the benchmark's peak-
    VRAM question (docs/runbooks/whisperx-benchmark.md §4) is answerable from
    CloudWatch logs alone -- no SSM access to the shared production instance
    role required. Best-effort: never raises."""
    try:
        import subprocess
        out = subprocess.run(
            ["nvidia-smi", "--query-gpu=memory.used,memory.total,utilization.gpu",
             "--format=csv,noheader"],
            check=True, capture_output=True, text=True, timeout=10,
        )
        print(f"GPU[{stage}]: {out.stdout.strip()}")
    except Exception as e:
        print(f"GPU[{stage}]: nvidia-smi unavailable ({e})")


def _turns_from_diarization(diarization) -> list[tuple]:
    """Extracts (start, end, label) turns from a pyannote result. 4.x may
    return a wrapper whose Annotation lives at .speaker_diarization (3.x
    returned the Annotation itself) -- unwrap defensively so both work."""
    annotation = getattr(diarization, "speaker_diarization", diarization)
    return [
        (turn.start, turn.end, label)
        for turn, _, label in annotation.itertracks(yield_label=True)
    ]


def _stripped_text_len(segs: list[dict]) -> int:
    """Total non-space character count across segment texts — the coverage
    metric the re-split sanity check compares (build_result joins segment
    texts, so this is exactly the text that survives into the output)."""
    return sum(len(s.get("text", "").replace(" ", "")) for s in segs)


def _interpolate_missing_timestamps(segs: list[dict], span_start: float, span_end: float) -> int:
    """Fills missing start/end timestamps on re-split aligned segments,
    in place, and returns how many segments were touched (their `words`
    are dropped — interpolated boundaries are too coarse for word-majority
    voting). Pure function so the boundary math is unit-testable without
    whisperx.

    Rules (round-2 review MAJORs 1+2 of PR #173):
    - A partially-missing segment keeps its KNOWN side; only the missing
      side is filled (a valid aligner-provided start must never be
      overwritten by interpolation).
    - A maximal run of consecutive fully-missing segments splits the gap
      between its known neighbors (previous known end ~ next known start,
      falling back to the input span edges) EVENLY — a naive per-segment
      walk gives the first segment the whole gap and collapses the rest to
      zero length, leaving them unable to overlap any diarization turn.
    """
    n = len(segs)
    touched = 0

    # Boundary lookups read a SNAPSHOT of the aligner-provided timestamps,
    # never values this function already filled — otherwise fill order
    # bleeds between segments (e.g. an interpolated end consuming the gap a
    # later segment's start needed, re-creating the zero-length collapse
    # this helper exists to prevent). Either known field of a neighbor
    # bounds the gap.
    snapshot = [(s.get("start"), s.get("end")) for s in segs]

    def known_end_before(i: int) -> float:
        for j in range(i - 1, -1, -1):
            s0, e0 = snapshot[j]
            if e0 is not None:
                return e0
            if s0 is not None:
                return s0
        return span_start

    def known_start_after(i: int) -> float:
        for j in range(i + 1, n):
            s0, e0 = snapshot[j]
            if s0 is not None:
                return s0
            if e0 is not None:
                return e0
        return span_end

    i = 0
    while i < n:
        seg = segs[i]
        has_start = seg.get("start") is not None
        has_end = seg.get("end") is not None
        if has_start and has_end:
            i += 1
            continue
        if has_start or has_end:
            # Partial: fill only the missing side from the nearest known
            # boundary on that side, clamped so start <= end.
            if not has_start:
                seg["start"] = min(known_end_before(i), seg["end"])
            else:
                seg["end"] = max(known_start_after(i), seg["start"])
            seg.pop("words", None)
            touched += 1
            i += 1
            continue
        # Fully-missing run: [i, run_end)
        run_end = i
        while run_end < n and segs[run_end].get("start") is None and segs[run_end].get("end") is None:
            run_end += 1
        gap_start = known_end_before(i)
        gap_stop = known_start_after(run_end - 1)
        if gap_stop < gap_start:
            gap_stop = gap_start
        k = run_end - i
        width = (gap_stop - gap_start) / k
        for j in range(k):
            s = segs[i + j]
            s["start"] = gap_start + j * width
            s["end"] = gap_start + (j + 1) * width
            s.pop("words", None)
            touched += 1
        i = run_end
    return touched


def _load_wav_waveform(wav_path: str) -> dict:
    """Reads the 16kHz mono s16le WAV our own ffmpeg call just wrote into
    the in-memory {'waveform': (channel, time) tensor, 'sample_rate': int}
    dict pyannote accepts. This BYPASSES pyannote 4.x's torchcodec decoding
    path entirely — the first bench run failed with RuntimeError because
    torchcodec's FFmpeg-6 variant links against libpython3.12.so.1.0, which
    Ubuntu's `python3` package doesn't ship (it lives in `libpython3.12`,
    now installed in Dockerfile.whisperx as belt-and-suspenders). Passing a
    preloaded waveform is pyannote's own documented workaround, and stdlib
    `wave` suffices because we control the format (ffmpeg -ar 16000 -ac 1).
    """
    import wave

    import numpy as np
    import torch

    with wave.open(wav_path, "rb") as w:
        sample_rate = w.getframerate()
        raw = w.readframes(w.getnframes())
    pcm = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
    return {"waveform": torch.from_numpy(pcm).unsqueeze(0), "sample_rate": sample_rate}


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
        diarization = pipeline(_load_wav_waveform(wav_path), **kwargs)
        # pyannote.audio 4.x may return a result wrapper whose Annotation
        # lives at .speaker_diarization (3.x returned the Annotation itself);
        # unwrap defensively so both shapes work.
        return _turns_from_diarization(diarization)
    except Exception as e:
        # Never print raw ffmpeg stderr or the exception message text here:
        # ffmpeg's stderr on failure includes container/file metadata (and a
        # library exception's str() can otherwise embed transcript
        # fragments) -- both are meeting PII and the task log group retains
        # them for 30 days (FINDING 2(b), round 11). Log only the exception
        # type plus, for a subprocess failure, the returncode and stderr
        # BYTE LENGTH (not content).
        returncode = getattr(e, "returncode", None)
        stderr = getattr(e, "stderr", None)
        # subprocess.run above is called WITHOUT text=True, so
        # CalledProcessError.stderr is bytes, not str -- len() on it counts
        # bytes either way, but guard for a str from any other subprocess
        # call this broad except also catches.
        stderr_len = len(stderr) if stderr is not None else 0
        if returncode is not None:
            print(f"Diarization failed, falling back to unlabeled segments: "
                  f"{type(e).__name__} (ffmpeg rc={returncode}, stderr "
                  f"{stderr_len} bytes suppressed -- PII hygiene)")
        else:
            print(f"Diarization failed, falling back to unlabeled segments: "
                  f"{type(e).__name__}")
        return []


def _empty_cuda_cache() -> None:
    """Releases cached (already-freed) CUDA memory back to the allocator.
    Takes no model argument on purpose (FINDING 1, round 12): a prior version
    took the model as a parameter and did `del <param>` inside this helper,
    which only drops the *callee's own* local binding -- the caller's local
    variable still held a live reference the whole time, so the model was
    NEVER actually freed and this empty_cache() call was releasing nothing.
    The caller MUST drop its own reference (e.g. `align_model = None`) before
    calling this, so the object's refcount can actually reach zero first.
    Best-effort: lazy torch import, never raises (mirrors the `del model` +
    empty_cache() pattern already used for the ASR model in main())."""
    try:
        import torch
        torch.cuda.empty_cache()
    except Exception:
        pass


def _try_align(segments: list[dict], audio, language: str) -> tuple[list[dict], bool, int]:
    """Best-effort wav2vec2 forced alignment for word timestamps. Korean
    model availability in whisperx's registry is unconfirmed (see design
    spec) -- on ANY failure return the input segments unchanged and False.

    whisperx.align() may print warnings to stdout/stderr that embed
    per-segment TRANSCRIPT TEXT on partial-alignment failures (e.g. 'Failed
    to align segment ("...")') -- that text is meeting PII and the task log
    group retains it for 30 days (FINDING 2(a), round 11). Both
    load_align_model and align are called with stdout/stderr redirected into
    a buffer that is inspected but never printed or logged verbatim -- only
    derived facts (line count, exception type) reach the task log.

    GPU residency (FINDING 1, round 12): on every path (keep, discard,
    exception) this function's own `finally` drops ITS OWN local references
    to the align model/metadata (`align_model = None`, `metadata = None`)
    BEFORE calling `_empty_cuda_cache()` -- clearing the reference in THIS
    scope is what lets the object's refcount reach zero (nothing else in
    this process holds one), so the cache-clear that follows actually
    reclaims its memory. A prior version passed the model into a helper that
    did `del` on its own parameter, which only removed the callee's binding
    and left this scope's reference alive through the empty_cache() call --
    the model was never actually freed. Without this, the model would stay
    resident for the rest of the run, so the GPU[align-freed]/GPU[diarized]
    samples in §4 would include its memory even though those stages don't
    use it -- contradicting the per-stage-residency claim in
    _log_gpu_memory's caller and the runbook.

    GPU sampling (FINDING 1, round 13): main()'s post-call GPU sample fires
    AFTER this function's `finally` has already freed the align model and
    emptied the CUDA cache, so alignment's own VRAM residency -- the run's
    likely peak -- was never sampled and §4's "max across samples" claim
    systematically under-reported. This function samples itself, immediately
    after `whisperx.align(...)` returns and the redirect context above has
    exited (so the GPU[...] line reaches real stdout, not the suppressed
    buffer) but BEFORE the `finally` below drops the model reference --
    i.e. while the align model is still resident on the GPU.

    Returns (segments, alignment_enabled, repaired_count) — repaired_count
    is the number of segments the aligner couldn't fully align, repaired to
    segment-level timestamps (input-index copy on the count-match path,
    neighbor interpolation on the re-split path) with their words dropped.
    No segment — and therefore no transcript TEXT, since build_result joins
    segment texts — is ever silently discarded; wholesale content loss
    trips the coverage check and falls back to the input instead. Surfaced
    as alignment_repaired in whisper_metadata."""
    import contextlib
    import io
    stdout_buf, stderr_buf = io.StringIO(), io.StringIO()
    align_model = None
    metadata = None
    try:
        import whisperx
        with contextlib.redirect_stdout(stdout_buf), \
                contextlib.redirect_stderr(stderr_buf):
            align_model, metadata = whisperx.load_align_model(
                language_code=language, device="cuda")
            aligned = whisperx.align(segments, align_model, metadata, audio, "cuda")
        # Sample GPU memory now, while align_model/metadata are still
        # resident (this function's own `finally` hasn't run yet) -- see the
        # GPU-sampling note in this function's docstring.
        _log_gpu_memory("aligning")
        captured_lines = sum(
            1 for buf in (stdout_buf, stderr_buf)
            for line in buf.getvalue().splitlines() if line.strip())
        if captured_lines:
            print(f"Alignment emitted {captured_lines} warning line(s) "
                  f"(suppressed -- may contain transcript text)")
        aligned_segments = aligned["segments"]
        if len(aligned_segments) != len(segments):
            # A count mismatch is whisperx.align()'s NORMAL behavior, not a
            # failure: it re-splits segments along alignment boundaries (the
            # first real bench run returned 117 for 43 inputs). The earlier
            # all-or-nothing discard here silently killed word-majority
            # speaker assignment — the thing this benchmark exists to
            # evaluate — on every real run.
            #
            # Coverage sanity check FIRST: build_result derives the final
            # transcript string by joining SEGMENT texts, so text the
            # aligner loses (or duplicates) is a silent quality-comparison
            # skew. Outside a ±10% tolerance band, discard and fall back;
            # any loss INSIDE the band is accepted but logged so it is
            # never invisible.
            in_len = _stripped_text_len(segments)
            out_len = _stripped_text_len(aligned_segments)
            if in_len and not (0.9 * in_len <= out_len <= 1.1 * in_len):
                direction = "lost" if out_len < in_len else "duplicated"
                print(f"Alignment re-split output {direction} text beyond "
                      f"the 10% tolerance ({out_len}/{in_len} non-space "
                      f"chars); discarding aligned result, using "
                      f"segment-level timestamps")
                return segments, False, 0
            if out_len < in_len:
                print(f"Alignment re-split output carries {out_len}/{in_len} "
                      f"non-space chars (within 10% tolerance, accepted)")
            # Accept the re-split output, preserving EVERY segment: index
            # mapping to the inputs is impossible across a re-split, so
            # missing timestamps are interpolated from neighboring known
            # boundaries (runs of fully-missing segments split their gap
            # evenly; a partially-missing segment keeps its known side) and
            # those segments lose only their words — symmetric with the
            # count-match path's per-segment repair.
            span_start = segments[0]["start"] if segments else 0.0
            span_end = segments[-1]["end"] if segments else 0.0
            interpolated = _interpolate_missing_timestamps(
                aligned_segments, span_start, span_end)
            print(f"Alignment re-split {len(segments)} segment(s) into "
                  f"{len(aligned_segments)} (kept all; {interpolated} "
                  f"repaired via neighbor-interpolated timestamps, words "
                  f"dropped for those only)")
            return aligned_segments, True, interpolated
        repaired = 0
        for i, seg in enumerate(aligned_segments):
            if seg.get("start") is None or seg.get("end") is None:
                # Index mapping is valid (counts match) -- repair just this
                # segment from the corresponding input segment's
                # segment-level timestamps and drop its words, rather than
                # discarding the whole aligned result (which would silently
                # degrade word-majority speaker assignment -- the thing
                # this benchmark exists to evaluate -- to segment-overlap
                # for every segment, not just the bad one).
                seg["start"] = segments[i]["start"]
                seg["end"] = segments[i]["end"]
                seg.pop("words", None)
                repaired += 1
        if repaired:
            print(f"Alignment produced {repaired} segment(s) with missing "
                  f"start/end; repaired from segment-level timestamps "
                  f"(words dropped for those segments only), kept aligned "
                  f"words for the rest")
        else:
            print("Alignment succeeded (word-level timestamps available)")
        return aligned_segments, True, repaired
    except Exception as e:
        # Exception message text (not just captured print output) can also
        # embed transcript fragments from library internals -- log only the
        # type, never str(e), for this specific path (FINDING 2(a)/(c)).
        print(f"Alignment unavailable, using segment-level timestamps: "
              f"{type(e).__name__}")
        return segments, False, 0
    finally:
        # Drop THIS scope's references first, then clear the cache -- see
        # the GPU-residency note in this function's docstring for why order
        # matters here.
        if align_model is not None:
            align_model = None
            metadata = None
            _empty_cuda_cache()


def build_result(segments: list[dict], language: str, language_probability: float,
                 duration_seconds: float, transcription_seconds: float,
                 diarization_enabled: bool, num_speakers_detected: int,
                 alignment_enabled: bool, alignment_repaired: int = 0) -> dict:
    """Pure: renders the exact transcript JSON cmd/summarize consumes
    (same shape as transcribe.py's output; engine string differs, plus
    additive alignment_enabled/alignment_repaired fields Go ignores).
    alignment_repaired is the count of segments _try_align had to repair
    from segment-level timestamps (partial alignment failure) -- surfaced
    so a §6 benchmark table can distinguish a full-alignment run from a
    partial-repair one instead of both collapsing to alignment_enabled=true."""
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
            "alignment_repaired": alignment_repaired,
        },
    }


class BenchConfigError(ValueError):
    """Raised ONLY by this module's own operator-facing config checks
    (validate_output_key, validate_audio_key, and main()'s MEETING_ID/
    USER_ID/no-audio-found checks) -- never by whisperx/pyannote/ffmpeg or
    any other library call. Its message is built entirely from S3 key
    strings, env-var values, and meeting/user identifiers this process
    itself received, never from transcript/audio content -- every one of
    these checks runs before any audio download or model/GPU work, so no
    transcript content can exist yet -- so it is safe-by-construction to log
    verbatim (FINDING 2, round 12; extended round 13) -- unlike a generic
    exception's str(), which can embed transcript fragments raised from
    library internals. Subclasses ValueError so any existing
    `assertRaises(ValueError)` test coverage for these validators keeps
    passing unchanged."""


def _is_real_pipeline_key(key: str, meeting_id: str) -> bool:
    """Shared real/bench judgment: True only for the exact shapes
    validate_output_key accepts as an explicit Phase-2 drop-in escape hatch
    into the real pipeline -- exactly transcripts/{meeting_id}.json, or the
    ONE legitimate multi-part variant transcripts/{meeting_id}_part_NNN.json.
    An EMPTY key is deliberately NOT "real" here (see validate_output_key /
    should_mark_meeting_error): OUTPUT_KEY is now required, so an empty key
    means the run should fail fast, not silently fall into the production
    transcripts/ namespace. Kept as a single helper so should_mark_meeting_error
    and validate_output_key cannot drift apart on what counts as "real". The
    multipart branch is an exact fullmatch, not a startswith prefix: a
    startswith(f"transcripts/{meeting_id}_") check would also match an
    operator's mistyped bench key like
    transcripts/{meeting_id}_bench_whisperx.json (dropping the required
    "bench-" prefix/directory), letting it dodge fail-fast, land in the
    production transcripts/ namespace, and mark the real meeting errored on
    task failure."""
    if not key:
        return False
    default_key = f"transcripts/{meeting_id}.json"
    if key == default_key:
        return True
    return bool(re.fullmatch(
        rf"transcripts/{re.escape(meeting_id)}_part_\d{{3}}\.json", key))


def should_mark_meeting_error(output_key: str, meeting_id: str) -> bool:
    """A fatal failure should surface on the real meeting row only when this
    run feeds the real pipeline for THIS meeting_id via an EXPLICIT real
    key (exactly transcripts/{meeting_id}.json / transcripts/{meeting_id}_*
    -- the Phase-2 drop-in escape hatch). An EMPTY OUTPUT_KEY returns False:
    validate_output_key now rejects an empty key before any real work
    happens, so a run that reaches this handler with an empty key failed
    fast and must never mark the real meeting as errored. Benchmark runs
    (bench-transcripts/) and any other/mistyped key (e.g. a typo'd
    transcripts/OTHER.json) also return False -- the operator reads the
    task log instead."""
    key = (output_key or "").strip()
    return _is_real_pipeline_key(key, meeting_id)


def validate_output_key(output_key: str, meeting_id: str) -> str:
    """Resolves and validates the transcript destination. OUTPUT_KEY is now
    REQUIRED: this is a Phase-1 benchmark-only engine that nothing invokes
    automatically, so a forgotten env var must never silently default into
    the production transcripts/{meeting_id}.json key and fire the summarize
    pipeline. An empty/whitespace-only key raises BenchConfigError (a
    ValueError subclass -- see its docstring) immediately. Only two shapes
    are legal beyond that: a benchmark key under bench-transcripts/, or the
    real pipeline's own key transcripts/{meeting_id}.json -- including the
    multi-part variant transcripts/{meeting_id}_part_NNN.json (exact 3-digit
    fullmatch only, see _is_real_pipeline_key) -- kept on purpose as a
    deliberate Phase-2 drop-in escape hatch so this engine can be pointed at
    the real pipeline key once it's promoted. Anything else (another
    meeting's transcripts/ key, a mistyped bench key missing the
    bench-transcripts/ prefix, an arbitrary prefix) raises BenchConfigError
    before a single byte is written, mirroring should_mark_meeting_error's
    bench/real split on the S3 side.

    NOTE (round-11 review, FINDING 3): as of this PR, ttobak-whisperx-task-role
    (infra/lib/whisper-stack.ts) no longer grants write on transcripts/* --
    only bench-transcripts/*. This function still ACCEPTS the real-pipeline
    key shape (the app-level escape hatch stays coded for Phase 2), but
    actually pointing OUTPUT_KEY at it in Phase 1 now also fails at the IAM
    layer (S3 PutObject AccessDenied) after passing this validation -- a
    second, independent layer of defense-in-depth on top of this being an
    operator-driven runbook procedure that should never target a real key.
    Phase 2's cutover to real-pipeline runs must add the transcripts/* write
    grant back to this role (or reuse the legacy task role) for this escape
    hatch to actually work."""
    key = (output_key or "").strip()
    if not key:
        raise BenchConfigError(
            "Phase 1 benchmark engine requires an explicit OUTPUT_KEY (use "
            "bench-transcripts/...); refusing to default to the production "
            "transcripts/ key")
    if key.startswith("bench-transcripts/"):
        return key
    if _is_real_pipeline_key(key, meeting_id):
        return key
    raise BenchConfigError(
        f"OUTPUT_KEY {key!r} is not a valid bench-transcripts/ key or this "
        f"meeting's own transcripts/{meeting_id}(.json|_part_NNN.json) key")


def validate_audio_key(audio_key: str, user_id: str, meeting_id: str) -> str:
    """AUDIO_KEY may only point inside this meeting's own audio prefix
    audio/{user_id}/{meeting_id}/ -- rejects cross-meeting/cross-user reads
    (the task role is bucket-wide, so IAM does not enforce this)."""
    prefix = f"audio/{user_id}/{meeting_id}/"
    if not audio_key.startswith(prefix):
        raise BenchConfigError(
            f"AUDIO_KEY {audio_key!r} is not under this meeting's own audio "
            f"prefix {prefix!r}")
    return audio_key


def main():
    # Read via .get()+check (not os.environ[...]) so a missing env var
    # raises BenchConfigError, not a bare KeyError: this failure happens
    # before any S3/audio access, so no transcript content is possible yet
    # -- the message is safe-by-construction, same as validate_output_key/
    # validate_audio_key (FINDING 2(b), round 13).
    meeting_id = os.environ.get("MEETING_ID", "").strip()
    if not meeting_id:
        raise BenchConfigError("MEETING_ID env var is required")
    user_id = os.environ.get("USER_ID", "").strip()
    if not user_id:
        raise BenchConfigError("USER_ID env var is required")

    # Validate OUTPUT_KEY/AUDIO_KEY before any S3 download or model/GPU work --
    # a bad OUTPUT_KEY (e.g. a typo'd bench key) must fail fast, not after an
    # entire GPU run's worth of download/transcription/alignment/diarization.
    output_key = validate_output_key(os.environ.get("OUTPUT_KEY", ""), meeting_id)

    audio_key = os.environ.get("AUDIO_KEY")
    if audio_key:
        audio_key = validate_audio_key(audio_key, user_id, meeting_id)
    if audio_key and not common.audio_key_exists(s3, BUCKET, audio_key):
        print(f"AUDIO_KEY {audio_key!r} not found in S3; falling back to prefix scan")
        audio_key = None
    if not audio_key:
        audio_key = common.find_audio_key(s3, BUCKET, user_id, meeting_id)
    if not audio_key:
        # Safe by construction, same as MEETING_ID/USER_ID above: no audio
        # has been downloaded yet at this point, so no transcript content
        # can be embedded in this message (FINDING 2(b), round 13).
        raise BenchConfigError(f"No audio file found for meeting {meeting_id}")

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
    _log_gpu_memory("model-loaded")

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
    _log_gpu_memory("transcribed")

    # Free the ASR model before alignment/diarization load their own models --
    # §4's per-stage VRAM samples should reflect per-stage residency (only the
    # model(s) actually in use for that stage), not all-models-resident, since
    # that's what the Phase 2 instance-sizing decision needs to see.
    del model
    try:
        import torch
        torch.cuda.empty_cache()
    except Exception:
        pass

    segments, alignment_enabled, alignment_repaired = _try_align(segments, audio, language)
    # _try_align already sampled GPU[aligning] itself, while the align model
    # was still resident (see its docstring) -- this sample is taken after
    # _try_align's own `finally` has freed that model and emptied the CUDA
    # cache, so it reflects post-alignment residency, not alignment's own
    # peak.
    _log_gpu_memory("align-freed")

    num_speakers_env = os.environ.get("NUM_SPEAKERS", "").strip()
    num_speakers = int(num_speakers_env) if num_speakers_env.isdigit() else None

    diarization_config = _ensure_diarization_config()
    num_speakers_detected = 0
    turns = []
    if diarization_config and segments:
        print("Diarizing (pyannote 4.x)...")
        diarize_start = time.time()
        turns = _diarize(diarization_config, local_path, num_speakers)
        _log_gpu_memory("diarized")
        if turns:
            segments = common.assign_speakers(segments, turns)
            num_speakers_detected = len({t[2] for t in turns})
            print(f"Diarization done in {time.time() - diarize_start:.1f}s, "
                  f"{num_speakers_detected} speaker(s) detected")
        else:
            print("Diarization produced no turns; segments left unlabeled")

    # diarization_enabled reflects whether turns were actually produced, not
    # merely whether the config bundle was available -- a staged-but-empty
    # diarization run (e.g. pyannote failure) must not report enabled:true.
    diarization_enabled = bool(turns)

    result = build_result(
        segments=segments, language=language,
        # whisperx's batched pipeline doesn't expose a language probability;
        # 0.0 keeps the field present for schema parity without faking confidence.
        language_probability=0.0,
        duration_seconds=duration_seconds, transcription_seconds=elapsed,
        diarization_enabled=diarization_enabled,
        num_speakers_detected=num_speakers_detected,
        alignment_enabled=alignment_enabled,
        alignment_repaired=alignment_repaired)

    common.upload_transcript(s3, BUCKET, output_key, result)
    print(f"Uploaded s3://{BUCKET}/{output_key}")


def format_fatal_error(e: Exception) -> str:
    """Renders the fatal-error line for the __main__ handler. Pure and
    independently testable (FINDING 2, round 12): a plain
    `str(e)[:300]` length cap is NOT redaction -- 300 characters of a
    library exception (whisperx/pyannote internals) can still embed a
    transcript fragment verbatim, just a truncated one, which is meeting
    PII the task log group would then retain for 30 days.

    BenchConfigError (see its docstring) is the one exception type this
    module raises whose message is safe-by-construction -- built only from
    S3 keys/env values, never transcript content -- so its full message is
    printed verbatim. Every other exception type prints ONLY its type name
    plus the message's byte length, never any of the message content
    itself, since an arbitrary exception (including one raised by a
    third-party library this module doesn't control) could contain
    anything."""
    if isinstance(e, BenchConfigError):
        return f"ERROR: {type(e).__name__}: {e}"
    message_len = len(str(e))
    return (f"ERROR: {type(e).__name__} (message suppressed, "
            f"{message_len} chars -- PII hygiene; see task exit code)")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        # See format_fatal_error's docstring for why a type-only message is
        # used for any exception other than this module's own
        # BenchConfigError (FINDING 2, round 12).
        print(format_fatal_error(e), file=sys.stderr)
        meeting_id = os.environ.get("MEETING_ID", "")
        user_id = os.environ.get("USER_ID", "")
        if meeting_id and user_id and should_mark_meeting_error(
                os.environ.get("OUTPUT_KEY", ""), meeting_id):
            common.mark_meeting_error(table, user_id, meeting_id)
        sys.exit(1)
