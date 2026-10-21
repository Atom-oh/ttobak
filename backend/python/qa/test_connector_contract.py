import hashlib
import json
import unittest
from pathlib import Path
from connector_contract import (
    Approval, ConnectorError, Grants, effective_capabilities, required_capabilities,
)
from connector_operations import CONTRACT_SHA256


class ContractTests(unittest.TestCase):
    def test_inventory_parity_and_explicit_capabilities(self):
        raw = (Path(__file__).resolve().parents[3] / "contracts/connector/v1/operations.json").read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), CONTRACT_SHA256)
        for op, destination, caps, allowed, _, _ in json.loads(raw)["operations"]:
            if allowed:
                self.assertEqual(required_capabilities(op, destination), tuple(caps))
            else:
                with self.assertRaises(ConnectorError):
                    required_capabilities(op, destination)
        with self.assertRaises(ConnectorError):
            required_capabilities("api.unknown", "ttobak-api-bridge")
        self.assertEqual(required_capabilities("api.share.revokeShare", "ttobak-api-bridge"), ("read", "share"))
        self.assertEqual(required_capabilities("qa.detectQuestions", "ttobak-qa-bridge"), ("read",))

    def test_current_approval_and_client_intersection(self):
        caps = ("delete", "read", "share", "write")
        payload = {"sub": "user", "clientId": "client", "capabilities": ["read", "write"],
                   "approvalGeneration": 7, "expiresAt": 150}
        approval = Approval("user", "client", 7, caps, 200)
        valid = Grants("user", "client", True, caps, approval)
        self.assertEqual(effective_capabilities(payload, valid, 100), frozenset(("read", "write")))
        for invalid in [
            Grants("user", "client", True, ("read",), approval),
            Grants("user", "client", False, caps, approval),
            Grants("user", "client", True, caps),
            Grants("user", "client", True, caps, Approval("user", "client", 7, caps, 200, True)),
            Grants("user", "client", True, caps, Approval("other", "client", 7, caps, 200)),
            Grants("user", "client", True, caps, Approval("user", "client", 8, caps, 200)),
            Grants("user", "client", True, caps, Approval("user", "client", 7, caps, 149)),
            Grants("user", "client", True, caps, Approval("user", "client", 7, caps, 9007199254740992)),
        ]:
            with self.assertRaises(ConnectorError):
                effective_capabilities(payload, invalid, 100)
        payload.update(capabilities=["read"], approvalGeneration=0)
        self.assertEqual(effective_capabilities(payload, Grants("user", "client", True, ("read",)), 100), frozenset(("read",)))
