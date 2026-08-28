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
