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

import json
import tarfile

from botocore.exceptions import ClientError


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
