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
                                       legacy=PREVIOUS, now=lambda: self.now)

    def test_rotation_expiry_is_checked_even_inside_valid_cache(self):
        self.assertTrue(self.verifier.verify(event()))
        self.assertTrue(self.verifier.verify(event(PREVIOUS)))
        self.client.get_secret_value.assert_called_once_with(SecretId="synthetic-secret-arn")
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

    def test_legacy_is_compatible_but_empty_configuration_requires_explicit_local_mode(self):
        self.assertTrue(OriginVerifier("", None, legacy=PREVIOUS).verify(event(PREVIOUS)))
        self.assertFalse(OriginVerifier("", None).verify(event()))
        self.assertTrue(OriginVerifier("", None, local_development=True).verify({}))
        self.assertFalse(OriginVerifier("arn", None, legacy=PREVIOUS, local_development=True).verify(event(PREVIOUS)))

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


if __name__ == "__main__":
    unittest.main()
