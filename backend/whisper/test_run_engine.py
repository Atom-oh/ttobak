"""Unit tests for run_engine's dispatch allowlist (dependency-free module,
no stubbing needed — resolve_engine is pure)."""

import unittest

import run_engine


class TestResolveEngine(unittest.TestCase):
    def test_default_is_whisperx(self):
        self.assertEqual(
            run_engine.resolve_engine(['run_engine.py'], {}),
            'transcribe_whisperx.py')
        self.assertEqual(
            run_engine.resolve_engine(['run_engine.py'], {'ENGINE': ''}),
            'transcribe_whisperx.py')

    def test_whitespace_only_engine_rejected_not_defaulted(self):
        # A SET-but-blank ENGINE means the operator intended a selection —
        # loud failure, not a silent whisperx fallback (runbook §3c's
        # "fails fast and loud" contract).
        for val in (' ', '\t', '  \n'):
            with self.assertRaises(ValueError, msg=repr(val)):
                run_engine.resolve_engine(['run_engine.py'], {'ENGINE': val})

    def test_fw_p4_selectable_case_insensitively(self):
        for val in ('fw_p4', 'FW_P4', ' fw_p4 '):
            self.assertEqual(
                run_engine.resolve_engine(['run_engine.py'], {'ENGINE': val}),
                'transcribe_fw_p4.py', msg=val)

    def test_unknown_engine_rejected(self):
        for bad in ('legacy', 'transcribe_whisperx.py', '-c', 'qwen3'):
            with self.assertRaises(ValueError, msg=bad):
                run_engine.resolve_engine(['run_engine.py'], {'ENGINE': bad})

    def test_any_argv_rejected(self):
        # PR #175 round-2 L3 MAJOR: a containerOverrides command must be a
        # hard no-op channel — even an argument naming an allowlisted
        # script is refused, so env stays the single selection surface.
        for argv in (['run_engine.py', 'transcribe_fw_p4.py'],
                     ['run_engine.py', '-c', 'print(1)'],
                     ['run_engine.py', 'transcribe_whisperx.py']):
            with self.assertRaises(ValueError, msg=argv):
                run_engine.resolve_engine(argv, {'ENGINE': 'fw_p4'})


if __name__ == '__main__':
    unittest.main()
