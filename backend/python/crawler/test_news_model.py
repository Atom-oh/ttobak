"""Mantle request binding and signed transport bounds; no real AWS calls."""

import json
import unittest
from unittest import mock

from botocore.credentials import Credentials

from news_enrichment import FINAL_MODEL_ID, QualityRejected
from news_model import MANTLE_MODEL, MANTLE_URL, NewsModel, count_body


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
        self.assertFalse(call.kwargs["redirect"])
        self.assertFalse(call.kwargs["retries"])
        self.assertFalse(call.kwargs["preload_content"])
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


if __name__ == "__main__":
    unittest.main()
