"""Run the workflow's actual publication shell with offline GitHub responses."""

import glob
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / ".github/workflows/pr-review.yml"
HEAD = "a" * 40


def steps():
    # Only extract named step blocks and their literal run/env fields. No YAML
    # dependency is needed in the hosted Python protocol-test job.
    text = WORKFLOW.read_text()
    return {
        match[1]: match[2]
        for match in re.finditer(
            r"^      - name: ([^\n]+)\n(.*?)(?=^      - |\Z)", text, re.M | re.S
        )
    }


def scalar(block, key, indent=8):
    match = re.search(rf"^{' ' * indent}{re.escape(key)}: (.+)$", block, re.M)
    return match[1] if match else ""


def expand(text, context):
    return re.sub(r"\$\{\{\s*(.*?)\s*\}\}", lambda m: context.get(m[1], ""), text)


def allowed(block, context, failed=False, cancelled=False):
    condition = scalar(block, "if")
    if not condition:
        return not failed and not cancelled  # Actions' implicit success().
    if not re.search(r"\b(cancelled|success|failure|always)\(\)", condition):
        if failed or cancelled:
            return False
    condition = condition.removeprefix("${{").removesuffix("}}").strip()
    # These are the workflow's status expressions, not a general YAML runner.
    condition = condition.replace("!cancelled()", str(not cancelled))
    condition = re.sub(r"steps\.[\w.-]+", lambda m: repr(context.get(m[0], "")), condition)
    condition = condition.replace("&&", " and ").replace("||", " or ")
    if not re.fullmatch(r"[\w\s'\"=!.()/\-]+", condition):
        raise AssertionError(f"Unsupported workflow condition: {condition}")
    return bool(eval(condition, {"__builtins__": {}}, {}))


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.blocks = steps()
        self.context = {
            "github.event.pull_request.head.sha": HEAD,
            "github.event.pull_request.number": "253",
            "github.repository": "example/repo",
            "github.token": "offline-token",
            "github.run_id": "1234",
            "github.run_attempt": "1",
        }
        self.env = {
            "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
            "HOME": str(self.root),
            "HEAD_SHA": HEAD,
            "FAKE_HEAD": HEAD,
            "TEST_ROOT": str(self.root),
            "GITHUB_OUTPUT": str(self.root / "outputs"),
            "RUNNER_TEMP": str(self.root / "runner-temp"),
        }
        (self.root / "runner-temp").mkdir()
        fake = self.root / "gh"
        fake.write_text("""#!/usr/bin/env python3
import os,pathlib,sys
root = pathlib.Path(os.environ["TEST_ROOT"])
args = sys.argv[1:]
if args == ["api", "repos/example/repo/pulls/253", "--jq", ".head.sha"]:
    if os.environ.get("DENY_HEAD") == "1":
        raise SystemExit(1)
    print(os.environ["FAKE_HEAD"])
elif args[:2] == ["api", "repos/example/repo/issues/253/comments"]:
    pass
elif args[:3] == ["pr", "comment", "253"] and args[3] == "--body-file":
    (root / "published").write_bytes(pathlib.Path(args[4]).read_bytes())
else:
    raise SystemExit("Unexpected GitHub operation")
""")
        fake.chmod(0o755)

    def run_step(self, name):
        block = self.blocks[name]
        match = re.search(r"^        run: \|\n((?:          .*\n|\n)+)", block, re.M)
        self.assertIsNotNone(match, name)
        script = "\n".join(line[10:] for line in match[1].splitlines())
        script = expand(script, self.context).replace("/tmp/", str(self.root) + "/")
        env = dict(self.env)
        for key, value in re.findall(r"^          ([A-Z_]+): (.+)$", block, re.M):
            env[key] = expand(value, self.context)
        return subprocess.run(
            ["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script],
            env=env, text=True, capture_output=True, timeout=5,
        )

    def publish(self, review="success", evidence="success", artifact="42",
                report="Reviewed complete input.\nVERDICT: PASS\n", cancelled=False,
                workspace="success", artifact_input="success"):
        for name in ("outputs", "published", "review.md"):
            (self.root / name).unlink(missing_ok=True)
        self.context = {key: value for key, value in self.context.items()
                        if not key.startswith("steps.")}
        for name, outcome in (
            ("Prepare fresh review workspace", workspace),
            ("Validate and stage specialist evidence", artifact_input),
            ("Run specialists and conditionally adjudicate findings", review),
            ("Preserve specialist scope and execution evidence", evidence),
        ):
            if name not in self.blocks:
                continue
            step_id = scalar(self.blocks[name], "id")
            self.context[f"steps.{step_id}.outcome"] = outcome
            if name.startswith("Preserve"):
                self.context[f"steps.{step_id}.outputs.artifact-id"] = artifact
        if report is not None:
            (self.root / "review.md").write_text(report)
        failed = review != "success" or evidence != "success"
        gate = self.blocks["Check for blocking issues"]
        if allowed(gate, self.context, failed, cancelled):
            result = self.run_step("Check for blocking issues")
            self.context["steps.gate.outcome"] = "success" if result.returncode == 0 else "failure"
            output = self.root / "outputs"
            if output.exists():
                for line in output.read_text().splitlines():
                    key, value = line.split("=", 1)
                    self.context[f"steps.gate.outputs.{key}"] = value
            failed |= result.returncode != 0
        else:
            self.context["steps.gate.outcome"] = "skipped"
        post = self.blocks["Post review comment (upsert)"]
        self.post_result = None
        if allowed(post, self.context, failed, cancelled):
            self.post_result = self.run_step("Post review comment (upsert)")
        path = self.root / "published"
        return path.read_text() if path.exists() else ""

    def step_with_outputs(self, name):
        (self.root / "outputs").unlink(missing_ok=True)
        result = self.run_step(name)
        step_id = scalar(self.blocks[name], "id")
        self.context[f"steps.{step_id}.outcome"] = "success" if result.returncode == 0 else "failure"
        if (self.root / "outputs").exists():
            for line in (self.root / "outputs").read_text().splitlines():
                key, value = line.split("=", 1)
                self.context[f"steps.{step_id}.outputs.{key}"] = value
        return result

    def prepare_artifacts(self):
        result = self.step_with_outputs("Prepare fresh review workspace")
        self.assertEqual(result.returncode, 0, result.stderr)
        work = self.root / "pr-review"
        (work / "slot").mkdir()
        (work / "role-plan.json").write_text('{"head_sha":"' + HEAD + '"}\n')
        (work / "slot/codex-result.json").write_text('{"valid":false}\n')
        return work

    def upload_candidates(self, before_upload=None):
        # Run the actual validator shell, then resolve the action's actual path
        # input as a file consumer. The sink deliberately follows links like the
        # uploader; unsafe inputs must never reach it. No provider/network calls.
        validation = self.blocks.get("Validate and stage specialist evidence")
        if validation and allowed(validation, self.context, failed=True):
            self.validation_result = self.step_with_outputs("Validate and stage specialist evidence")
        if before_upload:
            before_upload()
        upload = self.blocks["Preserve specialist scope and execution evidence"]
        self.upload_ran = allowed(upload, self.context, failed=True)
        if not self.upload_ran:
            return []
        paths = scalar(upload, "path", indent=10)
        if paths == "|":
            paths = "\n".join(re.findall(r"^            (.+)$", upload, re.M))
        paths = expand(paths.replace("/tmp/", str(self.root) + "/"), self.context)
        files = []
        for pattern in paths.splitlines():
            for name in glob.glob(pattern):
                path = Path(name)
                candidates = path.rglob("*") if path.is_dir() else [path]
                files.extend(item.read_bytes() for item in candidates if item.is_file())
        return files

    def test_rejected_workspace_symlink_never_reaches_upload(self):
        foreign = self.root / "foreign"
        (foreign / "slot").mkdir(parents=True)
        secret = self.root / "credential"
        secret.write_text("OFFLINE_SECRET_MUST_NOT_UPLOAD")
        (foreign / "slot/leak-result.json").symlink_to(secret)
        (self.root / "pr-review").symlink_to(foreign, target_is_directory=True)
        result = self.step_with_outputs("Prepare fresh review workspace")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)
        self.assert_blocked(workspace="failure", artifact_input="skipped",
                            review="skipped", evidence="skipped")

    def test_nested_artifact_symlink_never_reaches_upload(self):
        work = self.prepare_artifacts()
        secret = self.root / "credential"
        secret.write_text("OFFLINE_SECRET_MUST_NOT_UPLOAD")
        (work / "slot/codex-result.json").unlink()
        (work / "slot/codex-result.json").symlink_to(secret)
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)
        self.assert_blocked(artifact_input="failure", evidence="skipped")

    def test_root_artifact_symlink_never_reaches_upload(self):
        work = self.prepare_artifacts()
        secret = self.root / "credential"
        secret.write_text("OFFLINE_SECRET_MUST_NOT_UPLOAD")
        (work / "role-plan.json").unlink()
        (work / "role-plan.json").symlink_to(secret)
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_unknown_artifact_name_cannot_extend_the_upload_allowlist(self):
        work = self.prepare_artifacts()
        (work / "slot/leak-result.json").write_text("unowned output")
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_nonregular_artifact_is_rejected_without_blocking_read(self):
        work = self.prepare_artifacts()
        (work / "slot/codex-result.json").unlink()
        os.mkfifo(work / "slot/codex-result.json")
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_slot_directory_symlink_never_reaches_upload(self):
        work = self.prepare_artifacts()
        (work / "slot").rename(self.root / "foreign-slot")
        (work / "slot").symlink_to(self.root / "foreign-slot", target_is_directory=True)
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_replaced_workspace_cannot_supply_another_runs_evidence(self):
        work = self.prepare_artifacts()
        work.rename(self.root / "original-work")
        (work / "slot").mkdir(parents=True)
        (work / "slot/codex-result.json").write_text("foreign run")
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_hardlinked_artifact_is_not_an_owned_output(self):
        work = self.prepare_artifacts()
        secret = self.root / "credential"
        secret.write_text("OFFLINE_SECRET_MUST_NOT_UPLOAD")
        (work / "slot/codex-result.json").unlink()
        os.link(secret, work / "slot/codex-result.json")
        self.assertEqual(self.upload_candidates(), [])
        self.assertFalse(self.upload_ran)

    def test_safe_failure_artifacts_are_preserved_without_raw_inputs(self):
        work = self.prepare_artifacts()
        (work / "slot/kiro-preflight-kiro-sol.flag").write_text("preflight_failed\n")
        (work / "roles").mkdir()
        (work / "roles/raw.diff").write_text("PRIVATE_DIFF_MUST_NOT_UPLOAD")
        uploaded = self.upload_candidates()
        self.assertTrue(self.upload_ran)
        self.assertEqual(sorted(uploaded), sorted([
            b'{"head_sha":"' + HEAD.encode() + b'"}\n',
            b'{"valid":false}\n', b"preflight_failed\n",
        ]))

    def test_upload_uses_regular_copies_after_source_changes(self):
        work = self.prepare_artifacts()
        secret = self.root / "credential"
        secret.write_text("OFFLINE_SECRET_MUST_NOT_UPLOAD")
        def replace_source():
            (work / "slot/codex-result.json").unlink()
            (work / "slot/codex-result.json").symlink_to(secret)
        uploaded = self.upload_candidates(before_upload=replace_source)
        self.assertTrue(self.upload_ran)
        self.assertIn(b'{"valid":false}\n', uploaded)
        self.assertNotIn(secret.read_bytes(), uploaded)

    def assert_blocked(self, **kwargs):
        body = self.publish(**kwargs)
        self.assertIn("**Status: BLOCKED**", body)
        self.assertNotIn("**Status: PASSED**", body)
        self.assertIn(HEAD, body)
        final = self.blocks["Fail if CRITICAL or MAJOR"]
        self.assertTrue(allowed(final, self.context, failed=True))
        self.assertNotEqual(self.run_step("Fail if CRITICAL or MAJOR").returncode, 0)
        return body

    def test_attempts_retain_separate_artifacts_without_overwrite(self):
        upload = self.blocks["Preserve specialist scope and execution evidence"]
        name = scalar(upload, "name", indent=10)
        first = expand(name, self.context)
        second = expand(name, dict(self.context, **{"github.run_attempt": "2"}))
        self.assertNotEqual(first, second)
        self.assertIn(HEAD, first)
        self.assertNotEqual(scalar(upload, "overwrite", indent=10), "true")
        self.assertEqual(scalar(upload, "if-no-files-found", indent=10), "error")

    def test_verified_execution_and_uploaded_evidence_publish_pass(self):
        body = self.publish()
        self.assertIn("**Status: PASSED**", body)
        self.assertIn("Reviewed complete input.", body)
        self.assertIn(HEAD, body)

    def test_synthesis_valid_trailing_blank_lines_keep_terminal_verdict(self):
        body = self.publish(report="Reviewed complete input.\nVERDICT: PASS\n\n")
        self.assertIn("**Status: PASSED**", body)
        self.assertNotIn("VERDICT:", body)

    def test_execution_failure_cannot_reuse_a_pass_report(self):
        body = self.assert_blocked(review="failure")
        self.assertIn("Review execution: failure", body)
        self.assertNotIn("Reviewed complete input.", body)

    def test_artifact_failure_cannot_publish_pass(self):
        for outcome in ("failure", "skipped", ""):
            with self.subTest(outcome=outcome):
                body = self.assert_blocked(evidence=outcome)
                self.assertIn(f"Evidence upload: {outcome or 'missing'}", body)
                self.assertNotIn("Reviewed complete input.", body)

    def test_missing_review_or_evidence_is_not_a_pass(self):
        for outcome in ("skipped", ""):
            with self.subTest(outcome=outcome):
                self.assert_blocked(review=outcome, evidence=outcome, report=None)

    def test_successful_upload_without_artifact_receipt_is_blocked(self):
        self.assert_blocked(artifact="")

    def test_missing_or_incomplete_verdict_is_blocked(self):
        for report in (None, "", "VERDICT: PASS\n", "VERDICT: PASS\nunfinished\n",
                       "VERDICT: PASS\nVERDICT: FAIL\n", "ordinary prose\n"):
            with self.subTest(report=report):
                (self.root / "review.md").unlink(missing_ok=True)
                self.assert_blocked(report=report)

    def test_blocking_findings_remain_visible(self):
        body = self.assert_blocked(report="MAJOR: concrete failure.\nVERDICT: FAIL\n")
        self.assertIn("MAJOR: concrete failure.", body)

    def test_failed_coverage_cannot_publish_pass(self):
        self.env["chair_failed"] = "1"
        self.assert_blocked()

    def test_missing_initialization_or_validation_cannot_publish_pass(self):
        self.assert_blocked(workspace="skipped")
        self.assert_blocked(artifact_input="failure")

    def test_changed_head_never_posts(self):
        self.env["FAKE_HEAD"] = "b" * 40
        self.assertEqual(self.publish(review="failure"), "")
        self.assertIsNotNone(self.post_result)
        self.assertNotEqual(self.post_result.returncode, 0)

    def test_unverifiable_head_never_posts(self):
        self.env["DENY_HEAD"] = "1"
        self.assertEqual(self.publish(), "")
        self.assertNotEqual(self.post_result.returncode, 0)

    def test_cancelled_run_never_posts_even_after_failure(self):
        self.assertEqual(self.publish(review="failure", cancelled=True), "")
        self.assertIsNone(self.post_result)
        self.assertFalse(allowed(
            self.blocks["Preserve specialist scope and execution evidence"],
            self.context, failed=True, cancelled=True,
        ))

    def test_failed_gate_never_defaults_to_pass_and_keeps_job_failed(self):
        self.context["steps.gate.outcome"] = "failure"
        self.context["steps.gate.outputs.result"] = "pass"
        block = self.blocks["Post review comment (upsert)"]
        self.assertTrue(allowed(block, self.context, failed=True))
        result = self.run_step("Post review comment (upsert)")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("**Status: BLOCKED**", (self.root / "published").read_text())
        self.assertTrue(allowed(
            self.blocks["Fail if CRITICAL or MAJOR"], self.context, failed=True,
        ))


if __name__ == "__main__":
    unittest.main()
