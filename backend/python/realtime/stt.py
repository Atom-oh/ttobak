"""Faster-whisper STT wrapper for real-time transcription."""

import numpy as np
from faster_whisper import WhisperModel


class WhisperSTT:
    """Wrapper around faster-whisper for real-time speech-to-text."""

    def __init__(
        self,
        model_size: str = "large-v3",
        device: str = "cuda",
        compute_type: str = "float16",
    ):
        """Initialize the Whisper model.

        Args:
            model_size: Whisper model size (e.g., "large-v3", "medium", "small")
            device: Device to run inference on ("cuda" or "cpu")
            compute_type: Compute precision ("float16", "int8", "float32")
        """
        self.model_size = model_size
        self.model = WhisperModel(model_size, device=device, compute_type=compute_type)

    def transcribe(self, audio_buffer: bytes, language: str = "ko") -> dict:
        """Transcribe audio buffer to text.

        Args:
            audio_buffer: Raw 16kHz 16-bit PCM audio bytes
            language: Language code for transcription (default: Korean)

        Returns:
            Dictionary with transcription results:
            - text: Full transcribed text
            - segments: List of segment dictionaries with timing info
            - language: Detected or specified language
        """
        if not audio_buffer:
            return {"text": "", "segments": [], "language": language}

        # Convert raw 16-bit PCM bytes to numpy float32 array
        # PCM 16-bit samples are in range [-32768, 32767]
        audio_array = np.frombuffer(audio_buffer, dtype=np.int16).astype(np.float32)
        audio_array /= 32768.0  # Normalize to [-1.0, 1.0]

        # Run transcription with VAD filter for better accuracy
        segments, info = self.model.transcribe(
            audio_array,
            beam_size=5,
            language=language,
            vad_filter=True,
        )

        # Collect segments and build full text
        segment_list = []
        text_parts = []

        for segment in segments:
            segment_list.append(
                {
                    "start": segment.start,
                    "end": segment.end,
                    "text": segment.text.strip(),
                }
            )
            text_parts.append(segment.text.strip())

        full_text = " ".join(text_parts)

        return {
            "text": full_text,
            "segments": segment_list,
            "language": info.language if info.language else language,
        }
