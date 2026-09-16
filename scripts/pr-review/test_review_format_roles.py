"""The approved presentation contract gates publication, not metadata."""

import unittest
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from unittest.mock import patch

import role_review
import synthesize_roles
import test_role_review
import test_synthesize_roles


class ReviewFormatTests(unittest.TestCase):
    def test_shell_adapter_gets_instructions_and_fixed_failure(self):
        script = Path(__file__).with_name("review_format.py")
        result = subprocess.run([sys.executable, str(script), "instructions"],
                                capture_output=True, text=True, check=True)
        self.assertIn("fenced code blocks", result.stdout)
        with tempfile.TemporaryDirectory() as root:
            text = Path(root) / "reply.md"
            for value, expected in (("Checked `validate()`.", 0),
                                    ("password='synthetic-private'", 2)):
                text.write_text(value)
                result = subprocess.run([sys.executable, str(script), "check", str(text)],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, expected)
                self.assertEqual(result.stdout, "" if expected == 0 else
                                 "unsupported_review_format\n")
                self.assertNotIn("synthetic-private", result.stdout + result.stderr)

    def test_stream_adapter_emits_nothing_before_rejecting_invalid_output(self):
        script = Path(__file__).with_name("review_format.py")
        for value, accepted in (("Checked `validate()`.\n", True),
                                ("Public prefix.\npassword='synthetic-private'\n", False)):
            result = subprocess.run([sys.executable, str(script), "filter"], input=value,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0 if accepted else 2)
            self.assertEqual(result.stdout, value if accepted else "")
            self.assertEqual(result.stderr, "" if accepted else "unsupported_review_format\n")

    def response(self, evidence):
        path = "src/password=example.py"
        plan = {"head_sha": "a" * 40, "roles": {
            "codex": {"role": "implementation", "paths": [path]}}}
        response = {
            "head_sha": plan["head_sha"], "role": "implementation",
            "scope_complete": True, "reviewed_paths": [path],
            "checks": [{"path": path, "evidence": evidence}],
            "findings": [], "uncertainties": [],
        }
        return response, plan

    def test_supported_prose_references_and_fenced_examples(self):
        for text in (
            "Checked `validate()` and `src/service.py:12` against the caller.",
            "See `AWS::IAM::Role`, `--context`, `$NAME`, and `[REDACTED]`.",
            "Example:\n```sh\npassword='synthetic'\n```\nThe caller rejects it.",
            "Example:\n~~~~js\nconst text = `template`;\n~~~~\nChecked the caller.",
            "Example:\n````md\n```sh\npassword='synthetic'\n```\n````\nChecked.",
        ):
            with self.subTest(text=text):
                response, plan = self.response(text)
                role_review.validate_response(response, plan, "codex")

    def heading_examples(self):
        return (
            "Authorization:\nThe handler checks the caller.",
            "**Authorization:**\nThe handler checks the caller.",
            "origin-verify:\nThe origin gate remains enforced.",
            "Checked `token`\n===\nThe caller verifies its scope.",
            "Token\n=\nThe caller verifies its scope.",
            "See `Authorization`:\nThe caller is checked.",
        )

    def test_heading_prose_keeps_specialist_coverage(self):
        for text in self.heading_examples():
            with self.subTest(text=text):
                response, plan = self.response(text)
                try:
                    role_review.validate_response(response, plan, "codex")
                except role_review.Invalid as error:
                    self.fail(f"Heading rejected: {error}")
                helper = test_role_review.RoleReviewTests()
                helper.setUp()
                try:
                    helper.prepare()
                    helper.finish({"codex": helper.response("codex", checks=[{
                        "path": test_role_review.FRONTEND,
                        "evidence": text + "\nPUBLIC_AFTER",
                    }])})
                    result = helper.read("slot/codex-result.json")
                    self.assertTrue(result["valid"])
                    self.assertIn("PUBLIC_AFTER", result["response"]["checks"][0]["evidence"])
                    self.assertTrue((helper.work / "deterministic-review.md").read_text()
                                    .endswith("VERDICT: PASS\n"))
                finally:
                    helper.tearDown()

    def test_heading_prose_keeps_chair_adjudication(self):
        for text in self.heading_examples():
            with self.subTest(text=text):
                reply = (0, text + "\nPUBLIC_AFTER\nVERDICT: PASS\n", "")
                calls, published = self.chair([reply, reply])
                self.assertEqual(calls, 1)
                self.assertIn("PUBLIC_AFTER", published)
                self.assertTrue(published.endswith("VERDICT: PASS\n"))

    def citation_examples(self):
        return (
            "See [auth.ts](web/lib/auth.ts:42) for the missing guard.",
            "Authorization: The caller is checked.",
            "Checked `web/lib/token.ts`: the guard is missing.",
            "Per `docs/decisions/002-auth-and-login.md`: signup is closed.",
            "The guard at web/lib/auth.ts:42 is missing.",
            "See `app/src/lib/chart-tokens.ts:42` for palette mapping.",
            "See `AWS::SecretsManager::Secret` for the resource type.",
            "See `web/lib/token.ts:42-45` for token validation.",
            "Authorization:\n```http\nGET /health HTTP/1.1\n```",
        )

    def test_native_citations_and_prose_keep_specialist_coverage(self):
        for text in self.citation_examples():
            with self.subTest(text=text):
                response, plan = self.response(text)
                try:
                    role_review.validate_response(response, plan, "codex")
                except role_review.Invalid as error:
                    self.fail(f"Citation/prose rejected: {error}")
                helper = test_role_review.RoleReviewTests()
                helper.setUp()
                try:
                    helper.prepare()
                    helper.finish({"codex": helper.response("codex", checks=[{
                        "path": test_role_review.FRONTEND,
                        "evidence": text + "\nPUBLIC_AFTER",
                    }])})
                    result = helper.read("slot/codex-result.json")
                    self.assertTrue(result["valid"])
                    self.assertIn("PUBLIC_AFTER", result["response"]["checks"][0]["evidence"])
                    self.assertTrue((helper.work / "deterministic-review.md").read_text()
                                    .endswith("VERDICT: PASS\n"))
                finally:
                    helper.tearDown()

    def test_native_citations_and_prose_keep_chair_adjudication(self):
        for text in self.citation_examples():
            with self.subTest(text=text):
                reply = (0, text + "\nPUBLIC_AFTER\nVERDICT: PASS\n", "")
                calls, published = self.chair([reply, reply])
                self.assertEqual(calls, 1)
                self.assertIn("PUBLIC_AFTER", published)
                self.assertTrue(published.endswith("VERDICT: PASS\n"))

    def test_large_citation_list_scrubs_within_bounded_time(self):
        # Repeated same-line citations used to rescan every preceding reference.
        # Exercise real scrubbing in a child so a regression cannot hang the suite.
        program = """
import role_review
text = "Checked " + "`src/token.ts:42`, " * 12000 + "PUBLIC_AFTER"
assert len(text.encode()) < role_review.MAX_OUTPUT_BYTES
clean = role_review.scrub(text)
assert clean == "Checked " + "`src/[REDACTED]`, " * 12000 + "PUBLIC_AFTER"
assert role_review.format_violation(clean, role_review.PROSE_SENSITIVE_KEY) is None
"""
        try:
            result = subprocess.run(
                [sys.executable, "-c", program], cwd=Path(__file__).parent,
                capture_output=True, text=True, timeout=10,
            )
        except subprocess.TimeoutExpired:
            self.fail("A bounded citation list exceeded the scrubbing deadline")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_unsupported_examples_in_each_prose_field(self):
        for text in (
            "Example: `password='synthetic'`.",
            "Run `echo hello`.",
            "Run `first\nsecond`.",
            "Checked `unclosed.",
            "Example:\n```sh\npassword='synthetic'\n",
            "Example:\n```sh\npassword='synthetic'\n~~~",
            "Example:\n> ```sh\n> password='synthetic'\n> ```",
            "Example:\n    ```sh\n    password='synthetic'\n    ```",
            "Example:\npassword = 'synthetic'",
            'Example:\n"api_key": "synthetic"',
            "Example:\npassword\n= 'synthetic'",
            "Example: `` password='synthetic' ``.",
            "Set `password` = 'synthetic-private'.",
            "Set `api_key`: 'synthetic-private'.",
            "Set `password`\n= 'synthetic-private'.",
            "Authorization: Bearer synthetic-private",
            "origin-verify: synthetic-private",
            "password=",
            "password:admin",
            "password: admin",
            "See web/lib/auth.ts:42; password='synthetic-private'.",
            "Authorization: The caller is checked; token=synthetic-private",
            "Checked `web/lib/token.ts`: the guard is missing; api_key='synthetic-private'.",
        ):
            for field in ("check", "condition", "evidence", "uncertainty"):
                with self.subTest(text=text, field=field):
                    response, plan = self.response("Checked the changed caller.")
                    if field == "check":
                        response["checks"][0]["evidence"] = text
                    elif field == "uncertainty":
                        response["uncertainties"] = [text]
                    else:
                        finding = {"severity": "MAJOR", "path": response["reviewed_paths"][0],
                                   "condition": "The caller fails.", "evidence": "Checked the caller."}
                        finding[field] = text
                        response["findings"] = [finding]
                    with self.assertRaisesRegex(role_review.Invalid, "^unsupported_review_format$"):
                        role_review.validate_response(response, plan, "codex")

    def chair(self, replies):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        (root / "chair-mode.txt").write_text("review\n")
        (root / "role-summary.json").write_text('{"findings":[]}')
        (root / "project-context.md").write_text("Trusted base.")
        (root / "roles").mkdir()
        (root / "roles/codex.diff").write_text("Complete supplied diff.")
        with patch.dict(synthesize_roles.os.environ, {
                "GITHUB_ENV": str(root / "test-env"), "CHAIR_TIMEOUT": "10",
                "CHAIR_PRIMARY_MODEL": "global.anthropic.claude-fable-5-1",
                "CHAIR_FALLBACK_MODEL": "global.anthropic.claude-opus-5"}), \
                patch.object(synthesize_roles, "execute", side_effect=replies) as invoke:
            synthesize_roles.synthesize(root, root / "review.md")
        return invoke.call_count, (root / "review.md").read_text()

    def test_chair_does_not_publish_unsupported_examples(self):
        reply = (0, "Example: `password='synthetic-private'`.\nVERDICT: PASS\n", "")
        calls, text = self.chair([reply, reply])
        self.assertEqual(calls, 2)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))
        self.assertIn("format", text.lower())
        self.assertNotIn("synthetic-private", text)

    def test_chair_checks_sanitized_format_too(self):
        reply = (0, "Checked `validate()`.\nVERDICT: PASS\n", "")
        with patch.object(synthesize_roles, "scrub_decoded",
                          return_value="Checked `unclosed.\nVERDICT: PASS\n"):
            _, text = self.chair([reply, reply])
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))

    def test_format_failure_does_not_hide_account_limit(self):
        calls, text = self.chair([
            (0, "Run `bad\nexample`.\nVERDICT: PASS\n", "quota exceeded"),
            (0, "Must not run.\nVERDICT: PASS\n", ""),
        ])
        self.assertEqual(calls, 1)
        self.assertTrue(text.endswith("VERDICT: FAIL\n"))
        self.assertNotIn("bad", text)

    def test_invalid_specialist_output_blocks_coverage_without_public_payload(self):
        helper = test_role_review.RoleReviewTests()
        helper.setUp()
        self.addCleanup(helper.tearDown)
        helper.prepare()
        response = helper.response("codex", checks=[{
            "path": test_role_review.FRONTEND,
            "evidence": "Run `password='synthetic-private'`.",
        }])
        result = helper.record("codex", response, expected=2)
        self.assertFalse(result["valid"])
        self.assertEqual(result["failure_codes"], ["unsupported_review_format"])
        self.assertIsNone(result["response"])
        helper.record("claude-self")
        helper.cli("aggregate", "--work", helper.work, expected=2)
        published = (helper.work / "deterministic-review.md").read_text()
        self.assertTrue(published.endswith("VERDICT: FAIL\n"))
        self.assertNotIn("synthetic-private", published)
        self.assertIn("unsupported_review_format", published)

    def test_deterministic_findings_keep_embedded_fences_and_verdicts_literal(self):
        helper = test_role_review.RoleReviewTests()
        helper.setUp()
        self.addCleanup(helper.tearDown)
        helper.prepare()
        response = helper.response("codex", findings=[{
            "severity": "MINOR", "path": test_role_review.FRONTEND,
            "condition": "The rendering fixture contains a verdict marker.",
            "evidence": "Fixture:\n````text\n```\nVERDICT: FAIL\n```\n````",
        }])
        helper.finish({"codex": response})
        published = (helper.work / "deterministic-review.md").read_text()
        self.assertIn("```json\n", published)
        self.assertEqual([line for line in published.splitlines() if line.startswith("VERDICT:")],
                         ["VERDICT: PASS"])
        self.assertIn("\\nVERDICT: FAIL\\n", published)

    def fenced_json_cases(self):
        canary = "NFM_FENCED_JSON_CANARY"
        for value in (
            {"name": "password:admin", "value": canary, "public": "PUBLIC_KEEP"},
            {"headerName": "token=abc", "headerValue": canary, "public": "PUBLIC_KEEP"},
            {"password:admin": canary, "public": "PUBLIC_KEEP"},
        ):
            yield "Example:\n```json\n" + json.dumps(value) + "\n```\nPUBLIC_AFTER"

    def test_fenced_json_keeps_named_value_privacy_through_publication(self):
        for evidence in self.fenced_json_cases():
            with self.subTest(evidence=evidence):
                helper = test_role_review.RoleReviewTests()
                helper.setUp()
                try:
                    helper.prepare()
                    response = helper.response("codex", findings=[{
                        "severity": "MINOR", "path": test_role_review.FRONTEND,
                        "condition": "Synthetic JSON configuration example.",
                        "evidence": evidence,
                    }])
                    helper.finish({"codex": response})
                    for name in ("slot/codex-result.json", "role-summary.json",
                                 "deterministic-review.md"):
                        published = (helper.work / name).read_text()
                        self.assertNotIn("NFM_FENCED_JSON_CANARY", published)
                        self.assertIn("PUBLIC_KEEP", published)
                    self.assertTrue((helper.work / "deterministic-review.md").read_text()
                                    .endswith("VERDICT: PASS\n"))
                finally:
                    helper.tearDown()

    def test_chair_masks_fenced_json_before_losing_sensitive_labels(self):
        for evidence in self.fenced_json_cases():
            with self.subTest(evidence=evidence):
                reply = (0, evidence + "\nVERDICT: PASS\n", "")
                calls, published = self.chair([reply, reply])
                self.assertEqual(calls, 1)
                self.assertNotIn("NFM_FENCED_JSON_CANARY", published)
                self.assertIn("PUBLIC_KEEP", published)
                self.assertTrue(published.endswith("VERDICT: PASS\n"))

    def test_fenced_json_adapter_does_not_repair_other_block_bodies(self):
        for text in (
            "```python\npassword = 'synthetic'\n```\nPUBLIC_AFTER",
            '```json\n{"password": "synthetic",}\n```\nPUBLIC_AFTER',
            '```json\n{"name":"public","name":"password","value":"synthetic"}\n```',
            '```json\n{"password": "synthetic"}\n',
        ):
            with self.subTest(text=text):
                self.assertEqual(role_review.mask_fenced_json(text), text)





    def qualified_yaml_examples(self):
        return (
            'db.password: "FMT_V4_PRIVATE"',
            '/prod/db/password: "FMT_V4_PRIVATE"',
            '`config.password`: "FMT_V4_PRIVATE"',
            '`/prod/db/password` = "FMT_V4_PRIVATE"',
            'password: !!str "FMT_V4_PRIVATE"',
            'password: &credential "FMT_V4_PRIVATE"',
        )

    def test_v4_rejects_qualified_and_yaml_forms_in_each_prose_field(self):
        for text in self.qualified_yaml_examples():
            for field in ("check", "condition", "evidence", "uncertainty"):
                with self.subTest(text=text, field=field):
                    response, plan = self.response("Checked the changed caller.")
                    if field == "check":
                        response["checks"][0]["evidence"] = text
                    elif field == "uncertainty":
                        response["uncertainties"] = [text]
                    else:
                        finding = {"severity": "MAJOR", "path": response["reviewed_paths"][0],
                                   "condition": "The caller fails.", "evidence": "Checked the caller."}
                        finding[field] = text
                        response["findings"] = [finding]
                    with self.assertRaisesRegex(role_review.Invalid, "^unsupported_review_format$"):
                        role_review.validate_response(response, plan, "codex")

    def test_v4_invalid_forms_never_enter_published_role_results(self):
        for evidence in self.qualified_yaml_examples():
            with self.subTest(evidence=evidence):
                helper = test_role_review.RoleReviewTests()
                helper.setUp()
                try:
                    helper.prepare()
                    response = helper.response("codex", checks=[{
                        "path": test_role_review.FRONTEND, "evidence": evidence}])
                    result = helper.record("codex", response, expected=2)
                    self.assertEqual(result["failure_codes"], ["unsupported_review_format"])
                    self.assertIsNone(result["response"])
                    helper.record("claude-self")
                    helper.cli("aggregate", "--work", helper.work, expected=2)
                    for name in ("slot/codex-result.json", "role-summary.json", "deterministic-review.md"):
                        self.assertNotIn("FMT_V4_PRIVATE", (helper.work / name).read_text())
                    self.assertTrue((helper.work / "deterministic-review.md").read_text()
                                    .endswith("VERDICT: FAIL\n"))
                finally:
                    helper.tearDown()

    def test_v4_chair_rejects_qualified_and_yaml_examples(self):
        for evidence in self.qualified_yaml_examples():
            with self.subTest(evidence=evidence):
                reply = (0, evidence + "\nVERDICT: PASS\n", "")
                calls, published = self.chair([reply, reply])
                self.assertEqual(calls, 2)
                self.assertTrue(published.endswith("VERDICT: FAIL\n"))
                self.assertNotIn("FMT_V4_PRIVATE", published)

    def test_complete_fenced_examples_preserve_real_chair_adjudication(self):
        canary = "SYNTHETIC_FENCED_PRIVATE"
        examples = [f'password = (previous {operator}\n "{canary}")'
                    for operator in ("or", "||", "??")]
        examples += [f'secret: |\n  Cookie: session=public\n  {canary}',
                     f'name: PASSWORD\nvalue: "{canary}"',
                     "curl -d '" + json.dumps({"password": canary, "public": "kept"})
                     + "' https://example.invalid", "password="]
        for example in examples:
            with self.subTest(example=example):
                reply = (0, "Example:\n```text\n" + example
                         + "\n```\nPUBLIC_AFTER\nVERDICT: PASS\n", "")
                calls, text = self.chair([reply, reply])
                self.assertEqual(calls, 1)
                self.assertNotIn(canary, text)
                self.assertIn("PUBLIC_AFTER", text)
                self.assertTrue(text.endswith("VERDICT: PASS\n"))

    def test_closing_boundary_does_not_exempt_opening_fence_or_unclosed_value(self):
        canary = "SYNTHETIC_BOUNDARY_PRIVATE"
        for body in ('password=\n```text\n' + canary + '\n```',
                     '```text\npassword="' + canary + '\n```'):
            with self.subTest(body=body):
                raw = body + "\nPUBLIC_AFTER\nVERDICT: PASS\n"
                self.assertNotIn(canary, role_review.scrub(raw))
                calls, text = self.chair([(0, raw, ""), (0, raw, "")])
                self.assertEqual(calls, 2)
                self.assertNotIn(canary, text)
                self.assertTrue(text.endswith("VERDICT: FAIL\n"))

    def test_escaped_fenced_json_preserves_existing_decoding_and_guards(self):
        canary = "SYNTHETIC_ESCAPED_PRIVATE"
        for value in ({"password": canary, "public": "KEEP_AFTER"},
                      {"name": "password:admin", "value": canary, "public": "KEEP_AFTER"}):
            encoded = json.dumps(json.dumps(value))[1:-1]
            text = "Example:\n```text\n" + encoded + "\n```\nPUBLIC_AFTER"
            with self.subTest(value=value):
                clean = role_review.scrub(text)
                self.assertNotIn(canary, clean)
                self.assertIn("KEEP_AFTER", clean)
                calls, published = self.chair([(0, text + "\nVERDICT: PASS\n", "")] * 2)
                self.assertEqual(calls, 1)
                self.assertNotIn(canary, published)
                self.assertIn("KEEP_AFTER", published)
        public = '```text\n' + json.dumps(json.dumps({"public": "kept"}))[1:-1] + '\n```'
        self.assertEqual(role_review.scrub(public), public)
        malformed = '```text\n' + r'{\"password\": BROKEN}' + '\n```'
        self.assertEqual(role_review.mask_fenced_json(malformed), malformed)
        nested = "public"
        for _ in range(31):
            nested = [nested]
        encoded = json.dumps(json.dumps(nested))[1:-1]
        for text in (encoded, '```text\n' + encoded + '\n```'):
            with self.assertRaisesRegex(role_review.Invalid, "^scrub_nesting_limit$"):
                role_review.scrub(text)

    def test_fenced_json_retains_public_spelling_and_shared_limits(self):
        text = 'Example:\n```json\n{\n  "public": "kept", "list": [1, 2]\n}\n```\nPUBLIC_AFTER'
        self.assertEqual(role_review.scrub(text), text)
        half = role_review.MAX_OUTPUT_BYTES // 2
        frame = '```json\n' + json.dumps({"public": "b" * half}) + '\n```'
        with self.assertRaisesRegex(role_review.Invalid, "^output_byte_limit$"):
            role_review.scrub(["a" * half, frame])
        nested = "public"
        for _ in range(33):
            nested = [nested]
        for value in (json.dumps(nested), '```json\n' + json.dumps(nested) + '\n```'):
            with self.subTest(fenced=value.startswith("```")):
                with self.assertRaisesRegex(role_review.Invalid, "^scrub_nesting_limit$"):
                    role_review.scrub(value)

    def test_original_fail_with_invalid_format_cannot_fall_back_to_pass(self):
        first = (0, "Blocking issue remains. Run `echo details`.\nVERDICT: FAIL\n", "")
        fallback = (0, "Fallback must not approve this review.\nVERDICT: PASS\n", "")
        calls, published = self.chair([first, fallback])
        self.assertEqual(calls, 1)
        self.assertTrue(published.endswith("VERDICT: FAIL\n"))
        self.assertIn("withheld", published.lower())
        self.assertNotIn("echo details", published)

    def test_original_fail_format_error_keeps_quota_precedence(self):
        first = (0, "Blocking issue remains. Run `echo details`.\nVERDICT: FAIL\n",
                 "Error: insufficient credits")
        fallback = (0, "Must not run.\nVERDICT: PASS\n", "")
        calls, published = self.chair([first, fallback])
        self.assertEqual(calls, 1)
        self.assertTrue(published.endswith("VERDICT: FAIL\n"))
        self.assertNotIn("details were withheld", published.lower())



if __name__ == "__main__":
    unittest.main()
