"""Unit tests for transcribe_whisperx's pure result-building logic.

Same stubbing convention as test_transcribe.py: whisperx/torch are
container-only deps, and the module reads required env vars at import time,
so both are stubbed before import."""

import contextlib
import gc
import io
import os
import sys
import types
import unittest
import weakref
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
            alignment_enabled=True, alignment_repaired=2)

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
        self.assertEqual(meta['alignment_repaired'], 2)
        # words are an internal alignment artifact -- stripped from output,
        # and start/end rounded like the legacy engine's segments
        self.assertEqual(meta['segments'][0],
                         {'start': 0.0, 'end': 2.0, 'text': '안녕하세요',
                          'speaker': 'spk_0'})
        self.assertEqual(meta['segments'][1],
                         {'start': 2.0, 'end': 4.0, 'text': '반갑습니다'})

    def test_alignment_repaired_defaults_to_zero(self):
        segments = [{'start': 0.0, 'end': 1.0, 'text': 'a'}]
        result = transcribe_whisperx.build_result(
            segments=segments, language='ko', language_probability=0.0,
            duration_seconds=1.0, transcription_seconds=0.5,
            diarization_enabled=False, num_speakers_detected=0,
            alignment_enabled=False)
        self.assertEqual(result['whisper_metadata']['alignment_repaired'], 0)


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

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}), \
                mock.patch.object(
                    transcribe_whisperx, '_log_gpu_memory') as mock_log_gpu:
            result_segments, alignment_enabled, repaired = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        # FINDING 1 (round 13): the run's likely peak VRAM is alignment's own
        # residency -- must be sampled while the align model is still
        # resident, not only after _try_align's own `finally` has freed it.
        mock_log_gpu.assert_called_once_with('aligning')

        self.assertTrue(alignment_enabled)
        self.assertEqual(repaired, 1)
        # First segment untouched, with words intact.
        self.assertEqual(result_segments[0]['start'], 0.0)
        self.assertEqual(result_segments[0]['end'], 1.0)
        self.assertEqual(result_segments[0]['words'], [{'word': 'a'}])
        # Second segment repaired from the input's segment-level timestamps,
        # words dropped.
        self.assertEqual(result_segments[1]['start'], 1.0)
        self.assertEqual(result_segments[1]['end'], 2.0)
        self.assertNotIn('words', result_segments[1])

    def test_segment_count_mismatch_accepts_resplit_and_preserves_text(self):
        # whisperx.align() re-splits segments along alignment boundaries as
        # its NORMAL behavior (first real bench run: 43 inputs -> 117
        # aligned). Re-split output must be ACCEPTED, and — since
        # build_result joins SEGMENT texts into the final transcript — no
        # segment may be dropped: timestamp-less ones are repaired by
        # interpolating from neighbor boundaries and lose only their words.
        input_segments = [
            {'start': 0.0, 'end': 2.0, 'text': 'a b'},
            {'start': 2.0, 'end': 4.0, 'text': 'c'},
        ]
        aligned_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'a', 'words': [{'word': 'a', 'start': 0.1, 'end': 0.9}]},
            {'start': None, 'end': None, 'text': 'b', 'words': [{'word': 'b'}]},
            {'start': 3.0, 'end': 4.0, 'text': 'c', 'words': [{'word': 'c', 'start': 3.1, 'end': 3.9}]},
        ]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled, repaired = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertTrue(alignment_enabled)
        # ALL text preserved — nothing dropped
        self.assertEqual([s['text'] for s in result_segments], ['a', 'b', 'c'])
        self.assertEqual(repaired, 1)
        # middle segment interpolated: prev end (1.0) ~ next valid start (3.0)
        self.assertEqual(result_segments[1]['start'], 1.0)
        self.assertEqual(result_segments[1]['end'], 3.0)
        self.assertNotIn('words', result_segments[1])
        # neighbors keep their words
        self.assertIn('words', result_segments[0])
        self.assertIn('words', result_segments[2])

    def test_interpolation_consecutive_run_splits_gap_evenly(self):
        # Round-2 MAJOR-1: a naive per-segment walk gave the first missing
        # segment the whole gap and collapsed the rest to zero length —
        # zero-length segments overlap no diarization turn. A run must
        # split its gap evenly.
        segs = [
            {'start': 0.0, 'end': 1.0, 'text': 'a', 'words': []},
            {'start': None, 'end': None, 'text': 'b', 'words': []},
            {'start': None, 'end': None, 'text': 'c', 'words': []},
            {'start': 3.0, 'end': 4.0, 'text': 'd', 'words': []},
        ]
        touched = transcribe_whisperx._interpolate_missing_timestamps(segs, 0.0, 4.0)
        self.assertEqual(touched, 2)
        self.assertEqual((segs[1]['start'], segs[1]['end']), (1.0, 2.0))
        self.assertEqual((segs[2]['start'], segs[2]['end']), (2.0, 3.0))
        self.assertNotIn('words', segs[1])
        self.assertNotIn('words', segs[2])
        self.assertIn('words', segs[0])

    def test_interpolation_partial_missing_keeps_known_side(self):
        # Round-2 MAJOR-2: a segment with a valid aligner-provided start
        # must keep it — only the missing side is filled.
        segs = [
            {'start': 0.0, 'end': 1.0, 'text': 'a'},
            {'start': 4.0, 'end': None, 'text': 'b'},
            {'start': None, 'end': 7.0, 'text': 'c'},
        ]
        touched = transcribe_whisperx._interpolate_missing_timestamps(segs, 0.0, 8.0)
        self.assertEqual(touched, 2)
        # Known sides preserved; missing sides filled from the SNAPSHOT of
        # aligner-provided boundaries (either field of a neighbor), never
        # from values this pass itself filled — fill order must not bleed.
        self.assertEqual(segs[1]['start'], 4.0)   # known side preserved
        self.assertEqual(segs[1]['end'], 7.0)     # next known boundary = c's end
        self.assertEqual(segs[2]['end'], 7.0)     # known side preserved
        self.assertEqual(segs[2]['start'], 4.0)   # prev known boundary = b's start

    def test_interpolation_all_missing_distributes_span(self):
        segs = [
            {'start': None, 'end': None, 'text': 'a'},
            {'start': None, 'end': None, 'text': 'b'},
        ]
        touched = transcribe_whisperx._interpolate_missing_timestamps(segs, 0.0, 10.0)
        self.assertEqual(touched, 2)
        self.assertEqual((segs[0]['start'], segs[0]['end']), (0.0, 5.0))
        self.assertEqual((segs[1]['start'], segs[1]['end']), (5.0, 10.0))

    def test_resplit_with_wholesale_text_loss_falls_back(self):
        # Coverage sanity: if the aligner returned meaningfully less TEXT
        # than it was given (wholesale content loss, not re-segmentation),
        # the aligned result must be discarded — accepting it would skew the
        # ASR-quality comparison the benchmark exists to make.
        input_segments = [
            {'start': 0.0, 'end': 2.0, 'text': 'aaaa bbbb'},
            {'start': 2.0, 'end': 4.0, 'text': 'cccc'},
        ]
        aligned_segments = [
            {'start': 0.0, 'end': 1.0, 'text': 'aaaa',
             'words': [{'word': 'aaaa', 'start': 0.1, 'end': 0.9}]},
        ]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled, repaired = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertEqual(result_segments, input_segments)
        self.assertFalse(alignment_enabled)
        self.assertEqual(repaired, 0)

    def test_load_wav_waveform_contract(self):
        # _load_wav_waveform must return pyannote's documented in-memory
        # input shape ({'waveform', 'sample_rate'}) parsed from OUR OWN
        # ffmpeg-written 16k mono s16le WAV — this is what bypasses the
        # torchcodec decode path that broke the first bench run. numpy/torch
        # are container-only; stub just the two calls the function makes.
        import struct
        import tempfile
        import wave

        with tempfile.NamedTemporaryFile(suffix='.wav', delete=False) as f:
            path = f.name
        self.addCleanup(os.unlink, path)
        with wave.open(path, 'wb') as w:
            w.setnchannels(1)
            w.setsampwidth(2)
            w.setframerate(16000)
            w.writeframes(struct.pack('<4h', 0, 1000, -1000, 32767))

        class FakeArr:
            def __init__(self, raw):
                self.raw = raw
            def astype(self, dtype):
                return self
            def __truediv__(self, other):
                return self

        class FakeTensor:
            def unsqueeze(self, dim):
                assert dim == 0
                return 'TENSOR'

        fake_np = types.ModuleType('numpy')
        fake_np.int16 = 'int16'
        fake_np.float32 = 'float32'
        captured = {}
        def frombuffer(raw, dtype):
            captured['raw_len'] = len(raw)
            return FakeArr(raw)
        fake_np.frombuffer = frombuffer

        fake_torch = types.ModuleType('torch')
        fake_torch.from_numpy = lambda arr: FakeTensor()

        with mock.patch.dict(sys.modules, {'numpy': fake_np, 'torch': fake_torch}):
            out = transcribe_whisperx._load_wav_waveform(path)

        self.assertEqual(out['sample_rate'], 16000)
        self.assertEqual(out['waveform'], 'TENSOR')
        self.assertEqual(captured['raw_len'], 8)  # 4 frames * 2 bytes

    def test_resplit_with_no_timestamps_and_text_loss_falls_back(self):
        # A degenerate aligned result that both re-splits AND loses text —
        # the coverage check trips before interpolation even runs.
        input_segments = [
            {'start': 0.0, 'end': 2.0, 'text': 'a'},
            {'start': 2.0, 'end': 4.0, 'text': 'b'},
        ]
        aligned_segments = [{'start': None, 'end': None, 'text': 'x'}]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled, repaired = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertEqual(result_segments, input_segments)
        self.assertFalse(alignment_enabled)
        self.assertEqual(repaired, 0)

    def test_all_numeric_timestamps_returns_aligned_result(self):
        input_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a'}]
        aligned_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a', 'words': []}]

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled, _ = transcribe_whisperx._try_align(
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
            result_segments, alignment_enabled, _ = transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        self.assertIn('words', result_segments[0])
        self.assertNotIn('words', result_segments[1])

        turns = [(0.0, 1.0, 'SPEAKER_00'), (1.0, 2.0, 'SPEAKER_01')]
        from whisper_common import assign_speakers
        out = assign_speakers(result_segments, turns)

        self.assertEqual(out[0]['speaker'], 'spk_0')
        self.assertEqual(out[1]['speaker'], 'spk_1')

    def test_align_warnings_containing_segment_text_are_not_printed(self):
        # FINDING 2(a) (round 11): whisperx.align() may print warnings that
        # embed per-segment TRANSCRIPT TEXT to stdout/stderr on partial
        # alignment failures (e.g. 'Failed to align segment ("...")'). That
        # text is meeting PII the task log group would otherwise retain for
        # 30 days. _try_align must capture and suppress it -- only a derived
        # line count may reach the real stdout.
        input_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a'}]
        aligned_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a', 'words': []}]

        SECRET_TEXT = 'SECRET_MEETING_TRANSCRIPT_FRAGMENT_XYZ'

        def fake_align(segments, model, metadata, audio, device):
            print(f'Failed to align segment ("{SECRET_TEXT}")')
            return {'segments': aligned_segments}

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            object(), object())
        fake_whisperx.align = fake_align

        outer_stdout = io.StringIO()
        with contextlib.redirect_stdout(outer_stdout), \
                mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            transcribe_whisperx._try_align(
                input_segments, audio=object(), language='ko')

        printed = outer_stdout.getvalue()
        self.assertNotIn(SECRET_TEXT, printed)
        self.assertIn('warning line(s)', printed)

    def test_exception_message_text_not_printed_on_failure(self):
        # FINDING 2(a)/(c): an exception's str() can also carry transcript
        # fragments from library internals -- only the exception type name
        # may reach the log on this path, never str(e).
        SECRET_TEXT = 'SECRET_EXCEPTION_TEXT_ABC'

        def raising_load_align_model(language_code, device):
            raise RuntimeError(SECRET_TEXT)

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = raising_load_align_model

        outer_stdout = io.StringIO()
        with contextlib.redirect_stdout(outer_stdout), \
                mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            result_segments, alignment_enabled, repaired = transcribe_whisperx._try_align(
                [{'start': 0.0, 'end': 1.0, 'text': 'a'}], audio=object(),
                language='ko')

        self.assertFalse(alignment_enabled)
        self.assertEqual(repaired, 0)
        printed = outer_stdout.getvalue()
        self.assertNotIn(SECRET_TEXT, printed)
        self.assertIn('RuntimeError', printed)

    def test_align_model_actually_freed_after_successful_alignment(self):
        # FINDING 1 (round 12): a previous "freed" test mocked
        # _free_align_model itself, so it could only assert the helper was
        # CALLED, not that the model was actually released -- and in fact it
        # wasn't (the helper did `del` on its own parameter, which drops only
        # the callee's binding while _try_align's own local `align_model`
        # kept the object alive). This test uses a REAL sentinel object (not
        # a mock) and a weakref to it, so it can only pass if _try_align's
        # caller-side scope genuinely drops its last reference.
        class _AlignModelSentinel:
            """Plain object subclass -- supports weakref, unlike a bare
            object() instance."""

        sentinel = _AlignModelSentinel()
        sentinel_ref = weakref.ref(sentinel)

        aligned_segments = [{'start': 0.0, 'end': 1.0, 'text': 'a', 'words': []}]
        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            sentinel, object())
        fake_whisperx.align = lambda segments, model, metadata, audio, device: {
            'segments': aligned_segments}

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            transcribe_whisperx._try_align(
                [{'start': 0.0, 'end': 1.0, 'text': 'a'}], audio=object(),
                language='ko')

        del sentinel  # drop this test's own reference too
        gc.collect()
        self.assertIsNone(
            sentinel_ref(),
            'align model sentinel is still alive after _try_align returned '
            '-- something is still holding a reference, so it was never '
            'actually freed')

    def test_align_model_actually_freed_after_exception(self):
        # Same real-leak check as above, but on the exception path -- the
        # model must still be released even though alignment failed.
        class _AlignModelSentinel:
            """Plain object subclass -- supports weakref, unlike a bare
            object() instance."""

        sentinel = _AlignModelSentinel()
        sentinel_ref = weakref.ref(sentinel)

        def raising_align(segments, model, metadata, audio, device):
            raise RuntimeError('boom')

        fake_whisperx = types.ModuleType('whisperx')
        fake_whisperx.load_align_model = lambda language_code, device: (
            sentinel, object())
        fake_whisperx.align = raising_align

        with mock.patch.dict(sys.modules, {'whisperx': fake_whisperx}):
            transcribe_whisperx._try_align(
                [{'start': 0.0, 'end': 1.0, 'text': 'a'}], audio=object(),
                language='ko')

        del sentinel
        gc.collect()
        self.assertIsNone(
            sentinel_ref(),
            'align model sentinel is still alive after _try_align raised '
            '-- something is still holding a reference, so it was never '
            'actually freed')


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


class TestBenchConfigErrorRaised(unittest.TestCase):
    """FINDING 2 (round 12): validate_output_key/validate_audio_key must
    raise BenchConfigError specifically (not a bare ValueError), so
    format_fatal_error can tell an operator-facing config error (safe to log
    verbatim) apart from a library exception (never safe to log verbatim).
    BenchConfigError subclasses ValueError, so existing
    assertRaises(ValueError) coverage above still passes unchanged."""

    def test_validate_output_key_raises_bench_config_error(self):
        with self.assertRaises(transcribe_whisperx.BenchConfigError):
            transcribe_whisperx.validate_output_key('', 'm123')

    def test_validate_audio_key_raises_bench_config_error(self):
        with self.assertRaises(transcribe_whisperx.BenchConfigError):
            transcribe_whisperx.validate_audio_key(
                'audio/u2/m123/rec.mp3', 'u1', 'm123')


class TestFormatFatalError(unittest.TestCase):
    """FINDING 2 (round 12): a bare `str(e)[:300]` length cap is not
    redaction -- 300 characters of a library exception can still embed a
    transcript fragment verbatim. format_fatal_error must print a
    BenchConfigError's message verbatim (safe by construction) but reduce
    any OTHER exception to its type name plus a message length, never the
    message content itself."""

    def test_generic_exception_message_is_not_printed(self):
        marker = 'SECRET_MEETING_TRANSCRIPT_FRAGMENT_777'
        line = transcribe_whisperx.format_fatal_error(RuntimeError(marker))
        self.assertNotIn(marker, line)
        self.assertIn('RuntimeError', line)
        self.assertIn(str(len(marker)), line)

    def test_bench_config_error_message_is_printed_verbatim(self):
        message = "OUTPUT_KEY 'files/x' is not a valid bench-transcripts/ key"
        line = transcribe_whisperx.format_fatal_error(
            transcribe_whisperx.BenchConfigError(message))
        self.assertIn(message, line)
        self.assertIn('BenchConfigError', line)

    def test_bench_config_error_subclasses_value_error(self):
        self.assertTrue(
            issubclass(transcribe_whisperx.BenchConfigError, ValueError))


if __name__ == '__main__':
    unittest.main()
