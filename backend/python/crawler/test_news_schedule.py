"""Daily scheduling contracts using synthetic sources and actual SDK pagination."""

import copy
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
                schedule.handler({}, context())
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

    def test_duplicate_and_malformed_source_identities_fail_closed(self):
        rows = [
            [source("source"), source("source")],
            [source("source", sourceId="different")],
            [source("source", SK="OTHER")],
            [source("../source")],
        ]
        for items in rows:
            with self.subTest(items=items), self.assertRaises((StateRejected, DiscoveryRejected)):
                schedule.list_sources(self.table([{"Items": items}]), "all", "", NOW, lambda: 100)

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
                result = schedule.handler({}, context())
            self.assertEqual([r["sourceId"] for r in result["sources"]], ["a", "z"])
            self.assertEqual(table.scan(ConsistentRead=True)["Items"], before)


if __name__ == "__main__":
    unittest.main()
