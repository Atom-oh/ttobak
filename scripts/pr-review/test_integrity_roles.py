"""Regressions from independent review of failure and byte-provenance handling."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run_role
import synthesize_roles


class IntegrityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work = self.root / "work"
        self.raw = (
            "diff --git a/backend/worker.ts b/backend/worker.ts\n"
            "--- a/backend/worker.ts\n+++ b/backend/worker.ts\n"
            "@@ -1 +1 @@\n-return 1;\r\n+return 1;\n"
        )
        (self.root / "diff").write_bytes(self.raw.encode())
        (self.root / "context").write_text("Trusted context.\n")
        subprocess.run([
            sys.executable, str(Path(run_role.__file__).with_name("role_review.py")),
            "prepare", "--head", "a" * 40, "--base", "b" * 40,
            "--diff", str(self.root / "diff"), "--context", str(self.root / "context"),
            "--work", str(self.work),
        ], check=True, capture_output=True)
        self.plan = json.loads((self.work / "role-plan.json").read_text())
        self.environment = patch.dict(os.environ, {
            "PANEL_RETRIES": "2", "PANEL_TIMEOUT": "10", "KIRO_PREFLIGHT_TIMEOUT": "10",
            "CHAIR_TIMEOUT": "10",
            "CHAIR_PRIMARY_MODEL": "global.anthropic.claude-fable-5-1",
            "CHAIR_FALLBACK_MODEL": "global.anthropic.claude-opus-5",
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def response(self, tag):
        return json.dumps({
            "head_sha": self.plan["head_sha"], "role": self.plan["roles"][tag]["role"],
            "scope_complete": True, "reviewed_paths": ["backend/worker.ts"],
            "checks": [{"path": "backend/worker.ts", "evidence": "Checked changed return bytes."}],
            "findings": [], "uncertainties": [],
        })

    def chair(self, responses, scrubber=None):
        (self.work / "chair-mode.txt").write_text("review\n")
        (self.work / "role-summary.json").write_text('{"findings":[]}\n')
        (self.work / "project-context.md").write_text("Trusted context.\n")
        with patch.object(synthesize_roles, "execute", side_effect=responses) as execute:
            with patch.object(synthesize_roles, "scrub", side_effect=scrubber or (lambda value: value)):
                synthesize_roles.synthesize(self.work, self.work / "review.md")
        return execute, (self.work / "review.md").read_text()

    def test_terminal_failure_cannot_disappear_in_a_later_retry(self):
        for tag in ("codex", "claude-self"):
            with self.subTest(tag=tag):
                with patch.object(run_role, "execute", side_effect=[
                    (1, "", "Monthly request limit reached"),
                    (0, self.response(tag), ""),
                ]) as execute:
                    run_role.run(self.work, tag)
                self.assertEqual(execute.call_count, 1)
                result = json.loads((self.work / "slot" / f"{tag}-result.json").read_text())
                self.assertFalse(result["valid"])
                self.assertIn("quota_diagnostic", result["failure_codes"])

    def assert_codex_overflow_stops_retry(self, final_file=False):
        calls = 0
        def reply(command, *unused):
            nonlocal calls
            calls += 1
            if calls == 1 and not final_file:
                return 1, "", "output_byte_limit"
            response = self.response("codex")
            destination = Path(command[command.index("--output-last-message") + 1])
            destination.write_text("*" * (1024 * 1024 + 1) if calls == 1 else response)
            events = "\n".join(json.dumps(event) for event in (
                {"type": "turn.started"},
                {"type": "item.completed", "item": {"type": "agent_message", "text": response}},
                {"type": "turn.completed"},
            ))
            return 0, events, "Nonterminal CLI notice"
        with patch.object(run_role, "execute", side_effect=reply):
            run_role.run(self.work, "codex")
        self.assertEqual(calls, 1)
        result = json.loads((self.work / "slot/codex-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("output_byte_limit", result["failure_codes"])
        aggregate = subprocess.run(
            [sys.executable, str(Path(run_role.__file__).with_name("role_review.py")),
             "aggregate", "--work", str(self.work)], capture_output=True, text=True,
        )
        self.assertEqual(aggregate.returncode, 2)
        summary = json.loads((self.work / "role-summary.json").read_text())
        self.assertIn("output_byte_limit:codex", summary["failure_codes"])

    def test_codex_executor_overflow_cannot_retry_to_clean_success(self):
        self.assert_codex_overflow_stops_retry()

    def test_codex_final_file_overflow_survives_other_diagnostics(self):
        self.assert_codex_overflow_stops_retry(final_file=True)

    def test_chair_rejects_model_selection_failure_even_with_pass_footer(self):
        reply = (0, "Reviewed candidates.\nVERDICT: PASS\n",
                 "[warn] failed to set model: Method not found")
        execute, text = self.chair([reply, reply])
        self.assertEqual(execute.call_count, 2)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))

    def test_chair_quota_failure_does_not_consume_fallback(self):
        reply = (0, "Reviewed candidates.\nVERDICT: PASS\n", "Monthly request limit reached")
        execute, text = self.chair([reply, reply])
        self.assertEqual(execute.call_count, 1)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))

    def test_oversized_chair_output_cannot_pass_or_consume_fallback(self):
        reply = (0, "*" * (1024 * 1024 + 1) + "\nVERDICT: PASS\n", "")
        execute, text = self.chair([reply])
        self.assertEqual(execute.call_count, 1)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))
        self.assertNotIn("***", text)

    def test_raw_scrubber_newline_overflow_writes_failed_chair_status(self):
        suffix = "\nVERDICT: PASS"
        reply = (0, "*" * (1024 * 1024 - len(suffix)) + suffix, "")
        environment_file = self.root / "github-env"
        with patch.dict(os.environ, {"GITHUB_ENV": str(environment_file)}):
            execute, text = self.chair([reply], scrubber=run_role.scrub)
        self.assertEqual(execute.call_count, 1)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))
        self.assertIn("chair_failed=1", environment_file.read_text())

    def test_final_redaction_expansion_cannot_publish_an_oversized_chair_answer(self):
        prefix, suffix = "token=x\n", "\nVERDICT: PASS"
        text = prefix + "*" * (1024 * 1024 - 2 - len(prefix + suffix)) + suffix
        environment_file = self.root / "github-env"
        with patch.dict(os.environ, {"GITHUB_ENV": str(environment_file)}):
            execute, output = self.chair([(0, text, "")], scrubber=run_role.scrub)
        self.assertEqual(execute.call_count, 1)
        self.assertTrue(output.endswith("VERDICT: FAIL\n"))
        self.assertLessEqual(len(output.encode()), 1024 * 1024)
        self.assertIn("chair_failed=1", environment_file.read_text())

    def test_explicit_clean_fallback_can_resolve_selection_failure(self):
        execute, text = self.chair([
            (0, "Reviewed candidates.\nVERDICT: PASS\n", "[warn] failed to set model"),
            (0, "Reviewed candidates with the configured fallback.\nVERDICT: PASS\n", ""),
        ])
        self.assertEqual(execute.call_count, 2)
        self.assertTrue(text.endswith("VERDICT: PASS\n"))

    def test_stored_and_delivered_specialist_diff_bytes_match(self):
        for tag in ("codex", "kiro-fable"):
            with self.subTest(tag=tag):
                def reply(command, *unused):
                    response = self.response(tag)
                    if tag == "codex":
                        Path(command[command.index("--output-last-message") + 1]).write_text(response)
                        response = "\n".join(json.dumps(event) for event in (
                            {"type": "turn.started"},
                            {"type": "item.completed", "item": {"type": "agent_message", "text": response}},
                            {"type": "turn.completed"},
                        ))
                    return 0, response, ""
                with patch.object(run_role, "preflight", return_value=(True, 0, "")):
                    with patch.object(run_role, "execute", side_effect=reply) as execute:
                        run_role.run(self.work, tag)
                delivered = execute.call_args.args[3] if tag == "codex" else execute.call_args.args[0][2]
                self.assertIn(self.raw.encode(), delivered.encode())
                self.assertIn("BEGIN DIFF ", delivered)
                self.assertIn("END DIFF ", delivered)
                self.assertEqual((self.work / "roles" / f"{tag}.diff").read_bytes(), self.raw.encode())
                result = json.loads((self.work / "slot" / f"{tag}-result.json").read_text())
                self.assertTrue(result["valid"])
                self.assertEqual(len(result["invocation_nonce"]), 32)
                self.assertNotEqual(result["request_digest"], self.plan["roles"][tag]["request_digest"])
                if tag == "codex":
                    self.assertEqual(execute.call_args.args[0][-1], "-")
                    self.assertIn("Review tag: codex", delivered)

    def test_chair_receives_the_same_raw_diff_bytes(self):
        execute, _ = self.chair([(0, "Evidence checked.\nVERDICT: FAIL\n", "")])
        self.assertIn(self.raw, execute.call_args.args[3])


if __name__ == "__main__":
    unittest.main()
