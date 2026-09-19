"""Claude's CLI envelope is transport metadata, never a replacement review."""
import json
from itertools import permutations
import re
import unittest

import role_review
import run_role


class ClaudeTransportTests(unittest.TestCase):
    def test_prose_schema_prevents_observed_inline_code_failures(self):
        properties = run_role.claude_schema()["properties"]
        prose_fields = (
            properties["checks"]["items"]["properties"]["evidence"],
            properties["findings"]["items"]["properties"]["condition"],
            properties["findings"]["items"]["properties"]["evidence"],
            properties["uncertainties"]["items"],
        )
        for field in prose_fields:
            for invalid in (
                'Adds `id="user-management"` to the section.',
                "The `admin invite` call is unchanged.",
                "Only `resendInvite(user.userId)` changes.",
                'Adds `<Link href="/settings#user-management">`.',
            ):
                with self.subTest(invalid=invalid):
                    self.assertIsNone(re.search(field["pattern"], invalid))
                    self.assertEqual(role_review.format_violation(
                        invalid, role_review.PROSE_SENSITIVE_KEY), "unsupported_review_format")
            for valid in (
                "The resendInvite call keeps its userId argument.",
                "Checked src/auth.ts and the /settings#user-management target.",
                'Example:\n~~~html\n<p id="example">Text</p>\n~~~\nThe ID is unique.',
            ):
                with self.subTest(valid=valid):
                    self.assertIsNotNone(re.search(field["pattern"], valid))
                    self.assertIsNone(role_review.format_violation(
                        valid, role_review.PROSE_SENSITIVE_KEY))
        for field in (
            properties["head_sha"], properties["role"], properties["reviewed_paths"]["items"],
            properties["checks"]["items"]["properties"]["path"],
            properties["findings"]["items"]["properties"]["path"],
        ):
            self.assertNotIn("pattern", field)

    def test_producer_schema_does_not_replace_host_format_validation(self):
        response = dict(self.response, checks=[{
            "path": self.response["reviewed_paths"][0],
            "evidence": "Run `echo synthetic`.",
        }])
        output, error, complete = self.decode(json.dumps(dict(
            self.envelope, structured_output=response)))
        self.assertTrue(complete, error)
        plan = {"head_sha": response["head_sha"], "roles": {
            "claude-self": {"role": response["role"], "paths": response["reviewed_paths"]}}}
        with self.assertRaisesRegex(role_review.Invalid, "^unsupported_review_format$"):
            role_review.validate_response(role_review.parse_response(output), plan, "claude-self")

    def setUp(self):
        self.response = {
            "head_sha": "a" * 40, "role": "requirements", "scope_complete": True,
            "reviewed_paths": ["backend/example.py"],
            "checks": [{"path": "backend/example.py", "evidence": "Checked the change."}],
            "findings": [], "uncertainties": [],
        }
        self.envelope = {
            "type": "result", "subtype": "success", "is_error": False,
            "result": "Human-readable completion text is not the review.",
            "structured_output": self.response,
            "session_id": "synthetic-session", "usage": {"input_tokens": 1},
        }

    def decode(self, value, expected_model=None, exit_code=0):
        self.assertTrue(callable(getattr(run_role, "claude_response", None)),
                        "Claude needs a strict structured-output transport adapter")
        return (run_role.claude_response(value, expected_model, exit_code) if exit_code
                else run_role.claude_response(value, expected_model))

    def test_success_extracts_only_the_structured_review(self):
        output, error, valid = self.decode(json.dumps(self.envelope))
        self.assertTrue(valid, error)
        self.assertEqual(json.loads(output), self.response)
        self.assertNotIn("Human-readable", output)
        self.assertNotIn("synthetic-session", output)

    def test_prose_fences_arrays_and_multiple_envelopes_are_not_searched_for_json(self):
        raw = json.dumps(self.envelope)
        for text in ("Some prose", "prefix\n" + raw, raw + "\ntrailing",
                     "```json\n" + raw + "\n```", json.dumps([self.envelope]),
                     raw + "\n" + raw):
            with self.subTest(text=text[:30]):
                output, error, valid = self.decode(text)
                self.assertEqual(output, "")
                self.assertFalse(valid)
                self.assertTrue(error)
                self.assertNotIn(text, error)

    def test_error_missing_or_nonobject_structured_output_never_falls_back_to_result(self):
        for changes in (
            {"is_error": True}, {"is_error": 0}, {"is_error": None},
            {"subtype": "error_max_structured_output_retries"},
            {"type": "assistant"}, {"structured_output": None},
            {"structured_output": json.dumps(self.response)}, {"structured_output": []},
        ):
            with self.subTest(changes=changes):
                envelope = dict(self.envelope, result=json.dumps(self.response), **changes)
                output, _, valid = self.decode(json.dumps(envelope))
                self.assertEqual(output, "")
                self.assertFalse(valid)
        for field in ("is_error", "structured_output", "subtype", "type"):
            envelope = dict(self.envelope)
            del envelope[field]
            self.assertFalse(self.decode(json.dumps(envelope))[2])

    def test_duplicate_keys_and_nonfinite_values_are_rejected(self):
        raw = json.dumps(self.envelope)
        for text in (
            '{"is_error":true,' + raw[1:],
            raw.replace('"scope_complete": true', '"scope_complete": false, "scope_complete": true'),
            raw.replace('"input_tokens": 1', '"input_tokens": NaN'),
        ):
            with self.subTest(text=text[:50]):
                output, _, valid = self.decode(text)
                self.assertEqual(output, "")
                self.assertFalse(valid)

    def test_raw_size_failure_precedes_parsing_and_remains_terminal(self):
        output, error, valid = self.decode("x" * (1024 * 1024 + 1))
        self.assertEqual((output, error, valid), ("", "output_byte_limit", False))
        self.assertEqual(role_review.diagnostic_failure(error), "output_byte_limit")

    def test_envelope_diagnostics_are_not_discarded_with_transport_metadata(self):
        for text, expected in (
            ("[warn] failed to set model: Method not found", "model_selection_diagnostic"),
            ("Falling back to another model", "model_fallback_diagnostic"),
            ("quota exceeded", "quota_diagnostic"),
        ):
            for field in ("errors", "warnings"):
                with self.subTest(field=field, diagnostic=expected):
                    envelope = dict(self.envelope, **{
                        field: text if field == "result" else [text],
                    })
                    output, error, valid = self.decode(json.dumps(envelope))
                    self.assertEqual(output, "")
                    self.assertFalse(valid)
                    self.assertEqual(role_review.diagnostic_failure(error), expected)
        envelope = dict(self.envelope, errors=["PRIVATE_ERROR_TEXT"])
        output, error, valid = self.decode(json.dumps(envelope))
        self.assertFalse(valid)
        self.assertNotIn("PRIVATE_ERROR_TEXT", error)

    def test_reported_usage_must_include_the_requested_profile_or_its_model_name(self):
        expected = "global.anthropic.claude-fable-5-1"
        for name in (expected, "claude-fable-5-1"):
            envelope = dict(self.envelope, modelUsage={name: {"inputTokens": 1}})
            self.assertTrue(self.decode(json.dumps(envelope), expected)[2])
        for usage in ({}, [], {"claude-other": {"inputTokens": 1}},
                      {"claude-fable-5-1-other": {"inputTokens": 1}}):
            envelope = dict(self.envelope, modelUsage=usage)
            output, error, valid = self.decode(json.dumps(envelope), expected)
            self.assertFalse(valid)
            self.assertEqual(output, "")
            self.assertEqual(role_review.diagnostic_failure(error), "model_selection_diagnostic")

    def test_failed_envelope_retains_terminal_diagnostics_without_structured_output(self):
        for text, expected in (
            ("Error: quota exceeded for this account", "quota_diagnostic"),
            ("Falling back to another model", "model_fallback_diagnostic"),
            ("[warn] failed to set model: Method not found", "model_selection_diagnostic"),
        ):
            with self.subTest(diagnostic=expected):
                envelope = {"type": "result", "subtype": "error_during_execution",
                            "is_error": True, "errors": [text]}
                output, error, valid = self.decode(json.dumps(envelope))
                self.assertEqual(output, "")
                self.assertFalse(valid)
                self.assertEqual(role_review.diagnostic_failure(error), expected)

    def test_failed_envelope_retains_reported_model_mismatch(self):
        envelope = {"type": "result", "subtype": "error_during_execution",
                    "is_error": True, "errors": ["Temporary connection reset"],
                    "modelUsage": {"claude-other": {"inputTokens": 1}}}
        output, error, valid = self.decode(
            json.dumps(envelope), "global.anthropic.claude-fable-5-1", exit_code=1)
        self.assertEqual(output, "")
        self.assertFalse(valid)
        self.assertEqual(role_review.diagnostic_failure(error), "model_selection_diagnostic")
        envelope["warnings"] = {"unsupported": "metadata"}
        output, error, valid = self.decode(
            json.dumps(envelope), "global.anthropic.claude-fable-5-1", exit_code=1)
        self.assertEqual(output, "")
        self.assertFalse(valid)
        self.assertEqual(role_review.diagnostic_failure(error), "model_selection_diagnostic")

    def test_present_nondictionary_usage_is_terminal_on_failed_envelopes(self):
        for usage in (None, [], ["claude-other"], "", "claude-other",
                      False, True, 0, 1, 0.0, 1.5):
            for errors in (None, ["Temporary connection reset"]):
                with self.subTest(usage=usage, errors=errors):
                    envelope = {"type": "result", "subtype": "error_during_execution",
                                "is_error": True, "modelUsage": usage}
                    if errors is not None:
                        envelope["errors"] = errors
                    output, error, valid = self.decode(
                        json.dumps(envelope), "global.anthropic.claude-fable-5-1", exit_code=1)
                    self.assertEqual(output, "")
                    self.assertFalse(valid)
                    self.assertEqual(role_review.diagnostic_failure(error),
                                     "model_selection_diagnostic")

    def test_terminal_messages_survive_malformed_siblings_in_every_field_order(self):
        diagnostics = (
            ("quota exceeded", "quota_diagnostic"),
            ("Falling back to another model", "model_fallback_diagnostic"),
            ("[warn] failed to set model: Method not found", "model_selection_diagnostic"),
        )
        for terminal, malformed in permutations(("errors", "warnings", "result"), 2):
            for bad in (None, {"unsupported": "metadata"}, [None]):
                for message, expected in diagnostics:
                    with self.subTest(terminal=terminal, malformed=malformed, bad=bad,
                                      diagnostic=expected):
                        envelope = {"type": "result", "subtype": "error_during_execution",
                                    "is_error": True, terminal: [message], malformed: bad}
                        output, error, valid = self.decode(json.dumps(envelope), exit_code=1)
                        self.assertEqual(output, "")
                        self.assertFalse(valid)
                        self.assertEqual(role_review.diagnostic_failure(error), expected)

    def test_terminal_string_inside_a_mixed_diagnostic_list_remains_terminal(self):
        for values in ([None, "quota exceeded"], ["quota exceeded", {}]):
            output, error, valid = self.decode(json.dumps({
                "type": "result", "subtype": "error_during_execution",
                "is_error": True, "errors": values,
            }), exit_code=1)
            self.assertEqual(output, "")
            self.assertFalse(valid)
            self.assertEqual(role_review.diagnostic_failure(error), "quota_diagnostic")

    def test_inner_review_can_discuss_errors_without_becoming_a_transport_diagnostic(self):
        self.response["checks"][0]["evidence"] = "quota exceeded\nFalling back to another model"
        output, error, valid = self.decode(json.dumps(self.envelope))
        self.assertTrue(valid, error)
        self.assertEqual(json.loads(output), self.response)

    def test_successful_human_result_is_not_a_provider_diagnostic(self):
        for text in ("quota exceeded", "Falling back to another model",
                     "[warn] failed to set model: Method not found"):
            with self.subTest(text=text):
                envelope = dict(self.envelope, result=text)
                output, error, valid = self.decode(json.dumps(envelope))
                self.assertTrue(valid, error)
                self.assertEqual(json.loads(output), self.response)

    def test_result_text_is_diagnostic_for_nonzero_or_failed_envelopes(self):
        for exit_code in (0, 1):
            envelope = dict(self.envelope, result="quota exceeded")
            if exit_code == 0:
                envelope.update(is_error=True, subtype="error_during_execution")
            output, error, valid = self.decode(json.dumps(envelope), exit_code=exit_code)
            self.assertEqual(output, "")
            self.assertFalse(valid)
            self.assertEqual(role_review.diagnostic_failure(error), "quota_diagnostic")

    def test_unicode_separators_survive_the_existing_raw_and_json_handoff(self):
        evidence = "한국어 첫 줄" + chr(0x2028) + "둘째 줄" + chr(0x2029) + "끝"
        self.response["checks"][0]["evidence"] = evidence
        output, error, valid = self.decode(json.dumps(self.envelope))
        self.assertTrue(valid, error)
        # Exercise the actual downstream handoff, including splitlines in parse_response.
        decoded = role_review.parse_response(run_role.scrub(output))
        self.assertEqual(decoded, self.response)
        self.assertIn("한국어", output)

    def test_c1_controls_in_separate_findings_cannot_erase_a_major(self):
        self.response["findings"] = [
            {"severity": "MAJOR", "path": "backend/example.py",
             "condition": "Blocking regression", "evidence": "중요 근거" + chr(0x9D)},
            {"severity": "INFO", "path": "backend/example.py",
             "condition": "Context only", "evidence": chr(0x9C) + "추가 근거"},
        ]
        output, error, valid = self.decode(json.dumps(self.envelope))
        self.assertTrue(valid, error)
        decoded = role_review.parse_response(run_role.scrub(output))
        role_review.validate_response(decoded, {
            "head_sha": "a" * 40, "roles": {
                "claude-self": {"role": "requirements", "paths": ["backend/example.py"]},
            },
        }, "claude-self")
        self.assertEqual([finding["severity"] for finding in decoded["findings"]],
                         ["MAJOR", "INFO"])
        self.assertEqual(decoded, self.response)

    def test_controls_in_individual_strings_stay_escaped_until_json_parsing(self):
        controls = [*range(0x20), *range(0x7F, 0xA0), 0x2028, 0x2029]
        self.response["checks"] = [
            {"path": "backend/example.py", "evidence": "한국어" + chr(code) + "끝"}
            for code in controls
        ]
        output, error, valid = self.decode(json.dumps(self.envelope))
        self.assertTrue(valid, error)
        self.assertIn("한국어", output)
        for code in controls:
            self.assertNotIn(chr(code), output)
        decoded = role_review.parse_response(run_role.scrub(output))
        self.assertEqual(decoded, self.response)

    def test_separator_escaping_keeps_the_final_output_byte_limit(self):
        for code in (0x2028, 0x9D):
            self.response["checks"][0]["evidence"] = chr(code) * 200000
            output, error, valid = self.decode(json.dumps(self.envelope, ensure_ascii=False))
            self.assertEqual((output, error, valid), ("", "output_byte_limit", False))

    def test_inner_head_paths_and_coverage_still_require_existing_validation(self):
        plan = {"head_sha": "a" * 40, "roles": {
            "claude-self": {"role": "requirements", "paths": ["backend/example.py"]},
        }}
        for change, code in (
            ({"head_sha": "b" * 40}, "response_identity"),
            ({"role": "implementation"}, "response_identity"),
            ({"reviewed_paths": ["foreign.py"]}, "reviewed_paths"),
            ({"scope_complete": False}, "scope_incomplete"),
            ({"checks": []}, "checks_missing"),
        ):
            with self.subTest(change=change):
                envelope = dict(self.envelope, structured_output=dict(self.response, **change))
                output, error, valid = self.decode(json.dumps(envelope))
                self.assertTrue(valid, error)
                with self.assertRaisesRegex(role_review.Invalid, "^" + code + "$"):
                    role_review.validate_response(json.loads(output), plan, "claude-self")


if __name__ == "__main__":
    unittest.main()
