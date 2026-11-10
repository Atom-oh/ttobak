"""Review input size pre-check regressions."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

MODULE = Path(__file__).with_name("check_review_size.py")
spec = importlib.util.spec_from_file_location("check_review_size", MODULE)
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)

SCOPE = {"schema_version": 1, "basenames": ["package-lock.json"], "extensions": [".png"],
         "directories": ["dist"], "prefixes": [], "path_regexes": []}


class SizeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.previous = Path.cwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, self.previous)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "Review test")
        self.git("config", "user.email", "review@example.invalid")
        (self.root / "scripts/pr-review").mkdir(parents=True)
        (self.root / "scripts/pr-review/role-input-scope.json").write_text(json.dumps(SCOPE))
        self.commit("base")

    def git(self, *arguments):
        return subprocess.check_output(["git", *arguments], text=True)

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def run_check(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            code = check.main(["--base", "main", "--head", "feature", "--json"])
        return code, json.loads(out.getvalue()) if out.getvalue() else None

    def branch(self):
        self.git("checkout", "-qb", "feature")

    def test_within_limit(self):
        self.branch()
        self.write("a.txt", "hello\n")
        self.commit("change")
        code, report = self.run_check()
        self.assertEqual(code, 0)
        self.assertEqual(report["over"], [])
        self.assertEqual(report["kept_files"], 1)

    def test_over_byte_limit(self):
        self.branch()
        self.write("a.txt", "x" * 500 + "\n")
        self.commit("change")
        with patch.object(check.role_review, "MAX_DIFF_BYTES", 100):
            code, report = self.run_check()
        self.assertEqual(code, 1)
        self.assertEqual(report["over"], ["bytes"])
        self.assertEqual(report["largest"][0]["path"], "a.txt")

    def test_excluded_bundle_not_counted(self):
        self.branch()
        self.write("a.txt", "small\n")
        self.write("package-lock.json", "y" * 5000 + "\n")
        self.write("dist/app.js", "z" * 5000 + "\n")
        self.commit("change")
        with patch.object(check.role_review, "MAX_DIFF_BYTES", 1000):
            code, report = self.run_check()
        self.assertEqual(code, 0)
        self.assertEqual(report["excluded_files"], 2)
        self.assertLess(report["bytes"], 1000)

    def test_merge_base_used(self):
        self.branch()
        self.write("a.txt", "mine\n")
        self.commit("change")
        self.git("checkout", "-q", "main")
        self.write("big.txt", "w" * 5000 + "\n")
        self.commit("base advanced")
        with patch.object(check.role_review, "MAX_DIFF_BYTES", 1000):
            code, report = self.run_check()
        self.assertEqual(code, 0)
        self.assertEqual(report["kept_files"], 1)

    def test_warning_at_ninety_percent(self):
        self.branch()
        self.write("a.txt", "x" * 200 + "\n")
        self.commit("change")
        size = self.run_check()[1]["bytes"]
        with patch.object(check.role_review, "MAX_DIFF_BYTES", int(size / 0.95)):
            code, report = self.run_check()
        self.assertEqual(code, 0)
        self.assertEqual(report["warn"], ["bytes"])

    def test_bad_ref_is_usage_error(self):
        with contextlib.redirect_stderr(io.StringIO()):
            code = check.main(["--base", "nope", "--head", "HEAD"])
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
