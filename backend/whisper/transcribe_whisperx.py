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
        if not os.path.isfile(config_path):
            print(f"Diarization bundle extracted but {config_path} is missing, "
                  f"skipping diarization")
            return None
        print(f"Diarization model ready ({time.time() - start:.0f}s)")
        return config_path
    except Exception as e:
        print(f"Diarization model unavailable, skipping diarization: {e}")
        return None


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


def should_mark_meeting_error(output_key: str) -> bool:
    """A fatal failure should surface on the real meeting row only when this
    run feeds the real pipeline (OUTPUT_KEY empty -> defaults to
    transcripts/{meeting_id}.json, or explicitly under transcripts/).
    Benchmark runs write elsewhere (e.g. bench-transcripts/) and must never
    touch production meeting state -- the operator reads the task log instead."""
    key = (output_key or "").strip()
    return key == "" or key.startswith("transcripts/")


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
    turns = []
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
        if meeting_id and user_id and should_mark_meeting_error(os.environ.get("OUTPUT_KEY", "")):
            common.mark_meeting_error(table, user_id, meeting_id)
        sys.exit(1)
