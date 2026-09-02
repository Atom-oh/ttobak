"""BENCH-ONLY hybrid engine: legacy faster-whisper ASR + pyannote 4.x
(community-1) diarization, in the whisperx image.

Why this exists (2026-09-02): the a435a3dc bench series showed the two
engines win on DIFFERENT axes -- legacy faster-whisper still has the best
ASR recall (whisperx's best VAD config, silero onset 0.25, leaves ~143s of
real content uncovered vs legacy's ~49s), while whisperx's clean 4-speaker
output beat legacy's 8-with-phantoms. The hypothesis this engine tests is
that the diarization win comes from the pyannote community-1 (4.x) MODEL,
not from whisperx itself: if legacy-parameter faster-whisper ASR plus
community-1 diarization reproduces both the legacy recall AND the clean
speaker count, "keep the existing whisper, upgrade only diarization" wins
over a whisperx cutover -- the operator's stated decision rule is accuracy
and diarization over speed.

It runs from the SAME image and task definition as transcribe_whisperx.py
(the whisperx image already contains faster-whisper as a whisperx
dependency, the same S3-staged large-v3 model dir, and the community-1
diarization bundle), selected per run via an ECS environment override:
    {"name": "ENGINE", "value": "fw_p4"}
The image's ENTRYPOINT is pinned to run_engine.py, which allowlists the
two engine scripts and rejects command-override arguments outright (see
its docstring for the security rationale). No new task definition or IAM
change; the existing Dockerfile gains COPY entries plus the dispatcher
ENTRYPOINT, so ONE image rebuild is required before the first hybrid run
(an older image has no run_engine/ENGINE handling — verify the engine per
the runbook §3c before recording results).

Deliberately bench-only, enforced two ways: OUTPUT_KEY goes through
validate_output_key AND must additionally sit under bench-transcripts/ --
the real-pipeline escape hatch validate_output_key allows for Phase 2 is
refused here, so this file can never be wired into the meeting pipeline
by a task-definition edit alone.

ASR parameters mirror transcribe.py's exactly (language=ko, beam_size=5,
vad_filter=True with min_silence_duration_ms=500, word_timestamps=True,
initial_prompt from the custom vocab). One known non-parity: this image
pins faster-whisper 1.2.1 (whisperx 3.8.6's dependency), while the legacy
Dockerfile installs faster-whisper UNPINNED (whatever was current at its
last build) -- treat a surprising ASR delta vs bench_legacy as possibly
version-driven and check the running legacy container's actually installed
version before concluding anything about the parameters.

PII hygiene follows transcribe_whisperx.py: transcript text never reaches
logs; fatal logging is type-only via format_fatal_error.
"""
import os
import sys
import time

import whisper_common as common

# Reuses the whisperx module's env-derived clients (s3/table/BUCKET) and its
# reviewed helpers; importing it only builds clients, it runs no pipeline work.
import transcribe_whisperx as wx


ENGINE_NAME = "fw-legacy-pyannote4-bench"


def validate_bench_only_output_key(output_key: str, meeting_id: str) -> str:
    """validate_output_key, then refuse anything outside bench-transcripts/.

    validate_output_key deliberately accepts exact real-pipeline keys as a
    Phase-2 escape hatch; this engine is an experiment and must never write
    where the summarize pipeline reads.
    """
    key = wx.validate_output_key(output_key, meeting_id)
    if not key.startswith("bench-transcripts/"):
        raise wx.BenchConfigError(
            "transcribe_fw_p4 is bench-only: OUTPUT_KEY must be under "
            "bench-transcripts/")
    if ".." in key.split("/"):
        # S3 and IAM treat keys literally, so 'bench-transcripts/../x' never
        # actually escapes the prefix -- but CLAUDE.md's trust-boundary rule
        # is to reject traversal shapes outright rather than rely on that.
        raise wx.BenchConfigError(
            "transcribe_fw_p4 OUTPUT_KEY must not contain '..' segments")
    return key


def main():
    meeting_id = os.environ.get("MEETING_ID", "").strip()
    if not meeting_id:
        raise wx.BenchConfigError("MEETING_ID env var is required")
    user_id = os.environ.get("USER_ID", "").strip()
    if not user_id:
        raise wx.BenchConfigError("USER_ID env var is required")
    output_key = validate_bench_only_output_key(
        os.environ.get("OUTPUT_KEY", ""), meeting_id)

    audio_key = os.environ.get("AUDIO_KEY")
    if audio_key:
        audio_key = wx.validate_audio_key(audio_key, user_id, meeting_id)
    if audio_key and not common.audio_key_exists(wx.s3, wx.BUCKET, audio_key):
        print(f"AUDIO_KEY {audio_key!r} not found in S3; falling back to prefix scan")
        audio_key = None
    if not audio_key:
        audio_key = common.find_audio_key(wx.s3, wx.BUCKET, user_id, meeting_id)
    if not audio_key:
        raise wx.BenchConfigError(f"No audio file found for meeting {meeting_id}")

    basename = audio_key.rsplit("/", 1)[-1]
    ext = basename.rsplit(".", 1)[-1] if "." in basename else "bin"
    local_path = f"/tmp/audio.{ext}"
    print(f"Downloading s3://{wx.BUCKET}/{audio_key}")
    wx.s3.download_file(wx.BUCKET, audio_key, local_path)

    vocab_prompt = common.load_custom_vocab_prompt(wx.s3, wx.BUCKET, wx.VOCAB_KEY)
    env_prompt = os.environ.get("INITIAL_PROMPT", "").strip()
    if env_prompt:
        vocab_prompt = f"{vocab_prompt} {env_prompt}".strip()

    model_dir = wx._ensure_model()

    from faster_whisper import WhisperModel
    print("Loading Whisper large-v3 via faster-whisper (GPU float16, legacy params)...")
    model = WhisperModel(model_dir, device="cuda", compute_type="float16")
    wx._log_gpu_memory("model-loaded")

    print("Transcribing (legacy sequential)...")
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
    fw_segments, info = model.transcribe(local_path, **transcribe_kwargs)
    segments = [
        {"start": seg.start, "end": seg.end, "text": seg.text}
        for seg in fw_segments
    ]
    elapsed = time.time() - start
    print(f"Done: {len(segments)} segments in {elapsed:.1f}s")
    wx._log_gpu_memory("transcribed")

    # Free the ASR model before diarization loads its own (same per-stage
    # VRAM-sampling rationale as transcribe_whisperx.main).
    del model
    wx._empty_cuda_cache()

    num_speakers_env = os.environ.get("NUM_SPEAKERS", "").strip()
    num_speakers = int(num_speakers_env) if num_speakers_env.isdigit() else None

    diarization_config = wx._ensure_diarization_config()
    num_speakers_detected = 0
    turns = []
    if diarization_config and segments:
        print("Diarizing (pyannote 4.x community-1)...")
        diarize_start = time.time()
        turns = wx._diarize(diarization_config, local_path, num_speakers)
        wx._log_gpu_memory("diarized")
        if turns:
            # use_words=False: legacy faster-whisper segments carry no word
            # dicts here, so overlap assignment matches transcribe.py's
            # segment-level behavior.
            segments = common.assign_speakers(segments, turns, use_words=False)
            num_speakers_detected = len({t[2] for t in turns})
            print(f"Diarization done in {time.time() - diarize_start:.1f}s, "
                  f"{num_speakers_detected} speaker(s) detected")
        else:
            print("Diarization produced no turns; segments left unlabeled")
    elif not diarization_config:
        # Loud skip: a §6 row from a run whose diarization silently never
        # ran would be recorded as a hybrid data point when it's really
        # ASR-only.
        print("Diarization model unavailable; segments left unlabeled")
    else:
        print("No ASR segments; diarization skipped")

    result = wx.build_result(
        segments=segments, language=info.language,
        language_probability=info.language_probability,
        duration_seconds=info.duration, transcription_seconds=elapsed,
        diarization_enabled=bool(turns),
        num_speakers_detected=num_speakers_detected,
        # No whisperx alignment stage in this engine.
        alignment_enabled=False, alignment_repaired=0)
    result["whisper_metadata"]["engine"] = ENGINE_NAME

    common.upload_transcript(wx.s3, wx.BUCKET, output_key, result)
    print(f"Uploaded s3://{wx.BUCKET}/{output_key}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(wx.format_fatal_error(e), file=sys.stderr)
        # No mark_meeting_error branch at all: this engine refuses non-bench
        # OUTPUT_KEYs up front, and bench failures must never touch meeting
        # rows (should_mark_meeting_error would agree, but not calling it is
        # simpler to reason about for a bench-only file).
        sys.exit(1)
