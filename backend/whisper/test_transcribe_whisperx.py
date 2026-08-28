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


class TestLogGpuMemory(unittest.TestCase):
    """_log_gpu_memory is best-effort task-log GPU reporting (FINDING 1 fix:
    replaces the runbook's SSM-attach fallback -- no elevated production
    instance-role access is needed to answer the benchmark's peak-VRAM
    question, just the CloudWatch task log)."""

    def test_nvidia_smi_failure_does_not_raise(self):
        with mock.patch('subprocess.run', side_effect=OSError('nvidia-smi not found')):
            transcribe_whisperx._log_gpu_memory('model-loaded')  # must not raise

    def test_successful_run_prints_gpu_line(self):
        fake_result = mock.Mock(stdout='1024 MiB, 24576 MiB, 5 %\n')
        with mock.patch('subprocess.run', return_value=fake_result), \
             mock.patch('builtins.print') as mock_print:
            transcribe_whisperx._log_gpu_memory('transcribed')

        mock_print.assert_called_once()
        printed = mock_print.call_args[0][0]
        self.assertIn('GPU[transcribed]', printed)
        self.assertIn('1024 MiB, 24576 MiB, 5 %', printed)


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


class TestTryAlign(unittest.TestCase):
    def test_any_none_timestamp_repairs_that_segment_only(self):
        # Regression for FINDING A (round 10): whisperx.align() may emit a
        # segment with a missing/None start or end (partial alignment
        # failure). Returning such a segment would crash build_result's
        # round(seg["start"], 2). Previously this discarded the ENTIRE
        # aligned result, degrading word-majority speaker assignment to
        # segment-overlap for every segment. Since the index mapping is
        # still valid (counts match), only the bad segment is repaired --
        # timestamps copied from the same-index input segment, words
        # dropped -- while every other aligned segment (with words) is
        # kept intact.
        input_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a'},
            {'start': 1.0, 'end': 2.0, 'text': 'b'},
        ]
        aligned_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a', 'words': [{'word': 'a'}]},
            {'start': None, 'end': None, 'text': 'b', 'words': [{'word': 'b'}]},
        ]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertTrue(alignment_enabled)
        # First segment untouched, with words intact.
        self.assertEqual(result_segments[0]['start'], 0.0)
        self.assertEqual(result_segments[0]['end'], 1.0)
        self.assertEqual(result_segments[0]['words'], [{'word': 'a'}])
        # Second segment repaired from the input's segment-level timestamps,
        # words dropped.
        self.assertEqual(result_segments[1]['start'], 1.0)
        self.assertEqual(result_segments[1]['end'], 2.0)
        self.assertNotIn('words', result_segments[1])

    def test_segment_count_mismatch_discards_aligned_result(self):
        # Regression for FINDING 3: whisperx.align() may silently drop a
        # segment (e.g. one it couldn't align at all). Returning a
        # shorter/longer segment list than the input would skew the
        # benchmark, so treat a length mismatch the same as any other
        # alignment failure: discard and fall back to the original input
        # segments with alignment_enabled=False.
        input_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a'},
            {'start': 1.0, 'end': 2.0, 'text': 'b'},
        ]
        aligned_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a'},
        ]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertEqual(result_segments, input_segments)
        self.assertFalse(alignment_enabled)

    def test_all_numeric_timestamps_returns_aligned_result(self):
        input_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a'}]
        aligned_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a', 'words': []}]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertEqual(result_segments, aligned_segments)
        self.assertTrue(alignment_enabled)

    def test_repaired_mix_passes_through_assign_speakers(self):
        # A repaired-mix result (one segment with words, one repaired
        # without) must flow through whisper_common.assign_speakers without
        # error: the good segment uses word-majority voting, the repaired
        # one falls back to segment overlap.
        input_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a'},
            {'start': 1.0, 'end': 2.0, 'text': 'b'},
        ]
        aligned_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a',
             'words': [{'word': 'a', 'start': 0.1, 'end': 0.9}]},
            {'start': None, 'end': None, 'text': 'b',
             'words': [{'word': 'b', 'start': 1.1, 'end': 1.9}]},
        ]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertIn('words', result_segments[0])
        self.assertNotIn('words', result_segments[1])

        turns = [(0.0, 1.0, 'SPEAKER_00'), (1.0, 2.0, 'SPEAKER_01')]
        from whisper_common import assign_speakers
        out = assign_speakers(result_segments, turns)

        self.assertEqual(out[0]['speaker'], 'spk_0')
        self.assertEqual(out[1]['speaker'], 'spk_1')


class TestValidateOutputKey(unittest.TestCase):
    def test_empty_raises(self):
        # Regression for FINDING 1: OUTPUT_KEY is now REQUIRED for this
        # Phase-1 benchmark engine -- an empty key must never silently
        # default into the production transcripts/{meeting_id}.json key.
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key('', 'm123')

    def test_whitespace_raises(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key('   ', 'm123')

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

    def test_mistyped_bench_key_missing_bench_dash_rejected(self):
        # Regression for FINDING 1: an operator typo that drops the
        # "bench-" prefix (e.g. transcripts/m123_bench_whisperx.json instead
        # of bench-transcripts/m123_bench_whisperx.json) must NOT pass as a
        # legitimate multi-part real-pipeline key.
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key(
                'transcripts/m123_bench_whisperx.json', 'm123')

    def test_multipart_key_wrong_digit_count_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key(
                'transcripts/m123_part_2.json', 'm123')

    def test_multipart_key_trailing_suffix_rejected(self):
        with self.assertRaises(ValueError):
            transcribe_whisperx.validate_output_key(
                'transcripts/m123_part_002.json.bak', 'm123')


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
    def test_empty_output_key_is_never_marked(self):
        # Regression for FINDING 1: an empty OUTPUT_KEY now fails fast in
        # validate_output_key before any real work happens, so it must
        # never mark the real meeting as errored.
        self.assertFalse(
            transcribe_whisperx.should_mark_meeting_error('', 'm123'))

    def test_own_meeting_key_is_real_pipeline(self):
        self.assertTrue(transcribe_whisperx.should_mark_meeting_error(
            'transcripts/m123.json', 'm123'))

    def test_own_meeting_multipart_key_is_real_pipeline(self):
        self.assertTrue(transcribe_whisperx.should_mark_meeting_error(
            'transcripts/m123_part_002.json', 'm123'))

    def test_bench_transcripts_prefix_is_never_marked(self):
        self.assertFalse(transcribe_whisperx.should_mark_meeting_error(
            'bench-transcripts/m123_bench_whisperx.json', 'm123'))

    def test_other_meetings_transcripts_key_is_never_marked(self):
        # Regression for the reported incident: a typo'd/mistyped OUTPUT_KEY
        # that validate_output_key correctly rejects must NOT be treated as
        # this meeting's real pipeline output -- it must never mark the real
        # meeting row as errored.
        self.assertFalse(transcribe_whisperx.should_mark_meeting_error(
            'transcripts/OTHER.json', 'm123'))

    def test_mistyped_bench_key_missing_bench_dash_is_never_marked(self):
        # Regression for FINDING 1: same typo'd key as above, checked on the
        # should_mark_meeting_error path.
        self.assertFalse(transcribe_whisperx.should_mark_meeting_error(
            'transcripts/m123_bench_whisperx.json', 'm123'))


class TestValidateOutputKeyAndShouldMarkMeetingErrorAgree(unittest.TestCase):
    """validate_output_key and should_mark_meeting_error must agree on which
    keys count as "real pipeline" for a given meeting_id -- both derive from
    the same _is_real_pipeline_key helper, so this asserts they can't drift."""

    def test_real_pipeline_shapes_agree(self):
        meeting_id = 'm123'
        real_keys = [
            f'transcripts/{meeting_id}.json',
            f'transcripts/{meeting_id}_part_001.json',
            f'transcripts/{meeting_id}_part_042.json',
        ]
        for key in real_keys:
            with self.subTest(key=key):
                # validate_output_key must accept it (no ValueError)...
                resolved = transcribe_whisperx.validate_output_key(
                    key, meeting_id)
                self.assertTrue(resolved)
                # ...and should_mark_meeting_error must treat the raw input
                # (and the key validate_output_key resolved it to) as real.
                self.assertTrue(
                    transcribe_whisperx.should_mark_meeting_error(
                        key, meeting_id))
                self.assertTrue(
                    transcribe_whisperx.should_mark_meeting_error(
                        resolved, meeting_id))

    def test_empty_key_agrees_as_non_real(self):
        # Regression for FINDING 1: an empty/whitespace OUTPUT_KEY is
        # rejected outright by validate_output_key (fail fast, no default),
        # and must also never be treated as "real" by
        # should_mark_meeting_error.
        meeting_id = 'm123'
        for key in ['', '   ']:
            with self.subTest(key=key):
                with self.assertRaises(ValueError):
                    transcribe_whisperx.validate_output_key(key, meeting_id)
                self.assertFalse(
                    transcribe_whisperx.should_mark_meeting_error(
                        key, meeting_id))

    def test_bench_and_rejected_shapes_agree_as_non_real(self):
        meeting_id = 'm123'
        bench_keys = ['bench-transcripts/m123_bench_whisperx.json']
        for key in bench_keys:
            with self.subTest(key=key):
                resolved = transcribe_whisperx.validate_output_key(
                    key, meeting_id)
                self.assertEqual(resolved, key)
                self.assertFalse(
                    transcribe_whisperx.should_mark_meeting_error(
                        key, meeting_id))

        rejected_keys = ['transcripts/OTHER.json', 'files/x']
        for key in rejected_keys:
            with self.subTest(key=key):
                with self.assertRaises(ValueError):
                    transcribe_whisperx.validate_output_key(key, meeting_id)
                # Even though validate_output_key rejects it outright, the
                # error-marking judgment on the same raw key must also be
                # False -- this is the exact incident FINDING 1(b) fixes.
                self.assertFalse(
                    transcribe_whisperx.should_mark_meeting_error(
                        key, meeting_id))


if __name__ == '__main__':
    unittest.main()
