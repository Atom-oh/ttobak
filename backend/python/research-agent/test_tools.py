"""Unit tests for research-agent tools.py — web_search only (fetch_page/save_report unchanged)."""

import json
import os
import sys
import types
import unittest
from unittest import mock

os.environ['TABLE_NAME'] = 'test-table'
os.environ['KB_BUCKET_NAME'] = 'test-bucket'
os.environ['WEB_SEARCH_GATEWAY_URL'] = 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp'
os.environ['WEB_SEARCH_GATEWAY_REGION'] = 'us-east-1'

# strands is a container-only dependency (not installed in the test env).
# Stub strands.tools.tool as a passthrough decorator so tools.py imports.
if 'strands' not in sys.modules:
    strands_mod = types.ModuleType('strands')
    strands_tools_mod = types.ModuleType('strands.tools')
    strands_tools_mod.tool = lambda fn: fn
    strands_mod.tools = strands_tools_mod
    sys.modules['strands'] = strands_mod
    sys.modules['strands.tools'] = strands_tools_mod

import tools


class TestWebSearch(unittest.TestCase):
    """The real Gateway wraps the MCP CallToolResult under a top-level
    "result" key (JSON-RPC 2.0 envelope) — that's the shape mocked here."""

    @mock.patch('tools._sigv4_post')
    def test_returns_results_from_gateway(self, mock_post):
        mock_post.return_value = json.dumps({
            'jsonrpc': '2.0', 'id': 1,
            'result': {
                'content': [{'type': 'text', 'text': json.dumps({
                    'id': 'x',
                    'results': [{'text': 'snippet', 'url': 'https://example.com', 'title': 'T', 'publishedDate': '2026-07-01'}],
                })}],
                'isError': False,
            },
        })

        raw = tools.web_search('AWS Bedrock')
        parsed = json.loads(raw)

        self.assertEqual(len(parsed['results']), 1)
        self.assertEqual(parsed['results'][0]['url'], 'https://example.com')

    @mock.patch('tools._sigv4_post')
    def test_filters_results_missing_url(self, mock_post):
        mock_post.return_value = json.dumps({
            'jsonrpc': '2.0', 'id': 1,
            'result': {
                'content': [{'type': 'text', 'text': json.dumps({
                    'id': 'x',
                    'results': [{'text': 'no url'}, {'text': 'has url', 'url': 'https://example.com/2'}],
                })}],
                'isError': False,
            },
        })

        raw = tools.web_search('query')
        parsed = json.loads(raw)

        self.assertEqual(len(parsed['results']), 1)
        self.assertEqual(parsed['results'][0]['url'], 'https://example.com/2')

    @mock.patch('tools._sigv4_post')
    def test_returns_empty_results_on_error_without_leaking_exception_detail(self, mock_post):
        mock_post.side_effect = Exception('secret-internal-timeout-detail')

        raw = tools.web_search('query')
        parsed = json.loads(raw)

        self.assertEqual(parsed['results'], [])
        self.assertNotIn('secret-internal-timeout-detail', raw)


class TestSigv4PostConfigGuard(unittest.TestCase):
    def test_raises_when_gateway_url_unset(self):
        original = tools.WEB_SEARCH_GATEWAY_URL
        try:
            tools.WEB_SEARCH_GATEWAY_URL = ''
            with self.assertRaises(RuntimeError):
                tools._sigv4_post('{}')
        finally:
            tools.WEB_SEARCH_GATEWAY_URL = original


if __name__ == '__main__':
    unittest.main()
