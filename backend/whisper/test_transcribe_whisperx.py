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


if __name__ == '__main__':
    unittest.main()
