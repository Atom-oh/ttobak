"""Claude's CLI envelope is transport metadata, never a replacement review."""
import json
from itertools import permutations
import unittest

import role_review
import run_role


class ClaudeTransportTests(unittest.TestCase):
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

    def test_separator_escaping_keeps_the_final_output_byte_limit(self):
        self.response["checks"][0]["evidence"] = chr(0x2028) * 200000
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
