"""Local input bound and runtime-only Converse adapter; no real AWS calls."""

from decimal import Decimal
from pathlib import Path
import unittest
from unittest import mock

from news_enrichment import FINAL_MODEL_ID, MAX_REQUEST_BYTES, QualityRejected, enrich_article
from news_model import FRAMING_TOKENS, NewsModel, input_token_bound
from test_news_enrichment import bedrock_response, candidate_fixture, valid_body


def count_request():
    return {"modelId": FINAL_MODEL_ID, "input": {"converse": {
        "messages": [{"role": "user", "content": [{"text": "공개 기사 요약"}]}],
        "system": [{"text": "Use the source."}],
        "additionalModelRequestFields": {"thinking": {"type": "disabled"}},
    }}}


class TestNewsModel(unittest.TestCase):
    def setUp(self):
        self.runtime = mock.Mock()
        self.model = NewsModel(self.runtime)

    def test_bound_counts_every_utf8_byte_of_source_and_system_text(self):
        expected = FRAMING_TOKENS + len("공개 기사 요약".encode("utf-8")) + len(b"Use the source.")
        self.assertEqual(self.model.input_token_bound(**count_request()), {"inputTokens": expected})
        self.runtime.count_tokens.assert_not_called()
        self.runtime.converse.assert_not_called()

    def test_unsupported_fields_and_model_are_rejected(self):
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
        value = count_request()
        value["input"]["converse"]["additionalModelRequestFields"] = {}
        values.append(value)
        value = count_request()
        value["input"]["converse"]["messages"][0]["content"] = [{"text": "x" * (MAX_REQUEST_BYTES + 4097)}]
        values.append(value)
        for value in values:
            with self.assertRaisesRegex(QualityRejected, "INVALID_COUNT_REQUEST"):
                input_token_bound(value)

    def test_real_enrichment_request_passes_bound_and_converse_adapter(self):
        from news_worker import MeteredModel, ModelPolicy
        self.runtime.converse.return_value = bedrock_response()
        meter = MeteredModel(self.model, ModelPolicy(Decimal("5"), Decimal("25")), lambda: 900)
        artifact, _ = enrich_article(candidate_fixture(), [valid_body()], meter)
        self.assertEqual(artifact["schemaVersion"], 1)
        self.runtime.converse.assert_called_once()
        self.assertEqual(self.runtime.converse.call_args.kwargs["modelId"], FINAL_MODEL_ID)

    def test_converse_rejects_unbounded_or_foreign_requests(self):
        for value in ({"modelId": "another-model"}, {"modelId": FINAL_MODEL_ID}):
            with self.assertRaisesRegex(QualityRejected, "MODEL_BUDGET"):
                self.model.converse(**value)
        self.runtime.converse.assert_not_called()

    def test_adapter_has_no_network_or_signing_path(self):
        source = Path(__file__).with_name("news_model.py").read_text(encoding="utf-8")
        for token in ("bedrock-mantle", "SigV4", "urlopen", "http.client", "_open_response"):
            self.assertNotIn(token, source)


if __name__ == "__main__":
    unittest.main()
