"""worker/ai_step.py: the agents/report step the worker runs as a child. Fakes only."""
import datetime, io, json, os, re, shutil, signal, subprocess, sys, tempfile, threading, time, unittest, uuid
from unittest import mock

from worker_fakes import *  # noqa: F401,F403 (fakes, constants, Base; also puts worker/ on sys.path)
from worker_fakes import W, _split_top  # noqa: E402,F401


class TestAiStep(unittest.TestCase):
    def test_agents_args_per_source(self):
        import ai_step
        sys.path.insert(0, os.path.join(HERE, "..", "scripts"))
        import agents
        seen = []
        orig = agents.run
        agents.run = lambda d, **kw: (seen.append((d, kw)), {"ai": "none"})[1]
        tmp = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        buf, out = io.StringIO(), sys.stdout
        try:
            sys.stdout = buf
            self.assertEqual(ai_step.main(["agents", tmp, "cot", "/f.json", os.path.join(tmp, "c")]), 0)
            self.assertTrue(os.path.isfile(os.path.join(tmp, "tag_signals.json")))  # an older version gets its tag signals
            self.assertEqual(ai_step.main(["agents", tmp, "cur", "-", os.path.join(tmp, "c")]), 0)
            self.assertEqual(ai_step.main(["agents", tmp, "xls", "-", os.path.join(tmp, "c")]), 2)
        finally:
            sys.stdout = out
            agents.run = orig
            shutil.rmtree(tmp, ignore_errors=True)
        self.assertEqual(seen[0][1], {"cache": os.path.join(tmp, "c"), "feed_path": "/f.json"})
        self.assertEqual(seen[1][1], {"cache": os.path.join(tmp, "c"), "feed_path": None, "areas": ["business", "cur"], "synth": False,
                                      "extras": False})
        self.assertEqual(buf.getvalue().splitlines()[-1], '{"ai": "none"}')

    def test_report_runs_isolated(self):
        """ai_step.py under python3 -I still finds its scripts (report needs no boto3 credentials)."""
        tmp = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        try:
            md = os.path.join(tmp, "report.md")
            r = subprocess.run(["python3", "-I", os.path.join(HERE, "..", "worker", "ai_step.py"), "report", tmp, md,
                                "https://dash.example.test"], capture_output=True, text=True, timeout=120,
                               env={"PATH": os.environ.get("PATH", ""), "HOME": tmp, "PYTHONNOUSERSITE": "1",
                                    # a shared-library python (CI setup-python) needs its loader path
                                    **{k: os.environ[k] for k in ("LD_LIBRARY_PATH",) if k in os.environ}})
            self.assertEqual(r.returncode, 0, r.stderr[-500:])
            self.assertTrue(os.path.isfile(md))
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
