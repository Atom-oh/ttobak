"""Run the workflow's actual publication shell with offline GitHub responses."""

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
    if not re.fullmatch(r"[\w\s'\"=!.()\-]+", condition):
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
        }
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
                report="Reviewed complete input.\nVERDICT: PASS\n", cancelled=False):
        for name in ("outputs", "published", "review.md"):
            (self.root / name).unlink(missing_ok=True)
        self.context = {key: value for key, value in self.context.items()
                        if not key.startswith("steps.")}
        for name, outcome in (
            ("Run specialists and conditionally adjudicate findings", review),
            ("Preserve specialist scope and execution evidence", evidence),
        ):
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
