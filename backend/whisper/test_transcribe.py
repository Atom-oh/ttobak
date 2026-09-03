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


class TestSafeDiarize(unittest.TestCase):
    """_safe_diarize wraps audio-conversion + diarization so that an
    ffmpeg failure (bad/unusual codec) never aborts the whole transcription
    job -- it must return [] on any failure, matching _diarize's own
    existing best-effort contract, instead of letting subprocess.run's
    CalledProcessError propagate out of main()."""

    def test_returns_empty_list_when_wav_conversion_fails(self):
        with mock.patch.object(
            transcribe, '_to_mono16k_wav',
            side_effect=__import__('subprocess').CalledProcessError(1, ['ffmpeg']),
        ):
            result = transcribe._safe_diarize('config.yaml', '/tmp/audio.webm', None)

        self.assertEqual(result, [])

    def test_returns_diarize_result_on_success(self):
        with mock.patch.object(transcribe, '_to_mono16k_wav', return_value='/tmp/audio-16k-mono.wav'), \
             mock.patch.object(transcribe, '_diarize', return_value=[(0.0, 1.0, 'SPEAKER_00')]):
            result = transcribe._safe_diarize('config.yaml', '/tmp/audio.webm', None)

        self.assertEqual(result, [(0.0, 1.0, 'SPEAKER_00')])


class TestTurnsFromDiarization(unittest.TestCase):
    """Phase 2 (pyannote 3.x -> 4.x community-1): 4.x may return a result
    wrapper whose Annotation lives at .speaker_diarization, while 3.x
    returned the Annotation itself -- _turns_from_diarization must unwrap
    both shapes (same helper the whisperx bench engine validated)."""

    class _FakeTurn:
        def __init__(self, start, end):
            self.start, self.end = start, end

    class _FakeAnnotation:
        def __init__(self, turns):
            self._turns = turns

        def itertracks(self, yield_label=False):
            for start, end, label in self._turns:
                yield TestTurnsFromDiarization._FakeTurn(start, end), None, label

    def test_3x_annotation_returned_directly(self):
        ann = self._FakeAnnotation([(0.0, 1.5, 'SPEAKER_00'), (1.5, 3.0, 'SPEAKER_01')])
        self.assertEqual(
            transcribe._turns_from_diarization(ann),
            [(0.0, 1.5, 'SPEAKER_00'), (1.5, 3.0, 'SPEAKER_01')])

    def test_4x_wrapper_unwrapped_via_speaker_diarization(self):
        ann = self._FakeAnnotation([(0.0, 2.0, 'SPEAKER_00')])
        wrapper = types.SimpleNamespace(speaker_diarization=ann)
        self.assertEqual(
            transcribe._turns_from_diarization(wrapper),
            [(0.0, 2.0, 'SPEAKER_00')])

    def test_wrapper_with_none_annotation_is_empty_not_error(self):
        # An explicit None (no speech found) is a legitimate empty result --
        # it must not surface as an AttributeError routed through _diarize's
        # failure logging.
        wrapper = types.SimpleNamespace(speaker_diarization=None)
        self.assertEqual(transcribe._turns_from_diarization(wrapper), [])


class TestBundlePyannoteMismatch(unittest.TestCase):
    """ADR-035 deploy-ordering guard: a half-deployed state (new image +
    old bundle, or the reverse) must produce one loud line and a skip, not
    a swallowed config-incompatibility inside _diarize."""

    def test_matching_pairs_pass(self):
        for key, ver in (('models/whisperx-diarization-4.x.tar.gz', '4.0.7'),
                         ('models/pyannote-diarization-3.1.tar.gz', '3.4.0')):
            self.assertIsNone(
                transcribe._bundle_pyannote_mismatch(key, ver), msg=key)

    def test_mismatched_pairs_flagged_both_directions(self):
        msg = transcribe._bundle_pyannote_mismatch(
            'models/pyannote-diarization-3.1.tar.gz', '4.0.7')
        self.assertIn('MISMATCH', msg)
        msg = transcribe._bundle_pyannote_mismatch(
            'models/whisperx-diarization-4.x.tar.gz', '3.4.0')
        self.assertIn('MISMATCH', msg)
        self.assertIn('ADR-035', msg)

    def test_unrecognized_key_skips_check(self):
        # No marker at all, and a numeric marker outside the known
        # generations (a future date-based scheme) — both skip, never
        # false-flag.
        self.assertIsNone(transcribe._bundle_pyannote_mismatch(
            'models/some-future-bundle.tar.gz', '4.0.7'))
        self.assertIsNone(transcribe._bundle_pyannote_mismatch(
            'models/diarization-20270101.tar.gz', '4.0.7'))


class TestDiarizeKwargsPassthrough(unittest.TestCase):
    def test_max_speakers_reaches_pipeline_call(self):
        # NUM_SPEAKERS flows as max_speakers; if a pyannote major ever
        # rejects that kwarg it becomes a swallowed TypeError -> [] only on
        # meetings WITH Participants set — a nasty partial regression. Pin
        # the passthrough with a stub Pipeline.
        captured = {}

        class FakePipeline:
            @staticmethod
            def from_pretrained(config_path):
                return FakePipeline()

            def to(self, device):
                return self

            def __call__(self, waveform, **kwargs):
                captured.update(kwargs)
                return types.SimpleNamespace(speaker_diarization=None)

        fake_torch = types.ModuleType('torch')
        fake_torch.device = lambda name: name
        fake_pa = types.ModuleType('pyannote.audio')
        fake_pa.Pipeline = FakePipeline
        fake_pyannote = types.ModuleType('pyannote')
        fake_pyannote.audio = fake_pa
        modules = {'torch': fake_torch, 'pyannote': fake_pyannote,
                   'pyannote.audio': fake_pa}
        with mock.patch.dict(sys.modules, modules), \
                mock.patch.object(transcribe, '_load_wav_waveform',
                                  return_value={'waveform': 'W',
                                                'sample_rate': 16000}):
            out = transcribe._diarize('config.yaml', '/tmp/a.wav', 5)
        self.assertEqual(out, [])  # None annotation -> legitimate empty
        self.assertEqual(captured, {'max_speakers': 5})


class TestLoadWavWaveform(unittest.TestCase):
    """_load_wav_waveform decodes the ffmpeg-written 16kHz mono s16le WAV
    into the dict pyannote accepts. numpy and torch are NOT installed on
    the CI runner (test_transcribe_whisperx stubs them for the same helper
    — that precedent is the contract), so both are stubbed here too; the
    numeric int16->float32 scale is exercised in the container, not here."""

    def _write_wav(self, path, channels=1, sampwidth=2, framerate=16000,
                   samples=(0, 16384, -16384, 32767)):
        import struct
        import wave
        with wave.open(path, 'wb') as w:
            w.setnchannels(channels)
            w.setsampwidth(sampwidth)
            w.setframerate(framerate)
            frames = b''.join(struct.pack('<h', s) for s in samples)
            # `samples` is a flat sample list: for stereo, 4 samples = 2
            # frames; for mono, duplicate nothing. writeframes just needs
            # a whole number of frames either way.
            w.writeframes(frames)

    def _patch_np_torch(self, captured):
        class FakeArr:
            def __init__(self, raw):
                self.raw = raw

            def astype(self, dtype):
                return self

            def __itruediv__(self, other):
                # transcribe uses in-place division (avoids a second float32
                # copy of the waveform) — capture the scale here.
                captured['divisor'] = other
                return self

        class FakeTensor:
            def unsqueeze(self, dim):
                assert dim == 0
                return 'TENSOR'

        fake_np = types.ModuleType('numpy')
        fake_np.int16 = 'int16'
        fake_np.float32 = 'float32'

        def frombuffer(raw, dtype):
            captured['raw_len'] = len(raw)
            captured['dtype'] = dtype
            return FakeArr(raw)

        fake_np.frombuffer = frombuffer
        fake_torch = types.ModuleType('torch')
        fake_torch.from_numpy = lambda arr: FakeTensor()
        return mock.patch.dict(sys.modules, {'numpy': fake_np, 'torch': fake_torch})

    def test_decodes_rate_bytes_and_scale_divisor(self):
        import tempfile
        captured = {}
        with tempfile.TemporaryDirectory() as d:
            path = f'{d}/t.wav'
            self._write_wav(path)
            with self._patch_np_torch(captured):
                out = transcribe._load_wav_waveform(path)
        self.assertEqual(out['sample_rate'], 16000)
        self.assertEqual(out['waveform'], 'TENSOR')
        self.assertEqual(captured['raw_len'], 8)  # 4 samples * 2 bytes
        self.assertEqual(captured['dtype'], 'int16')
        self.assertEqual(captured['divisor'], 32768.0)

    def test_rejects_unexpected_format_loudly(self):
        # A future ffmpeg-flag change (stereo, non-16-bit) must raise an
        # explicit ValueError (before any numpy use; survives -O, unlike a
        # bare assert), not silently mis-scale the waveform.
        import tempfile
        captured = {}
        with tempfile.TemporaryDirectory() as d:
            path = f'{d}/stereo.wav'
            self._write_wav(path, channels=2)
            with self._patch_np_torch(captured):
                with self.assertRaises(ValueError):
                    transcribe._load_wav_waveform(path)
        self.assertNotIn('raw_len', captured)


if __name__ == '__main__':
    unittest.main()
