import copy
import unittest
from unittest import mock

from connector_contract import ConnectorError, Grants
from connector_ingress import ReadOnlyQAIngress, READ_SCOPE, validate_detection_body, MAX_REQUEST_BYTES


class ConnectorIngressTests(unittest.TestCase):
    def setUp(self):
        self.policy = ReadOnlyQAIngress("readclient", "https://example.invalid/api/mcp", ("webclient",))
        self.event = {
            "rawPath": "/api/qa/detect-questions",
            "requestContext": {
                "http": {"method": "POST"},
                "authorizer": {"jwt": {"claims": {
                    "sub": "verified-owner", "client_id": "readclient", "token_use": "access",
                    "aud": "https://example.invalid/api/mcp", "scope": READ_SCOPE, "exp": "200",
                }}},
            },
            "headers": {"authorization": "Bearer caller-controlled-synthetic-data"},
        }

    def test_verified_context_owns_identity_and_read_only_detection(self):
        self.assertEqual(self.policy.authorize_http(self.event, now=100), "verified-owner")
        self.event["headers"]["authorization"] = "Bearer forged-header-owner"
        self.assertEqual(self.policy.authorize_http(self.event, now=100), "verified-owner")

    def test_direct_qa_mutations_and_id_token_alternative_reject(self):
        for path in ("/api/qa/ask", "/api/qa/jobs", "/api/qa/meeting/m", "/api/qa/jobs/j"):
            event = copy.deepcopy(self.event)
            event["rawPath"] = path
            with self.assertRaisesRegex(ConnectorError, "^CAPABILITY_DENIED$"):
                self.policy.authorize_http(event, now=100)
        claims = self.event["requestContext"]["authorizer"]["jwt"]["claims"]
        claims.update(token_use="id", aud="readclient")
        claims.pop("client_id")
        with self.assertRaisesRegex(ConnectorError, "^CAPABILITY_DENIED$"):
            self.policy.authorize_http(self.event, now=100)

    def test_identity_only_scopes_wrong_resource_and_expiry_reject(self):
        for field, value in (
            ("scope", "openid email profile"), ("scope", READ_SCOPE + " ttobak.connector/write"),
            ("aud", "https://other.invalid/api/mcp"), ("exp", "100"),
        ):
            event = copy.deepcopy(self.event)
            event["requestContext"]["authorizer"]["jwt"]["claims"][field] = value
            with self.assertRaises(ConnectorError):
                self.policy.authorize_http(event, now=100)

    def test_missing_trusted_authorizer_cannot_fall_back_to_decoded_headers(self):
        del self.event["requestContext"]["authorizer"]
        with self.assertRaisesRegex(ConnectorError, "^INVALID_CONNECTOR_IDENTITY$"):
            self.policy.authorize_http(self.event, now=100)
        self.assertIsNone(ReadOnlyQAIngress().authorize_http(self.event, now=100))

    def test_web_identity_remains_the_verified_authorizer_user(self):
        claims = self.event["requestContext"]["authorizer"]["jwt"]["claims"]
        claims.update(token_use="id", aud="webclient")
        claims.pop("client_id")
        claims["scope"] = "openid email profile"
        self.event["rawPath"] = "/api/qa/ask"
        self.assertEqual(self.policy.authorize_http(self.event, now=100), "verified-owner")

    def test_unknown_clients_do_not_become_full_privilege_web_identities(self):
        claims = self.event["requestContext"]["authorizer"]["jwt"]["claims"]
        claims.update(client_id="otherclient", scope="openid email profile")
        with self.assertRaisesRegex(ConnectorError, "^CAPABILITY_DENIED$"):
            self.policy.authorize_http(self.event, now=100)

    def test_request_limit_counts_utf8_and_rejects_malformed_detection_dtos(self):
        self.event["body"] = "한" * (MAX_REQUEST_BYTES // 3 + 1)
        with self.assertRaisesRegex(ConnectorError, "^INVALID_CONNECTOR_REQUEST$"):
            self.policy.authorize_http(self.event, now=100)
        for body in ([], {"transcript": "text", "persist": True},
                     {"transcript": "text", "previousQuestions": [{}]}, {"transcript": "\ud800"}):
            with self.assertRaisesRegex(ConnectorError, "^INVALID_CONNECTOR_REQUEST$"):
                validate_detection_body(body)

    def test_quarantine_and_invalid_partial_configuration(self):
        self.assertIsNone(ReadOnlyQAIngress().authorize_http({}, now=100))
        for client, audience in (
            ("readclient", ""), ("", "https://example.invalid/api/mcp"),
            ("readclient", "http://example.invalid/api/mcp"),
        ):
            with self.assertRaisesRegex(ValueError, "^INVALID_CONNECTOR_CONFIG$"):
                ReadOnlyQAIngress(client, audience)

    def test_current_grants_are_required_before_and_after_detection(self):
        claims = self.event["requestContext"]["authorizer"]["jwt"]["claims"]
        claims["exp"] = "9007199254740991"
        self.event["body"] = '{"transcript":"synthetic"}'
        good = Grants("verified-owner", "readclient", True, ("read",))
        disabled = Grants("verified-owner", "readclient", False, ("read",))
        execute = mock.Mock(return_value={"statusCode": 200, "body": '{"questions":[]}'})
        with self.assertRaisesRegex(ConnectorError, "INVALID_GRANT"):
            self.policy.detection(self.event, execute, None)
        execute.assert_not_called()
        with self.assertRaisesRegex(ConnectorError, "INVALID_GRANT"):
            self.policy.detection(self.event, execute, mock.Mock(side_effect=[good, disabled]))
        self.assertEqual(execute.call_count, 1)
        self.assertEqual(self.policy.detection(self.event, execute, lambda *_: good)["statusCode"], 200)

    def test_duplicate_json_and_strict_base64_do_not_reach_detection(self):
        self.event["requestContext"]["authorizer"]["jwt"]["claims"]["exp"] = "9007199254740991"
        execute = mock.Mock()
        resolve = lambda user, client: Grants(user, client, True, ("read",))
        for body, encoded in (('{"transcript":"a","transcript":"b"}', False), ("!not-base64!", True)):
            self.event.update(body=body, isBase64Encoded=encoded)
            with self.assertRaisesRegex(ConnectorError, "INVALID_CONNECTOR_REQUEST"):
                self.policy.detection(self.event, execute, resolve)
        execute.assert_not_called()

    def test_actual_http_dispatch_rejects_before_any_ask_or_job_write(self):
        # test_handler supplies the existing synthetic boto3 import boundary.
        import test_handler
        handler = test_handler.handler
        claims = self.event["requestContext"]["authorizer"]["jwt"]["claims"]
        claims["exp"] = "9007199254740991"
        self.event["body"] = '{"question":"synthetic"}'
        with mock.patch.object(handler, "_CONNECTOR_INGRESS", self.policy), \
                mock.patch.object(handler, "handle_ask") as ask, \
                mock.patch.object(handler, "_handle_job_http") as job:
            for path in ("/api/qa/ask", "/api/qa/jobs"):
                self.event["rawPath"] = path
                self.assertEqual(handler._handle_verified_http(self.event, None)["statusCode"], 403)
            ask.assert_not_called()
            job.assert_not_called()
        self.event["rawPath"] = "/api/qa/detect-questions"
        self.event["body"] = '{"transcript":"synthetic"}'
        with mock.patch.object(handler, "_CONNECTOR_INGRESS", self.policy), \
                mock.patch.object(handler, "_CONNECTOR_GRANTS", lambda user, client: Grants(user, client, True, ("read",))), \
                mock.patch.object(handler, "handle_detect_questions", return_value=handler.response(200, {
                    "questions": ["한" * 5000],
                })):
            result = handler._handle_verified_http(self.event, None)
            self.assertEqual(result["statusCode"], 413)
            self.assertNotIn("한", result["body"])

    def test_deep_bounded_json_returns_400_before_detection(self):
        import test_handler
        handler = test_handler.handler
        self.event["requestContext"]["authorizer"]["jwt"]["claims"]["exp"] = "9007199254740991"
        self.event["body"] = "[" * 15000 + "0" + "]" * 15000
        self.assertLess(len(self.event["body"]), MAX_REQUEST_BYTES)
        with mock.patch.object(handler, "_CONNECTOR_INGRESS", self.policy), \
                mock.patch.object(handler, "_CONNECTOR_GRANTS",
                                  lambda user, client: Grants(user, client, True, ("read",))), \
                mock.patch.object(handler, "handle_detect_questions") as detect:
            result = handler._handle_verified_http(self.event, None)
        self.assertEqual(result["statusCode"], 400)
        self.assertIn("INVALID_CONNECTOR_REQUEST", result["body"])
        detect.assert_not_called()

    def test_actual_model_surrogate_response_returns_closed_502(self):
        import test_handler
        handler = test_handler.handler
        self.event["requestContext"]["authorizer"]["jwt"]["claims"]["exp"] = "9007199254740991"
        self.event["body"] = '{"transcript":"synthetic"}'
        model_output = {"output": {"message": {"content": [{"text": '["\\ud800"]'}]}}}
        with mock.patch.object(handler, "_CONNECTOR_INGRESS", self.policy), \
                mock.patch.object(handler, "_CONNECTOR_GRANTS",
                                  lambda user, client: Grants(user, client, True, ("read",))), \
                mock.patch.object(handler.bedrock_runtime, "converse", return_value=model_output):
            result = handler._handle_verified_http(self.event, None)
        self.assertEqual(result["statusCode"], 502)
        self.assertIn("INVALID_CONNECTOR_RESULT", result["body"])
        result["body"].encode("utf-8", errors="strict")


if __name__ == "__main__":
    unittest.main()
