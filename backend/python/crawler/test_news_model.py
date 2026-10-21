"""Mantle request binding and signed transport bounds; no real AWS calls."""

import json
import http.client
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

from botocore.credentials import Credentials

import article_fetcher
import news_model
from news_enrichment import FINAL_MODEL_ID, QualityRejected, enrich_article
from news_model import MANTLE_MODEL, MANTLE_URL, NewsModel, count_body
from test_news_enrichment import bedrock_response, candidate_fixture, valid_body
from test_article_fetcher import public_resolver


def count_request():
    return {"modelId": FINAL_MODEL_ID, "input": {"converse": {
        "messages": [{"role": "user", "content": [{"text": "공개 기사 요약"}]}],
        "system": [{"text": "Use the source."}],
        "additionalModelRequestFields": {"thinking": {"type": "disabled"}},
    }}}


class TestNewsModel(unittest.TestCase):
    def setUp(self):
        self.transport = mock.Mock()
        self.response = self.transport.request.return_value
        self.response.status = 200
        self.response.read.return_value = b'{"input_tokens":12}'
        self.runtime = mock.Mock()
        self.model = NewsModel(self.runtime, lambda: Credentials("synthetic", "synthetic"),
                               transport=self.transport)

    def test_count_body_preserves_complete_source_and_system_text(self):
        value = json.loads(count_body(count_request()))
        self.assertEqual(value["model"], MANTLE_MODEL)
        self.assertEqual(value["messages"][0]["content"][0]["text"], "공개 기사 요약")
        self.assertEqual(value["system"], [{"type": "text", "text": "Use the source."}])
        self.assertEqual(value["thinking"], {"type": "disabled"})

    def test_mantle_only_signed_no_redirect_no_retry_bounded_and_closed(self):
        self.assertEqual(self.model.count_tokens(**count_request()), {"inputTokens": 12})
        call = self.transport.request.call_args
        self.assertEqual(call.args, ("POST", MANTLE_URL))
        self.assertIn("/us-east-1/bedrock-mantle/aws4_request",
                      call.kwargs["headers"]["Authorization"])
        self.response.read.assert_called_once_with(4097, decode_content=False)
        self.response.close.assert_called_once()
        self.runtime.count_tokens.assert_not_called()

    def test_error_redirect_oversized_and_invalid_count_fail_closed(self):
        for status, data in ((302, b"redirect"), (403, b"private service error"),
                             (200, b"x" * 4097), (200, b'{"input_tokens":true}'),
                             (200, b'{"input_tokens":0}'), (200, b"[]")):
            self.response.status, self.response.read.return_value = status, data
            with self.subTest(status=status, size=len(data)), self.assertRaisesRegex(
                    QualityRejected, "^TOKEN_COUNT_FAILED$"):
                self.model.count_tokens(**count_request())
        self.assertEqual(self.response.close.call_count, 6)

    def test_unsupported_fields_and_model_never_reach_network(self):
        values = []
        value = count_request()
        value["modelId"] = "another-model"
        values.append(value)
        value = count_request()
        value["input"]["converse"]["toolConfig"] = {}
        values.append(value)
        value = count_request()
        value["input"]["converse"]["messages"][0]["content"] = [{"image": {}}]
        values.append(value)
        for value in values:
            with self.assertRaisesRegex(QualityRejected, "INVALID_COUNT_REQUEST"):
                self.model.count_tokens(**value)
        self.transport.request.assert_not_called()

    def test_real_enrichment_request_passes_count_and_converse_adapter(self):
        from news_worker import MeteredModel, ModelPolicy
        from decimal import Decimal
        self.runtime.converse.return_value = bedrock_response()
        meter = MeteredModel(self.model, ModelPolicy(Decimal("5"), Decimal("25")), lambda: 900)
        artifact, _ = enrich_article(candidate_fixture(), [valid_body()], meter)
        self.assertEqual(artifact["schemaVersion"], 1)
        self.runtime.converse.assert_called_once()
        counted = json.loads(self.transport.request.call_args.kwargs["body"])
        sent = self.runtime.converse.call_args.kwargs
        self.assertEqual(counted["messages"][0]["content"][0]["text"],
                         sent["messages"][0]["content"][0]["text"])

    def test_default_count_transport_kills_stalled_resolver_under_absolute_budget(self):
        model = NewsModel(self.runtime, lambda: Credentials("synthetic", "synthetic"))
        with tempfile.TemporaryDirectory() as folder:
            pid_file = Path(folder) / "count-resolver.pid"
            script = ("import os,time\n"
                      f"open({str(pid_file)!r},'w').write(str(os.getpid()))\n"
                      "time.sleep(60)\n")
            with mock.patch.object(article_fetcher, "_DNS_SCRIPT", script), \
                    mock.patch.object(news_model, "COUNT_SECONDS", 0.5):
                start = time.monotonic()
                with self.assertRaisesRegex(QualityRejected, "TOKEN_COUNT_FAILED"):
                    model.count_tokens(**count_request())
                self.assertLess(time.monotonic() - start, 3)
            pid = int(pid_file.read_text())
            with self.assertRaises(ChildProcessError):
                article_fetcher.os.waitpid(pid, article_fetcher.os.WNOHANG)
            with self.assertRaises(ProcessLookupError):
                article_fetcher.os.kill(pid, 0)

    def test_default_count_transport_rejects_body_trickle_after_total_deadline(self):
        clock = [0]
        data = [b"HTTP/1.1 200 OK\r\nContent-Length: 19\r\n\r\n{", b'"']
        connection = mock.Mock()
        class SlowSocket:
            def settimeout(self, value):
                pass
            def recv_into(self, buffer):
                chunk = data.pop(0)
                buffer[:len(chunk)] = chunk
                if len(data) == 0:
                    clock[0] = 21
                return len(chunk)
        def opened(parsed, answers, deadline, **request):
            self.assertEqual(request["method"], "POST")
            response = http.client.HTTPResponse(article_fetcher._ResponseSocket(SlowSocket(), deadline))
            response.begin()
            return connection, response
        model = NewsModel(self.runtime, lambda: Credentials("synthetic", "synthetic"))
        with mock.patch.object(article_fetcher.time, "monotonic", side_effect=lambda: clock[0]), \
                mock.patch.object(article_fetcher, "_resolve_addresses", side_effect=public_resolver), \
                mock.patch.object(news_model, "_open_response", side_effect=opened):
            with self.assertRaisesRegex(QualityRejected, "TOKEN_COUNT_FAILED"):
                model.count_tokens(**count_request())
        connection.close.assert_called_once()

    def test_default_count_transport_shares_deadline_across_header_and_body_writes(self):
        clock, timeouts = [0], []
        class SlowSocket:
            closed = False
            def settimeout(self, seconds):
                self.timeout = seconds
            def connect(self, address):
                clock[0] += 2
            def sendall(self, value):
                timeouts.append(self.timeout)
                elapsed = min(9, self.timeout)
                clock[0] += elapsed
                if elapsed < 9:
                    raise TimeoutError()
            def close(self):
                self.closed = True
        sock = SlowSocket()
        def resolve(*args):
            clock[0] += 4
            return public_resolver(*args)
        def wrap(*args, **kwargs):
            clock[0] += 2
            return sock
        context = mock.Mock()
        context.wrap_socket.side_effect = wrap
        model = NewsModel(self.runtime, lambda: Credentials("synthetic", "synthetic"))
        with mock.patch.object(article_fetcher.time, "monotonic", side_effect=lambda: clock[0]), \
                mock.patch.object(article_fetcher, "_resolve_addresses", side_effect=resolve), \
                mock.patch.object(article_fetcher.socket, "socket", return_value=sock), \
                mock.patch.object(article_fetcher.ssl, "create_default_context", return_value=context):
            with self.assertRaisesRegex(QualityRejected, "TOKEN_COUNT_FAILED"):
                model.count_tokens(**count_request())
        self.assertEqual(timeouts, [10, 3])
        self.assertEqual(clock[0], 20)
        self.assertTrue(sock.closed)

    def test_default_transport_preserves_the_signed_host_on_the_wire(self):
        writes = []
        wire = bytearray(b'HTTP/1.1 200 OK\r\nContent-Length: 19\r\n\r\n{"input_tokens":12}')
        class WireSocket:
            def settimeout(self, value):
                pass
            def connect(self, address):
                pass
            def sendall(self, value):
                writes.append(bytes(value))
            def recv_into(self, buffer):
                size = min(len(buffer), len(wire))
                buffer[:size] = wire[:size]
                del wire[:size]
                return size
            def close(self):
                pass
        sock, context = WireSocket(), mock.Mock()
        context.wrap_socket.return_value = sock
        model = NewsModel(self.runtime, lambda: Credentials("synthetic", "synthetic"))
        with mock.patch.object(article_fetcher, "_resolve_addresses", side_effect=public_resolver), \
                mock.patch.object(article_fetcher.socket, "socket", return_value=sock), \
                mock.patch.object(article_fetcher.ssl, "create_default_context", return_value=context):
            self.assertEqual(model.count_tokens(**count_request()), {"inputTokens": 12})
        headers = writes[0].lower()
        self.assertIn(b"\r\nhost: bedrock-mantle.us-east-1.api.aws\r\n", headers)
        self.assertNotIn(b"api.aws:443", headers)


if __name__ == "__main__":
    unittest.main()
