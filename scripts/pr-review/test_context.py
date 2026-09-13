"""Exercise prompt delivery without calling external AI services."""

import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
BUILDER = ROOT / "scripts/pr-review/build-prompts.py"


class ReviewContextTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = "# Canonical guide\nPrivate delivery mechanics stay out.\n"
        (self.root / "CLAUDE.md").write_text(self.source)
        digest = hashlib.sha256(self.source.encode()).hexdigest()[:12]
        self.context = (
            f"<!-- generated-by: co-agent · claude-md-sha: {digest} -->\n"
            "# Review context\n"
            "Any account member may add another member; owner cannot be assigned.\n"
        )
        (self.root / "AGENTS.md").write_text(self.context)
        self.lenses = self.root / "lenses"

    def build(self):
        return subprocess.run(
            ["python3", str(BUILDER), str(self.root), str(self.lenses)],
            capture_output=True, text=True, check=False,
        )

    def test_every_lens_gets_same_trusted_context_and_evidence_rules(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual({p.stem for p in self.lenses.glob("*.txt")},
                         {"L2", "L3", "L4", "L5"})
        for path in self.lenses.glob("*.txt"):
            prompt = path.read_text()
            self.assertIn(self.context.strip(), prompt)
            self.assertIn("base checkout", prompt)
            self.assertIn("Missing context", prompt)
            self.assertIn("English", prompt)
            self.assertNotIn("Private delivery mechanics", prompt)

    def test_missing_stale_and_oversized_context_fail_before_prompt_output(self):
        for body in (None, self.context.replace("claude-md-sha:", "old-sha:"),
                     self.context + ("x" * 25000)):
            with self.subTest(body_size=None if body is None else len(body)):
                agents = self.root / "AGENTS.md"
                if body is None:
                    agents.unlink()
                else:
                    agents.write_text(body)
                result = self.build()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(list(self.lenses.glob("*.txt")))

    def capture_panel(self, diff_text):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        binaries = self.root / "bin"
        binaries.mkdir()
        (binaries / "codex").write_text("#!/bin/sh\ncat >/dev/null\nprintf 'No findings\\n'\n")
        (binaries / "kiro-cli").write_text(
            "#!/usr/bin/env python3\n"
            "import json, pathlib, sys\n"
            "if sys.argv[1:] == ['--version']:\n"
            "    print('kiro-cli test'); sys.exit(0)\n"
            "assert sys.argv[sys.argv.index('--agent') + 1] == 'pr-review-notools'\n"
            "agent = json.loads(pathlib.Path('.kiro/agents/pr-review-notools.json').read_text())\n"
            "assert agent['tools'] == []\n"
            "assert '--no-interactive' in sys.argv\n"
            "if sys.argv[2].startswith('Kiro startup safety check.'):\n"
            "    print('NO_TOOLS'); sys.exit(0)\n"
            "pathlib.Path('captured.txt').write_text(sys.argv[2])\n"
            "print('No findings')\n"
        )
        for path in binaries.iterdir():
            path.chmod(0o755)
        diff = self.root / "diff.txt"
        diff.write_text(diff_text)
        work = self.root / "work"
        result = subprocess.run(
            ["bash", str(ROOT / "scripts/pr-review/run-panel.sh"),
             str(diff), str(self.lenses), str(work)],
            cwd=ROOT,
            env={**os.environ, "PATH": f"{binaries}:{os.environ['PATH']}",
                 "PANEL_RETRIES": "1", "PANEL_TIMEOUT": "5"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        captured = list((work / "kiro-cwd").glob("*/captured.txt"))
        self.assertEqual(len(captured), 8)
        self.assertEqual(len((work / "responded.txt").read_text().splitlines()), 12)
        return [path.read_text() for path in captured]

    def test_isolated_kiro_receives_context_and_diff_without_file_reads(self):
        captured = self.capture_panel(
            "diff --git a/test.go b/test.go\n+// unique-diff-evidence\n"
        )
        for prompt in captured:
            self.assertIn(self.context.strip(), prompt)
            self.assertIn("unique-diff-evidence", prompt)

    def test_maximum_context_and_diff_fit_the_kiro_argument_budget(self):
        self.context += "c" * (24000 - len(self.context.encode()))
        (self.root / "AGENTS.md").write_text(self.context)
        captured = self.capture_panel("x" * 99995 + "TAIL\n")
        for prompt in captured:
            self.assertIn(self.context.strip(), prompt)
            self.assertIn("TAIL", prompt)
            self.assertLess(len(prompt.encode()), 128 * 1024)

    def test_chair_receives_shared_context_and_both_input_sections(self):
        binaries = self.root / "bin"
        binaries.mkdir()
        capture = self.root / "chair-input.txt"
        executable = binaries / "claude"
        executable.write_text(
            "#!/usr/bin/env python3\n"
            "import json, pathlib, sys\n"
            f"pathlib.Path({str(capture)!r}).write_text(sys.argv[2] + sys.stdin.read())\n"
            "print(json.dumps({'type':'result', 'result':'No findings\\nVERDICT: PASS'}))\n"
        )
        executable.chmod(0o755)
        work = self.root / "work"
        (work / "slot").mkdir(parents=True)
        (work / "slot/codex-L2.md").write_text("unique-panel-evidence\n")
        (work / "responded.txt").write_text("codex/L2\n")
        diff = self.root / "diff.txt"
        diff.write_text("unique-diff-evidence\n")
        output = self.root / "review.md"
        result = subprocess.run(
            ["bash", str(ROOT / "scripts/pr-review/synthesize.sh"),
             str(diff), str(work), "1", "Context test", str(output)],
            cwd=ROOT,
            env={**os.environ, "PATH": f"{binaries}:{os.environ['PATH']}",
                 "CHAIR_TIMEOUT": "5"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        prompt = capture.read_text()
        self.assertIn((ROOT / "AGENTS.md").read_text().strip(), prompt)
        self.assertIn("unique-diff-evidence", prompt)
        self.assertIn("unique-panel-evidence", prompt)
        self.assertIn("concise English", prompt)
        self.assertTrue(output.read_text().rstrip().endswith("VERDICT: PASS"))


if __name__ == "__main__":
    unittest.main()
