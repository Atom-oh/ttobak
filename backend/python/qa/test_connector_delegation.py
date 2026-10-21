import base64
import hashlib
import json
import unittest
from dataclasses import FrozenInstanceError, replace
from pathlib import Path
from connector_contract import Approval, ConnectorError, Grants
from connector_delegation import DelegationVerifier, kms_signing_input

FIXTURE = json.loads((Path(__file__).resolve().parents[3] / "contracts/connector/v1/delegation-fixtures.json").read_text())
FULL = ("delete", "read", "share", "write")


def grants(user, client):
    return Grants(user, client, True, FULL, Approval(user, client, 7, FULL, 1800004000))


def verifier(resolve=grants, clock=None):
    return DelegationVerifier(FIXTURE["keyId"], base64.b64decode(FIXTURE["publicKeySPKI"]),
                              "ttobak-api-bridge", resolve, clock or (lambda: FIXTURE["now"]))


def read(v, vector, arguments=None, operation=None, request_id="fixture-request"):
    return v.verify(vector["wire"].encode(), operation or vector["operation"], request_id,
                    FIXTURE["arguments"].encode() if arguments is None else arguments)


class DelegationTests(unittest.TestCase):
    def test_shared_kms_signature_vectors(self):
        for vector in FIXTURE["cases"]:
            with self.subTest(vector=vector["name"]):
                if vector["expected"] == "OK":
                    p = read(verifier(), vector)
                    self.assertEqual(p.capabilities, frozenset(vector["capabilities"]))
                    p.require(vector["operation"], "ttobak-api-bridge", FIXTURE["now"])
                    with self.assertRaises(FrozenInstanceError):
                        p.user_id = "forged"
                    with self.assertRaises(ConnectorError):
                        p.require(vector["operation"], "ttobak-api-bridge", p.expires_at)
                    for invalid_now in (0, -1, True, float("nan"), 9007199254740992):
                        with self.assertRaises(ConnectorError):
                            p.require(vector["operation"], "ttobak-api-bridge", invalid_now)
                else:
                    with self.assertRaises(ConnectorError) as ctx:
                        read(verifier(), vector)
                    self.assertEqual(ctx.exception.code, vector["expected"])
        e = json.loads(FIXTURE["cases"][1]["wire"])
        raw = base64.urlsafe_b64decode(e["payload"] + "=" * (-len(e["payload"]) % 4))
        request = kms_signing_input(FIXTURE["keyId"], raw)
        self.assertEqual(request["MessageType"], "DIGEST")
        self.assertEqual(request["SigningAlgorithm"], "RSASSA_PSS_SHA_256")
        self.assertEqual(request["Message"], hashlib.sha256(raw).digest())

    def test_request_body_and_wire_substitution(self):
        vector = FIXTURE["cases"][1]
        for kwargs in ({"arguments": b"{}"}, {"arguments": b"\xff"},
                       {"arguments": b"x" * (512 * 1024 + 1)},
                       {"operation": "api.meeting.deleteMeeting"}, {"request_id": "different"}):
            with self.assertRaises(ConnectorError):
                read(verifier(), vector, **kwargs)
        for wire in (" " + vector["wire"], vector["wire"].replace('"keyId":', '"keyId":"duplicate","keyId":', 1)):
            with self.assertRaises(ConnectorError) as ctx:
                read(verifier(), dict(vector, wire=wire))
            self.assertEqual(ctx.exception.code, "INVALID_DELEGATION")
        e = json.loads(vector["wire"])
        e["signature"] = "A" * len(e["signature"])
        with self.assertRaises(ConnectorError) as ctx:
            read(verifier(), dict(vector, wire=json.dumps(e, separators=(",", ":"))))
        self.assertEqual(ctx.exception.code, "INVALID_SIGNATURE")

    def test_current_revocation_deadline_and_closed_diagnostics(self):
        vector = FIXTURE["cases"][1]
        with self.assertRaises(ConnectorError) as ctx:
            read(verifier(lambda u, c: replace(grants(u, c), approval=replace(grants(u, c).approval, revoked=True))), vector)
        self.assertEqual(ctx.exception.code, "INVALID_GRANT")
        clock = [FIXTURE["now"]]
        def slow(user, client):
            clock[0] = 1800000050
            return grants(user, client)
        with self.assertRaises(ConnectorError) as ctx:
            read(verifier(slow, lambda: clock[0]), vector)
        self.assertEqual(ctx.exception.code, "EXPIRED_DELEGATION")
        def failed(*_):
            raise RuntimeError("private token fixture")
        with self.assertRaises(ConnectorError) as ctx:
            read(verifier(failed), vector)
        self.assertEqual(str(ctx.exception), "POLICY_UNAVAILABLE")
        self.assertIsNone(ctx.exception.__cause__)
        self.assertIsNone(ctx.exception.__context__)
