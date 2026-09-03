from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import time
from contextlib import closing

import boto3
from botocore.exceptions import ClientError
from faster_whisper import WhisperModel

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
BUCKET = os.environ["BUCKET_NAME"]
TABLE = os.environ["TABLE_NAME"]
VOCAB_KEY = os.environ.get("VOCAB_KEY", "config/custom-vocabulary.txt")
MODEL_S3_KEY = os.environ.get("MODEL_S3_KEY", "models/faster-whisper-large-v3.tar.gz")
MODEL_LOCAL_DIR = "/tmp/whisper-model"
# Phase 2 (2026-09-03): diarization upgraded to pyannote 4.x community-1 --
# the a435a3dc/3804e0f5 benches showed the community-1 MODEL (not whisperx)
# is what fixes phantom speakers (legacy 8-with-phantoms -> 4 clean, matching
# operator-confirmed ground truth). Same self-contained bundle the whisperx
# bench image stages. This in-image default is the key's SOURCE OF TRUTH
# (ADR-035): the CDK task definition deliberately does not set it, so the
# bundle generation always ships atomically with this image's pyannote pin.
# The 3.1 bundle stays in S3, but rolling back means reverting the whole
# ADR-035 commit and rebuilding -- not editing env or this line alone.
DIARIZATION_S3_KEY = os.environ.get("DIARIZATION_S3_KEY", "models/whisperx-diarization-4.x.tar.gz")
DIARIZATION_LOCAL_DIR = "/tmp/diarization-model"

# Audio discovery filters — exclude empty/placeholder uploads and progress/checkpoint sidecars.
MIN_AUDIO_SIZE_BYTES = 1024
SKIP_KEY_SUBSTRINGS = ("recording_progress", "checkpoint")

s3 = boto3.client("s3", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)


def _stream_extract(s3_key: str, dest_dir: str, max_attempts: int = 3) -> None:
    """Streams an S3 object directly into a tar extractor instead of
    downloading it to disk first -- the model tarballs are multi-GB, and
    holding both the compressed archive and its extracted contents on disk
    at once was contributing to the GPU instance's root volume filling up
    (see infra/lib/whisper-stack.ts's blockDevices comment). mode="r|gz" is
    the streaming (non-seekable) tar mode required for a StreamingBody.
    filter="data" (PEP 706, stdlib since Python 3.12/3.8.17+) rejects tar
    members that would escape dest_dir via ../ paths or symlinks -- the
    container's Ubuntu 24.04 base ships Python 3.12, so this is always
    available here.

    Retries on any failure: unlike s3.download_file's Transfer Manager (which
    retries at the part level), a single get_object stream has no built-in
    recovery from a mid-transfer hiccup (throttling, connection reset) on a
    multi-GB object, so one would otherwise fail the whole task. Each retry
    wipes dest_dir first so a partial extraction from the failed attempt
    can't interfere with the next one or with the caller's file-existence
    cache check."""
    last_err: Exception | None = None
    for attempt in range(1, max_attempts + 1):
        try:
            if os.path.isdir(dest_dir):
                shutil.rmtree(dest_dir)
            os.makedirs(dest_dir, exist_ok=True)
            with closing(s3.get_object(Bucket=BUCKET, Key=s3_key)["Body"]) as body:
                with tarfile.open(fileobj=body, mode="r|gz") as tar:
                    tar.extractall(dest_dir, filter="data")
            return
        except Exception as e:
            last_err = e
            print(f"Stream-extract of s3://{BUCKET}/{s3_key} failed "
                  f"(attempt {attempt}/{max_attempts}): {e}")
            if attempt < max_attempts:
                time.sleep(2 ** attempt)
    raise last_err


def _ensure_model() -> str:
    if os.path.exists(os.path.join(MODEL_LOCAL_DIR, "model.bin")):
        print("Model already cached locally")
        return MODEL_LOCAL_DIR

    print(f"Downloading model from s3://{BUCKET}/{MODEL_S3_KEY}")
    start = time.time()
    _stream_extract(MODEL_S3_KEY, MODEL_LOCAL_DIR)
    elapsed = time.time() - start
    print(f"Model ready ({elapsed:.0f}s)")
    return MODEL_LOCAL_DIR


def _bundle_pyannote_mismatch(bundle_key: str, installed_version: str) -> str | None:
    """Returns an operator-facing error line if the diarization bundle's
    generation (from its key name) can't work with the installed
    pyannote.audio major, else None. Pure and unit-testable.

    Why (ADR-035): the bundle key is image-owned (the CDK task definition
    deliberately doesn't set it), but a stale pre-ADR-035 env on an old
    task-def revision or a per-run RunTask override can still pair a 4.x
    runtime with the 3.1 bundle (or the reverse). Loading such a pair fails
    config-incompatible and _diarize would swallow it into a silent
    unlabeled fallback; this precheck turns that into one loud, greppable
    line naming both sides. Keys without a recognizable generation marker
    skip the check (never block a future bundle naming scheme)."""
    installed_major = installed_version.split(".")[0]
    m = re.search(r"diarization-(\d+)", bundle_key.rsplit("/", 1)[-1])
    if not m:
        return None
    expected = m.group(1)
    if len(expected) > 2:
        # A long number (e.g. diarization-20270101) is a date-style naming
        # scheme, not a generation marker -- skip rather than false-flag it
        # (the docstring's "never block a future bundle naming scheme"
        # contract). Short numbers ARE treated as generations, so a future
        # diarization-5 bundle on a 4.x runtime still trips the check.
        return None
    if installed_major != expected:
        return (f"DIARIZATION BUNDLE/RUNTIME MISMATCH: bundle {bundle_key!r} "
                f"expects pyannote.audio {expected}.x but {installed_version} "
                f"is installed -- check for a stale DIARIZATION_S3_KEY env "
                f"or RunTask override (ADR-035); skipping diarization")
    return None


def _ensure_diarization_model() -> str | None:
    """Returns the local pipeline config.yaml path, or None if the S3 bundle
    is missing/unreadable or generation-incompatible with the installed
    pyannote (see _bundle_pyannote_mismatch). Diarization is best-effort:
    transcription must never fail because the diarization bundle isn't
    there yet."""
    try:
        import importlib.metadata as _md
        mismatch = _bundle_pyannote_mismatch(
            DIARIZATION_S3_KEY, _md.version("pyannote.audio"))
        if mismatch:
            print(mismatch)
            return None
    except Exception as e:
        # The precheck must never become its own failure mode -- note it
        # (type-only) and fall through to the normal load, whose failure
        # path already logs.
        print(f"Diarization bundle precheck skipped: {type(e).__name__}")
    config_path = os.path.join(DIARIZATION_LOCAL_DIR, "pipeline", "config.yaml")
    if os.path.exists(config_path):
        print("Diarization model already cached locally")
        return config_path

    print(f"Downloading diarization model from s3://{BUCKET}/{DIARIZATION_S3_KEY}")
    try:
        start = time.time()
        _stream_extract(DIARIZATION_S3_KEY, DIARIZATION_LOCAL_DIR)
        elapsed = time.time() - start
        print(f"Diarization model ready ({elapsed:.0f}s)")
        return config_path
    except Exception as e:
        print(f"Diarization model unavailable, skipping diarization: {e}")
        return None


def _to_mono16k_wav(input_path: str) -> str:
    """pyannote needs a decodable waveform; the uploaded audio can be
    webm/opus/m4a etc. ffmpeg is already in the image for this."""
    wav_path = "/tmp/audio-16k-mono.wav"
    subprocess.run(
        ["ffmpeg", "-y", "-i", input_path, "-ar", "16000", "-ac", "1", wav_path],
        check=True, capture_output=True,
    )
    return wav_path


class WavFormatError(ValueError):
    """Raised only by _load_wav_waveform's own format check -- the message
    is built exclusively from WAV header numbers (channel count, sample
    width), never file content, so _diarize may safely log it verbatim
    where library exceptions get type-only treatment."""


def _load_wav_waveform(wav_path: str) -> dict:
    """Reads the 16kHz mono s16le WAV _to_mono16k_wav just wrote into the
    in-memory {'waveform': (channel, time) tensor, 'sample_rate': int} dict
    pyannote accepts. This BYPASSES pyannote 4.x's torchcodec decoding path
    entirely -- torchcodec's FFmpeg-6 variant needs libpython3.12.so.1.0
    (the Dockerfile installs it as belt-and-suspenders), and passing a
    preloaded waveform is pyannote's own documented workaround, proven in
    the whisperx bench image (transcribe_whisperx.py has the same helper).
    stdlib `wave` suffices because we control the format (ffmpeg -ar 16000
    -ac 1)."""
    import wave

    import numpy as np
    import torch

    with wave.open(wav_path, "rb") as w:
        # We wrote this file ourselves (ffmpeg -ar 16000 -ac 1, s16le) --
        # verify the assumptions the int16 decode below depends on with an
        # explicit raise (a bare assert would vanish under -O, and the
        # numbers are format facts, not PII), so a future ffmpeg-flag
        # change surfaces as a distinct error line via _diarize's handler
        # instead of degrading quality through a wrong scale/shape.
        if w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise WavFormatError(
                f"unexpected WAV format for diarization: channels="
                f"{w.getnchannels()} sampwidth={w.getsampwidth()} "
                f"(expected mono s16le)")
        sample_rate = w.getframerate()
        raw = w.readframes(w.getnframes())
    pcm = np.frombuffer(raw, dtype=np.int16).astype(np.float32)
    pcm /= 32768.0  # in-place: avoids a second float32 copy of the waveform
    return {"waveform": torch.from_numpy(pcm).unsqueeze(0), "sample_rate": sample_rate}


def _turns_from_diarization(diarization) -> list[tuple]:
    """Extracts (start, end, label) turns from a pyannote result. 4.x may
    return a wrapper whose Annotation lives at .speaker_diarization (3.x
    returned the Annotation itself) -- unwrap defensively so both work.
    A wrapper carrying an explicit None (no speech found) is a legitimate
    empty result, not a failure to route through _diarize's except path."""
    annotation = getattr(diarization, "speaker_diarization", diarization)
    if annotation is None:
        return []
    return [
        (turn.start, turn.end, label)
        for turn, _, label in annotation.itertracks(yield_label=True)
    ]


def _diarize(config_path: str, wav_path: str, num_speakers: int | None):
    """Runs pyannote (4.x community-1) diarization. Returns a list of
    (start, end, label) turns, or [] if diarization fails for any reason
    (caller falls back to unlabeled segments -- never let this abort the
    transcription)."""
    try:
        import torch
        from pyannote.audio import Pipeline

        pipeline = Pipeline.from_pretrained(config_path)
        pipeline.to(torch.device("cuda"))
        # num_speakers is a registered-participant headcount, not an actual speaker
        # count -- passed as max_speakers (an upper bound pyannote auto-detects
        # within) rather than num_speakers (which would force exactly that many
        # clusters and over-split when fewer people actually spoke).
        kwargs = {"max_speakers": num_speakers} if num_speakers else {}
        diarization = pipeline(_load_wav_waveform(wav_path), **kwargs)
        return _turns_from_diarization(diarization)
    except WavFormatError as e:
        # Our own exception, message safe-by-construction (header numbers
        # only) -- print it verbatim so the ffmpeg-flag regression it exists
        # to catch is actually diagnosable from the log.
        print(f"Diarization skipped: {e}")
        return []
    except Exception as e:
        # Type-only logging: a library exception's str() can embed transcript
        # fragments or file metadata, and this log group retains entries --
        # same PII rule the whisperx bench engine follows. The module path
        # is kept for diagnosability (it distinguishes a pyannote config
        # error from a torch/CUDA one without any message content).
        print(f"Diarization failed, falling back to unlabeled segments: "
              f"{type(e).__module__}.{type(e).__name__}")
        return []


def _safe_diarize(config_path: str, local_path: str, num_speakers: int | None) -> list[tuple]:
    """Converts local_path to a diarization-ready wav and runs _diarize, but
    never lets a conversion failure (e.g. ffmpeg choking on an unusual codec)
    propagate out of main() -- diarization must be best-effort, same
    guarantee _diarize itself already provides for the pyannote call.
    Returns [] on any failure."""
    try:
        wav_path = _to_mono16k_wav(local_path)
        return _diarize(config_path, wav_path, num_speakers)
    except Exception as e:
        # Type-only: ffmpeg's failure output embeds container/file metadata
        # (meeting PII) -- log module+class and the non-PII returncode (when
        # present), never the message. Same rule as _diarize's handler.
        returncode = getattr(e, "returncode", None)
        rc = f" (rc={returncode})" if returncode is not None else ""
        print(f"Audio conversion for diarization failed, skipping "
              f"diarization: {type(e).__module__}.{type(e).__name__}{rc}")
        return []


def _assign_speakers(segments: list[dict], turns: list[tuple]) -> list[dict]:
    """Assigns each Whisper segment the speaker of the diarization turn with
    maximum time overlap. Segments with zero overlap (e.g. a turn boundary
    falls mid-segment) fall back to the turn whose midpoint is closest.
    Raw pyannote labels ("SPEAKER_00") are normalized to "spk_N" in
    first-appearance order, matching the existing spk_N/speakerMap
    convention used by RefineTranscript and the frontend. Pure function --
    no pyannote/torch import, so it's importable in unit tests without the
    heavy runtime deps."""
    if not turns:
        return segments

    label_order: list[str] = []

    def _normalize(raw_label: str) -> str:
        if raw_label not in label_order:
            label_order.append(raw_label)
        return f"spk_{label_order.index(raw_label)}"

    for seg in segments:
        best_label, best_overlap = None, 0.0
        for turn_start, turn_end, label in turns:
            overlap = min(seg["end"], turn_end) - max(seg["start"], turn_start)
            if overlap > best_overlap:
                best_label, best_overlap = label, overlap
        if best_label is None:
            seg_mid = (seg["start"] + seg["end"]) / 2
            best_label = min(
                turns, key=lambda t: abs((t[0] + t[1]) / 2 - seg_mid)
            )[2]
        seg["speaker"] = _normalize(best_label)
    return segments


def _load_custom_vocab_prompt() -> str:
    try:
        resp = s3.get_object(Bucket=BUCKET, Key=VOCAB_KEY)
        lines = resp["Body"].read().decode("utf-8").strip().split("\n")
        terms = []
        for line in lines[1:]:
            cols = line.split("\t")
            display = cols[2].strip() if len(cols) >= 3 else cols[0].strip()
            if display:
                terms.append(display)
        prompt = " ".join(terms)
        print(f"Custom vocab loaded: {len(terms)} terms")
        return prompt
    except Exception as e:
        print(f"Custom vocab not available: {e}")
        return ""


def _audio_key_exists(key: str) -> bool:
    """Verify the given S3 key exists. Returns False only on 404; re-raises auth/throttle errors."""
    try:
        s3.head_object(Bucket=BUCKET, Key=key)
        return True
    except ClientError as e:
        code = e.response.get("Error", {}).get("Code", "")
        if code in ("404", "NoSuchKey", "NotFound"):
            return False
        raise


def _find_audio_key(user_id: str, meeting_id: str) -> str | None:
    """Find the audio file by listing the S3 prefix (avoids Unicode normalization issues).

    Uses a paginator to handle prefixes with >1000 objects, and picks the most
    recently modified valid candidate so re-recordings supersede older uploads.
    """
    prefix = f"audio/{user_id}/{meeting_id}/"
    paginator = s3.get_paginator("list_objects_v2")
    candidates = []
    for page in paginator.paginate(Bucket=BUCKET, Prefix=prefix):
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


def main():
    meeting_id = os.environ["MEETING_ID"]
    user_id = os.environ["USER_ID"]
    audio_key = os.environ.get("AUDIO_KEY")
    if audio_key and not _audio_key_exists(audio_key):
        print(f"AUDIO_KEY {audio_key!r} not found in S3 (likely Unicode mismatch); falling back to prefix scan")
        audio_key = None
    if not audio_key:
        audio_key = _find_audio_key(user_id, meeting_id)
    if not audio_key:
        raise RuntimeError(f"No audio file found for meeting {meeting_id}")

    basename = audio_key.rsplit("/", 1)[-1]
    ext = basename.rsplit(".", 1)[-1] if "." in basename else "bin"
    local_path = f"/tmp/audio.{ext}"

    print(f"Downloading s3://{BUCKET}/{audio_key}")
    s3.download_file(BUCKET, audio_key, local_path)
    file_mb = os.path.getsize(local_path) / 1048576
    print(f"Audio: {file_mb:.1f} MB")

    vocab_prompt = _load_custom_vocab_prompt()

    # Merge with INITIAL_PROMPT env var (user's custom dictionary from DynamoDB)
    env_prompt = os.environ.get("INITIAL_PROMPT", "").strip()
    if env_prompt:
        print(f"INITIAL_PROMPT from env: {len(env_prompt.split(','))} terms")
        if vocab_prompt:
            vocab_prompt = f"{vocab_prompt} {env_prompt}"
        else:
            vocab_prompt = env_prompt

    model_path = _ensure_model()
    print("Loading Whisper large-v3 (GPU float16)...")
    model = WhisperModel(model_path, device="cuda", compute_type="float16")

    print("Transcribing...")
    start = time.time()
    transcribe_kwargs = dict(
        language="ko",
        beam_size=5,
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
        word_timestamps=True,
    )
    if vocab_prompt:
        transcribe_kwargs["initial_prompt"] = vocab_prompt
    segments, info = model.transcribe(local_path, **transcribe_kwargs)

    all_segments = []
    for seg in segments:
        all_segments.append({
            "start": round(seg.start, 2),
            "end": round(seg.end, 2),
            "text": seg.text.strip(),
        })

    elapsed = time.time() - start
    transcript_text = " ".join(s["text"] for s in all_segments)
    print(f"Done: {len(transcript_text):,} chars in {elapsed:.1f}s")

    num_speakers_env = os.environ.get("NUM_SPEAKERS", "").strip()
    num_speakers = int(num_speakers_env) if num_speakers_env.isdigit() else None

    diarization_config = _ensure_diarization_model()
    num_speakers_detected = 0
    if diarization_config and all_segments:
        print("Diarizing...")
        diarize_start = time.time()
        turns = _safe_diarize(diarization_config, local_path, num_speakers)
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

    result = {
        "results": {
            "transcripts": [{"transcript": transcript_text}],
        },
        "status": "COMPLETED",
        "whisper_metadata": {
            "engine": "whisper-large-v3-gpu",
            "language": info.language,
            "language_probability": round(info.language_probability, 3),
            "duration_seconds": round(info.duration, 1),
            "transcription_duration_seconds": round(elapsed, 1),
            "segments": all_segments,
            "diarization": {
                "enabled": bool(diarization_config),
                "num_speakers_detected": num_speakers_detected,
            },
        },
    }

    # OUTPUT_KEY lets the transcribe Lambda route multi-part audio to a
    # per-part key; without honoring it, every part would overwrite the same
    # transcripts/{meeting_id}.json.
    output_key = os.environ.get("OUTPUT_KEY", "").strip() or f"transcripts/{meeting_id}.json"
    s3.put_object(
        Bucket=BUCKET,
        Key=output_key,
        Body=json.dumps(result, ensure_ascii=False, indent=2).encode("utf-8"),
        ContentType="application/json",
    )
    print(f"Uploaded s3://{BUCKET}/{output_key}")
    print("Transcript uploaded — EventBridge will trigger summarize Lambda")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        meeting_id = os.environ.get("MEETING_ID", "")
        user_id = os.environ.get("USER_ID", "")
        if meeting_id and user_id:
            try:
                dynamodb.Table(TABLE).update_item(
                    Key={"PK": f"USER#{user_id}", "SK": f"MEETING#{meeting_id}"},
                    UpdateExpression="SET #s = :s",
                    ExpressionAttributeNames={"#s": "status"},
                    ExpressionAttributeValues={":s": "error"},
                )
            except Exception:
                pass
        sys.exit(1)
