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
