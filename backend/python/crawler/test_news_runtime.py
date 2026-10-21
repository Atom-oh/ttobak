"""Invocation and recovery contracts; synthetic data only, no live services."""

import copy
from decimal import Decimal
from email.message import Message
import json
import os
import unittest
from unittest import mock
from urllib.parse import urlsplit

import boto3
from botocore.credentials import Credentials

import news_recovery as recovery
import news_runtime as runtime
import news_search as search
from news_discovery import source_revision
from news_lifecycle import NewsRuns, Run
from news_store import NewsStore, StateRejected
from news_worker import WorkerDeps
from test_news_enrichment import bedrock_response, valid_body
from test_news_store import CONFIG, NOW, mock_aws

ENV = {
    "TABLE_NAME": "synthetic-runtime", "NEWS_PIPELINE_MODE": "canary",
    "NEWS_CANARY_SOURCE_ID": "source", "NEWS_PRICE_VERIFIED_ON": "2026-10-21",
    "NEWS_INPUT_USD_PER_MTOK": "5", "NEWS_OUTPUT_USD_PER_MTOK": "25",
    "NEWS_RUN_LIMIT_USD": "2", "NEWS_MONTH_LIMIT_USD": "25",
    "NEWS_ARTIFACT_BUCKET": "synthetic-private", "KB_BUCKET_NAME": "synthetic-kb",
    "WEB_SEARCH_GATEWAY_URL": "https://synthetic.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp",
}


def context():
    return mock.Mock(get_remaining_time_in_millis=mock.Mock(return_value=840000))


class TestInvocationBoundaries(unittest.TestCase):
    def test_disabled_and_foreign_canary_requests_do_not_create_clients(self):
        for mode, source in (("off", "source"), ("typo", "source"), ("canary", "foreign")):
            with mock.patch.dict(os.environ, {**ENV, "NEWS_PIPELINE_MODE": mode}, clear=True), \
                    mock.patch.object(runtime.boto3, "Session") as session:
                with self.assertRaisesRegex(StateRejected, "PIPELINE_DISABLED"):
                    runtime.discover_handler({"sourceId": source, "runId": "run"}, context())
                session.assert_not_called()

    def test_bad_identity_or_oversized_payload_fails_before_sdk(self):
        for event in ({"sourceId": "../source", "runId": "run"},
                      {"sourceId": "source", "runId": "a" * 129},
                      {"sourceId": "source", "runId": "run", "extra": True},
                      {"sourceId": "한" * 80000, "runId": "run"},
                      {"sourceId": "\ud800", "runId": "run"}):
            with mock.patch.object(runtime.boto3, "Session") as session, self.assertRaises(StateRejected):
                runtime.discover_handler(event, context())
            session.assert_not_called()

    def test_pricing_is_nonzero_current_and_can_reserve_worst_case(self):
        with mock.patch.dict(os.environ, ENV, clear=True):
            policy, run_limit, month_limit = runtime._policy(NOW)
            self.assertEqual(policy.reservation, Decimal("1.3"))
            self.assertEqual((run_limit, month_limit), (Decimal(2), Decimal(25)))
            for key, value in (("NEWS_PRICE_VERIFIED_ON", "2026-10-22"),
                               ("NEWS_PRICE_VERIFIED_ON", "2026-01-01"),
                               ("NEWS_INPUT_USD_PER_MTOK", "0"),
                               ("NEWS_OUTPUT_USD_PER_MTOK", "NaN"),
                               ("NEWS_RUN_LIMIT_USD", "1"),
                               ("NEWS_MONTH_LIMIT_USD", "1")):
                with mock.patch.dict(os.environ, {key: value}), self.assertRaisesRegex(
                        StateRejected, "INVALID_PRICING"):
                    runtime._policy(NOW)

    def test_no_retry_clients_and_monotonic_deadline_preserve_settlement_time(self):
        for config in (runtime.SDK_CONFIG, runtime.S3_CONFIG, runtime.MODEL_CONFIG):
            self.assertEqual(config.retries["total_max_attempts"], 1)
            self.assertLessEqual(config.connect_timeout, 5)
            self.assertLessEqual(config.read_timeout, 120)
        client = mock.Mock()
        wrapped = runtime.DeadlineClient(client, lambda: 9)
        with self.assertRaisesRegex(StateRejected, "RUN_BUDGET"):
            wrapped.get_item(TableName="synthetic")
        client.get_item.assert_not_called()
        with mock.patch.object(runtime.time, "monotonic", side_effect=[10, 20, 30]):
            left = runtime._remaining(mock.Mock(
                get_remaining_time_in_millis=mock.Mock(return_value=30000)))
            self.assertEqual(left(), 20)
            self.assertEqual(left(), 10)

    def test_workflow_result_money_is_exact_and_overflow_is_rejected(self):
        self.assertEqual(runtime._result({"chargedUsd": Decimal("0.012345678901")}),
                         {"chargedUsd": "0.012345678901"})
        with self.assertRaisesRegex(StateRejected, "RESULT_TOO_LARGE"):
            runtime._result({"data": "가" * 70000})


class TestSearchAdapter(unittest.TestCase):
    def make_search(self, remaining=100):
        return search.NewsSearch(ENV["WEB_SEARCH_GATEWAY_URL"],
                                 lambda: Credentials("synthetic-key", "synthetic-secret"),
                                 lambda: remaining)

    def response(self, data, headers=None, status=200):
        message = Message()
        for key, value in headers or [("Content-Length", str(len(data)))]:
            message.add_header(key, value)
        return mock.Mock(status=status, headers=message, read=mock.Mock(return_value=data))

    def test_plain_json_sse_and_signed_destination_have_equivalent_results(self):
        value = {"jsonrpc": "2.0", "id": 1, "result": {"content": [{
            "type": "text", "text": json.dumps({"results": [{"url": "https://example.com/a"}]})}]}}
        raw = json.dumps(value)
        for body in (raw, "event: message\ndata: " + raw + "\n\n"):
            response, connection = self.response(body.encode()), mock.Mock()
            with mock.patch.object(search, "_target", return_value=(
                    urlsplit(ENV["WEB_SEARCH_GATEWAY_URL"]), [])), \
                    mock.patch.object(search, "_open_response", return_value=(connection, response)) as opened:
                self.assertEqual(self.make_search()("공개 뉴스", 25),
                                 ([{"url": "https://example.com/a"}], None))
                kwargs = opened.call_args.kwargs
                self.assertEqual(kwargs["headers"]["host"],
                                 urlsplit(ENV["WEB_SEARCH_GATEWAY_URL"]).hostname)
                self.assertEqual(json.loads(kwargs["body"])["params"]["arguments"]["query"], "공개 뉴스")
                self.assertIn("Authorization", kwargs["headers"])
            response.close.assert_called_once()
            connection.close.assert_called_once()

    def test_endpoint_changes_and_short_deadlines_never_send_credentials(self):
        for url in ("http://example.com/mcp", "https://evil.example/mcp",
                    ENV["WEB_SEARCH_GATEWAY_URL"] + "?target=evil",
                    ENV["WEB_SEARCH_GATEWAY_URL"].replace("/mcp", ":443/mcp")):
            with self.assertRaises(ValueError):
                search.NewsSearch(url, mock.Mock(), lambda: 100)
        with mock.patch.object(search, "_open_response") as opened:
            self.assertEqual(self.make_search(30)("news", 25), ([], "SEARCH_DEADLINE"))
            opened.assert_not_called()

    def test_response_limits_and_header_ambiguity_fail_without_payload_logs(self):
        data = b'{"error":"private upstream text"}'
        cases = [
            self.response(data), self.response(data, status=302),
            self.response(data, [("Content-Length", "2"), ("Content-Length", "2")]),
            self.response(data, [("Content-Length", str(len(data))), ("Transfer-Encoding", "chunked")]),
            self.response(data, [("Content-Encoding", "gzip")]),
            self.response(b"x" * (search.MAX_RESPONSE_BYTES + 1), []),
        ]
        for response in cases:
            with mock.patch.object(search, "_target", return_value=(
                    urlsplit(ENV["WEB_SEARCH_GATEWAY_URL"]), [])), \
                    mock.patch.object(search, "_open_response", return_value=(mock.Mock(), response)), \
                    self.assertLogs(search.logger, level="WARNING") as logs:
                self.assertEqual(self.make_search()("private query", 25), ([], "SEARCH_FAILED"))
            self.assertNotIn("private", "\n".join(logs.output))


@unittest.skipUnless(mock_aws, "Install moto[dynamodb] for invocation transactions")
class TestRuntimeTransactions(unittest.TestCase):
    def setUp(self):
        active = mock_aws()
        active.start()
        self.addCleanup(active.stop)
        resource = boto3.Session().resource("dynamodb", region_name="ap-northeast-2")
        self.table = resource.create_table(
            TableName="synthetic-runtime",
            KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"},
                       {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": "PK", "AttributeType": "S"},
                                  {"AttributeName": "SK", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST")
        self.table.put_item(Item=copy.deepcopy(CONFIG))
        self.store = NewsStore(resource.meta.client, self.table.name)
        for patcher in (mock.patch.dict(os.environ, ENV, clear=True),
                        mock.patch.object(runtime, "_store", return_value=self.store),
                        mock.patch.object(runtime.time, "time", return_value=NOW)):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_discovery_to_finalization_and_duplicate_delivery(self):
        with mock.patch.object(runtime, "NewsSearch", return_value=lambda query, maximum: ([], None)) as gateway:
            result = runtime.discover_handler({"sourceId": "source", "runId": "run"}, context())
            self.assertEqual(result["candidates"], [])
            self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#run")["runState"], "OPEN")
            receipt = runtime.finalize_handler({"discovery": result, "outcomes": []}, context())
            self.assertEqual(receipt["status"], "COMPLETED")
            self.assertEqual(receipt["chargedUsd"], "0")
            again = runtime.discover_handler({"sourceId": "source", "runId": "run"}, context())
            self.assertEqual(again["status"], "FINALIZED")
            self.assertEqual(gateway.call_count, 1)
            self.assertEqual(runtime.finalize_handler({"discovery": again, "outcomes": []}, context()), receipt)

    def test_expired_run_does_not_restart_discovery(self):
        NewsRuns(self.store).begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        with mock.patch.object(runtime, "NewsSearch") as gateway, self.assertRaisesRegex(
                StateRejected, "RUN_CLOSED"):
            runtime.discover_handler({"sourceId": "source", "runId": "expired"}, context())
        gateway.assert_not_called()

    def test_discovery_candidate_reaches_worker_and_atomic_publication(self):
        config = {**CONFIG, "customUrls": ["https://example.com/a"]}
        self.table.put_item(Item=config)
        with mock.patch.object(runtime, "NewsSearch", return_value=lambda query, maximum: ([], None)):
            discovery = runtime.discover_handler({"sourceId": "source", "runId": "run"}, context())
        self.assertEqual(len(discovery["candidates"]), 1)
        model = mock.Mock()
        model.count_tokens.return_value = {"inputTokens": 100}
        model.converse.return_value = bedrock_response()
        s3 = mock.Mock()
        session = mock.Mock()
        session.client.return_value = s3
        with mock.patch.object(runtime.boto3, "Session", return_value=session), \
                mock.patch.object(runtime, "NewsModel", return_value=model), \
                mock.patch.object(runtime, "WorkerDeps", side_effect=lambda **kwargs: WorkerDeps(
                    **kwargs, fetch=lambda url, **options: valid_body(url=url))):
            outcome = runtime.worker_handler(discovery["candidates"][0], context())
        self.assertEqual(outcome["status"], "PUBLISHED")
        self.assertEqual(s3.put_object.call_count, 2)
        for call in s3.put_object.call_args_list:
            self.assertEqual(call.kwargs["IfNoneMatch"], "*")
        document = self.store._get("CRAWLER#source", "DOC#" + outcome["docHash"])
        self.assertEqual(document["qualityStatus"], "PUBLISHED")
        self.assertEqual(document["sourceRevision"], source_revision(config))
        model.converse.assert_called_once()
        # Rollback can disable new writers while allowing settlement/history.
        with mock.patch.dict(os.environ, {"NEWS_PIPELINE_MODE": "off"}):
            receipt = runtime.finalize_handler(
                {"discovery": discovery, "outcomes": [outcome]}, context())
        self.assertEqual(receipt["docsAdded"], 1)
        self.assertEqual(Decimal(receipt["chargedUsd"]), Decimal("0.00175"))

    def test_recovery_finishes_expired_runs_and_preserves_live_admission(self):
        runs = NewsRuns(self.store)
        runs.begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        runs.begin(Run("source", "live", source_revision(CONFIG)), NOW)
        result = recovery.recover(self.store, lambda: 120, NOW, "synthetic-token")
        self.assertEqual(result["recovered"], 1)
        self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#expired")["runState"], "FINALIZED")
        self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#live")["runState"], "OPEN")
        checkpoint = self.store._get(**{"pk": recovery.CHECKPOINT["PK"], "sk": "RECOVERY"})
        self.assertNotIn("leaseToken", checkpoint)
        self.assertNotIn("scanCursor", checkpoint)

    def test_recovery_budget_before_first_page_defers_without_claiming_completion(self):
        NewsRuns(self.store).begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        with mock.patch.object(self.store.client, "scan", wraps=self.store.client.scan) as scan:
            result = recovery.recover(self.store, lambda: 29, NOW, "mine")
        self.assertEqual(result["status"], "CONTINUE")
        self.assertEqual(result["pages"], 0)
        self.assertEqual(result["recovered"], 0)
        scan.assert_not_called()
        self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#expired")["runState"], "OPEN")
        resumed = recovery.recover(self.store, lambda: 120, NOW, "next")
        self.assertEqual(resumed["status"], "COMPLETE")
        self.assertEqual(resumed["recovered"], 1)

    def test_recovery_cursor_resumes_and_concurrent_lease_is_busy(self):
        NewsRuns(self.store).begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        self.table.put_item(Item={**recovery.CHECKPOINT, "leaseToken": "other", "leaseUntil": NOW + 50})
        self.assertEqual(recovery.recover(self.store, lambda: 120, NOW, "mine")["status"], "BUSY")
        self.table.delete_item(Key=recovery.CHECKPOINT)
        total = 0
        with mock.patch.object(recovery, "PAGE_SIZE", 1), mock.patch.object(recovery, "MAX_PAGES", 1):
            for _ in range(10):
                result = recovery.recover(self.store, lambda: 120, NOW, "mine")
                total += result["recovered"]
                if result["status"] == "COMPLETE":
                    break
        self.assertEqual(total, 1)
        self.assertEqual(result["status"], "COMPLETE")


if __name__ == "__main__":
    unittest.main()
