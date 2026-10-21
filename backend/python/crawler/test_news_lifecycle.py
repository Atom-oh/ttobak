"""Run lifecycle transactions; Moto tests supplement required boundary tests."""

import copy
from dataclasses import replace
from decimal import Decimal
import unittest
from unittest import mock

import boto3

from news_lifecycle import ACCEPT_SECONDS, NewsRuns, Run, _aggregate_charge
from news_discovery import source_revision
from news_store import LEASE_SECONDS, NewsStore, StateRejected
from test_news_store import CONFIG, NOW, attempt, document, mock_aws


class TestRunBoundaries(unittest.TestCase):
    def test_invalid_recovery_flag_and_source_identity_never_touch_storage(self):
        store = mock.Mock()
        job = attempt()
        run = Run(job.source_id, job.run_id, job.revision)
        for value in (1, 0, "true", None):
            with self.subTest(value=value), self.assertRaisesRegex(StateRejected, "INVALID_WORKER_RESULTS"):
                NewsRuns(store).finish(run, NOW, recovery=value)
        self.assertEqual(store.mock_calls, [])
        for source_id in ("__tech__", " source", "한/글", "²", "\ud800", "a" * 129):
            with self.subTest(source_id=source_id), self.assertRaisesRegex(StateRejected, "INVALID_IDENTITY"):
                Run(source_id, "run", job.revision)
        with self.assertRaisesRegex(StateRejected, "INVALID_IDENTITY"):
            Run("우리은행", "한글", job.revision)

    def test_aggregate_charge_is_bounded_without_relaxing_attempt_inputs(self):
        self.assertEqual(_aggregate_charge(Decimal("6000000")), Decimal("6000000"))
        for value in (True, 1.0, "120000", Decimal("NaN"), Decimal("Infinity"),
                      Decimal("-1"), Decimal("0.0000000000001"), Decimal("6000001")):
            with self.subTest(value=value), self.assertRaises(StateRejected):
                _aggregate_charge(value)

    def test_uninitialized_run_cannot_reserve(self):
        client = mock.Mock()
        store = NewsStore(client, "table")
        operation = store._budget_reservation(attempt(), "NEWSRUN#run", "5")["Update"]
        self.assertIn("#runState = :open", operation["ConditionExpression"])
        self.assertIn("sourceRevision = :revision", operation["ConditionExpression"])
        self.assertIn("attemptCount < :maximum", operation["ConditionExpression"])

    def test_incomplete_attempt_query_never_finalizes(self):
        client = mock.Mock()
        store, job = NewsStore(client, "table"), attempt()
        client.get_item.return_value = {"Item": {
            "runState": "OPEN", "sourceRevision": job.revision, "startedAt": NOW,
            "attemptCount": 1, "finishedCount": 0, "chargedUsd": Decimal(1), "unknownAttempts": 0}}
        client.query.return_value = {"Items": []}
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            NewsRuns(store).finish(Run(job.source_id, job.run_id, job.revision), NOW + 1)
        self.assertTrue(client.query.call_args.kwargs["ConsistentRead"])
        client.transact_write_items.assert_not_called()

    def test_repeated_pagination_cursor_stops_without_writing(self):
        client = mock.Mock()
        client.query.return_value = {"Items": [], "LastEvaluatedKey": {"PK": "same", "SK": "same"}}
        store, job = NewsStore(client, "table"), attempt()
        with self.assertRaisesRegex(StateRejected, "STATE_UNAVAILABLE"):
            NewsRuns(store)._attempts(Run(job.source_id, job.run_id, job.revision))
        self.assertEqual(client.query.call_count, 2)
        client.transact_write_items.assert_not_called()


@unittest.skipUnless(mock_aws, "Install moto[dynamodb] for lifecycle transaction integration")
class TestRunTransactions(unittest.TestCase):
    def setUp(self):
        context = mock_aws()
        context.start()
        self.addCleanup(context.stop)
        resource = boto3.Session().resource("dynamodb", region_name="ap-northeast-2")
        self.table = resource.create_table(
            TableName="synthetic-news-runs",
            KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"},
                       {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": "PK", "AttributeType": "S"},
                                  {"AttributeName": "SK", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST")
        self.table.put_item(Item=copy.deepcopy(CONFIG))
        self.store = NewsStore(resource.meta.client, self.table.name)
        self.runs = NewsRuns(self.store)
        self.job = attempt()
        self.run = Run(self.job.source_id, self.job.run_id, self.job.revision)
        self.runs.begin(self.run, NOW)

    def reserve(self, job=None, now=NOW):
        self.store.reserve(job or self.job, now=now, month_limit="100", run_limit="100")

    def settle(self, job=None, status="REJECTED", known=True):
        job = job or self.job
        return self.store.finish(
            job, now=NOW + 1, status=status, known_cost="0.1", usage_complete=known,
            usage={"inputTokens": 100, "outputTokens": 10},
            document=document(job) if status == "PUBLISHED" else None)

    def item(self, sk):
        return self.table.get_item(Key={"PK": self.run.pk, "SK": sk},
                                   ConsistentRead=True).get("Item")

    def history(self):
        return [row for row in self.table.scan()["Items"] if row["SK"].startswith("HISTORY#")]

    def test_repeat_begin_preserves_charge_counters_and_acceptance_deadline(self):
        self.reserve()
        row = self.runs.begin(self.run, NOW + 100)
        self.assertEqual(row["attemptCount"], 1)
        self.assertEqual(row["chargedUsd"], 1)
        self.assertEqual(row["acceptUntil"], NOW + ACCEPT_SECONDS)

    def test_two_known_overruns_above_single_attempt_cap_finalize_once(self):
        second = replace(self.job, doc_hash="b" * 16, token="c" * 32)
        self.reserve()
        self.reserve(second)
        for job in (self.job, second):
            self.store.finish(
                job, now=NOW + 1, status="REJECTED", known_cost="60000",
                usage_complete=True, usage={"inputTokens": 100, "outputTokens": 10})
        receipt = self.runs.finish(self.run, NOW + 2)
        self.assertEqual(receipt["chargedUsd"], Decimal("120000"))
        self.assertEqual(receipt["attemptCount"], 2)
        self.assertEqual(self.runs.finish(self.run, NOW + 3), receipt)
        self.assertEqual(len(self.history()), 1)
        self.assertEqual(self.item("NEWSBUDGET#" + self.job.month)["chargedUsd"], Decimal("120000"))
        self.assertTrue(self.item("NEWSBUDGET#" + self.job.month)["blocked"])

    def test_uninitialized_mismatched_or_expired_run_cannot_reserve(self):
        for job, now in (
            (replace(self.job, run_id="other"), NOW),
            (replace(self.job, lease_until=NOW + ACCEPT_SECONDS + LEASE_SECONDS), NOW + ACCEPT_SECONDS),
        ):
            with self.subTest(run=job.run_id), self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.reserve(job, now)
        self.assertEqual(self.item(self.run.sk)["chargedUsd"], 0)
        self.assertIsNone(self.item("NEWSBUDGET#2026-10"))

    def test_zero_candidate_run_has_one_stable_history_and_no_cost(self):
        first = self.runs.finish(self.run, NOW + 10)
        second = self.runs.finish(self.run, NOW + 100)
        self.assertEqual(first, second)
        self.assertEqual(first["chargedUsd"], 0)
        self.assertEqual(len(self.history()), 1)
        self.assertEqual(self.history()[0]["duration"], 10000)
        self.assertEqual(self.item("CONFIG"), CONFIG)

    def test_recovery_waits_for_admission_deadline_even_with_no_attempts(self):
        for now in (NOW, NOW + ACCEPT_SECONDS - 1):
            with self.subTest(now=now), self.assertRaisesRegex(StateRejected, "RUN_BUSY"):
                self.runs.finish(self.run, now, recovery=True)
            self.assertEqual(self.item(self.run.sk)["runState"], "OPEN")
            self.assertEqual(self.history(), [])
        receipt = self.runs.finish(self.run, NOW + ACCEPT_SECONDS, recovery=True)
        self.assertIn("RECOVERED_WITHOUT_OUTCOMES", receipt["errors"])
        self.assertEqual(receipt["chargedUsd"], 0)
        self.assertEqual(self.runs.finish(self.run, NOW + ACCEPT_SECONDS + 1, recovery=True), receipt)
        self.assertEqual(len(self.history()), 1)

    def test_recovery_respects_a_live_lease_after_admission_closes(self):
        now = NOW + ACCEPT_SECONDS - 1
        job = replace(self.job, lease_until=now + LEASE_SECONDS)
        self.reserve(job, now=now)
        with self.assertRaisesRegex(StateRejected, "RUN_BUSY"):
            self.runs.finish(self.run, NOW + ACCEPT_SECONDS, recovery=True)
        self.assertEqual(self.history(), [])
        receipt = self.runs.finish(self.run, job.lease_until, recovery=True)
        self.assertEqual(receipt["expiredAttempts"], 1)
        self.assertEqual(receipt["chargedUsd"], 1)

    def test_changed_admission_deadline_aborts_recovery_atomically(self):
        original = self.store._transaction
        def change_deadline(operations):
            self.table.update_item(
                Key={"PK": self.run.pk, "SK": self.run.sk},
                UpdateExpression="SET acceptUntil = :later",
                ExpressionAttributeValues={":later": NOW + ACCEPT_SECONDS + 100})
            return original(operations)
        with mock.patch.object(self.store, "_transaction", side_effect=change_deadline):
            with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.runs.finish(self.run, NOW + ACCEPT_SECONDS, recovery=True)
        self.assertEqual(self.history(), [])
        self.assertEqual(self.item(self.run.sk)["runState"], "OPEN")

    def test_unicode_run_start_and_history_preserve_exact_source_identity(self):
        for source_id in ("우리은행", "école-١", "news_source"):
            with self.subTest(source_id=source_id):
                config = dict(CONFIG, PK="CRAWLER#" + source_id, sourceId=source_id)
                self.table.put_item(Item=config)
                run = Run(source_id, "run", source_revision(config))
                self.runs.begin(run, NOW)
                receipt = self.runs.finish(run, NOW + 1)
                self.assertEqual(receipt["sourceId"], source_id)
                self.assertEqual(receipt["sourceRevision"], run.revision)
                self.assertEqual(self.runs.finish(run, NOW + 2), receipt)

    def test_finalization_does_not_charge_again_and_published_count_is_canonical(self):
        self.reserve()
        self.settle(status="PUBLISHED")
        first = self.runs.finish(self.run, NOW + 10)
        self.assertEqual(first["docsAdded"], 1)
        self.assertEqual(first["chargedUsd"], Decimal("0.1"))
        self.assertEqual(first["inputTokens"], 100)
        self.assertEqual(self.runs.finish(self.run, NOW + 20), first)
        self.assertEqual(self.item("NEWSBUDGET#2026-10")["chargedUsd"], Decimal("0.1"))
        self.assertEqual(len(self.history()), 1)

    def test_live_attempt_defers_history_and_expired_attempt_keeps_full_charge(self):
        self.reserve()
        with self.assertRaisesRegex(StateRejected, "RUN_BUSY"):
            self.runs.finish(self.run, NOW + 10)
        self.assertEqual(self.history(), [])
        receipt = self.runs.finish(self.run, self.job.lease_until)
        self.assertEqual(receipt["expiredAttempts"], 1)
        self.assertEqual(receipt["chargedUsd"], 1)
        self.assertFalse(receipt["usageComplete"])
        self.assertEqual(self.item(self.job.sk)["state"], "EXPIRED")
        self.assertEqual(self.item("NEWSBUDGET#2026-10")["unknownAttempts"], 1)
        self.assertEqual(self.runs.finish(self.run, self.job.lease_until + 1), receipt)
        self.assertEqual(self.item("NEWSBUDGET#2026-10")["unknownAttempts"], 1)
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.settle()

    def test_unknown_model_usage_remains_visible_and_reservation_is_not_refunded(self):
        self.reserve()
        self.settle(known=False)
        receipt = self.runs.finish(self.run, NOW + 10)
        self.assertEqual(receipt["unknownAttempts"], 1)
        self.assertEqual(receipt["chargedUsd"], 1)
        self.assertFalse(receipt["usageComplete"])

    def test_disabled_or_deleted_config_does_not_block_accounting_or_revive_source(self):
        self.reserve()
        self.settle()
        self.table.delete_item(Key={"PK": self.run.pk, "SK": "CONFIG"})
        self.assertEqual(self.runs.finish(self.run, NOW + 10)["chargedUsd"], Decimal("0.1"))
        self.assertIsNone(self.item("CONFIG"))

    def test_finalized_run_rejects_late_attempt_without_month_charge(self):
        self.runs.finish(self.run, NOW + 1)
        with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
            self.reserve()
        self.assertIsNone(self.item("NEWSBUDGET#2026-10"))

    def test_new_reservation_between_query_and_finalize_aborts_whole_history_write(self):
        original = self.runs._attempts
        def race(run):
            values = original(run)
            self.reserve()
            return values
        with mock.patch.object(self.runs, "_attempts", side_effect=race):
            with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.runs.finish(self.run, NOW + 10)
        self.assertEqual(self.history(), [])
        self.assertEqual(self.item(self.run.sk)["runState"], "OPEN")
        self.assertEqual(self.item(self.run.sk)["attemptCount"], 1)

    def test_settlement_between_snapshot_and_finalize_prevents_stale_receipt(self):
        self.reserve()
        self.settle()
        original = self.runs._attempts
        def race(run):
            values = original(run)
            self.table.update_item(
                Key={"PK": run.pk, "SK": run.sk},
                UpdateExpression="SET finishedCount = :n", ExpressionAttributeValues={":n": 2})
            return values
        with mock.patch.object(self.runs, "_attempts", side_effect=race):
            with self.assertRaisesRegex(StateRejected, "STATE_CONFLICT"):
                self.runs.finish(self.run, NOW + 10)
        self.assertEqual(self.history(), [])

    def test_discovery_diagnostics_are_bound_and_do_not_copy_raw_query_text(self):
        discovery = {"sourceId": self.run.source_id, "runId": self.run.run_id,
                     "sourceRevision": self.run.revision, "queryCount": 2,
                     "errors": [{"code": "SEARCH_FAILED", "queryHash": "synthetic",
                                 "privateRawQuery": "must not enter history"}]}
        receipt = self.runs.finish(self.run, NOW + 1, discovery=discovery)
        self.assertEqual(receipt["errors"], ["SEARCH_FAILED"])
        self.assertEqual(receipt["queryCount"], 2)
        self.assertNotIn("must not enter history", str(self.history()))

    def test_fetch_failures_are_visible_even_without_billable_attempts(self):
        outcome = {"sourceId": self.run.source_id, "runId": self.run.run_id,
                   "status": "FETCH_REJECTED", "reason": "ACCESS_DENIED"}
        receipt = self.runs.finish(self.run, NOW + 1, outcomes=[outcome])
        self.assertEqual(receipt["status"], "PARTIAL")
        self.assertEqual(receipt["workerOutcomes"], {"FETCH_REJECTED": 1})
        self.assertEqual(receipt["errors"], ["ACCESS_DENIED"])
        self.assertEqual(receipt["chargedUsd"], 0)

    def test_foreign_worker_result_cannot_enter_run_history(self):
        with self.assertRaisesRegex(StateRejected, "INVALID_WORKER_RESULTS"):
            self.runs.finish(self.run, NOW + 1, outcomes=[{
                "sourceId": "other", "runId": self.run.run_id,
                "status": "FETCH_REJECTED", "reason": "ACCESS_DENIED"}])
        self.assertEqual(self.history(), [])


if __name__ == "__main__":
    unittest.main()
