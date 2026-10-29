"""Import contract: the worker is self-contained, ships a minimal image context and a public-only seed."""
import json, os, subprocess, sys, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))  # services/billing/worker
SCRIPTS = os.path.join(ROOT, "scripts")

# Built from parts so this file does not match its own scan.
COMPANION = ("dashboard" + "-app/", "analy" + "sis/", "run_" + "pipeline.sh")
SKIP_DIRS = {"__pycache__", ".git", "node_modules", "datasets", "out", ".agent_cache"}


from fileutil import jload, rd  # noqa: E402

def worker_files():
    for d, ds, fs in os.walk(ROOT):
        ds[:] = [x for x in ds if x not in SKIP_DIRS]
        for f in fs:
            if not f.endswith(".pyc"):
                yield os.path.join(d, f)


class Contract(unittest.TestCase):
    def test_no_companion_paths(self):
        hits = []
        for p in worker_files():
            with open(p, encoding="utf-8", errors="replace") as f:
                for i, line in enumerate(f, 1):
                    if any(c in line for c in COMPANION):
                        hits.append((os.path.relpath(p, ROOT), i))
        self.assertEqual(hits, [])

    def test_dockerignore_allowlist(self):
        lines = [l.strip() for l in rd(os.path.join(ROOT, ".dockerignore"), encoding="utf-8").splitlines()]
        rules = [l for l in lines if l and not l.startswith("#")]
        self.assertEqual(rules[0], "*")
        for excluded in ("tests/", "datasets/", "out/", ".agent_cache/"):
            self.assertNotIn("!" + excluded, rules)
            self.assertFalse(any(r.startswith("!") and r[1:].rstrip("/") + "/" == excluded for r in rules), excluded)

    def test_whats_new_seed_is_public_aws(self):
        seed = jload(os.path.join(SCRIPTS, "aws_whats_new.json"), encoding="utf-8")
        entries = seed.values() if isinstance(seed, dict) else seed
        self.assertTrue(entries)
        for e in entries:
            self.assertEqual(set(e), {"title", "date", "url", "summary"})
            self.assertTrue(e["url"].startswith(("https://aws.amazon.com/", "https://repost.aws/")), e["url"])

    def test_sessions_loads_seed(self):
        r = subprocess.run([sys.executable, "-I", "-c", "import sys; sys.path.insert(0, sys.argv[1]); import sessions; print(len(sessions.WN))", SCRIPTS],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertGreater(int(r.stdout), 0)


if __name__ == "__main__":
    unittest.main()
