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
        # pyannote.audio 4.x may return a result wrapper whose Annotation
        # lives at .speaker_diarization (3.x returned the Annotation itself);
        # unwrap defensively so both shapes work.
        return _turns_from_diarization(diarization)
    except Exception as e:
        detail = str(e)
        stderr = getattr(e, "stderr", None)
        if stderr:
            # subprocess.CalledProcessError.stderr may be bytes (capture_output=True
            # above returns text, but be defensive for any other subprocess call
            # this except also catches) -- decode defensively, never raise here.
            if isinstance(stderr, bytes):
                stderr = stderr.decode("utf-8", errors="replace")
            detail = f"{detail} | stderr: {stderr.strip()}"
        print(f"Diarization failed, falling back to unlabeled segments: {detail}")
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
        aligned_segments = aligned["segments"]
        if len(aligned_segments) != len(segments):
            print(f"Alignment returned {len(aligned_segments)} segment(s), "
                  f"expected {len(segments)}; discarding aligned result "
                  f"(would skew the benchmark), using segment-level "
                  f"timestamps")
            return segments, False
        bad = sum(
            1 for seg in aligned_segments
            if seg.get("start") is None or seg.get("end") is None)
        if bad:
            print(f"Alignment produced {bad} segment(s) with missing "
                  f"start/end; discarding aligned result, using "
                  f"segment-level timestamps")
            return segments, False
        print("Alignment succeeded (word-level timestamps available)")
        return aligned_segments, True
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
    pipeline. An empty/whitespace-only key raises ValueError immediately.
    Only two shapes are legal beyond that: a benchmark key under
    bench-transcripts/, or the real pipeline's own key
    transcripts/{meeting_id}.json -- including the multi-part variant
    transcripts/{meeting_id}_part_NNN.json (exact 3-digit fullmatch only, see
    _is_real_pipeline_key) -- kept on purpose as a deliberate Phase-2
    drop-in escape hatch so this engine can be pointed at the real pipeline
    key once it's promoted. Anything else (another meeting's transcripts/
    key, a mistyped bench key missing the bench-transcripts/ prefix, an
    arbitrary prefix) raises ValueError before a single byte is written,
    mirroring should_mark_meeting_error's bench/real split on the S3 side."""
    key = (output_key or "").strip()
    if not key:
        raise ValueError(
            "Phase 1 benchmark engine requires an explicit OUTPUT_KEY (use "
            "bench-transcripts/...); refusing to default to the production "
            "transcripts/ key")
    if key.startswith("bench-transcripts/"):
        return key
    if _is_real_pipeline_key(key, meeting_id):
        return key
    raise ValueError(
        f"OUTPUT_KEY {key!r} is not a valid bench-transcripts/ key or this "
        f"meeting's own transcripts/{meeting_id}(.json|_part_NNN.json) key")


def validate_audio_key(audio_key: str, user_id: str, meeting_id: str) -> str:
    """AUDIO_KEY may only point inside this meeting's own audio prefix
    audio/{user_id}/{meeting_id}/ -- rejects cross-meeting/cross-user reads
    (the task role is bucket-wide, so IAM does not enforce this)."""
    prefix = f"audio/{user_id}/{meeting_id}/"
    if not audio_key.startswith(prefix):
        raise ValueError(
            f"AUDIO_KEY {audio_key!r} is not under this meeting's own audio "
            f"prefix {prefix!r}")
    return audio_key


def main():
    meeting_id = os.environ["MEETING_ID"]
    user_id = os.environ["USER_ID"]

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

    segments, alignment_enabled = _try_align(segments, audio, language)
    _log_gpu_memory("aligned")

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
        alignment_enabled=alignment_enabled)

    common.upload_transcript(s3, BUCKET, output_key, result)
    print(f"Uploaded s3://{BUCKET}/{output_key}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        meeting_id = os.environ.get("MEETING_ID", "")
        user_id = os.environ.get("USER_ID", "")
        if meeting_id and user_id and should_mark_meeting_error(
                os.environ.get("OUTPUT_KEY", ""), meeting_id):
            common.mark_meeting_error(table, user_id, meeting_id)
        sys.exit(1)
