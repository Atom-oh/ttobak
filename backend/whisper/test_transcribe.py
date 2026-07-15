"""Unit tests for the pure speaker-assignment logic in transcribe.py.

Uses stdlib unittest + unittest.mock only. transcribe.py creates boto3
clients and reads required env vars at import time, so both are stubbed
before import -- these tests exercise only _assign_speakers, which is a
pure function with no AWS/pyannote/torch imports.
"""

import os
import sys
import types
import unittest
from unittest import mock

os.environ['BUCKET_NAME'] = 'test-bucket'
os.environ['TABLE_NAME'] = 'test-table'

# faster_whisper is a container-only dependency (not installed in the test
# env, and heavy -- pulls in torch). Stub it so transcribe.py imports.
if 'faster_whisper' not in sys.modules:
    faster_whisper_mod = types.ModuleType('faster_whisper')
    faster_whisper_mod.WhisperModel = mock.MagicMock()
    sys.modules['faster_whisper'] = faster_whisper_mod

with mock.patch('boto3.client'), mock.patch('boto3.resource'):
    import transcribe


class TestAssignSpeakers(unittest.TestCase):
    """_assign_speakers maps each Whisper segment to a diarization turn by
    max time overlap, normalizing raw pyannote labels to spk_N in
    first-appearance order."""

    def test_empty_turns_leaves_segments_unlabeled(self):
        segments = [{'start': 0.0, 'end': 2.0, 'text': 'hi'}]

        result = transcribe._assign_speakers(segments, [])

        self.assertNotIn('speaker', result[0])

    def test_segment_assigned_to_max_overlap_turn(self):
        segments = [
            {'start': 0.0, 'end': 5.0, 'text': 'a'},
            {'start': 5.0, 'end': 10.0, 'text': 'b'},
        ]
        turns = [(0.0, 5.0, 'SPEAKER_00'), (5.0, 10.0, 'SPEAKER_01')]

        result = transcribe._assign_speakers(segments, turns)

        self.assertEqual(result[0]['speaker'], 'spk_0')
        self.assertEqual(result[1]['speaker'], 'spk_1')

    def test_labels_normalized_in_first_appearance_order(self):
        # SPEAKER_01 appears before SPEAKER_00 in the turns list -- spk_N
        # numbering should follow appearance order in the turns, not the
        # raw pyannote numeric suffix.
        segments = [
            {'start': 0.0, 'end': 5.0, 'text': 'a'},
            {'start': 5.0, 'end': 10.0, 'text': 'b'},
        ]
        turns = [(0.0, 5.0, 'SPEAKER_01'), (5.0, 10.0, 'SPEAKER_00')]

        result = transcribe._assign_speakers(segments, turns)

        self.assertEqual(result[0]['speaker'], 'spk_0')
        self.assertEqual(result[1]['speaker'], 'spk_1')

    def test_zero_overlap_segment_falls_back_to_nearest_midpoint(self):
        # A gap between two turns of silence -- a segment landing entirely
        # in the gap has zero overlap with any turn and must fall back to
        # whichever turn's midpoint is closest.
        segments = [{'start': 4.9, 'end': 5.0, 'text': 'gap speech'}]
        turns = [(0.0, 4.8, 'SPEAKER_00'), (5.2, 10.0, 'SPEAKER_01')]

        result = transcribe._assign_speakers(segments, turns)

        # segment midpoint 4.95; turn 0 midpoint 2.4 (dist 2.55), turn 1
        # midpoint 7.6 (dist 2.65) -- turn 0 is closer.
        self.assertEqual(result[0]['speaker'], 'spk_0')

    def test_repeated_speaker_reuses_same_label(self):
        segments = [
            {'start': 0.0, 'end': 2.0, 'text': 'a'},
            {'start': 2.0, 'end': 4.0, 'text': 'b'},
            {'start': 4.0, 'end': 6.0, 'text': 'c'},
        ]
        turns = [
            (0.0, 2.0, 'SPEAKER_00'),
            (2.0, 4.0, 'SPEAKER_01'),
            (4.0, 6.0, 'SPEAKER_00'),
        ]

        result = transcribe._assign_speakers(segments, turns)

        self.assertEqual(result[0]['speaker'], 'spk_0')
        self.assertEqual(result[1]['speaker'], 'spk_1')
        self.assertEqual(result[2]['speaker'], 'spk_0')


if __name__ == '__main__':
    unittest.main()
