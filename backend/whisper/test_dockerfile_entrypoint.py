"""Guards the image half of the dispatcher-pin security invariant.

The CDK half (task definition sets neither EntryPoint nor Command) is
asserted by infra/test/whisper-stack.test.ts; this suite asserts the
Dockerfile half — the pin only holds if the image's effective ENTRYPOINT
is the run_engine.py dispatcher and no CMD reopens a containerOverrides
command channel. Reverting Dockerfile.whisperx to a bare
ENTRYPOINT ["python3"] (+ CMD) would otherwise fail no test at all while
promoting the ECS command override into arbitrary-code execution on a
host-networkMode task. Runs in CI via test-backend.yml's whisper step
(its path trigger covers backend/whisper/**)."""

import json
import os
import re
import unittest

DOCKERFILE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                          'Dockerfile.whisperx')


def _instructions(name: str) -> list:
    """All values of a Dockerfile instruction, in order.

    Mirrors Docker's own parse order: comment lines are removed BEFORE
    backslash continuations are joined — so a `# comment \\` line followed
    by `CMD [...]` is a real CMD to Docker, and must be one to this guard
    too (comment-stripping after joining would hide it)."""
    with open(DOCKERFILE) as f:
        lines = [ln for ln in f.read().splitlines()
                 if not ln.lstrip().startswith('#')]
    joined = re.sub(r'\\\s*\n', ' ', '\n'.join(lines))
    values = []
    for line in joined.splitlines():
        stripped = line.strip()
        if stripped.upper().startswith(name.upper() + ' '):
            values.append(stripped[len(name):].strip())
    return values


class TestDispatcherPin(unittest.TestCase):
    def test_effective_entrypoint_is_the_dispatcher(self):
        entrypoints = _instructions('ENTRYPOINT')
        self.assertTrue(entrypoints, 'Dockerfile.whisperx must set ENTRYPOINT')
        # Docker uses the LAST ENTRYPOINT; exec (JSON) form required so no
        # shell string-splitting ambiguity exists.
        self.assertEqual(json.loads(entrypoints[-1]),
                         ['python3', 'run_engine.py'])

    def test_no_cmd_instruction(self):
        # A CMD would flow into run_engine's argv via the default command —
        # run_engine rejects argv loudly, but the image contract is NO
        # default command at all, so nothing normalizes operators toward
        # command-based selection.
        self.assertEqual(_instructions('CMD'), [])

    def test_dispatcher_and_engines_are_copied_into_the_image(self):
        copies = ' '.join(_instructions('COPY'))
        for script in ('run_engine.py', 'transcribe_whisperx.py',
                       'transcribe_fw_p4.py'):
            self.assertIn(script, copies)


LEGACY_DOCKERFILE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                 'Dockerfile')
VERIFY_PINS = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                           'verify_pins.py')


class TestLegacyPinLockstep(unittest.TestCase):
    """ADR-035's "pins must not move" invariant is enforced at build time by
    verify_pins.py — but only for packages that file lists. This guard keeps
    the Dockerfile's exact pins and verify_pins.py's PINS in lockstep (the
    same dual-guard convention as the dispatcher ENTRYPOINT tests above),
    so adding/changing a pin in one file without the other fails a PR."""

    def _dockerfile_pins(self) -> dict:
        with open(LEGACY_DOCKERFILE) as f:
            text = f.read()
        return dict(re.findall(r'([A-Za-z0-9_.\-]+)==([A-Za-z0-9+.]+)', text))

    def _verify_pins(self) -> dict:
        import ast
        with open(VERIFY_PINS) as f:
            tree = ast.parse(f.read())
        for node in ast.walk(tree):
            if isinstance(node, ast.Assign) and any(
                    getattr(t, 'id', None) == 'PINS' for t in node.targets):
                return dict(ast.literal_eval(node.value))
        raise AssertionError('PINS assignment not found in verify_pins.py')

    def test_every_dockerfile_pin_is_verified(self):
        docker = self._dockerfile_pins()
        verified = self._verify_pins()
        # torch is verified via torch.__version__ (exact, incl. +cu128), not
        # PINS; torchaudio appears in PINS with the cu128 local label the
        # index actually installs, while the Dockerfile pins the bare
        # version — compare on the public version part.
        self.assertEqual(docker.pop('torch'), '2.8.0')
        self.assertEqual(verified.pop('torchaudio').split('+')[0],
                         docker.pop('torchaudio'))
        docker_rest = {k: v for k, v in docker.items()}
        self.assertEqual(docker_rest, verified)


if __name__ == '__main__':
    unittest.main()
