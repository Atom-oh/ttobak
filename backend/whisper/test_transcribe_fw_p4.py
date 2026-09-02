"""Unit tests for transcribe_fw_p4's pure config logic.

Same stubbing convention as test_transcribe_whisperx.py: container-only
deps are stubbed and required env vars set before import (transcribe_fw_p4
imports transcribe_whisperx, which builds boto3 clients at import time)."""

import os
import sys
import types
import unittest
from unittest import mock

os.environ['BUCKET_NAME'] = 'test-bucket'
os.environ['TABLE_NAME'] = 'test-table'

for name in ('whisperx', 'torch', 'faster_whisper'):
    if name not in sys.modules:
        sys.modules[name] = types.ModuleType(name)

with mock.patch('boto3.client'), mock.patch('boto3.resource'):
    import transcribe_fw_p4
    import transcribe_whisperx


class TestBenchOnlyOutputKey(unittest.TestCase):
    MEETING = 'a435a3dc-9d3c-41c0-86fe-816073547b23'

    def test_accepts_bench_key(self):
        key = f'bench-transcripts/{self.MEETING}_bench_fw_p4.json'
        self.assertEqual(
            transcribe_fw_p4.validate_bench_only_output_key(key, self.MEETING),
            key)

    def test_rejects_real_pipeline_key_that_base_validator_allows(self):
        # validate_output_key deliberately accepts the exact real-pipeline
        # key as a Phase-2 escape hatch; this bench-only engine must not.
        real_key = f'transcripts/{self.MEETING}.json'
        self.assertEqual(
            transcribe_whisperx.validate_output_key(real_key, self.MEETING),
            real_key)  # precondition: the base validator DOES allow it
        with self.assertRaises(transcribe_whisperx.BenchConfigError):
            transcribe_fw_p4.validate_bench_only_output_key(
                real_key, self.MEETING)

    def test_rejects_empty_and_garbage_keys(self):
        for bad in ('', 'other-prefix/x.json', f'transcripts/{self.MEETING}_part_001.json'):
            with self.assertRaises(transcribe_whisperx.BenchConfigError, msg=bad):
                transcribe_fw_p4.validate_bench_only_output_key(bad, self.MEETING)

    def test_engine_name_marks_output_as_bench_hybrid(self):
        # The engine string is how §6 rows and cmp tooling tell this run
        # apart from both bench_legacy and bench_whisperx outputs.
        self.assertEqual(transcribe_fw_p4.ENGINE_NAME, 'fw-legacy-pyannote4-bench')
        self.assertNotIn('whisperx', transcribe_fw_p4.ENGINE_NAME)


if __name__ == '__main__':
    unittest.main()
