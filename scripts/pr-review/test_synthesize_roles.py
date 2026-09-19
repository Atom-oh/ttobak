"""The deterministic path must never start a model."""

import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


MODULE = Path(__file__).with_name("synthesize_roles.py")


class SynthesisTests(unittest.TestCase):
    def test_scrubbing_cannot_accept_conflicting_original_verdicts(self):
        (self.root / "chair-mode.txt").write_text("review\n")
        (self.root / "role-summary.json").write_text('{"findings":[]}')
        (self.root / "project-context.md").write_text("Trusted base.")
        (self.root / "roles").mkdir()
        (self.root / "roles/codex.diff").write_text("Complete supplied diff.")
        for failure in ("VERDICT: FAIL", "\x1b[31mVERDICT: FAIL\x1b[0m", "VERD\u200bICT: FAIL"):
            with self.subTest(failure=failure):
                reply = (0, f"Finding:\npassword = prior ||\n{failure}\nVERDICT: PASS\n", "")
                with patch.dict(os.environ, {"GITHUB_ENV": str(self.root / "test-env")}), \
                        patch.object(self.module, "execute", side_effect=[reply, reply]) as invoke:
                    self.module.synthesize(self.root, self.root / "review.md")
                self.assertEqual(invoke.call_count, 2)
                self.assertTrue((self.root / "review.md").read_text().rstrip().endswith("VERDICT: FAIL"))

    def setUp(self):
        self.assertTrue(MODULE.exists(), "Conditional synthesis is not implemented")
        spec = importlib.util.spec_from_file_location("synthesize_roles", MODULE)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def test_complete_clean_review_does_not_call_chair(self):
        (self.root / "chair-mode.txt").write_text("deterministic\n")
        (self.root / "deterministic-review.md").write_text("Scope complete.\nVERDICT: PASS\n")
        with patch.object(self.module, "execute", side_effect=AssertionError("Unexpected call")):
            self.module.synthesize(self.root, self.root / "review.md")
        self.assertTrue((self.root / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_chair_receives_plain_prose_guidance_without_relaxing_format_gate(self):
        (self.root / "chair-mode.txt").write_text("review\n")
        (self.root / "role-summary.json").write_text('{"findings":[]}')
        (self.root / "project-context.md").write_text("Trusted base.")
        (self.root / "roles").mkdir()
        (self.root / "roles/codex.diff").write_text("Complete supplied diff.")
        invalid = (0, 'Adds `id="user-management"`.\nVERDICT: PASS\n', "")
        with patch.dict(os.environ, {"GITHUB_ENV": str(self.root / "test-env")}), \
                patch.object(self.module, "execute", side_effect=[invalid, invalid]) as invoke:
            self.module.synthesize(self.root, self.root / "review.md")
        for call in invoke.call_args_list:
            prompt = call.args[0][2]
            self.assertIn("Prefer plain English prose", prompt)
            self.assertIn("never wrap those fragments in inline backticks", prompt)
        self.assertTrue((self.root / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_incomplete_review_cannot_be_waived_by_chair(self):
        (self.root / "chair-mode.txt").write_text("blocked\n")
        (self.root / "deterministic-review.md").write_text("Missing required role.\nVERDICT: FAIL\n")
        with patch.object(self.module, "execute", side_effect=AssertionError("Unexpected call")):
            self.module.synthesize(self.root, self.root / "review.md")
        self.assertTrue((self.root / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_unique_final_verdict_and_body_are_required(self):
        self.assertTrue(self.module.valid("Evidence reviewed.\nVERDICT: PASS\n", 0))
        for output, status in [
            ("VERDICT: PASS", 0),
            ("Evidence reviewed.\nVERDICT: PASS", 2),
            ("Evidence reviewed.\nVERDICT: PASS\nVERDICT: PASS", 0),
            ("Evidence reviewed.\nVERDICT: PASS\nmore text", 0),
            ("Evidence reviewed.\n VERDICT: PASS", 0),
            ("Evidence reviewed.\nVERDICT: PASS ", 0),
            ("Evidence reviewed without a verdict.", 0),
        ]:
            self.assertFalse(self.module.valid(output, status))


if __name__ == "__main__":
    unittest.main()
