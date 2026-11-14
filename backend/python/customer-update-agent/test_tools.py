import json
import logging
import os
import types
import unittest
from unittest import mock

import fetching
import tools

logging.getLogger("customer-update-agent").addHandler(logging.NullHandler())

QUERY = "하나은행 Bedrock 도입 사례"


def gateway_reply(results):
    return json.dumps({"jsonrpc": "2.0", "id": 1, "result": {
        "isError": False,
        "content": [{"type": "text", "text": json.dumps({"results": results})}],
    }})


class WebSearchTest(unittest.TestCase):
    def setUp(self):
        env = mock.patch.dict(os.environ, {
            "WEB_SEARCH_GATEWAY_URL": "https://gw.gateway.bedrock-agentcore.us-east-1.api.aws/mcp",
            "WEB_SEARCH_GATEWAY_REGION": "us-east-1"})
        env.start()
        self.addCleanup(env.stop)

    def search(self, reply, query=QUERY, max_results=5):
        with mock.patch.object(tools, "_sigv4_post", return_value=reply) as post:
            got = tools.web_search(query, max_results)
        return got, json.loads(post.call_args.args[0])

    def test_shapes_and_filters_results(self):
        results = [
            {"title": "T1\nline", "url": "https://a.example/1", "text": "s" * 900},
            {"title": "T2", "url": "http://b.example/2", "text": "insecure"},
            {"title": "T3", "url": "javascript:alert(1)", "text": "bad"},
            {"title": "T4", "url": "https://c.example/4 extra", "text": "space"},
            {"title": "T5", "url": "https://d.example/5"},
        ]
        got, body = self.search(gateway_reply(results))
        self.assertEqual([r["url"] for r in got], ["https://a.example/1", "https://d.example/5"])
        self.assertEqual(got[0], {"title": "T1 line", "url": "https://a.example/1",
                                  "snippet": "s" * 500})
        self.assertEqual(got[1]["snippet"], "")
        self.assertEqual(body["params"]["name"], "ttobak-web-search-tool___WebSearch")
        self.assertEqual(body["params"]["arguments"]["query"], QUERY)

    def test_max_results_capped_at_five(self):
        results = [{"title": f"T{i}", "url": f"https://a.example/{i}", "text": "x"}
                   for i in range(9)]
        got, body = self.search(gateway_reply(results), max_results=50)
        self.assertEqual(len(got), 5)
        self.assertEqual(body["params"]["arguments"]["maxResults"], 5)
        got, _ = self.search(gateway_reply(results), max_results=2)
        self.assertEqual(len(got), 2)

    def test_bad_max_results_clamped(self):
        results = [{"title": f"T{i}", "url": f"https://a.example/{i}", "text": "x"}
                   for i in range(9)]
        for value, expected in (("abc", 5), (None, 5), ([3], 5), (float("inf"), 5),
                                (float("nan"), 5), ("3", 3), (0, 1), (-4, 1), (2.9, 2)):
            with self.subTest(value=value):
                got, body = self.search(gateway_reply(results), max_results=value)
                self.assertEqual(body["params"]["arguments"]["maxResults"], expected)
                self.assertEqual(len(got), expected)

    def test_errors_raise_tool_error(self):
        for name, reply in {
            "jsonrpc_error": json.dumps({"jsonrpc": "2.0", "id": 1, "error": {"code": -1}}),
            "is_error": json.dumps({"result": {"isError": True, "content": []}}),
            "no_text": json.dumps({"result": {"content": []}}),
            "garbage": "<html>",
        }.items():
            with self.subTest(name):
                with self.assertRaises(tools.ToolError) as ctx:
                    self.search(reply)
                self.assertEqual(ctx.exception.code, "search_failed")

    def test_unconfigured_gateway(self):
        with mock.patch.dict(os.environ, {"WEB_SEARCH_GATEWAY_URL": ""}):
            with self.assertRaises(tools.ToolError) as ctx:
                tools.web_search(QUERY)
        self.assertEqual(ctx.exception.code, "search_unavailable")
        with mock.patch.dict(os.environ, {"WEB_SEARCH_GATEWAY_URL": "http://gw.example/mcp"}):
            with self.assertRaises(tools.ToolError):
                tools.web_search(QUERY)

    def test_logs_carry_no_query_text(self):
        results = [{"title": "T", "url": "https://a.example/1", "text": "x"}]
        with self.assertLogs("customer-update-agent", level="DEBUG") as logs:
            self.search(gateway_reply(results))
            with self.assertRaises(tools.ToolError):
                self.search("garbage")
        joined = "\n".join(logs.output)
        self.assertNotIn("하나은행", joined)
        self.assertNotIn("Bedrock", joined)
        self.assertNotIn("a.example", joined)
        self.assertIn("q#", joined)

    def test_query_ref_is_keyed(self):
        ref = tools._query_ref(QUERY)
        self.assertRegex(ref, r"^q#[0-9a-f]{12}/len=\d+$")
        self.assertNotIn(QUERY, ref)


class SseTest(unittest.TestCase):
    def test_extracts_response_frame(self):
        text = ('event: message\ndata: {"jsonrpc":"2.0","method":"notify"}\n\n'
                'event: message\ndata: {"jsonrpc":"2.0","id":1,"result":{}}\n\n')
        self.assertEqual(json.loads(tools._extract_sse_json(text))["id"], 1)

    def test_redirect_refused(self):
        handler = tools._NoRedirectHandler()
        with self.assertRaises(RuntimeError):
            handler.redirect_request(None, None, 302, "Found", {}, "https://evil.example/")


class FetchPublicTest(unittest.TestCase):
    def module(self, body=None, reject=None):
        class Rejected(Exception):
            def __init__(self, reason):
                super().__init__(reason)
                self.reason = reason

        calls = []

        def fetch_article(url, *, https_only=False):
            calls.append((url, https_only))
            if reject:
                raise Rejected(reject)
            return types.SimpleNamespace(title="제목", body=body)

        return types.SimpleNamespace(fetch_article=fetch_article, FetchRejected=Rejected), calls

    def test_text_is_capped(self):
        module, calls = self.module(body="가" * 20000)
        with mock.patch.object(fetching, "_module", module):
            text = tools.fetch_public("https://a.example/x")
        self.assertEqual(len(text), 8000)
        self.assertTrue(text.startswith("제목\n\n가"))
        self.assertEqual(calls, [("https://a.example/x", True)])

    def test_rejection_raises_tool_error(self):
        module, _ = self.module(reject="HTTP_403")
        with mock.patch.object(fetching, "_module", module):
            with self.assertRaises(tools.ToolError) as ctx:
                tools.fetch_public("https://a.example/x")
        self.assertEqual((ctx.exception.code, ctx.exception.reason), ("fetch_rejected", "HTTP_403"))


if __name__ == "__main__":
    unittest.main()
