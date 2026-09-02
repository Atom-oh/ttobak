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
    """All values of a Dockerfile instruction, in order (continuation lines
    joined; comments skipped)."""
    with open(DOCKERFILE) as f:
        raw = f.read()
    # Join backslash continuations so multi-line instructions parse whole.
    joined = re.sub(r'\\\s*\n', ' ', raw)
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


if __name__ == '__main__':
    unittest.main()
