"""Shared startup decisions cannot outlive their plan or no-tools configuration."""
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import role_review
import run_role


class KiroStartupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work = self.root / "work"
        (self.root / "context").write_text("Trusted base context.\n")
        self.prepare()

    def prepare(self, head="a" * 40, path="backend/worker.py"):
        (self.root / "diff").write_text(
            f"diff --git a/{path} b/{path}\n--- a/{path}\n+++ b/{path}\n"
            "@@ -1 +1 @@\n-old\n+new\n"
        )
        self.assertEqual(role_review.main([
            "prepare", "--head", head, "--base", "b" * 40, "--work", str(self.work),
            "--context", str(self.root / "context"), "--diff", str(self.root / "diff"),
        ]), 0)

    def test_standalone_kiro_run_checks_all_required_models_before_diff(self):
        with patch.object(run_role, "preflight", side_effect=[(True, 0, ""), (False, 0, "")]) as probe:
            with patch.object(run_role, "execute") as execute:
                run_role.run(self.work, "kiro-fable")
        self.assertEqual(probe.call_count, 2)
        execute.assert_not_called()
        self.assertTrue((self.work / "slot/kiro-preflight-kiro-fable.flag").exists())
        self.assertEqual(role_review.main(["aggregate", "--work", str(self.work)]), 2)

    def test_startup_is_bound_to_plan_and_agent_configuration(self):
        with patch.object(run_role, "preflight", return_value=(True, 0, "")):
            startup = run_role.prepare_kiro_startup(self.work)
        with patch.object(run_role, "execute") as execute:
            with patch.dict(run_role.AGENT, {"tools": ["read"]}):
                with self.assertRaises(role_review.Invalid):
                    run_role.run(self.work, "kiro-fable", startup)
            self.prepare(head="c" * 40)
            with self.assertRaises(role_review.Invalid):
                run_role.run(self.work, "kiro-fable", startup)
        execute.assert_not_called()

    def test_inactive_kiro_roles_do_not_probe(self):
        self.prepare(path="frontend/styles.css")
        with patch.object(run_role, "preflight") as probe:
            with patch.dict(os.environ, {"KIRO_PREFLIGHT_PASSED": "1"}):
                run_role.prepare_kiro_startup(self.work)
        probe.assert_not_called()

    def test_plan_change_after_issuance_blocks_delivery(self):
        with patch.object(run_role, "preflight", return_value=(True, 0, "")):
            startup = run_role.prepare_kiro_startup(self.work)
        original = run_role.issue_request
        calls = 0
        def issue(work, tag):
            nonlocal calls
            calls += 1
            result = original(work, tag)
            if calls == 2:
                self.prepare(head="c" * 40)
            return result
        with patch.object(run_role, "issue_request", side_effect=issue):
            with patch.object(run_role, "execute") as execute:
                with self.assertRaises(role_review.Invalid):
                    run_role.run(self.work, "kiro-fable", startup)
        execute.assert_not_called()
