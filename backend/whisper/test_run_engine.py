"""Unit tests for run_engine's dispatch allowlist (dependency-free module,
no stubbing needed — resolve_engine is pure; main() is tested with execv
mocked out)."""

import contextlib
import io
import unittest
from unittest import mock

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
        # A containerOverrides command must be a hard no-op channel — even
        # an argument naming an allowlisted script is refused, so env stays
        # the single selection surface (see run_engine's module docstring).
        for argv in (['run_engine.py', 'transcribe_fw_p4.py'],
                     ['run_engine.py', '-c', 'print(1)'],
                     ['run_engine.py', 'transcribe_whisperx.py']):
            with self.assertRaises(ValueError, msg=argv):
                run_engine.resolve_engine(argv, {'ENGINE': 'fw_p4'})


class TestMain(unittest.TestCase):
    def test_dispatch_log_only_after_existence_check_then_execv(self):
        buf = io.StringIO()
        # Snapshot the environment AT exec time via the mock's side effect:
        # asserting on os.environ after the `with` block would be vacuous —
        # mock.patch.dict restores the original dict on exit, so that would
        # test the runner's environment, not main()'s scrub.
        env_at_exec = {}

        def capture_env(*_args):
            env_at_exec.update(run_engine.os.environ)

        with mock.patch.object(run_engine.os, 'execv',
                               side_effect=capture_env) as execv, \
                mock.patch.object(run_engine.sys, 'argv', ['run_engine.py']), \
                mock.patch.dict(run_engine.os.environ,
                                {'ENGINE': 'fw_p4', 'PYTHONPATH': '/evil',
                                 'LD_PRELOAD': '/evil.so'}), \
                contextlib.redirect_stdout(buf):
            run_engine.main()
        execv.assert_called_once()
        argv = execv.call_args[0][1]
        self.assertTrue(argv[1].endswith('transcribe_fw_p4.py'))
        self.assertIn('run_engine: dispatching to transcribe_fw_p4.py',
                      buf.getvalue())
        # Interpreter-steering env was scrubbed before the engine exec'd.
        self.assertNotIn('PYTHONPATH', env_at_exec)
        self.assertNotIn('LD_PRELOAD', env_at_exec)
        self.assertEqual(env_at_exec.get('ENGINE'), 'fw_p4')

    def test_missing_script_fatals_without_dispatch_log(self):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(run_engine.os, 'execv') as execv, \
                mock.patch.object(run_engine.os.path, 'isfile', return_value=False), \
                mock.patch.object(run_engine.sys, 'argv', ['run_engine.py']), \
                mock.patch.dict(run_engine.os.environ, {'ENGINE': 'fw_p4'}), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            with self.assertRaises(SystemExit) as ctx:
                run_engine.main()
        self.assertEqual(ctx.exception.code, 1)
        execv.assert_not_called()
        self.assertIn('FATAL: engine script', err.getvalue())
        # The verification grep line must NOT appear when nothing dispatched.
        self.assertNotIn('dispatching', out.getvalue())

    def test_bad_engine_fatals_with_exit_1(self):
        err = io.StringIO()
        with mock.patch.object(run_engine.os, 'execv') as execv, \
                mock.patch.object(run_engine.sys, 'argv', ['run_engine.py']), \
                mock.patch.dict(run_engine.os.environ, {'ENGINE': 'qwen3'}), \
                contextlib.redirect_stderr(err):
            with self.assertRaises(SystemExit) as ctx:
                run_engine.main()
        self.assertEqual(ctx.exception.code, 1)
        execv.assert_not_called()
        self.assertIn('FATAL: ENGINE must be one of', err.getvalue())


if __name__ == '__main__':
    unittest.main()
