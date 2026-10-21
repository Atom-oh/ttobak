"""Worker acceptance and budget boundaries with synthetic source/model data."""

from dataclasses import replace
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import json
import unittest
from unittest import mock

from botocore.exceptions import ReadTimeoutError

from article_fetcher import FetchRejected
from news_discovery import source_revision
from news_enrichment import FINAL_MODEL_ID, MAX_TOKENS, QualityRejected
from news_store import StateRejected
from news_worker import (
    MAX_INPUT_TOKENS, MeteredModel, ModelPolicy, WorkerDeps, process_candidate,
)
from test_news_enrichment import bedrock_response, candidate_fixture, valid_body

NOW = int(datetime(2026, 8, 9, tzinfo=timezone.utc).timestamp())
POLICY = ModelPolicy(Decimal("5"), Decimal("25"))  # Synthetic rates, not current AWS prices.
CONFIG = {"sourceId": "woori", "sourceName": "우리은행", "aliases": ["Woori Bank"],
          "newsQueries": ["AI"], "status": "active", "ownerId": "owner"}


def request():
    return {"modelId": FINAL_MODEL_ID, "inferenceConfig": {"maxTokens": MAX_TOKENS},
            "messages": [{"role": "user", "content": [{"text": "synthetic"}]}]}


class TestMeteredModel(unittest.TestCase):
    def setUp(self):
        self.client = mock.Mock()
        self.client.count_tokens.return_value = {"inputTokens": 100}
        self.client.converse.return_value = bedrock_response()
        self.model = MeteredModel(self.client, POLICY, lambda: 900)

    def test_invalid_prices_never_become_free_and_reservation_covers_both_calls(self):
        for value in (0, -1, True, 0.1, "NaN"):
            with self.subTest(value=value), self.assertRaises(StateRejected):
                ModelPolicy(value, Decimal("25"))
        self.assertEqual(POLICY.reservation, Decimal("1.300000000000"))

    def test_oversized_or_missing_count_prevents_billable_model_call(self):
        for count in (None, True, 0, MAX_INPUT_TOKENS + 1):
            self.client.count_tokens.return_value = {"inputTokens": count}
            with self.subTest(count=count), self.assertRaisesRegex(QualityRejected, "INPUT_BUDGET"):
                self.model.converse(**request())
        self.client.converse.assert_not_called()
        self.assertTrue(self.model.complete)

    def test_count_tokens_and_converse_use_the_same_source_messages(self):
        value = request()
        self.model.converse(**value)
        self.assertEqual(self.client.count_tokens.call_args.kwargs["input"]["converse"]["messages"],
                         self.client.converse.call_args.kwargs["messages"])
        self.assertEqual(self.model.usage, {"inputTokens": 100, "outputTokens": 50})

    def test_second_request_timeout_keeps_first_usage_and_marks_unknown_cost(self):
        self.client.converse.side_effect = [
            bedrock_response(), ReadTimeoutError(endpoint_url="https://synthetic.invalid")]
        self.model.converse(**request())
        with self.assertRaises(ReadTimeoutError):
            self.model.converse(**request())
        self.assertEqual(self.model.usage, {"inputTokens": 100, "outputTokens": 50})
        self.assertFalse(self.model.complete)

    def test_unknown_first_call_stays_unknown_if_a_caller_later_invokes_again(self):
        self.client.converse.side_effect = [
            ReadTimeoutError(endpoint_url="https://synthetic.invalid"), bedrock_response()]
        with self.assertRaises(ReadTimeoutError):
            self.model.converse(**request())
        self.model.converse(**request())
        self.assertFalse(self.model.complete)
        self.assertEqual(self.model.usage, {"inputTokens": 100, "outputTokens": 50})

    def test_partial_usage_keeps_known_tokens_but_cannot_claim_complete_accounting(self):
        self.client.converse.return_value = bedrock_response(usage={"inputTokens": 100})
        with self.assertRaisesRegex(QualityRejected, "USAGE_INVALID"):
            self.model.converse(**request())
        self.assertFalse(self.model.complete)
        self.assertEqual(self.model.usage["inputTokens"], 100)

    def test_service_reported_overrun_is_charged_and_stops_publication(self):
        self.client.converse.return_value = bedrock_response(
            usage={"inputTokens": MAX_INPUT_TOKENS + 1, "outputTokens": 1})
        with self.assertRaisesRegex(QualityRejected, "MODEL_BUDGET"):
            self.model.converse(**request())
        self.assertTrue(self.model.complete)
        self.assertEqual(self.model.usage["inputTokens"], MAX_INPUT_TOKENS + 1)

    def test_two_call_and_remaining_time_limits_prevent_additional_requests(self):
        self.model.converse(**request())
        self.model.converse(**request())
        with self.assertRaisesRegex(QualityRejected, "MODEL_BUDGET"):
            self.model.converse(**request())
        self.assertEqual(self.client.converse.call_count, 2)
        short = MeteredModel(self.client, POLICY, lambda: 100)
        with self.assertRaisesRegex(QualityRejected, "RUN_BUDGET"):
            short.converse(**request())
        self.assertEqual(self.client.count_tokens.call_count, 2)


class TestCandidateWorker(unittest.TestCase):
    def setUp(self):
        self.store = mock.Mock()
        self.store.current_config.return_value = dict(CONFIG)
        self.store.published_hashes.return_value = set()
        self.store.finish.side_effect = lambda job, **kw: {
            "status": kw["status"], "reason": kw.get("reason", ""),
            "fetchWarnings": list(kw.get("warnings", []))}
        self.client = mock.Mock()
        self.client.count_tokens.return_value = {"inputTokens": 100}
        self.client.converse.return_value = bedrock_response()
        self.fetch = mock.Mock(return_value=valid_body())
        self.deps = WorkerDeps(
            self.store, mock.Mock(), self.client, "private-artifacts", "published-kb",
            POLICY, Decimal("25"), Decimal("5"), lambda: 900, lambda: NOW,
            fetch=self.fetch, token=lambda: "b" * 32)
        self.candidate = candidate_fixture(
            sourceRevision=source_revision(CONFIG), freshnessDays=7)

    def test_complete_acceptance_reserves_before_model_and_publishes_after_both_objects(self):
        events = []
        self.store.reserve.side_effect = lambda *a, **kw: events.append("reserve")
        self.client.converse.side_effect = lambda **kw: (events.append("model") or bedrock_response())
        self.deps.s3.put_object.side_effect = lambda **kw: events.append("put")
        self.store.finish.side_effect = lambda *a, **kw: (events.append("finish") or kw)
        result = process_candidate(self.candidate, self.deps)
        self.assertEqual(events, ["reserve", "model", "put", "put", "finish"])
        self.assertEqual(result["status"], "PUBLISHED")
        self.assertTrue(result["usage_complete"])
        row = result["document"]
        puts = self.deps.s3.put_object.call_args_list
        self.assertTrue(puts[0].kwargs["Key"].startswith("knowledge-artifacts/news/v1/"))
        self.assertTrue(puts[1].kwargs["Key"].startswith("news/v1/"))
        self.assertEqual(row["markdownSha256"], hashlib.sha256(puts[1].kwargs["Body"]).hexdigest())
        self.assertEqual(row["artifactSha256"], hashlib.sha256(puts[0].kwargs["Body"]).hexdigest())
        self.assertEqual(json.loads(puts[0].kwargs["Body"])["title"], row["title"])
        for call in puts:
            self.assertEqual(call.kwargs["IfNoneMatch"], "*")
        self.fetch.assert_called_once_with("https://example.com/a", https_only=True)

    def test_stale_candidate_or_contradictory_fetched_date_never_invokes_model(self):
        for candidate, body in (
            ({**self.candidate, "publishedAt": "2026-01-01"}, valid_body()),
            (self.candidate, replace(valid_body(), published_at="2026-01-01")),
            (self.candidate, replace(valid_body(), published_at="2027-01-01")),
        ):
            self.fetch.return_value = body
            result = process_candidate(candidate, self.deps)
            self.assertEqual(result["status"], "FETCH_REJECTED")
        self.store.reserve.assert_not_called()
        self.client.converse.assert_not_called()

    def test_invalid_type_or_non_tls_source_never_fetches(self):
        for change in ({"url": "http://example.com/a"}, {"ingestSource": "anything"},
                       {"freshnessDays": True}, {"freshnessDays": 31},
                       {"requireRelevance": False}):
            with self.subTest(change=change), self.assertRaisesRegex(StateRejected, "INVALID_CANDIDATE"):
                process_candidate({**self.candidate, **change}, self.deps)
        self.fetch.assert_not_called()

    def test_custom_url_is_current_config_bound_and_skips_bad_configured_neighbors(self):
        self.store.current_config.return_value = {
            **CONFIG, "customUrls": ["not a URL", "https://example.com/a"]}
        value = {**self.candidate, "ingestSource": "custom", "requireRelevance": False,
                 "publishedAt": "2020-01-01"}
        self.assertEqual(process_candidate(value, self.deps)["status"], "PUBLISHED")
        with self.assertRaisesRegex(StateRejected, "SOURCE_CHANGED"):
            process_candidate({**value, "url": "https://other.example/article"}, self.deps)

    def test_failed_lead_does_not_substitute_corroborating_article(self):
        self.fetch.side_effect = FetchRejected("ACCESS_DENIED")
        value = {**self.candidate, "corroboratingSources": [{"url": "https://other.example/a"}]}
        self.assertEqual(process_candidate(value, self.deps)["reason"], "ACCESS_DENIED")
        self.assertEqual(self.fetch.call_count, 1)
        self.store.reserve.assert_not_called()

    def test_failed_corroboration_remains_visible_in_terminal_receipt(self):
        self.fetch.side_effect = [valid_body(), FetchRejected("TIMEOUT")]
        value = {**self.candidate, "corroboratingSources": [{"url": "https://other.example/a"}]}
        result = process_candidate(value, self.deps)
        self.assertEqual(result["status"], "PUBLISHED")
        self.assertEqual(result["fetchWarnings"], ["CORROBORATION_FETCH_FAILED"])

    def test_retrieval_preserves_original_query_order_and_encoding(self):
        url = "https://example.com/a?z=2&tag=b&tag=a&signed=x%2fy&utm_source=receipt"
        result = process_candidate({**self.candidate, "url": url}, self.deps)
        self.assertEqual(result["status"], "PUBLISHED")
        self.fetch.assert_called_once_with(url, https_only=True)

    def test_http_optional_source_does_not_discard_valid_https_lead(self):
        value = {**self.candidate, "corroboratingSources": [{"url": "http://other.example/a"}]}
        result = process_candidate(value, self.deps)
        self.assertEqual(result["status"], "PUBLISHED")
        self.assertEqual(result["fetchWarnings"], ["CORROBORATION_UNUSABLE"])
        self.fetch.assert_called_once()

    def test_optional_redirect_to_canonical_lead_is_ignored_with_warning(self):
        other = replace(valid_body(), final_url="https://example.com/a?utm_source=copy")
        self.fetch.side_effect = [valid_body(), other]
        value = {**self.candidate, "corroboratingSources": [{"url": "https://other.example/a"}]}
        result = process_candidate(value, self.deps)
        self.assertEqual(result["status"], "PUBLISHED")
        self.assertEqual(result["fetchWarnings"], ["CORROBORATION_UNUSABLE"])
        self.client.converse.assert_called_once()

    def test_optional_amp_copy_with_same_publisher_canonical_is_ignored(self):
        other = replace(valid_body(), final_url="https://example.com/amp/a")
        self.fetch.side_effect = [valid_body(), other]
        value = {**self.candidate, "corroboratingSources": [{"url": other.final_url}]}
        result = process_candidate(value, self.deps)
        self.assertEqual(result["status"], "PUBLISHED")
        self.assertEqual(result["fetchWarnings"], ["CORROBORATION_UNUSABLE"])
        row = self.store.finish.call_args.kwargs["document"]
        self.assertEqual(row["corroboratingSourceCount"], 0)

    def test_budget_or_duplicate_rejection_prevents_model_and_uploads(self):
        self.store.published_hashes.return_value = {"known"}
        self.assertEqual(process_candidate(self.candidate, self.deps)["status"], "DUPLICATE")
        self.store.published_hashes.return_value = set()
        self.store.reserve.side_effect = StateRejected("BUDGET_PAUSED")
        with self.assertRaisesRegex(StateRejected, "BUDGET_PAUSED"):
            process_candidate(self.candidate, self.deps)
        self.client.converse.assert_not_called()
        self.deps.s3.put_object.assert_not_called()

    def test_model_timeout_settles_uncertain_usage_without_publishing_objects(self):
        self.client.converse.side_effect = ReadTimeoutError(endpoint_url="https://synthetic.invalid")
        result = process_candidate(self.candidate, self.deps)
        self.assertEqual(result["status"], "REJECTED")
        finish = self.store.finish.call_args.kwargs
        self.assertFalse(finish["usage_complete"])
        self.assertEqual(finish["known_cost"], Decimal(0))
        self.deps.s3.put_object.assert_not_called()

    def test_render_bound_rejection_settles_known_cost_without_uploads(self):
        with mock.patch("news_worker.render_news_markdown", side_effect=ValueError("size")):
            result = process_candidate(self.candidate, self.deps)
        self.assertEqual(result["reason"], "RENDER_REJECTED")
        self.assertTrue(self.store.finish.call_args.kwargs["usage_complete"])
        self.assertGreater(self.store.finish.call_args.kwargs["known_cost"], 0)
        self.deps.s3.put_object.assert_not_called()

    def test_source_change_after_upload_only_settles_failure_and_preserves_immutable_objects(self):
        self.store.finish.side_effect = [StateRejected("SOURCE_CHANGED"), {"status": "FAILED"}]
        self.assertEqual(process_candidate(self.candidate, self.deps)["status"], "FAILED")
        self.assertEqual(self.store.finish.call_args.kwargs["reason"], "SOURCE_CHANGED")
        self.assertNotIn("document", self.store.finish.call_args.kwargs)
        self.deps.s3.delete_object.assert_not_called()

    def test_ambiguous_upload_keeps_reservation_and_never_claims_terminal_success(self):
        self.deps.s3.put_object.side_effect = ReadTimeoutError(endpoint_url="https://synthetic.invalid")
        with self.assertRaises(ReadTimeoutError):
            process_candidate(self.candidate, self.deps)
        self.store.reserve.assert_called_once()
        self.store.finish.assert_not_called()
        self.deps.s3.delete_object.assert_not_called()

    def test_time_exhaustion_after_first_upload_never_publishes(self):
        remaining = [900]
        self.deps.remaining = lambda: remaining[0]
        self.deps.s3.put_object.side_effect = lambda **kw: remaining.__setitem__(0, 20)
        with self.assertRaisesRegex(StateRejected, "RUN_BUDGET"):
            process_candidate(self.candidate, self.deps)
        self.assertEqual(self.deps.s3.put_object.call_count, 1)
        self.store.finish.assert_not_called()

    def test_last_upload_crossing_freshness_window_settles_without_publication(self):
        published = int(datetime(2026, 8, 8, 9, tzinfo=timezone.utc).timestamp())
        current = [published + 7 * 86400 - 1]
        self.deps.now = lambda: current[0]
        uploads = []
        def put(**kw):
            uploads.append(kw["Key"])
            if len(uploads) == 2:
                current[0] += 2
        self.deps.s3.put_object.side_effect = put
        result = process_candidate(self.candidate, self.deps)
        self.assertEqual(len(uploads), 2)
        self.assertEqual(result["status"], "REJECTED")
        self.assertEqual(result["reason"], "STALE_ARTICLE")
        self.assertNotIn("document", self.store.finish.call_args.kwargs)
        self.deps.s3.delete_object.assert_not_called()


if __name__ == "__main__":
    unittest.main()
