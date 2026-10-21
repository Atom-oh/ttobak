"""Storage contracts; optional Moto cases exercise real DynamoDB expressions.

Run all cases with ``pip install 'moto[dynamodb]>=5,<6'`` in an isolated test
environment. Moto checks atomic state behavior, not AWS IAM or live deployment.
The dependency-free cases remain part of the established crawler CI suite.
"""

import copy
from dataclasses import replace
from datetime import datetime, timezone
from decimal import Decimal
import unittest
from unittest import mock

import boto3
from botocore.exceptions import EndpointConnectionError

from news_discovery import source_revision
from news_store import Attempt, LEASE_SECONDS, NewsStore, StateRejected

try:
    from moto import mock_aws
except ImportError:
    mock_aws = None

NOW = int(datetime(2026, 10, 21, tzinfo=timezone.utc).timestamp())
CONFIG = {"PK": "CRAWLER#source", "SK": "CONFIG", "sourceId": "source",
          "sourceName": "Synthetic source", "status": "active", "ownerId": "owner"}


def attempt(**changes):
    initial = Attempt("source", "a" * 16, "run", "b" * 32, "2026-10",
                      source_revision(CONFIG), Decimal("1"), NOW + LEASE_SECONDS)
    return replace(initial, **changes)


def document(job):
    return {
        "PK": job.pk, "SK": "DOC#" + job.doc_hash, "docHash": job.doc_hash,
        "sourceId": job.source_id, "schemaVersion": 1, "qualityStatus": "PUBLISHED",
        "attemptToken": job.token, "sourceRevision": job.revision,
        "s3Key": job.markdown_key, "artifactKey": job.artifact_key,
        "markdownSha256": "c" * 64, "artifactSha256": "d" * 64,
        "title": "Synthetic accepted article", "qualityScore": 0.9,
    }


class TestNewsStoreBoundaries(unittest.TestCase):
    def test_identity_and_budget_reject_path_injection_and_ambiguous_numbers(self):
        for changes in ({"source_id": "../source"}, {"source_id": "__tech__"},
                        {"token": "not-immutable"}, {"doc_hash": "A" * 16},
                        {"revision": ""}, {"month": "2026-99"},
                        {"reserved": 0}, {"reserved": 0.1}, {"reserved": True},
                        {"reserved": "NaN"}, {"reserved": "-1"},
                        {"reserved": "1e-100"}, {"reserved": "100001"}):
            with self.subTest(changes=changes), self.assertRaises(StateRejected):
                attempt(**changes)

    def test_immutable_object_paths_differ_for_every_attempt(self):
        first, second = attempt(), attempt(token="e" * 32)
        self.assertNotEqual(first.markdown_key, second.markdown_key)
        self.assertNotEqual(first.artifact_key, second.artifact_key)
        self.assertFalse(first.markdown_key.startswith("shared/"))

    def test_incomplete_published_batch_fails_closed_and_reads_are_strong(self):
        client = mock.Mock()
        store = NewsStore(client, "table")
        for response in ({}, {"Responses": {}},
                         {"Responses": {"table": []}, "UnprocessedKeys": {"table": {"Keys": [{}]}}},
                         {"Responses": {"table": [{"PK": "CRAWLER#other", "SK": "DOC#" + "a" * 16}]}}):
            client.batch_get_item.return_value = response
            with self.assertRaisesRegex(StateRejected, "STATE_UNAVAILABLE"):
                store.published_hashes("source", {"a" * 16})
        client.batch_get_item.return_value = {"Responses": {"table": []}}
        self.assertEqual(store.published_hashes("source", {"a" * 16}), set())
        client.batch_get_item.return_value = {"Responses": {"table": [{
            "PK": "CRAWLER#source", "SK": "DOC#" + "a" * 16,
            "schemaVersion": True, "qualityStatus": "PUBLISHED"}]}}
        self.assertEqual(store.published_hashes("source", {"a" * 16}), set())
        self.assertTrue(client.batch_get_item.call_args.kwargs[
            "RequestItems"]["table"]["ConsistentRead"])

    def test_deleted_disabled_or_changed_config_prevents_any_transaction(self):
        client = mock.Mock()
        store, job = NewsStore(client, "table"), attempt()
        for row in (None, {**CONFIG, "status": "disabled"},
                    {**CONFIG, "aliases": ["changed"]}, {**CONFIG, "ownerId": "new"}):
            client.get_item.return_value = {"Item": row}
            with self.assertRaisesRegex(StateRejected, "SOURCE_CHANGED"):
                store.reserve(job, now=NOW, month_limit="10", run_limit="5")
        client.transact_write_items.assert_not_called()

    def test_ambiguous_reservation_does_not_release_or_retry_with_new_token(self):
        client = mock.Mock()
        client.get_item.return_value = {"Item": CONFIG}
        error = EndpointConnectionError(endpoint_url="https://synthetic.invalid")
        client.transact_write_items.side_effect = error
        store = NewsStore(client, "table")
        with self.assertRaises(EndpointConnectionError):
            store.reserve(attempt(), now=NOW, month_limit="10", run_limit="5")
        client.transact_write_items.assert_called_once()
        client.delete_item.assert_not_called()

    def test_invalid_completion_never_queries_or_writes_state(self):
        client = mock.Mock()
        store, job = NewsStore(client, "table"), attempt()
        good = dict(now=NOW + 1, status="PUBLISHED", known_cost="0.1",
                    usage_complete=True, usage={}, document=document(job))
        for change in ({"now": NOW - 1}, {"usage_complete": 1},
                       {"usage": {"inputTokens": -1}}, {"usage": {"inputTokens": True}},
                       {"document": {**document(job), "s3Key": "shared/forged.md"}},
                       {"document": {**document(job), "schemaVersion": True}},
                       {"document": {**document(job), "title": "a" * 128000}},
                       {"document": {**document(job), "qualityScore": float("nan")}}):
            with self.subTest(change=list(change)), self.assertRaises(StateRejected):
                store.finish(job, **{**good, **change})
        client.get_item.assert_not_called()
        client.transact_write_items.assert_not_called()


@unittest.skipUnless(mock_aws, "Install moto[dynamodb] for expression/transaction integration cases")
class TestNewsStoreTransactions(unittest.TestCase):
    def setUp(self):
        context = mock_aws()
        context.start()
        self.addCleanup(context.stop)
        # The legacy crawler suite patches boto3.resource globally at import.
        # A separate Session still uses Moto's transport interception.
        resource = boto3.Session().resource("dynamodb", region_name="ap-northeast-2")
        self.table = resource.create_table(
            TableName="synthetic-news-store",
            KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"},
                       {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": "PK", "AttributeType": "S"},
                                  {"AttributeName": "SK", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST")
        self.table.put_item(Item=copy.deepcopy(CONFIG))
        self.store = NewsStore(resource.meta.client, self.table.name)
        self.job = attempt()

    def reserve(self, job=None, **changes):
        self.store.reserve(job or self.job, **{
            "now": NOW, "month_limit": "10", "run_limit": "5", **changes})

    def finish(self, job=None, **changes):
        return self.store.finish(job or self.job, **{
            "now": NOW + 1, "status": "REJECTED", "known_cost": "0.1",
            "usage_complete": True, "usage": {"inputTokens": 100, "outputTokens": 10},
            "reason": "QUALITY_REJECTED", **changes})

    def row(self, sk):
        return self.table.get_item(Key={"PK": self.job.pk, "SK": sk},
                                   ConsistentRead=True).get("Item")

    def charge(self, sk="NEWSBUDGET#2026-10"):
        return self.row(sk)["chargedUsd"]

    def test_duplicate_invocations_of_same_run_cannot_share_a_claim(self):
        self.reserve()
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.reserve(attempt(token="e" * 32))
        self.assertEqual(self.charge(), Decimal("1"))
        self.assertEqual(self.row("NEWSCLAIM#" + self.job.doc_hash)["attemptToken"], self.job.token)

    def test_simultaneous_budget_reservations_cannot_cross_month_or_run_limit(self):
        self.reserve(month_limit="1")
        for options in ({"month_limit": "1"}, {"run_limit": "1"}):
            next_job = attempt(doc_hash="e" * 16, token="e" * 32)
            with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.reserve(next_job, **options)
            self.assertIsNone(self.row(next_job.sk))
        self.assertEqual(self.charge(), Decimal("1"))

    def test_config_change_after_read_rolls_back_claim_and_budget(self):
        original = self.store._transaction

        def changed(operations):
            self.table.update_item(Key={"PK": self.job.pk, "SK": "CONFIG"},
                                   UpdateExpression="SET aliases = :aliases",
                                   ExpressionAttributeValues={":aliases": ["new"]})
            original(operations)

        with mock.patch.object(self.store, "_transaction", side_effect=changed):
            with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.reserve()
        self.assertIsNone(self.row(self.job.sk))
        self.assertIsNone(self.row("NEWSBUDGET#2026-10"))

    def test_rejected_usage_is_settled_once_and_retry_returns_same_receipt(self):
        self.reserve()
        first, second = self.finish(), self.finish()
        self.assertEqual(first, second)
        self.assertEqual(self.charge(), Decimal("0.1"))
        self.assertEqual(self.charge("NEWSRUN#run"), Decimal("0.1"))
        expired_retry = self.finish(now=NOW + LEASE_SECONDS + 1)
        self.assertEqual(first, expired_retry)
        self.assertEqual(self.charge(), Decimal("0.1"))
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.finish(known_cost="0.2")
        self.assertEqual(self.charge(), Decimal("0.1"))

    def test_unknown_model_usage_retains_full_reservation(self):
        self.reserve()
        self.finish(status="FAILED", usage_complete=False, reason="MODEL_ERROR")
        self.assertEqual(self.charge(), Decimal("1"))
        self.assertEqual(self.row("NEWSBUDGET#2026-10")["unknownAttempts"], 1)
        self.assertFalse(self.row(self.job.sk)["receipt"]["usageComplete"])

    def test_completion_cannot_refund_another_month_or_extend_the_recorded_lease(self):
        self.reserve()
        self.table.put_item(Item={"PK": self.job.pk, "SK": "NEWSBUDGET#2026-11",
                                  "chargedUsd": Decimal("1")})
        for job in (replace(self.job, month="2026-11"),
                    replace(self.job, lease_until=self.job.lease_until + 100)):
            with self.subTest(month=job.month, lease=job.lease_until):
                with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                    self.finish(job, now=NOW + 101)
        self.assertEqual(self.charge(), 1)
        self.assertEqual(self.charge("NEWSBUDGET#2026-11"), 1)
        self.assertEqual(self.row(self.job.sk)["state"], "RUNNING")

    def test_completion_digest_preserves_number_type_but_normalizes_numeric_scale(self):
        self.reserve()
        first = self.finish(status="PUBLISHED", document=document(self.job), reason="")
        replay = self.finish(status="PUBLISHED", document=document(self.job), reason="",
                             known_cost="0.10")
        self.assertEqual(first, replay)
        changed = {**document(self.job), "qualityScore": "0.9"}
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.finish(status="PUBLISHED", document=changed, reason="")
        self.assertEqual(self.charge(), Decimal("0.1"))

    def test_incomplete_schema_one_document_can_be_replaced_by_valid_publication(self):
        self.table.put_item(Item={"PK": self.job.pk, "SK": "DOC#" + self.job.doc_hash,
                                  "schemaVersion": 1})
        self.assertEqual(self.store.published_hashes(self.job.source_id, {self.job.doc_hash}), set())
        self.reserve()
        self.finish(status="PUBLISHED", document=document(self.job), reason="")
        self.assertEqual(self.row("DOC#" + self.job.doc_hash)["qualityStatus"], "PUBLISHED")

    def test_known_cost_overrun_is_recorded_and_blocks_further_reservations(self):
        self.reserve()
        self.finish(known_cost="1.2")
        self.assertEqual(self.charge(), Decimal("1.2"))
        self.assertTrue(self.row("NEWSBUDGET#2026-10")["blocked"])
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.reserve(attempt(doc_hash="e" * 16, token="e" * 32))

    def test_expired_attempt_cannot_publish_after_lease_takeover(self):
        self.reserve()
        later = NOW + LEASE_SECONDS + 1
        next_job = attempt(token="e" * 32, lease_until=later + LEASE_SECONDS)
        self.reserve(next_job, now=later)
        self.assertEqual(self.charge(), Decimal("2"))
        with self.assertRaisesRegex(StateRejected, "INVALID_COMPLETION"):
            self.finish(now=later, status="PUBLISHED", document=document(self.job))
        self.assertIsNone(self.row("DOC#" + self.job.doc_hash))
        self.assertEqual(self.row("NEWSCLAIM#" + self.job.doc_hash)["attemptToken"], next_job.token)

    def test_publication_and_receipt_share_one_transaction(self):
        self.reserve()
        self.finish(status="PUBLISHED", document=document(self.job), reason="")
        row = self.row("DOC#" + self.job.doc_hash)
        self.assertEqual(row["qualityStatus"], "PUBLISHED")
        self.assertEqual(row["qualityScore"], Decimal("0.9"))
        self.assertEqual(row["s3Key"], self.job.markdown_key)
        self.assertIsNone(self.row("NEWSCLAIM#" + self.job.doc_hash))
        self.assertEqual(self.row(self.job.sk)["receipt"]["docsAdded"], 1)
        self.assertEqual(self.store.published_hashes("source", {self.job.doc_hash}), {self.job.doc_hash})
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.reserve(attempt(token="e" * 32))
        self.assertEqual(self.charge(), Decimal("0.1"))

    def test_source_changed_during_generation_cannot_publish_but_can_settle_cost(self):
        self.reserve()
        self.table.delete_item(Key={"PK": self.job.pk, "SK": "CONFIG"})
        with self.assertRaisesRegex(StateRejected, "SOURCE_CHANGED"):
            self.finish(status="PUBLISHED", document=document(self.job))
        self.assertIsNone(self.row("DOC#" + self.job.doc_hash))
        self.assertEqual(self.charge(), Decimal("1"))
        self.finish(status="FAILED", reason="SOURCE_CHANGED")
        self.assertEqual(self.charge(), Decimal("0.1"))
        self.assertIsNone(self.row("CONFIG"))

    def test_finalized_run_cannot_accept_a_late_new_attempt(self):
        self.table.put_item(Item={"PK": self.job.pk, "SK": "NEWSRUN#run",
                                  "chargedUsd": Decimal("0"), "finalizedAt": NOW})
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.reserve()
        self.assertIsNone(self.row(self.job.sk))
