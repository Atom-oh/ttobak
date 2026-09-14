"""Shared startup decisions cannot outlive their plan or no-tools configuration."""
import json
import os
from pathlib import Path
import sys
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
        self.assertEqual(execute.call_count, 0)
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

    def test_claude_schema_counts_toward_the_complete_request_limit(self):
        _, prompt, payload = role_review.issue_request(self.work, "claude-self")
        old_request_size = len(prompt.encode()) + len(payload.encode())
        with patch.object(run_role, "MAX_REQUEST_BYTES", old_request_size + 1):
            with patch.object(run_role, "execute", return_value=(0, "", "")) as execute:
                run_role.run(self.work, "claude-self")
        self.assertEqual(execute.call_count, 0)
        result = json.loads((self.work / "slot/claude-self-result.json").read_text())
        self.assertFalse(result["valid"])

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

    def parent_run(self, fail_sol=False):
        plan = role_review.load_plan(self.work)
        models = {role["model"]: tag for tag, role in plan["roles"].items()}
        calls = []

        def execute(command, cwd, environment, input_text, timeout):
            model = command[command.index("--model") + 1]
            tag = models[model]
            probe = command[1] == "chat" and command[2].startswith("Kiro startup safety check.")
            calls.append((tag, probe, cwd))
            if tag.startswith("kiro-"):
                self.assertEqual(environment["HOME"], str(cwd))
                agent = json.loads((cwd / ".kiro/agents/inline-review.json").read_text())
                self.assertEqual(agent, run_role.AGENT)
                self.assertEqual(agent["tools"], [])
                self.assertEqual((cwd / "preflight-canary.txt").exists(), probe)
            if probe:
                self.assertEqual(input_text, "")
                self.assertNotIn("BEGIN DIFF", command[2])
                self.assertNotIn("backend/worker.py", command[2])
                text = (cwd / "preflight-canary.txt").read_text() if fail_sol and tag == "kiro-sol" else "NO_TOOLS"
                return 0, text, ""
            role = plan["roles"][tag]
            body = json.dumps({
                "head_sha": plan["head_sha"], "role": role["role"], "scope_complete": True,
                "reviewed_paths": role["paths"],
                "checks": [{"path": role["paths"][0], "evidence": "Checked synthetic change."}],
                "findings": [], "uncertainties": [],
            })
            if tag == "codex":
                Path(command[command.index("--output-last-message") + 1]).write_text(body)
                body = "\n".join(json.dumps(event) for event in (
                    {"type": "turn.started"},
                    {"type": "item.completed", "item": {"type": "agent_message", "text": body}},
                    {"type": "turn.completed"},
                ))
            elif tag == "claude-self":
                body = json.dumps({"type": "result", "subtype": "success", "is_error": False,
                                   "structured_output": json.loads(body)})
            return 0, body, ""

        with patch.object(run_role, "execute", side_effect=execute):
            with patch.object(sys, "argv", ["run_role.py", "--work", str(self.work), "--all"]):
                run_role.main()
        status = role_review.main(["aggregate", "--work", str(self.work)])
        return calls, status

    def test_parent_entrypoint_waits_for_both_probes_and_uses_fresh_request_directories(self):
        calls, status = self.parent_run()
        kiro = [row for row in calls if row[0].startswith("kiro-")]
        self.assertEqual(status, 0)
        self.assertEqual([probe for _, probe, _ in kiro], [True, True, False, False])
        self.assertEqual(len({cwd for _, _, cwd in kiro}), 4)
        self.assertEqual(sorted(tag for tag, probe, _ in kiro if probe), ["kiro-fable", "kiro-sol"])

    def test_parent_entrypoint_failed_second_probe_never_releases_either_kiro_role(self):
        with patch.dict(os.environ, {"KIRO_PREFLIGHT_PASSED": "1"}):
            calls, status = self.parent_run(fail_sol=True)
        self.assertEqual(status, 2)
        kiro = [row for row in calls if row[0].startswith("kiro-")]
        self.assertEqual([(tag, probe) for tag, probe, _ in kiro],
                         [("kiro-fable", True), ("kiro-sol", True)])
        self.assertEqual({tag for tag, probe, _ in calls if not probe}, {"codex", "claude-self"})

    def test_parent_entrypoint_probes_only_the_active_kiro_model(self):
        self.prepare(path="frontend/retry.css")
        calls, status = self.parent_run()
        self.assertEqual(status, 0)
        self.assertEqual([(tag, probe) for tag, probe, _ in calls if tag.startswith("kiro-")],
                         [("kiro-sol", True), ("kiro-sol", False)])
