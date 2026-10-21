from decimal import Decimal
import unittest
from unittest import mock

import boto3
from moto import mock_aws

from connector_contract import ConnectorError
from connector_grants import current_grants

AUDIENCE = "https://example.invalid/api/mcp"
ISSUER = "https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_test"


@mock_aws
class CurrentGrantsTests(unittest.TestCase):
    def setUp(self):
        # The handler suite patches module-level boto3 constructors for its import.
        # A Session keeps this SDK integration fixture independent of test order.
        self.table = boto3.Session(region_name="us-east-1").resource("dynamodb").create_table(
            TableName="test-grants", BillingMode="PAY_PER_REQUEST",
            KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"}, {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": k, "AttributeType": "S"} for k in ("PK", "SK")])
        self.policy = {"PK": "CONNECTOR_CLIENT#client", "SK": "POLICY", "clientId": "client",
                       "enabled": True, "maximum": {"read", "write"}, "audience": AUDIENCE,
                       "issuer": ISSUER, "generation": 2}
        self.approval = {"PK": "USER#user", "SK": "CONNECTOR_APPROVAL#client", "userId": "user",
                         "clientId": "client", "capabilities": {"read", "write"}, "generation": 3,
                         "expiresAt": 2000000000, "revoked": False, "blocked": False}
        self.table.put_item(Item=self.policy)
        self.table.put_item(Item=self.approval)

    def read(self):
        return current_grants(self.table, "user", "client", AUDIENCE, ISSUER)

    def test_atomic_resource_sdk_read_and_immediate_disable_block(self):
        real = self.table.meta.client.transact_get_items
        with mock.patch.object(self.table.meta.client, "transact_get_items", wraps=real) as transaction:
            grant = self.read()
        self.assertEqual(transaction.call_count, 1)
        self.assertEqual(len(transaction.call_args.kwargs["TransactItems"]), 2)
        self.assertEqual(grant.approval.generation, 3)
        self.assertEqual(grant.client_maximum, ("read", "write"))
        self.table.put_item(Item=dict(self.policy, enabled=False))
        with self.assertRaisesRegex(ConnectorError, "INVALID_GRANT"):
            self.read()
        self.table.put_item(Item=self.policy)
        self.table.put_item(Item=dict(self.approval, blocked=True, expiresAt=1))
        self.assertFalse(self.read().client_enabled)

    def test_missing_approval_is_baseline_but_partial_row_is_invalid(self):
        key = {k: self.approval[k] for k in ("PK", "SK")}
        self.table.delete_item(Key=key)
        self.assertIsNone(self.read().approval)
        self.table.put_item(Item=key)
        with self.assertRaisesRegex(ConnectorError, "INVALID_GRANT"):
            self.read()

    def test_foreign_and_malformed_rows_never_create_grants(self):
        cases = (("policy", "enabled", "true"), ("policy", "maximum", ["read"]),
                 ("policy", "maximum", {"write"}), ("policy", "audience", "https://foreign.invalid/api/mcp"),
                 ("policy", "generation", 0), ("approval", "userId", "other"),
                 ("approval", "blocked", None), ("approval", "generation", 9007199254740992),
                 ("approval", "expiresAt", Decimal("1.5")), ("approval", "capabilities", {"read", "admin"}))
        for kind, field, value in cases:
            with self.subTest(kind=kind, field=field):
                original = self.policy if kind == "policy" else self.approval
                self.table.put_item(Item=dict(original, **{field: value}))
                with self.assertRaisesRegex(ConnectorError, "INVALID_GRANT"):
                    self.read()
                self.table.put_item(Item=original)

    def test_failed_lookup_has_closed_diagnostic(self):
        with mock.patch.object(self.table.meta.client, "transact_get_items", side_effect=RuntimeError("private context")):
            with self.assertRaisesRegex(ConnectorError, "^POLICY_UNAVAILABLE$"):
                self.read()


if __name__ == "__main__":
    unittest.main()
