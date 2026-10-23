"""Daily scheduling contracts using synthetic sources and actual SDK pagination."""

import copy
from datetime import datetime, timezone
import hashlib
import json
import os
import unittest
from unittest import mock

import boto3

import news_schedule as schedule
import news_runtime as runtime
from news_store import StateRejected
from news_discovery import DiscoveryRejected, source_snapshot
from test_news_runtime import ENV, context
from test_news_store import CONFIG, NOW, mock_aws


def source(identity, **changes):
    return {**copy.deepcopy(CONFIG), "PK": "CRAWLER#" + identity,
            "sourceId": identity, **changes}


class ScheduleBoundaries(unittest.TestCase):
    def table(self, pages):
        table = mock.Mock()
        table.name = "synthetic-daily"
        table.meta.client.get_paginator.return_value.paginate.return_value = iter(pages)
        return table

    def test_disabled_mode_has_no_sdk_calls_and_rejects_external_selection(self):
        with mock.patch.dict(os.environ, {"NEWS_PIPELINE_MODE": "off"}, clear=True), \
                mock.patch.object(schedule.boto3, "Session") as session:
            self.assertEqual(schedule.handler({}, context()), {"mode": "off", "sources": []})
            for event in ({"sourceId": "source"}, None, [], {"mode": "all"}):
                with self.assertRaisesRegex(StateRejected, "INVALID_REQUEST"):
                    schedule.handler(event, context())
            session.assert_not_called()

    def test_bad_activation_and_stale_prices_fail_before_sdk(self):
        cases = [
            {"NEWS_PIPELINE_MODE": "typo"},
            {"NEWS_PIPELINE_MODE": "canary", "NEWS_CANARY_SOURCE_ID": ""},
            {"NEWS_PIPELINE_MODE": "all"},
            {"NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "on"},
            {"NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "typo"},
            {"NEWS_PRICE_VERIFIED_ON": "2026-01-01"},
        ]
        for changes in cases:
            with self.subTest(changes=changes), \
                    mock.patch.dict(os.environ, {**ENV, **changes}, clear=True), \
                    mock.patch.object(schedule.time, "time", return_value=NOW), \
                    mock.patch.object(schedule.boto3, "Session") as session, \
                    self.assertRaises(StateRejected):
                schedule.handler({"scheduledAt": datetime.fromtimestamp(
                    NOW, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}, context())
            session.assert_not_called()

    def test_all_writers_reject_legacy_on_and_accept_only_exact_off(self):
        for value in ("", "on", "OFF", "false"):
            with mock.patch.dict(os.environ, {
                    "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": value}, clear=True), \
                    self.assertRaisesRegex(StateRejected, "LEGACY_WRITER_ENABLED"):
                runtime._enabled("source")
        with mock.patch.dict(os.environ, {
                "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "off"}, clear=True):
            runtime._enabled("source")

    def test_scan_deadline_checked_before_first_or_next_network_page(self):
        consumed = []

        def pages():
            for i in range(3):
                consumed.append(i)
                yield {"Items": [], "LastEvaluatedKey": {"PK": str(i), "SK": "CONFIG"}}
        with self.assertRaisesRegex(StateRejected, "SCHEDULE_SCAN_BUDGET"):
            schedule.list_sources(self.table(pages()), "all", "", NOW, lambda: 9)
        self.assertEqual(consumed, [])
        remaining = mock.Mock(side_effect=[100, 9])
        with self.assertRaisesRegex(StateRejected, "SCHEDULE_SCAN_BUDGET"):
            schedule.list_sources(self.table(pages()), "all", "", NOW, remaining)
        self.assertEqual(consumed, [0])

    def test_duplicate_source_rows_still_fail_closed(self):
        with self.assertRaisesRegex(StateRejected, "DUPLICATE_SCHEDULE_SOURCE"):
            schedule.list_sources(self.table([{"Items": [source("source")] * 2}]),
                                  "all", "", NOW, lambda: 100)

    def test_bad_identity_is_reported_without_suppressing_healthy_sources(self):
        for bad in (source("a" * 129), source("../source"),
                    source("bad", sourceId="different"), source("bad", SK="OTHER")):
            for items in ([bad, source("good")], [source("good"), bad]):
                with self.subTest(items=items):
                    try:
                        result = schedule.list_sources(self.table([{"Items": items}]),
                                                       "all", "", NOW, lambda: 100)
                    except StateRejected as exc:
                        self.fail(f"One malformed identity suppressed the healthy source: {exc}")
                    good = [r for r in result["sources"] if "sourceId" in r]
                    self.assertEqual([r["sourceId"] for r in good], ["good"])
                    rejected = [r for r in result["sources"] if "schedulingError" in r]
                    self.assertEqual(rejected, [{
                        "schedulingError": "INVALID_SCHEDULE_SOURCE",
                        "sourceKeySha256": hashlib.sha256(bad["PK"].encode()).hexdigest(),
                    }])
                    self.assertNotIn(bad["PK"], str(result))

    def test_invalid_identities_count_against_the_same_capacity_limit(self):
        items = [source(f"good-{i}") for i in range(schedule.MAX_SOURCES)]
        items.append(source("x" * 129))
        with self.assertRaisesRegex(StateRejected, "SCHEDULE_SOURCE_LIMIT"):
            schedule.list_sources(self.table([{"Items": items}]),
                                  "all", "", NOW, lambda: 100)

    def test_original_scheduled_day_survives_next_day_replay(self):
        scheduled = datetime.fromtimestamp(NOW, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        results = []
        for replay_now in (NOW, NOW + 86400):
            with mock.patch.dict(os.environ, {
                    **ENV, "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "off"},
                    clear=True), mock.patch.object(schedule.time, "time", return_value=replay_now), \
                    mock.patch.object(schedule.boto3, "Session") as session:
                session.return_value.resource.return_value.Table.return_value = self.table(
                    [{"Items": [source("good")]}])
                try:
                    results.append(schedule.handler({"scheduledAt": scheduled}, context()))
                except StateRejected as exc:
                    self.fail(f"Valid original schedule timestamp was rejected: {exc}")
        self.assertEqual(results[0], results[1])
        self.assertEqual(results[0]["sources"][0]["runId"], "daily-20261021")

    def test_bad_schedule_timestamps_fail_before_sdk(self):
        values = [None, True, 123, "", "2026-10-21", "2026-10-21T19:10:00+00:00",
                  "2026-02-30T19:10:00Z", "2026-10-21T19:10:00.000Z",
                  datetime.fromtimestamp(NOW + 1, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                  datetime.fromtimestamp(NOW - 14 * 86400 - 1, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")]
        for value in values:
            with self.subTest(value=value), mock.patch.dict(os.environ, {
                    **ENV, "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "off"},
                    clear=True), mock.patch.object(schedule.time, "time", return_value=NOW), \
                    mock.patch.object(schedule.boto3, "Session") as session, \
                    self.assertRaisesRegex(StateRejected, "INVALID_SCHEDULE_TIME"):
                schedule.handler({"scheduledAt": value}, context())
            session.assert_not_called()

    def test_manual_replay_hash_rejects_changed_scan_before_returning_sources(self):
        scheduled = datetime.fromtimestamp(NOW, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        original = schedule.list_sources(self.table([{"Items": [source("a"), source("b")]}]),
                                         "all", "", NOW, lambda: 100)
        encode = lambda value: json.dumps(value, ensure_ascii=False, sort_keys=True,
                                         separators=(",", ":"), allow_nan=False)
        payload = {"mode": original["mode"], "sources": sorted(original["sources"], key=encode)}
        expected = hashlib.sha256(encode(payload).encode()).hexdigest()
        for items, accepted in (([source("b"), source("a")], True),
                                ([source("a")], False),
                                ([source("a"), source("b"), source("new")], False),
                                ([source("a"), source("c")], False),
                                ([source("a"), source("b", sourceId="wrong")], False)):
            with self.subTest(accepted=accepted, items=items), mock.patch.dict(os.environ, {
                    **ENV, "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "off"},
                    clear=True), mock.patch.object(schedule.time, "time", return_value=NOW), \
                    mock.patch.object(schedule.boto3, "Session") as session:
                session.return_value.resource.return_value.Table.return_value = self.table(
                    [{"Items": items}])
                event = {"scheduledAt": scheduled, "expectedSourcesSha256": expected}
                if accepted:
                    try:
                        self.assertEqual(schedule.handler(event, context()), original)
                    except StateRejected as exc:
                        self.fail(f"Unchanged snapshot-bound replay was rejected: {exc}")
                else:
                    with self.assertRaisesRegex(StateRejected, "SCHEDULE_SOURCES_CHANGED"):
                        schedule.handler(event, context())

    def test_manual_replay_digest_requires_exact_lowercase_hex_before_sdk(self):
        scheduled = datetime.fromtimestamp(NOW, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        for digest in (None, 1, True, "", "A" * 64, "0" * 63, "g" * 64):
            with self.subTest(digest=digest), mock.patch.dict(os.environ, ENV, clear=True), \
                    mock.patch.object(schedule.boto3, "Session") as session, \
                    self.assertRaisesRegex(StateRejected, "INVALID_REQUEST"):
                schedule.handler({"scheduledAt": scheduled, "expectedSourcesSha256": digest}, context())
            session.assert_not_called()

    def test_capacity_and_page_bounds_reject_whole_dispatch_without_truncation(self):
        items = [source(f"source-{i}") for i in range(schedule.MAX_SOURCES + 1)]
        with self.assertRaisesRegex(StateRejected, "SCHEDULE_SOURCE_LIMIT"):
            schedule.list_sources(self.table([{"Items": items}]), "all", "", NOW, lambda: 100)
        with mock.patch.object(schedule, "MAX_PAGES", 2), \
                self.assertRaisesRegex(StateRejected, "SCHEDULE_SCAN_BUDGET"):
            schedule.list_sources(self.table([{"Items": [], "LastEvaluatedKey": {"PK": "next", "SK": "CONFIG"}}] * 3), "all", "", NOW, lambda: 100)
        self.assertLessEqual(schedule.MAX_SOURCES * 45 * 60 + 120, 23 * 3600)

    def test_invalid_configuration_is_dispatched_without_blocking_valid_sources(self):
        # Subscription unions can exceed the child parser's per-source bounds.
        for changes in ({"newsQueries": [f"query-{i}" for i in range(33)]},
                        {"customUrls": [f"https://example.com/{i}" for i in range(21)]},
                        {"sourceName": "Source", "aliases": [f"alias-{i}" for i in range(32)]},
                        {"aliases": "invalid"}):
            bad = source("bad", **changes)
            with self.subTest(changes=changes):
                with self.assertRaises(DiscoveryRejected):
                    source_snapshot(bad)  # The child still rejects its current config.
                result = schedule.list_sources(self.table([{"Items": [bad, source("good")]}]),
                                               "all", "", NOW, lambda: 100)
                self.assertEqual([item["sourceId"] for item in result["sources"]], ["bad", "good"])
                self.assertTrue(all(set(item) == {"sourceId", "runId", "executionName"}
                                    for item in result["sources"]))

    def test_terminal_page_at_exact_cap_succeeds_without_another_network_request(self):
        consumed = []

        def pages():
            consumed.append(1)
            yield {"Items": [], "LastEvaluatedKey": {"PK": "next", "SK": "CONFIG"}}
            consumed.append(2)
            yield {"Items": [source("source")]}
            raise AssertionError("EXTRA_PAGE_FETCH")
        with mock.patch.object(schedule, "MAX_PAGES", 2):
            result = schedule.list_sources(self.table(pages()), "all", "", NOW, lambda: 100)
        self.assertEqual(consumed, [1, 2])
        self.assertEqual([item["sourceId"] for item in result["sources"]], ["source"])

    def test_daily_names_are_stable_across_order_and_unique_per_source_and_utc_day(self):
        def run(items, now=NOW):
            return schedule.list_sources(self.table([{"Items": items}]), "all", "", now, lambda: 100)
        first = run([source("b"), source("a")])
        self.assertEqual(first, run([source("a"), source("b")], NOW + 3600))
        self.assertEqual([r["sourceId"] for r in first["sources"]], ["a", "b"])
        names = {r["executionName"] for r in first["sources"]}
        self.assertEqual(len(names), 2)
        self.assertTrue(names.isdisjoint({r["executionName"] for r in
                                         run([source("a"), source("b")], NOW + 86400)["sources"]}))
        for row in first["sources"]:
            self.assertEqual(set(row), {"sourceId", "runId", "executionName"})
            self.assertEqual(row["runId"], "daily-20261021")
            self.assertRegex(row["executionName"], r"^news-20261021-[0-9a-f]{32}$")

    def test_canary_selection_and_disabled_and_synthetic_sources_never_dispatch(self):
        rows = [source("source"), source("other"), source("__tech__"),
                source("disabled", status="disabled")]
        result = schedule.list_sources(self.table([{"Items": rows}]), "canary", "source",
                                       NOW, lambda: 100)
        self.assertEqual([r["sourceId"] for r in result["sources"]], ["source"])
        result = schedule.list_sources(self.table([{"Items": rows}]), "all", "", NOW, lambda: 100)
        self.assertEqual([r["sourceId"] for r in result["sources"]], ["other", "source"])

    def test_output_byte_limit_does_not_silently_truncate(self):
        with mock.patch.object(schedule, "MAX_OUTPUT_BYTES", 10), \
                self.assertRaisesRegex(StateRejected, "SCHEDULE_OUTPUT_LIMIT"):
            schedule.list_sources(self.table([{"Items": [source("source")]}]),
                                  "all", "", NOW, lambda: 100)


@unittest.skipUnless(mock_aws, "moto required for real SDK pagination")
class ScheduleDynamoDB(unittest.TestCase):
    def test_real_paginated_scan_filters_and_projects_without_any_mutation(self):
        with mock.patch.dict(os.environ, {
                **ENV, "NEWS_PIPELINE_MODE": "all", "NEWS_LEGACY_WRITES": "off",
                "AWS_DEFAULT_REGION": "ap-northeast-2"}, clear=True), mock_aws():
            table = boto3.Session().resource("dynamodb", region_name="ap-northeast-2").create_table(
                TableName=ENV["TABLE_NAME"], BillingMode="PAY_PER_REQUEST",
                KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"},
                           {"AttributeName": "SK", "KeyType": "RANGE"}],
                AttributeDefinitions=[{"AttributeName": key, "AttributeType": "S"}
                                      for key in ("PK", "SK")])
            # Empty filtered pages must not terminate traversal.
            for i in range(205):
                table.put_item(Item={"PK": f"OTHER#{i}", "SK": "CONFIG", "body": "private"})
            for row in (source("a"), source("z"), source("disabled", status="disabled"),
                        source("__tech__"), {**source("a"), "SK": "DOC#legacy"}):
                table.put_item(Item=row)
            before = table.scan(ConsistentRead=True)["Items"]
            with mock.patch.object(schedule.time, "time", return_value=NOW):
                result = schedule.handler({"scheduledAt": datetime.fromtimestamp(
                    NOW, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}, context())
            self.assertEqual([r["sourceId"] for r in result["sources"]], ["a", "z"])
            self.assertEqual(table.scan(ConsistentRead=True)["Items"], before)


@unittest.skipUnless(mock_aws, "moto required for actual finalized child receipts")
class DailyChildReceiptContract(unittest.TestCase):
    def test_real_failed_search_finalizes_partial_with_complete_zero_charge(self):
        from news_discovery import discover
        from test_news_lifecycle import TestRunTransactions
        case = TestRunTransactions()
        case.setUp()
        self.addCleanup(case.doCleanups)
        discovery = discover(CONFIG, lambda *_: ([], True), lambda *_: set(),
                             datetime.fromtimestamp(NOW, timezone.utc), case.run.run_id)
        receipt = case.runs.finish(case.run, NOW + 1, discovery=discovery, outcomes=[])
        self.assertEqual(receipt["status"], "PARTIAL")
        self.assertEqual(receipt["errors"], ["SEARCH_FAILED"])
        self.assertIs(receipt["usageComplete"], True)
        self.assertEqual(receipt["docsAdded"], 0)
        self.assertEqual(receipt["chargedUsd"], 0)
        self.assertEqual(case.item(case.run.sk)["runState"], "FINALIZED")


if __name__ == "__main__":
    unittest.main()
