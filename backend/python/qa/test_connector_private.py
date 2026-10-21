import base64
import hashlib
import json
import unittest
from dataclasses import replace
from unittest import mock

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

from connector_contract import ConnectorError, Grants
from connector_private import PrivateQAConsumer


def encode(raw):
    return base64.urlsafe_b64encode(raw).decode().rstrip('=')


class PrivateQATests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        cls.key_id = 'arn:aws:kms:ap-northeast-2:111111111111:key/00000000-0000-0000-0000-000000000000'
        cls.spki = cls.key.public_key().public_bytes(
            serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)

    def setUp(self):
        self.now = 1800000000
        self.grants = Grants('verified-user', 'client', True, ('read',))
        self.resolve = mock.Mock(side_effect=lambda u, c: self.grants)
        self.kms = mock.Mock()
        self.kms.get_public_key.return_value = {
            'KeyId': self.key_id, 'KeyUsage': 'SIGN_VERIFY', 'PublicKey': self.spki}
        self.consumer = PrivateQAConsumer(self.key_id, self.kms, self.resolve, lambda: self.now)
        self.execute = mock.Mock(return_value={'statusCode': 200, 'body': '{"answer":"synthetic"}'})

    def frame(self, raw=None, request=None):
        args = raw if raw is not None else json.dumps({
            'method': 'POST', 'path': '/api/qa/readonly-ask',
            'body': request or {'question': '현재 메모 요약', 'meetingId': 'meeting'},
        }, ensure_ascii=False, separators=(',', ':')).encode()
        payload = json.dumps({
            'version': 1, 'protocol': 'ttobak.connector', 'destination': 'ttobak-qa-bridge',
            'sub': 'verified-user', 'clientId': 'client', 'capabilities': ['read'],
            'approvalGeneration': 0, 'operation': 'qa.readonlyAsk',
            'argumentsSHA256': hashlib.sha256(args).hexdigest(), 'requestId': 'request',
            'issuedAt': 1800000000, 'expiresAt': 1800000060, 'upstreamExpiresAt': 1800000600,
        }, separators=(',', ':')).encode()
        signature = self.key.sign(
            payload, padding.PSS(mgf=padding.MGF1(hashes.SHA256()), salt_length=32), hashes.SHA256())
        wire = json.dumps({
            'keyId': self.key_id, 'algorithm': 'RSASSA_PSS_SHA_256',
            'payload': encode(payload), 'signature': encode(signature),
        }, separators=(',', ':')).encode()
        return {'protocol': 'ttobak.connector.invoke.v1', 'operation': 'qa.readonlyAsk',
                'requestId': 'request', 'delegation': encode(wire), 'arguments': encode(args)}

    def test_signed_request_uses_verified_user_and_current_grants(self):
        for _ in range(2):
            self.assertEqual(self.consumer.invoke(self.frame(), self.execute)['statusCode'], 200)
        self.execute.assert_called_with({'question': '현재 메모 요약', 'meetingId': 'meeting'}, 'verified-user')
        self.assertEqual(self.kms.get_public_key.call_count, 1)
        self.assertEqual(self.resolve.call_count, 4)

    def test_revocation_and_expiry_during_execution_discard_result(self):
        for change in ('revoked', 'expired'):
            with self.subTest(change=change):
                self.setUp()
                def execute(*_):
                    if change == 'revoked':
                        self.grants = replace(self.grants, client_enabled=False)
                    else:
                        self.now += 60
                    return {'body': 'must never be released'}
                with self.assertRaises(ConnectorError):
                    self.consumer.invoke(self.frame(), execute)

    def test_request_substitution_and_ambiguous_input_never_execute(self):
        frame = self.frame()
        frame['arguments'] = encode(json.dumps({
            'method': 'POST', 'path': '/api/qa/readonly-ask',
            'body': {'question': 'different source', 'meetingId': 'other'},
        }, separators=(',', ':')).encode())
        with self.assertRaises(ConnectorError):
            self.consumer.invoke(frame, self.execute)
        for raw in (
            b'{"method":"POST","path":"/api/qa/readonly-ask","body":{"question":"x","question":"y"}}',
            b'{"method":"POST","path":"/api/qa/readonly-ask","body":{"question":"\\ud800"}}',
            b'{"method":"POST","path":"/api/qa/ask","body":{"question":"x"}}',
            b'{"method":"POST","path":"/api/qa/readonly-ask","body":{"question":"x","userId":"forged"}}',
            b'[' * 1100 + b'0' + b']' * 1100,
            b'x' * 32_001,
        ):
            with self.subTest(raw_length=len(raw)), self.assertRaises(ConnectorError):
                self.consumer.invoke(self.frame(raw), self.execute)
        self.execute.assert_not_called()

    def test_failed_or_foreign_key_is_never_cached(self):
        self.kms.get_public_key.return_value['KeyId'] = 'foreign'
        for _ in range(2):
            with self.assertRaises(ConnectorError) as caught:
                self.consumer.invoke(self.frame(), self.execute)
            self.assertEqual(caught.exception.code, 'POLICY_UNAVAILABLE')
        self.assertEqual(self.kms.get_public_key.call_count, 2)
        self.execute.assert_not_called()

    def test_invalid_or_oversized_results_are_closed(self):
        for value, code in (
            ({'body': '\ud800'}, 'INVALID_CONNECTOR_RESULT'),
            ({'body': 'x' * 14_000}, 'RESULT_TOO_LARGE'),
            ({'body': float('nan')}, 'INVALID_CONNECTOR_RESULT'),
        ):
            self.execute.return_value = value
            with self.subTest(code=code), self.assertRaises(ConnectorError) as caught:
                self.consumer.invoke(self.frame(), self.execute)
            self.assertEqual(caught.exception.code, code)


if __name__ == '__main__':
    unittest.main()
