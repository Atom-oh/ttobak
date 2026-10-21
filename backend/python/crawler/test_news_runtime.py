"""Invocation and recovery contracts; synthetic data only, no live services."""

import copy
from datetime import datetime, timezone
from decimal import Decimal
from email.message import Message
import http.client
import io
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

    def test_plain_json_and_sse_require_matching_jsonrpc_envelope(self):
        valid = {"jsonrpc": "2.0", "id": 1, "result": {"content": [{
            "type": "text", "text": json.dumps({"results": [{"url": "https://example.com/a"}]})}]}}
        invalid = [{key: value for key, value in valid.items() if key != "id"},
                   {**valid, "id": None}, {**valid, "id": 2}, {**valid, "id": "1"},
                   {**valid, "id": True}, {**valid, "jsonrpc": "1.0"},
                   {key: value for key, value in valid.items() if key != "jsonrpc"},
                   {**valid, "error": {"code": -1}}]
        for value in invalid:
            raw = json.dumps(value)
            for streaming in (False, True):
                with self.subTest(value=value, streaming=streaming):
                    if streaming:
                        headers = Message()
                        headers["Content-Type"] = "text/event-stream"
                        response = mock.Mock(status=200, headers=headers,
                                             read1=mock.Mock(side_effect=[
                                                 ("data: " + raw + "\n\n").encode(), b""]))
                    else:
                        response = self.response(raw.encode())
                    connection = mock.Mock()
                    with mock.patch.object(search, "_target", return_value=(
                            urlsplit(ENV["WEB_SEARCH_GATEWAY_URL"]), [])), \
                            mock.patch.object(search, "_open_response",
                                              return_value=(connection, response)):
                        self.assertEqual(self.make_search()("public", 25), ([], "SEARCH_FAILED"))
                    response.close.assert_called_once()
                    connection.close.assert_called_once()

    def test_chunked_sse_returns_matching_frame_without_waiting_for_stream_end(self):
        value = {"jsonrpc": "2.0", "id": 1, "result": {"content": [{
            "type": "text", "text": json.dumps({"results": [{"url": "https://example.com/한"}]},
                                             ensure_ascii=False)}]}}
        other = json.dumps({**value, "id": 2}).encode()
        event = b"data: " + json.dumps(value, ensure_ascii=False).encode() + b"\r\n\r\n"
        # Split a Korean code point and the final CRLF across real HTTP chunks.
        split = event.index("한".encode()) + 1
        chunks = [b"data: " + other + b"\n\n", event[:split], event[split:-3], event[-3:]]
        wire = b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n"
        wire += b"Content-Type: text/event-stream; charset=utf-8\r\n\r\n"
        wire += b"".join(f"{len(c):x}\r\n".encode() + c + b"\r\n" for c in chunks)

        class OpenStream(io.BytesIO):
            def readline(self, *args):
                if self.tell() == len(wire):
                    raise TimeoutError("Stream remains open after result")
                return super().readline(*args)

        stream = OpenStream(wire)
        response = http.client.HTTPResponse(mock.Mock(makefile=mock.Mock(return_value=stream)))
        response.begin()
        connection = mock.Mock()
        with mock.patch.object(search, "_target", return_value=(
                urlsplit(ENV["WEB_SEARCH_GATEWAY_URL"]), [])), \
                mock.patch.object(search, "_open_response", return_value=(connection, response)):
            self.assertEqual(self.make_search()("public", 25),
                             ([{"url": "https://example.com/한"}], None))
        self.assertTrue(stream.closed)
        connection.close.assert_called_once()

    def test_sse_bound_deadline_and_unrelated_id_do_not_return_a_result(self):
        message = Message()
        message["Content-Type"] = "text/event-stream"
        value = b'data: {"id":2,"result":{}}\n\n'
        for chunks, limit in (([value, b""], 2000000), ([b"x" * 20], 10)):
            response = mock.Mock(headers=message, read1=mock.Mock(side_effect=chunks))
            with mock.patch.object(search, "MAX_RESPONSE_BYTES", limit), \
                    self.assertRaises(ValueError):
                search._read_payload(response, float("inf"), None)
        response = mock.Mock(headers=message, read1=mock.Mock(return_value=b"data: {}\n\n"))
        with mock.patch.object(search.time, "monotonic", side_effect=[0, 21]), \
                self.assertRaises(ValueError):
            search._read_payload(response, 20, None)

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
        self.session = boto3.Session(region_name="ap-northeast-2")
        resource = self.session.resource("dynamodb")
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
                        mock.patch.object(runtime.boto3, "Session", return_value=self.session),
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
        session.resource.side_effect = self.session.resource
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

    def test_http_discovery_rejections_finalize_without_model_or_fetch(self):
        self.table.put_item(Item={**CONFIG, "customUrls": ["http://example.com/manual"]})
        values = [{"url": "http://example.com/news", "title": "Public news",
                   "publishedDate": datetime.fromtimestamp(NOW, timezone.utc).date().isoformat()}]
        with mock.patch.object(runtime, "NewsSearch", return_value=lambda query, maximum: (values, None)), \
                mock.patch.object(runtime, "WorkerDeps") as worker:
            discovery = runtime.discover_handler({"sourceId": "source", "runId": "http"}, context())
            self.assertEqual(discovery["candidates"], [])
            self.assertGreater(discovery["invalidRejected"], 1)
            receipt = runtime.finalize_handler({"discovery": discovery, "outcomes": []}, context())
            worker.assert_not_called()
        self.assertEqual(receipt["invalidRejected"], discovery["invalidRejected"])
        self.assertEqual(receipt["attemptCount"], 0)
        self.assertEqual(Decimal(receipt["chargedUsd"]), Decimal(0))

    def test_recovery_finishes_expired_runs_and_preserves_live_admission(self):
        runs = NewsRuns(self.store)
        runs.begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        runs.begin(Run("source", "live", source_revision(CONFIG)), NOW)
        result = recovery.handler({}, context())
        self.assertEqual(result["recovered"], 1)
        self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#expired")["runState"], "FINALIZED")
        self.assertEqual(self.store._get("CRAWLER#source", "NEWSRUN#live")["runState"], "OPEN")
        checkpoint = self.store._get(**{"pk": recovery.CHECKPOINT["PK"], "sk": "RECOVERY"})
        self.assertNotIn("leaseToken", checkpoint)
        self.assertNotIn("scanCursor", checkpoint)

    def test_invalid_endpoint_does_not_create_run(self):
        with mock.patch.dict(os.environ, {"WEB_SEARCH_GATEWAY_URL": "https://evil.example/mcp"}), \
                self.assertRaisesRegex(StateRejected, "INVALID_CONFIGURATION"):
            runtime.discover_handler({"sourceId": "source", "runId": "bad-config"}, context())
        self.assertIsNone(self.store._get("CRAWLER#source", "NEWSRUN#bad-config"))

    def test_corrupt_cursor_releases_lease_and_preserves_cursor(self):
        self.table.put_item(Item={**recovery.CHECKPOINT, "scanCursor": {"unexpected": "value"}})
        with self.assertRaisesRegex(StateRejected, "INVALID_RECOVERY_CURSOR"):
            recovery.recover(self.store, lambda: 900, NOW, "mine")
        checkpoint = self.store._get(recovery.CHECKPOINT["PK"], "RECOVERY")
        self.assertNotIn("leaseToken", checkpoint)
        self.assertEqual(checkpoint["scanCursor"], {"unexpected": "value"})

    def test_recovery_nested_calls_stop_within_lease_and_resume_same_page(self):
        NewsRuns(self.store).begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)
        clock = [0]
        original_get = self.store.client.get_item
        original_finish = NewsRuns.finish
        calls = []

        def slow_get(**kwargs):
            calls.append(clock[0])
            clock[0] += 40
            return original_get(**kwargs)

        def slow_finish(runs, run, now, **kwargs):
            # Model a long sequence of real native DynamoDB calls in finalization.
            for _ in range(10):
                runs.store._get(run.pk, run.sk)
            return original_finish(runs, run, now, **kwargs)

        with mock.patch.object(recovery.time, "monotonic", side_effect=lambda: clock[0]), \
                mock.patch.object(self.store.client, "get_item", side_effect=slow_get), \
                mock.patch.object(NewsRuns, "finish", slow_finish), \
                self.assertRaisesRegex(StateRejected, "RUN_BUDGET"):
            recovery.recover(self.store, lambda: 900, NOW, "mine")
        self.assertEqual(calls, [0, 40, 80, 120, 160, 200])
        checkpoint = self.store._get(recovery.CHECKPOINT["PK"], "RECOVERY")
        self.assertNotIn("leaseToken", checkpoint)
        self.assertNotIn("lastPageAt", checkpoint)
        self.assertEqual(recovery.recover(self.store, lambda: 900, NOW, "next")["recovered"], 1)

    def test_release_does_not_mask_failure_or_remove_replacement_lease(self):
        NewsRuns(self.store).begin(Run("source", "expired", source_revision(CONFIG)), NOW - 2000)

        def replaced(*args, **kwargs):
            self.table.update_item(
                Key=recovery.CHECKPOINT, UpdateExpression="SET leaseToken = :token",
                ExpressionAttributeValues={":token": "replacement"})
            raise StateRejected("INVALID_RUN_STATE")

        with mock.patch.object(NewsRuns, "finish", side_effect=replaced), \
                self.assertRaisesRegex(StateRejected, "INVALID_RUN_STATE"):
            recovery.recover(self.store, lambda: 900, NOW, "mine")
        self.assertEqual(self.store._get(recovery.CHECKPOINT["PK"], "RECOVERY")["leaseToken"],
                         "replacement")

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
