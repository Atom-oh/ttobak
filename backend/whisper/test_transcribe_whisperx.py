"""Unit tests for transcribe_whisperx's pure result-building logic.

Same stubbing convention as test_transcribe.py: whisperx/torch are
container-only deps, and the module reads required env vars at import time,
so both are stubbed before import."""

import os
import sys
import types
import unittest
from unittest import mock

os.environ['BUCKET_NAME'] = 'test-bucket'
os.environ['TABLE_NAME'] = 'test-table'

for name in ('whisperx', 'torch'):
    if name not in sys.modules:
        sys.modules[name] = types.ModuleType(name)

with mock.patch('boto3.client'), mock.patch('boto3.resource'):
    import transcribe_whisperx


class TestBuildResult(unittest.TestCase):
    def test_output_schema_matches_summarize_contract(self):
        segments = [
            {'start': 0.0, 'end': 2.0, 'text': '안녕하세요', 'speaker': 'spk_0',
             'words': [{'word': '안녕하세요', 'start': 0.1, 'end': 1.9}]},
            {'start': 2.0, 'end': 4.0, 'text': '반갑습니다'},
        ]
        result = transcribe_whisperx.build_result(
            segments=segments, language='ko', language_probability=0.0,
            duration_seconds=4.0, transcription_seconds=1.5,
            diarization_enabled=True, num_speakers_detected=1,
            alignment_enabled=True)

        self.assertEqual(result['status'], 'COMPLETED')
        self.assertEqual(result['results']['transcripts'][0]['transcript'],
                         '안녕하세요 반갑습니다')
        meta = result['whisper_metadata']
        self.assertEqual(meta['engine'], 'whisperx-large-v3-gpu')
        self.assertEqual(meta['language'], 'ko')
        self.assertEqual(meta['duration_seconds'], 4.0)
        self.assertEqual(meta['diarization'],
                         {'enabled': True, 'num_speakers_detected': 1})
        self.assertTrue(meta['alignment_enabled'])
        # words are an internal alignment artifact -- stripped from output,
        # and start/end rounded like the legacy engine's segments
        self.assertEqual(meta['segments'][0],
                         {'start': 0.0, 'end': 2.0, 'text': '안녕하세요',
                          'speaker': 'spk_0'})
        self.assertEqual(meta['segments'][1],
                         {'start': 2.0, 'end': 4.0, 'text': '반갑습니다'})


class TestTurnsFromDiarization(unittest.TestCase):
    def test_annotation_shape_direct_itertracks(self):
        """3.x-style: pipeline(...) returns the Annotation directly."""
        class FakeTurn:
            def __init__(self, start, end):
                self.start, self.end = start, end

        class FakeAnnotation:
            def itertracks(self, yield_label=False):
                yield FakeTurn(0.0, 1.0), None, 'SPEAKER_00'
                yield FakeTurn(1.0, 2.0), None, 'SPEAKER_01'

        turns = transcribe_whisperx._turns_from_diarization(FakeAnnotation())
        self.assertEqual(turns, [(0.0, 1.0, 'SPEAKER_00'), (1.0, 2.0, 'SPEAKER_01')])

    def test_wrapper_shape_speaker_diarization_attribute(self):
        """4.x-style: pipeline(...) returns a wrapper exposing
        .speaker_diarization as the Annotation."""
        class FakeTurn:
            def __init__(self, start, end):
                self.start, self.end = start, end

        class FakeAnnotation:
            def itertracks(self, yield_label=False):
                yield FakeTurn(0.0, 1.5), None, 'SPEAKER_00'

        class FakeResultWrapper:
            def __init__(self):
                self.speaker_diarization = FakeAnnotation()

        turns = transcribe_whisperx._turns_from_diarization(FakeResultWrapper())
        self.assertEqual(turns, [(0.0, 1.5, 'SPEAKER_00')])


class TestValidateOutputKey(unittest.TestCase):
    def test_empty_defaults_to_real_pipeline_key(self):
        self.assertEqual(
            transcribe_whisperx.validate_output_key('', 'm123'),
            'transcripts/m123.json')

    def test_whitespace_defaults_to_real_pipeline_key(self):
        self.assertEqual(
            transcribe_whisperx.validate_output_key('   ', 'm123'),
            'transcripts/m123.json')

    def test_bench_transcripts_key_passes_through(self):
        self.assertEqual(
            transcribe_whisperx.validate_output_key(
                'bench-transcripts/m123_bench_whisperx.json', 'm123'),
            'bench-transcripts/m123_bench_whisperx.json')

    def test_own_meeting_key_passes_through(self):
        self.assertEqual(
            transcribe_whisperx.validate_output_key(
                'transcripts/m123.json', 'm123'),
            'transcripts/m123.json')

    def test_own_meeting_multipart_key_passes_through(self):
        self.assertEqual(
            transcribe_whisperx.validate_output_key(
                'transcripts/m123_part_001.json', 'm123'),
            'transcripts/m123_part_001.json')

    def test_other_meetings_transcripts_key_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key(
                'transcripts/OTHER.json', 'm123')

    def test_arbitrary_prefix_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key('files/x', 'm123')


class TestValidateAudioKey(unittest.TestCase):
    def test_key_under_own_meeting_prefix_passes(self):
        self.assertEqual(
            transcribe_whisperx.validate_audio_key(
                'audio/u1/m123/rec.mp3', 'u1', 'm123'),
            'audio/u1/m123/rec.mp3')

    def test_other_users_audio_key_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_audio_key(
                'audio/u2/m123/rec.mp3', 'u1', 'm123')

    def test_other_meetings_audio_key_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_audio_key(
                'audio/u1/OTHER/rec.mp3', 'u1', 'm123')


class TestShouldMarkMeetingError(unittest.TestCase):
    def test_empty_output_key_defaults_to_real_pipeline(self):
        self.assertTrue(transcribe_whisperx.should_mark_meeting_error(''))

    def test_transcripts_prefix_is_real_pipeline(self):
        self.assertTrue(transcribe_whisperx.should_mark_meeting_error(
            'transcripts/m123.json'))

    def test_bench_transcripts_prefix_is_never_marked(self):
        self.assertFalse(transcribe_whisperx.should_mark_meeting_error(
            'bench-transcripts/m123_bench_whisperx.json'))


if __name__ == '__main__':
    unittest.main()
