"""Candidate discovery bounds, source revisions and freshness decisions."""

import json
import unittest
from datetime import datetime, timezone
from unittest import mock

import news_discovery as discovery
import news_pipeline as pipeline

NOW = datetime(2026, 10, 21, 12, tzinfo=timezone.utc)


def source(**changes):
    value = {"sourceId": "acme", "sourceName": "Acme", "aliases": [],
             "newsQueries": [], "customUrls": [], "status": "active"}
    value.update(changes)
    return value


def article(index=1, **changes):
    value = {"url": f"https://example.com/{index}", "title": f"event {index}",
             "publishedDate": "2026-10-20", "text": "secret search snippet"}
    value.update(changes)
    return value


class TestDiscovery(unittest.TestCase):
    def run_discovery(self, values=None, *, config=None, gateway=None, lookup=None, cap=20):
        gateway = gateway or mock.Mock(return_value=(values or [], None))
        lookup = lookup or mock.Mock(return_value=set())
        result = discovery.discover(config or source(), gateway, lookup, NOW, "run-1",
                                    max_candidates=cap)
        return result, gateway, lookup

    def test_output_forwards_only_candidate_contract_and_actual_discovery_time(self):
        result, gateway, _ = self.run_discovery([article(body="private body", unknown={"value": "secret"})])
        encoded = json.dumps(result)
        self.assertNotIn("secret", encoded)
        self.assertNotIn("private body", encoded)
        candidate = result["candidates"][0]
        self.assertEqual(candidate["discoveredAt"], NOW.isoformat())
        self.assertEqual(candidate["sourceRevision"], result["sourceRevision"])
        self.assertEqual(candidate["searchResultRank"], 1)
        self.assertTrue(candidate["requireRelevance"])
        self.assertLessEqual(gateway.call_count, 12)
        self.assertEqual(result["freshnessDays"], 7)

    def test_existing_unicode_source_identity_is_preserved_through_discovery(self):
        for identity in ("하나금융그룹", "우리은행", "école-١", "news_source"):
            with self.subTest(identity=identity):
                config = source(sourceId=identity)
                result, _, lookup = self.run_discovery([article()], config=config)
                self.assertEqual(result["sourceId"], identity)
                self.assertEqual(result["candidates"][0]["sourceId"], identity)
                self.assertEqual(result["sourceRevision"], discovery.source_revision(config))
                self.assertTrue(all(call.args[0] == identity for call in lookup.call_args_list))

    def test_source_identity_rejects_unsafe_or_rewritten_forms(self):
        for identity in ("", "__reserved", " source", "source ", "우리 은행", "a/b",
                         "a\\b", "a#b", "a%b", ".", "a\nb", "\ud800", "a²", "가" * 129):
            with self.subTest(identity=repr(identity)):
                self.assertFalse(discovery.valid_source_id(identity))
                with self.assertRaisesRegex(discovery.DiscoveryRejected, "INVALID_CONFIG"):
                    self.run_discovery([article()], config=source(sourceId=identity))

    def test_unknown_future_and_old_dates_never_claim_freshness(self):
        values = [article(1, publishedDate=""), article(2, publishedDate="2026-10-22"),
                  article(3, publishedDate="2026-08-01"), article(4)]
        result, _, _ = self.run_discovery(values)
        self.assertEqual([item["url"] for item in result["candidates"]], ["https://example.com/4"])
        self.assertGreaterEqual(result["staleRejected"], 3)

    def test_seven_day_miss_falls_back_to_verified_thirty_day_dates(self):
        result, gateway, _ = self.run_discovery([article(publishedDate="2026-10-01")])
        self.assertEqual(result["freshnessDays"], 30)
        self.assertEqual(len(result["candidates"]), 1)
        self.assertTrue(any("after:2026-10-14" in call.args[0] for call in gateway.call_args_list))
        self.assertTrue(any("after:2026-09-21" in call.args[0] for call in gateway.call_args_list))

    def test_unrepresentable_utc_date_does_not_discard_valid_neighbor(self):
        for published in ("0001-01-01T00:00:00+14:00", "9999-12-31T23:59:59-14:00"):
            with self.subTest(published=published):
                result, _, _ = self.run_discovery(
                    [article(1, publishedDate=published), article(2)])
                self.assertEqual([item["url"] for item in result["candidates"]],
                                 ["https://example.com/2"])
                self.assertGreater(result["invalidRejected"] + result["staleRejected"], 0)

    def test_already_published_primary_results_still_allow_fallback(self):
        def gateway(query, limit):
            return ([article()] if "2026-10-14" in query
                    else [article(2, publishedDate="2026-10-01")]), None

        known = pipeline.document_hash("https://example.com/1")
        lookup = mock.Mock(side_effect=lambda source_id, hashes: hashes & {known})
        result, _, _ = self.run_discovery(gateway=gateway, lookup=lookup)
        self.assertEqual(result["candidates"][0]["url"], "https://example.com/2")
        self.assertEqual(result["freshnessDays"], 30)
        self.assertGreater(result["duplicateRejected"], 0)

    def test_url_tracking_duplicates_do_not_spend_two_slots(self):
        result, _, _ = self.run_discovery(
            [article(url="https://example.com/a?utm_source=one"),
             article(url="https://example.com/a?utm_source=two")])
        self.assertEqual(len(result["candidates"]), 1)

    def test_bad_member_is_rejected_without_discarding_valid_article(self):
        result, _, _ = self.run_discovery(
            [None, article(url="https://[invalid"), article(title=5), article(4)])
        self.assertEqual(result["candidates"][0]["url"], "https://example.com/4")
        self.assertGreaterEqual(result["invalidRejected"], 3)

    def test_transport_failure_is_explicit_without_query_or_raw_error(self):
        gateway = mock.Mock(side_effect=RuntimeError("sensitive transport body"))
        result, _, _ = self.run_discovery(gateway=gateway)
        self.assertEqual(result["status"], "SEARCH_FAILED")
        encoded = json.dumps(result)
        self.assertNotIn("sensitive", encoded)
        self.assertNotIn("Acme", encoded)
        self.assertTrue(all(len(error["queryHash"]) == 12 for error in result["errors"]))

    def test_missing_or_incomplete_published_lookup_fails_closed(self):
        for lookup in (mock.Mock(side_effect=RuntimeError("database")),
                       mock.Mock(return_value={"unknown-hash"}),
                       mock.Mock(return_value=[])):
            with self.assertRaisesRegex(discovery.DiscoveryRejected, "STATE_UNAVAILABLE"):
                self.run_discovery([article()], lookup=lookup)

    def test_custom_url_is_prioritized_and_skips_only_relevance(self):
        result, _, _ = self.run_discovery(
            [article()], config=source(customUrls=["https://example.com/manual"]))
        custom = result["candidates"][0]
        self.assertEqual(custom["url"], "https://example.com/manual")
        self.assertFalse(custom["requireRelevance"])
        self.assertEqual(custom["ingestSource"], "custom")
        self.assertEqual(custom["title"], "")

    def test_custom_request_overrides_matching_search_candidate(self):
        result, _, _ = self.run_discovery(
            [article()], config=source(customUrls=["https://example.com/1"]))
        self.assertEqual(len(result["candidates"]), 1)
        self.assertFalse(result["candidates"][0]["requireRelevance"])

    def test_invalid_custom_url_and_duplicates_are_counted(self):
        result, _, _ = self.run_discovery(config=source(
            sourceName="", customUrls=["javascript:alert(1)", "https://example.com/a",
                                     "https://example.com/a?utm_source=x"]))
        self.assertEqual(len(result["candidates"]), 1)
        self.assertEqual(result["invalidRejected"], 1)
        self.assertEqual(result["duplicateRejected"], 1)

    def test_disabled_or_zero_budget_makes_no_external_calls(self):
        for config, cap, status in ((source(status="disabled"), 20, "DISABLED"),
                                    (source(), 0, "BUDGET_PAUSED")):
            result, gateway, lookup = self.run_discovery(config=config, cap=cap)
            gateway.assert_not_called()
            lookup.assert_not_called()
            self.assertEqual(result["status"], status)

    def test_candidate_count_and_encoded_output_are_bounded(self):
        config = source(aliases=["가" * 190 + str(n) for n in range(31)],
                        newsQueries=["나" * 190 + str(n) for n in range(32)])
        # Oversized generated queries are rejected, never truncated. A short
        # identity query still produces many candidates with large context.
        result, _, _ = self.run_discovery([article(n) for n in range(25)], config=config)
        self.assertGreater(result["budgetRejected"], 0)
        self.assertLessEqual(len(result["candidates"]), 20)
        self.assertLessEqual(len(json.dumps(result, ensure_ascii=False).encode()),
                             discovery.MAX_OUTPUT_BYTES)

    def test_worst_case_search_calls_and_result_inspection_are_bounded(self):
        gateway = mock.Mock(return_value=([article(n, publishedDate="2025-01-01")
                                         for n in range(1000)], None))
        result, _, _ = self.run_discovery(gateway=gateway, config=source(
            aliases=[f"Acme {n}" for n in range(31)]))
        self.assertEqual(gateway.call_count, 24)
        self.assertEqual(result["staleRejected"], 600)
        self.assertEqual(result["candidates"], [])
        self.assertTrue(all(call.args[1] == 25 for call in gateway.call_args_list))

    def test_configuration_revision_covers_publication_relevant_changes(self):
        base = source(ownerId="owner")
        revision = discovery.source_revision(base)
        for changes in ({"aliases": ["alias"]}, {"newsQueries": ["keyword"]},
                        {"customUrls": ["https://example.com"]}, {"status": "disabled"},
                        {"sourceName": "Other"}, {"ownerId": "new-owner"}):
            self.assertNotEqual(revision, discovery.source_revision({**base, **changes}))
        self.assertEqual(revision, discovery.source_revision({**base, "lastCrawledAt": "later"}))

    def test_invalid_configuration_and_run_parameters_are_rejected(self):
        for config in (source(sourceId="../escape"), source(sourceId="__tech__"),
                       source(newsQueries=["x"] * 33), source(sourceName="x" * 201),
                       source(aliases=[str(n) for n in range(32)])):
            with self.assertRaisesRegex(discovery.DiscoveryRejected, "INVALID_CONFIG"):
                self.run_discovery(config=config)
        for cap in (-1, 21, True):
            with self.assertRaisesRegex(discovery.DiscoveryRejected, "INVALID_REQUEST"):
                self.run_discovery(cap=cap)
