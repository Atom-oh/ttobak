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
body = json.dumps(response)
if tag == "claude-self":
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

    def test_aws_change_uses_four_reviews_and_two_safety_checks(self):
        calls = self.run_pipeline("infra/network.tf")
        self.assertEqual(len(calls), 6)
        self.assertEqual(sum(call["name"] == "kiro-cli" for call in calls), 4)
        kiro = [call for call in calls if call["name"] == "kiro-cli"]
        self.assertTrue(all(call["args"][1].startswith("Kiro startup safety check.") for call in kiro[:2]))
        self.assertTrue(all(not call["args"][1].startswith("Kiro startup safety check.") for call in kiro[2:]))
        self.assertEqual(len({call["cwd"] for call in kiro}), 4)
        for index, call in enumerate(kiro):
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
