"""Exercise immutable preparation, real subprocesses, aggregation and synthesis."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parent
FAKE = r'''#!/usr/bin/env python3
import json,os,pathlib,sys
root = pathlib.Path(ROOT)
argv = sys.argv[1:]
name = pathlib.Path(sys.argv[0]).name
if name == "gh":
    print((root / "base-sha").read_text().strip())
    raise SystemExit(0)
stdin = sys.stdin.read()
with (root / "calls.jsonl").open("a") as stream:
    agent = pathlib.Path(".kiro/agents/inline-review.json")
    stream.write(json.dumps({"name": name, "args": argv, "stdin": stdin,
                            "cwd": str(pathlib.Path.cwd()), "home": os.environ.get("HOME"),
                            "agent": json.loads(agent.read_text()) if agent.exists() else None,
                            "canary": pathlib.Path("preflight-canary.txt").exists()}) + "\n")
if name == "kiro-cli" and argv[1].startswith("Kiro startup safety check."):
    assert stdin == ""
    if (root / "failed-sol-probe").exists() and "gpt-5.6-sol" in argv:
        print(pathlib.Path("preflight-canary.txt").read_text())
        raise SystemExit(0)
    print("NO_TOOLS")
    raise SystemExit(0)
if name == "claude" and argv[1].startswith("You chair"):
    print("The supplied candidate remains a concrete regression.\nVERDICT: FAIL")
    raise SystemExit(0)
model = argv[argv.index("--model") + 1]
tag = {"global.openai.gpt-6-astra":"codex", "claude-opus-5":"kiro-fable",
       "gpt-5.6-sol":"kiro-sol", "global.anthropic.claude-fable-5-1":"claude-self"}[model]
plan = json.loads((root / "work/role-plan.json").read_text())
role = plan["roles"][tag]
paths = role["paths"]
response = {"head_sha":plan["head_sha"],"role":role["role"],"scope_complete":True,
            "reviewed_paths":paths,"checks":[{"path":paths[0],"evidence":"Checked changed behavior."}],
            "findings":[],"uncertainties":[]}
if (root / "major").exists() and tag == "codex":
    response["findings"] = [{"severity":"MAJOR","path":paths[0],"condition":"When the branch runs",
                            "evidence":"The changed return value loses state."}]
if tag == "kiro-fable":
    stdout_error = root / "kiro-stdout-error"
    stdout_emitted = root / "kiro-stdout-error-emitted"
    if stdout_error.exists() and not stdout_emitted.exists():
        stdout_emitted.touch()
        if (root / "kiro-large-stderr").exists():
            print("x" * 700000, file=sys.stderr)
        print(stdout_error.read_text())
        raise SystemExit(0)
    malformed = root / "kiro-malformed"
    once = root / "kiro-malformed-once"
    wrapper = root / "kiro-wrapper-once"
    emitted = root / "kiro-malformed-emitted"
    if malformed.exists() or ((once.exists() or wrapper.exists()) and not emitted.exists()):
        emitted.touch()
        if wrapper.exists():
            print("```json\n{}\n```\nUnexpected trailing prose.")
        else:
            print('{"checks":[{"evidence":"An unescaped "quote"."}]}')
        if (root / "kiro-quota").exists():
            print("quota exceeded", file=sys.stderr)
        if (root / "kiro-colored-stderr").exists():
            print("You have reached the limit for over\x1b[31mages", file=sys.stderr)
        if (root / "kiro-expanding-stderr").exists():
            print(("xoxb-" + "a" * 10 + "\n") * 65000, file=sys.stderr)
        raise SystemExit(0)
    if (root / "kiro-major").exists():
        response["findings"] = [{"severity":"MAJOR","path":paths[0],
                                "condition":"When the branch runs",
                                "evidence":"The changed return value loses state."}]
    if (root / "kiro-quoted-diagnostics").exists():
        response["checks"][0]["evidence"] = (
            "quota exceeded\nFalling back to another model\n"
            "You have reached the limit for overages\nServiceQuotaExceededException\n"
            "using tool: synthetic"
        )
body = json.dumps(response)
if tag == "claude-self":
    for marker, diagnostic in (
        ("claude-quota-once", "Error: quota exceeded for this account"),
        ("claude-fallback-once", "Falling back to another model"),
        ("claude-transient-once", "Temporary connection reset"),
        ("claude-wrong-model-once", "Temporary connection reset"),
        ("claude-wrong-model-no-errors-once", "Temporary connection reset"),
        ("claude-empty-usage-once", "Temporary connection reset"),
        ("claude-invalid-warnings-once", "quota exceeded"),
        ("claude-invalid-errors-once", "quota exceeded"),
        ("claude-invalid-result-once", "quota exceeded"),
        ("claude-mixed-errors-once", "quota exceeded"),
        ("claude-invalid-transient-once", "Temporary connection reset"),
        ("claude-usage-list-once", "Temporary connection reset"),
        ("claude-usage-string-once", "Temporary connection reset"),
        ("claude-usage-null-once", "Temporary connection reset"),
        ("claude-usage-bool-once", "Temporary connection reset"),
        ("claude-usage-number-once", "Temporary connection reset"),
    ):
        emitted = root / (marker + "-emitted")
        if (root / marker).exists() and not emitted.exists():
            emitted.touch()
            failed = {"type":"result", "subtype":"error_during_execution",
                      "is_error":True, "errors":[diagnostic]}
            if marker in ("claude-wrong-model-once", "claude-wrong-model-no-errors-once"):
                failed["modelUsage"] = {"claude-other": {"inputTokens": 1}}
                if marker == "claude-wrong-model-no-errors-once":
                    failed.pop("errors")
            elif marker == "claude-empty-usage-once":
                failed["modelUsage"] = {}
            elif marker in ("claude-invalid-warnings-once", "claude-invalid-transient-once"):
                failed["warnings"] = None
            elif marker == "claude-invalid-errors-once":
                failed.update(errors={}, warnings=[diagnostic])
            elif marker == "claude-invalid-result-once":
                failed.update(errors={}, warnings=None, result=diagnostic)
            elif marker == "claude-mixed-errors-once":
                failed["errors"] = [None, diagnostic]
            elif marker == "claude-usage-list-once":
                failed["modelUsage"] = ["claude-other"]
            elif marker == "claude-usage-string-once":
                failed["modelUsage"] = "claude-other"
            elif marker == "claude-usage-null-once":
                failed["modelUsage"] = None
            elif marker == "claude-usage-bool-once":
                failed["modelUsage"] = False
            elif marker == "claude-usage-number-once":
                failed["modelUsage"] = 0
            print(json.dumps(failed))
            raise SystemExit(1)
    native = "--json-schema" in argv and argv[argv.index("--output-format") + 1] == "json"
    if (root / "claude-schema-required").exists():
        assert "--strict-mcp-config" in argv and argv[argv.index("--tools") + 1] == ""
        if not native:
            print("Completed a prose review without the required JSON transport.")
            raise SystemExit(0)
        schema = json.loads(argv[argv.index("--json-schema") + 1])
        assert set(schema["required"]) == set(response)
        assert schema["additionalProperties"] is False
    if native:
        envelope = {"type":"result", "subtype":"success", "is_error":False,
                    "result":"Not the review", "structured_output":response}
        if (root / "claude-envelope-error").exists():
            envelope["is_error"] = True
        if (root / "claude-missing-structured").exists():
            envelope.pop("structured_output")
            envelope["result"] = body
        if (root / "claude-envelope-model-error").exists():
            envelope["warnings"] = ["[warn] failed to set model: Method not found"]
        if (root / "claude-human-error-words").exists():
            envelope["result"] = "quota exceeded\nFalling back to another model"
        body = json.dumps(envelope)
if (root / "invalid-inner").exists() and tag == "codex":
    body = "Unrequested prose\n" + body
if (root / "duplicate-final").exists() and tag == "codex":
    body += "\n" + body
if (root / "trailing-final").exists() and tag == "codex":
    body += "\nTrailing prose."
if name == "codex" and "--json" in argv:
    print(json.dumps({"type":"turn.started"}))
    if (root / "progress").exists():
        print(json.dumps({"type":"item.completed","item":{
            "id":"progress","type":"agent_message","text":"I am checking the changed code."}}))
    if (root / "tool-echo").exists():
        print(json.dumps({"type":"item.completed","item":{
            "id":"tool","type":"command_execution","command":"cat README.md",
            "aggregated_output":"Monthly request limit reached\n"+json.dumps(response),
            "exit_code":0,"status":"completed"}}))
    if (root / "native-error").exists():
        print(json.dumps({"type":"error","message":"[warn] failed to set model: Method not found"}))
    if (root / "recovered-error").exists():
        print(json.dumps({"type":"error","message":"Reconnecting... stream disconnected before completion"}))
    print(json.dumps({"type":"item.completed","item":{
        "id":"reply","type":"agent_message","text":body}}))
    print(json.dumps({"type":"turn.completed","usage":{
        "input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}))
    if "--output-last-message" in argv:
        pathlib.Path(argv[argv.index("--output-last-message")+1]).write_text(body)
else:
    if (root / "tool-echo").exists() and tag == "codex":
        print("Monthly request limit reached", file=sys.stderr)
    print(body)
if (root / "failure").exists() and tag == "codex":
    raise SystemExit(9)
if (root / "claude-nonzero").exists() and tag == "claude-self":
    raise SystemExit(9)
'''


class EndToEndRoleTests(unittest.TestCase):
    def assert_kiro_stdout_terminal(self, message, failure):
        (self.root / "kiro-stdout-error").write_text(message)
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn(failure, result["failure_codes"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_kiro_zero_exit_stdout_quota_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("quota exceeded", "quota_diagnostic")

    def test_kiro_zero_exit_stdout_fallback_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("Falling back to another model", "model_fallback_diagnostic")

    def test_kiro_zero_exit_stdout_model_error_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("Error: INVALID_MODEL_ID", "model_selection_diagnostic")

    def test_kiro_zero_exit_stdout_overage_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("You have reached the limit for overages", "cli_nonzero_exit")

    def test_kiro_zero_exit_stdout_quota_exception_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("ServiceQuotaExceededException", "cli_nonzero_exit")

    def test_kiro_zero_exit_stdout_tool_use_cannot_be_erased_by_json_retry(self):
        self.assert_kiro_stdout_terminal("using tool: synthetic", "cli_nonzero_exit")

    def test_kiro_csi_split_stdout_overage_cannot_be_retried(self):
        self.assert_kiro_stdout_terminal(
            'Malformed JSON\nYou have reached the limit for over\x1b[31mages',
            "cli_nonzero_exit")

    def test_kiro_c1_split_stdout_tool_use_cannot_be_retried(self):
        self.assert_kiro_stdout_terminal("using\x9b31m tool: synthetic", "cli_nonzero_exit")

    def test_kiro_osc_split_stdout_quota_cannot_be_retried(self):
        self.assert_kiro_stdout_terminal(
            "ServiceQuota\x1b]0;synthetic\x07ExceededException", "cli_nonzero_exit")

    def test_kiro_terminal_signal_is_checked_before_secret_masking(self):
        self.assert_kiro_stdout_terminal(
            "token='You have reached the limit for over\x1b[31mages'",
            "cli_nonzero_exit")

    def test_kiro_post_scrub_overflow_cannot_be_erased_by_json_retry(self):
        # Each synthetic token expands from 15 to 22 characters when scrubbed.
        self.assert_kiro_stdout_terminal(("xoxb-" + "a" * 10 + "\n") * 65000,
                                         "output_byte_limit")

    def test_kiro_combined_diagnostic_overflow_is_terminal(self):
        (self.root / "kiro-large-stderr").touch()
        self.assert_kiro_stdout_terminal(
            "You have reached the limit for overages\n" + "x" * 700000,
            "output_byte_limit")

    def test_kiro_csi_split_stderr_cannot_be_erased_by_json_retry(self):
        (self.root / "kiro-malformed-once").touch()
        (self.root / "kiro-colored-stderr").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("cli_nonzero_exit", result["failure_codes"])

    def test_kiro_post_scrub_stderr_overflow_cannot_be_erased_by_json_retry(self):
        (self.root / "kiro-malformed-once").touch()
        (self.root / "kiro-expanding-stderr").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("output_byte_limit", result["failure_codes"])

    def test_kiro_valid_review_can_quote_diagnostics_without_retry(self):
        (self.root / "kiro-quoted-diagnostics").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def kiro_review_calls(self, calls):
        return [call for call in calls if call["name"] == "kiro-cli"
                and not call["args"][1].startswith("Kiro startup safety check.")
                and "claude-opus-5" in call["args"]]

    def test_malformed_kiro_json_uses_existing_retry_budget_and_fresh_request(self):
        (self.root / "kiro-malformed-once").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 2)
        self.assertNotEqual(calls[0]["args"][1], calls[1]["args"][1])
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertTrue(result["valid"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_invalid_kiro_json_wrapper_uses_existing_retry_budget(self):
        (self.root / "kiro-wrapper-once").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_malformed_kiro_json_remains_blocked_after_retry_exhaustion(self):
        (self.root / "kiro-malformed").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 2)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("malformed_json", result["failure_codes"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_kiro_quota_diagnostic_cannot_be_erased_by_json_retry(self):
        (self.root / "kiro-malformed-once").touch()
        (self.root / "kiro-quota").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("quota_diagnostic", result["failure_codes"])

    def test_valid_kiro_major_is_adjudicated_without_retry(self):
        (self.root / "kiro-major").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.kiro_review_calls(self.run_pipeline("infra/lib/stack.ts"))
        self.assertEqual(len(calls), 1)
        result = json.loads((self.work / "slot/kiro-fable-result.json").read_text())
        self.assertTrue(result["valid"])
        self.assertEqual(result["response"]["findings"][0]["severity"], "MAJOR")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.work = self.root / "work"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("codex", "kiro-cli", "claude", "gh"):
            executable = self.bin / name
            executable.write_text(FAKE.replace("ROOT", repr(str(self.root))))
            executable.chmod(0o755)
        target = self.repo / "scripts/pr-review"
        target.mkdir(parents=True)
        for source in SOURCE.iterdir():
            if source.is_file() and source.suffix in (".py", ".sh"):
                shutil.copy2(source, target / source.name)
        self.assertTrue((target / "lib.sh").exists(), "Repository scrubbers must be available")
        (self.repo / "AGENTS.md").write_text("Trusted project context: preserve data and auth.\n")
        (self.repo / "CLAUDE.md").write_text("Project source instructions.\n")
        (target / "role-input-scope.json").write_text(json.dumps({
            "schema_version": 1, "basenames": ["package-lock.json"],
            "extensions": [".png"], "directories": [], "prefixes": [],
        }))
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.name", "Review tests")
        self.git("config", "user.email", "tests@example.invalid")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.base = self.git("rev-parse", "HEAD").strip()
        (self.root / "base-sha").write_text(self.base)
        self.git("remote", "add", "origin", str(self.repo))
        self.environment = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        self.environment.update(
            BASE_SHA=self.base, GH_REPO="example/project", PANEL_RETRIES="1",
            PANEL_TIMEOUT="10", KIRO_PREFLIGHT_TIMEOUT="10",
        )
        self.environment.pop("GITHUB_ENV", None)
        self.environment.pop("ROLE_REVIEW", None)

    def git(self, *arguments):
        return subprocess.check_output(["git", *arguments], cwd=self.repo, text=True)

    def run_pipeline(self, path):
        changed = self.repo / path
        changed.parent.mkdir(parents=True, exist_ok=True)
        changed.write_text("export const result = 1;\n")
        self.git("add", ".")
        self.git("commit", "-qm", "change")
        self.environment["HEAD_SHA"] = self.git("rev-parse", "HEAD").strip()
        self.git("checkout", "-q", "--detach", self.base)
        result = subprocess.run([
            "bash", "scripts/pr-review/run-specialists.sh", "unused", "unused", str(self.work),
        ], cwd=self.repo, env=self.environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result = subprocess.run([
            "python3", "scripts/pr-review/synthesize_roles.py",
            "--work", str(self.work), "--output", str(self.work / "review.md"),
        ], cwd=self.repo, env=self.environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.root / "calls.jsonl"
        return [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []

    def test_frontend_uses_two_reviews_and_no_chair(self):
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(sorted(call["name"] for call in calls), ["claude", "codex"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_claude_native_schema_preserves_exact_issued_input_and_no_tools(self):
        (self.root / "claude-schema-required").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))
        call = next(call for call in calls if call["name"] == "claude")
        self.assertEqual(call["args"][1], (self.work / "requests/claude-self.prompt").read_text())
        self.assertEqual(call["stdin"], (self.work / "requests/claude-self.input").read_text())
        schema = json.loads(call["args"][call["args"].index("--json-schema") + 1])
        properties = schema["properties"]
        prose_fields = (
            properties["checks"]["items"]["properties"]["evidence"],
            properties["findings"]["items"]["properties"]["condition"],
            properties["findings"]["items"]["properties"]["evidence"],
            properties["uncertainties"]["items"],
        )
        self.assertTrue(all(field.get("pattern") == "^[^`]*$" for field in prose_fields))
        self.assertEqual(len(calls), 2)

    def test_claude_error_envelope_cannot_pass_even_with_a_valid_inner_review(self):
        (self.root / "claude-envelope-error").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_claude_missing_structured_output_cannot_use_the_text_result(self):
        (self.root / "claude-missing-structured").touch()
        self.run_pipeline("frontend/components/Button.tsx")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_claude_nonzero_exit_with_structured_output_stays_blocked(self):
        (self.root / "claude-nonzero").touch()
        self.run_pipeline("frontend/components/Button.tsx")
        result = json.loads((self.work / "slot/claude-self-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("cli_nonzero_exit", result["failure_codes"])

    def test_claude_envelope_model_warning_is_preserved_as_a_terminal_diagnostic(self):
        (self.root / "claude-envelope-model-error").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        result = json.loads((self.work / "slot/claude-self-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("model_selection_diagnostic", result["failure_codes"])

    def test_claude_successful_summary_can_quote_diagnostics_without_losing_coverage(self):
        (self.root / "claude-human-error-words").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def assert_claude_terminal_envelope_stops_retry(self, marker, code):
        (self.root / marker).touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(sum(call["name"] == "claude" for call in calls), 1)
        result = json.loads((self.work / "slot/claude-self-result.json").read_text())
        self.assertFalse(result["valid"])
        self.assertIn("cli_nonzero_exit", result["failure_codes"])
        self.assertIn(code, result["failure_codes"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_nonzero_claude_quota_envelope_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry("claude-quota-once", "quota_diagnostic")

    def test_nonzero_claude_fallback_envelope_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-fallback-once", "model_fallback_diagnostic")

    def test_nonzero_claude_model_mismatch_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-wrong-model-once", "model_selection_diagnostic")

    def test_nonzero_claude_failed_status_preserves_model_mismatch_without_errors_field(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-wrong-model-no-errors-once", "model_selection_diagnostic")

    def test_nonzero_claude_nonempty_usage_list_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-usage-list-once", "model_selection_diagnostic")

    def test_nonzero_claude_nonempty_usage_string_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-usage-string-once", "model_selection_diagnostic")

    def test_nonzero_claude_null_usage_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-usage-null-once", "model_selection_diagnostic")

    def test_nonzero_claude_boolean_usage_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-usage-bool-once", "model_selection_diagnostic")

    def test_nonzero_claude_numeric_usage_cannot_be_erased_by_clean_retry(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-usage-number-once", "model_selection_diagnostic")

    def test_terminal_error_with_null_warnings_stops_after_one_call(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-invalid-warnings-once", "quota_diagnostic")

    def test_terminal_warning_with_invalid_errors_stops_after_one_call(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-invalid-errors-once", "quota_diagnostic")

    def test_terminal_result_after_invalid_siblings_stops_after_one_call(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-invalid-result-once", "quota_diagnostic")

    def test_terminal_message_inside_mixed_errors_stops_after_one_call(self):
        self.assert_claude_terminal_envelope_stops_retry(
            "claude-mixed-errors-once", "quota_diagnostic")

    def test_invalid_metadata_without_terminal_evidence_retains_generic_retry(self):
        (self.root / "claude-invalid-transient-once").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(sum(call["name"] == "claude" for call in calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_nonzero_claude_transient_error_with_no_model_usage_can_still_retry(self):
        (self.root / "claude-empty-usage-once").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(sum(call["name"] == "claude" for call in calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_nonzero_claude_generic_transient_error_can_still_retry(self):
        (self.root / "claude-transient-once").touch()
        self.environment["PANEL_RETRIES"] = "2"
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(sum(call["name"] == "claude" for call in calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_aws_change_uses_four_reviews_and_two_safety_checks(self):
        calls = self.run_pipeline("infra/network.tf")
        self.assertEqual(len(calls), 6)
        self.assertEqual(sum(call["name"] == "kiro-cli" for call in calls), 4)
        kiro = [call for call in calls if call["name"] == "kiro-cli"]
        self.assertTrue(all(call["args"][1].startswith("Kiro startup safety check.") for call in kiro[:2]))
        self.assertTrue(all(not call["args"][1].startswith("Kiro startup safety check.") for call in kiro[2:]))
        self.assertEqual(len({call["cwd"] for call in kiro}), 4)
        for index, call in enumerate(kiro):
            self.assertIn("--legacy-ui", call["args"])
            self.assertEqual(call["args"][call["args"].index("--agent-engine") + 1], "v1")
            self.assertEqual(call["home"], call["cwd"])
            self.assertEqual(call["agent"]["tools"], [])
            self.assertEqual(call["agent"]["mcpServers"], {})
            self.assertEqual(call["canary"], index < 2)
            if index < 2:
                self.assertNotIn("BEGIN DIFF", call["args"][1])
                self.assertNotIn("infra/network.tf", call["args"][1])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_failed_second_kiro_probe_withholds_diff_from_both_roles(self):
        (self.root / "failed-sol-probe").touch()
        self.environment["KIRO_PREFLIGHT_PASSED"] = "1"  # An environment value cannot release the barrier.
        calls = self.run_pipeline("infra/network.tf")
        kiro = [call for call in calls if call["name"] == "kiro-cli"]
        self.assertEqual(len(kiro), 2)
        self.assertTrue(all(call["args"][1].startswith("Kiro startup safety check.") for call in kiro))
        self.assertEqual({call["name"] for call in calls if call["name"] != "kiro-cli"}, {"codex", "claude"})
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))
        summary = json.loads((self.work / "role-summary.json").read_text())
        self.assertEqual(summary["mode"], "blocked")
        self.assertTrue(summary["roles"]["kiro-fable"]["required"])
        self.assertTrue(summary["roles"]["kiro-sol"]["required"])

    def test_only_active_kiro_role_is_probed_once(self):
        calls = self.run_pipeline("frontend/styles/retry.css")
        kiro = [call for call in calls if call["name"] == "kiro-cli"]
        self.assertEqual(len(kiro), 2)
        self.assertTrue(kiro[0]["args"][1].startswith("Kiro startup safety check."))
        self.assertFalse(kiro[1]["args"][1].startswith("Kiro startup safety check."))
        self.assertTrue(all("gpt-5.6-sol" in call["args"] for call in kiro))
        self.assertNotEqual(kiro[0]["cwd"], kiro[1]["cwd"])
        self.assertFalse(kiro[1]["canary"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_nonzero_output_blocks_without_chair_override(self):
        (self.root / "failure").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_major_candidate_adds_exactly_one_chair_call(self):
        (self.root / "major").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 3)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_codex_tool_echo_does_not_block_complete_review(self):
        (self.root / "tool-echo").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_codex_native_error_blocks_otherwise_valid_review(self):
        (self.root / "native-error").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_codex_recovered_stream_error_keeps_complete_review(self):
        (self.root / "recovered-error").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_codex_inner_json_validation_remains_strict(self):
        (self.root / "invalid-inner").touch()
        self.run_pipeline("frontend/components/Button.tsx")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_codex_progress_does_not_contaminate_cli_designated_final_reply(self):
        (self.root / "progress").touch()
        calls = self.run_pipeline("frontend/components/Button.tsx")
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_duplicate_json_inside_codex_final_reply_remains_invalid(self):
        (self.root / "duplicate-final").touch()
        self.run_pipeline("frontend/components/Button.tsx")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_trailing_prose_inside_codex_final_reply_remains_invalid(self):
        (self.root / "trailing-final").touch()
        self.run_pipeline("frontend/components/Button.tsx")
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: FAIL\n"))

    def test_approved_asset_only_scope_skips_models_without_losing_provenance(self):
        calls = self.run_pipeline("frontend/public/icon.png")
        self.assertEqual(calls, [])
        source = json.loads((self.work / "role-source.json").read_text())
        self.assertEqual(source["excluded_paths"], ["frontend/public/icon.png"])
        self.assertTrue((self.work / "review.md").read_text().endswith("VERDICT: PASS\n"))

    def test_approved_lockfile_only_scope_skips_models(self):
        self.assertEqual(self.run_pipeline("frontend/package-lock.json"), [])


if __name__ == "__main__":
    unittest.main()
