import json
import unittest
from unittest import mock

from origin_proof import OriginVerifier, parse_proofs

CURRENT = "synthetic-current-proof-1234567890"
PREVIOUS = "synthetic-previous-proof-1234567890"


def document(**changes):
    return json.dumps(dict({"schemaVersion": 1, "current": CURRENT, "previous": PREVIOUS,
                           "rotationStartedAt": 1000, "previousExpiresAt": 1030,
                           "generation": "a" * 32, "phase": "rotating"}, **changes))


def event(value=CURRENT):
    return {"headers": {"x-origin-verify": value}}


class OriginProofTests(unittest.TestCase):
    def setUp(self):
        self.now = 1010
        self.client = mock.Mock()
        self.client.get_secret_value.return_value = {"SecretString": document()}
        self.verifier = OriginVerifier("synthetic-secret-arn", self.client,
                                       version_id="b" * 32, now=lambda: self.now)

    def test_rotation_expiry_is_checked_even_inside_valid_cache(self):
        self.assertTrue(self.verifier.verify(event()))
        self.assertTrue(self.verifier.verify(event(PREVIOUS)))
        self.client.get_secret_value.assert_called_once_with(SecretId="synthetic-secret-arn", VersionId="b" * 32)
        self.now = 1030
        self.assertFalse(self.verifier.verify(event(PREVIOUS)))
        self.assertTrue(self.verifier.verify(event()))
        self.client.get_secret_value.assert_called_once()
        self.now = 1071
        self.client.get_secret_value.side_effect = RuntimeError("private provider output")
        self.assertFalse(self.verifier.verify(event()))
        self.assertFalse(self.verifier.verify(event(PREVIOUS)))

    def test_duplicate_conflicting_and_malformed_headers_never_read_secret(self):
        for value in ({}, event("short"), event(CURRENT + "," + CURRENT), event(" " + CURRENT),
                      event("x" * 129), {"headers": {"x-origin-verify": CURRENT, "X-Origin-Verify": CURRENT}},
                      dict(event(), multiValueHeaders={"X-Origin-Verify": [CURRENT, CURRENT]}),
                      dict(event(), multiValueHeaders={"X-Origin-Verify": [PREVIOUS]})):
            self.assertFalse(self.verifier.verify(value))
        self.client.get_secret_value.assert_not_called()

    def test_missing_or_invalid_secret_never_falls_back_to_legacy(self):
        for value in (None, "", "not json", "x" * 2049, "{}", "null"):
            self.client.get_secret_value.return_value = {"SecretString": value}
            self.assertFalse(self.verifier.verify(event(PREVIOUS)))

    def test_schema_and_overlap_bounds(self):
        for change in ({"schemaVersion": 2}, {"schemaVersion": True}, {"generation": "bad"},
                       {"current": "invalid,combined-value"}, {"previous": CURRENT},
                       {"previousExpiresAt": 1000 + 86401}, {"rotationStartedAt": 0},
                       {"previousExpiresAt": 1000}, {"phase": "active"},
                       {"rotationStartedAt": 1.5}):
            self.assertIsNone(parse_proofs(document(**change)), change)
        self.assertIsNotNone(parse_proofs(document(phase="active", previous="", previousExpiresAt=0)))

    def test_arn_and_version_are_required_outside_explicit_local_mode(self):
        with mock.patch.dict("os.environ", {"ORIGIN_VERIFY_SECRET": PREVIOUS}):
            self.assertFalse(OriginVerifier("", None).verify(event(PREVIOUS)))
        self.assertFalse(OriginVerifier("arn", self.client).verify(event()))
        self.assertTrue(OriginVerifier("", None, local_development=True).verify({}))
        self.assertFalse(OriginVerifier("arn", None, local_development=True).verify(event()))

    def test_handler_preserves_internal_transports_and_closes_unconfigured_http(self):
        import test_handler
        handler = test_handler.handler
        with mock.patch.object(handler, "_HTTP_ORIGIN", OriginVerifier("", None)):
            self.assertEqual(handler.lambda_handler({"rawPath": "/api/qa/jobs"}, None)["statusCode"], 403)
            with mock.patch.object(handler, "handle_ask_stream", return_value={"internal": "stream"}) as stream:
                self.assertEqual(handler.lambda_handler({"streamMode": "ask_live"}, None), {"internal": "stream"})
                stream.assert_called_once()
            with mock.patch.object(handler, "_handle_job_event", return_value={"internal": "queue"}) as queue:
                self.assertEqual(handler.lambda_handler({"Records": [{}]}, None), {"internal": "queue"})
                queue.assert_called_once()

    def test_protocol_marker_requires_verified_http_and_preserves_early_errors(self):
        import test_handler
        handler = test_handler.handler
        verifier = OriginVerifier("synthetic-secret-arn", self.client, version_id="b" * 32, now=lambda: self.now)
        request = dict(event(PREVIOUS), rawPath="/api/qa/__origin_probe__",
                       requestContext={"http": {"method": "GET"}}, body="{}")
        with mock.patch.object(handler, "_HTTP_ORIGIN", verifier), \
                mock.patch.object(handler, "handle_ask") as ask, \
                mock.patch.object(handler, "handle_meeting_ask") as meeting:
            result = handler.lambda_handler(request, None)
            self.assertEqual(result["statusCode"], 405)
            self.assertEqual(result["headers"]["x-ttobak-origin-protocol"], "1")
            request["requestContext"]["http"]["method"] = "POST"
            result = handler.lambda_handler(request, None)
            self.assertEqual(result["statusCode"], 401)
            self.assertEqual(result["headers"]["x-ttobak-origin-protocol"], "1")
            request["headers"] = {}
            result = handler.lambda_handler(request, None)
            self.assertEqual(result["statusCode"], 403)
            self.assertNotIn("x-ttobak-origin-protocol", result["headers"])
            ask.assert_not_called()
            meeting.assert_not_called()


    def test_immutable_version_pin_reaches_sdk_and_diagnostics_omit_values(self):
        pin = "b" * 32
        verifier = OriginVerifier("synthetic-secret-arn", self.client, version_id=pin, now=lambda: self.now)
        self.assertTrue(verifier.verify(event(CURRENT)))
        self.client.get_secret_value.assert_called_once_with(SecretId="synthetic-secret-arn", VersionId=pin)
        self.now += 61
        self.client.get_secret_value.side_effect = RuntimeError("private provider output")
        with self.assertLogs("origin_proof", level="WARNING") as messages:
            self.assertFalse(verifier.verify(event(CURRENT)))
        self.assertEqual(messages.records[0].getMessage(), "http_origin_rejected category=secret_unavailable")

    def test_configuration_parse_and_busy_lookup_have_fixed_diagnostics(self):
        for category in ("configuration_invalid", "secret_invalid", "secret_unavailable"):
            verifier = OriginVerifier("synthetic-secret-arn", self.client,
                                      version_id="" if category == "configuration_invalid" else "b" * 32)
            self.client.get_secret_value.return_value = {"SecretString": "private malformed document"}
            if category == "secret_unavailable":
                verifier.lock = mock.Mock()
                verifier.lock.acquire.return_value = False
            with self.assertLogs("origin_proof", level="WARNING") as messages:
                self.assertFalse(verifier.verify(event(CURRENT)))
            self.assertEqual(messages.records[0].getMessage(), "http_origin_rejected category=" + category)


if __name__ == "__main__":
    unittest.main()
