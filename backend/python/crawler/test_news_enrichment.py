"""Structured news schema, citation, completion and rendering boundaries."""

import json
import html
import unittest
from dataclasses import replace
from decimal import Decimal
from unittest import mock

from botocore.exceptions import ClientError, ReadTimeoutError

import article_fetcher
import news_enrichment as enrichment
import news_renderer


def article_body(text, url='https://example.com/a'):
    return article_fetcher.ArticleBody(
        requested_url=url, final_url=url, canonical_url=url,
        title='기사 제목', published_at='2026-08-08T09:00:00Z',
        body=text, blocks=(text,))

def candidate_fixture(**overrides):
    base = {
        'runId': 'run-1',
        'candidateId': 'cand-1',
        'kind': 'news',
        'sourceId': 'woori',
        'sourceName': '우리은행',
        'aliases': ['Woori Bank'],
        'keywords': ['AI'],
        'title': '우리은행, 생성형 AI 플랫폼 확대',
        'url': 'https://example.com/a',
        'canonicalUrl': 'https://example.com/a',
        'publishedAt': '2026-08-08T09:00:00Z',
        'discoveredAt': '2026-08-09T00:00:00Z',
        'queryClass': 'identity',
        'searchResultRank': 1,
        'ingestSource': 'search',
        'requireRelevance': True,
        'corroboratingSources': [],
    }
    base.update(overrides)
    return base

def valid_enrichment_json(executive_summary=None, evidence_excerpt=None, **overrides):
    summary = executive_summary or ('핵심 사건과 배경 및 영향을 설명한다. ' + '요' * 650)
    excerpt = evidence_excerpt or '우리은행은 생성형 AI 플랫폼을 확대했다.'
    base = {
        'schemaVersion': 1,
        'kind': 'news',
        'title': '우리은행, 생성형 AI 플랫폼 확대',
        'executiveSummary': summary,
        'keyFacts': [
            {'fact': '우리은행이 AI 플랫폼을 확대했다', 'evidenceIds': ['S1'], 'confidence': 0.95},
            {'fact': '이번 확대는 디지털 전환 전략의 일부다', 'evidenceIds': ['S1'], 'confidence': 0.9},
            {'fact': '업계는 이를 긍정적으로 평가한다', 'evidenceIds': ['S1'], 'confidence': 0.8},
        ],
        'context': '우리은행은 이전에도 AI 투자를 지속해왔다.',
        'impact': {'business': ['경쟁력 강화'], 'technical': ['인프라 확장']},
        'risks': ['운영 리스크'],
        'opportunities': ['신규 고객 확보'],
        'awsRelevance': [],
        'meetingQuestions': ['고객사의 AI 전략 우선순위는 무엇인가?'],
        'recommendedActions': ['관련 미팅을 제안한다'],
        'evidence': [
            {'id': 'S1', 'sourceUrl': 'https://example.com/a',
             'supportingExcerpt': excerpt, 'publishedAt': '2026-08-08T09:00:00Z'},
        ],
        'tags': ['금융', 'AI'],
        'terms': [],
        'diagrams': [],
        'relevant': True,
        'relevanceConfidence': 0.9,
        'relevanceRationale': '기사의 주체가 설정된 고객사와 일치한다',
    }
    base.update(overrides)
    return base


def valid_body(text=None, url="https://example.com/a"):
    return article_body(text or ("우리은행은 생성형 AI 플랫폼을 확대했다. " + "가" * 1200), url)


def bedrock_response(value=None, stop="end_turn", usage=None):
    text = value if isinstance(value, str) else json.dumps(value or valid_enrichment_json(), ensure_ascii=False)
    return {
        "stopReason": stop,
        "output": {"message": {"content": [{"text": text}]}},
        "usage": usage if usage is not None else {"inputTokens": 100, "outputTokens": 50},
    }


class TestStructuredQuality(unittest.TestCase):
    def test_accepts_complete_source_bound_artifact_without_mutating_caller(self):
        raw = valid_enrichment_json()
        accepted = enrichment.validate_enrichment(raw, [valid_body()])
        accepted["evidence"][0]["sourceUrl"] = "changed"
        self.assertEqual(raw["evidence"][0]["sourceUrl"], "https://example.com/a")

    def test_every_persisted_field_has_a_bounded_schema(self):
        variants = {
            "unknown": "payload",
            "schemaVersion": True,
            "kind": "meeting",
            "title": "a" * 301,
            "executiveSummary": "short",
            "context": "",
            "impact": {"business": "not-list", "technical": []},
            "risks": "not-list",
            "opportunities": [{"unexpected": "object"}],
            "meetingQuestions": ["x"] * 9,
            "recommendedActions": ["x" * 501],
            "tags": ["x"] * 11,
            "awsRelevance": [{"service": "x", "rationale": "x", "confidence": float("nan")}],
            "terms": [{"rawBody": "unexpected"}],
            "diagrams": ["<script>bad</script>"],
            "relevant": "false",
            "relevanceConfidence": True,
            "relevanceRationale": "\ud800",
        }
        for key, value in variants.items():
            raw = valid_enrichment_json()
            raw[key] = value
            with self.subTest(key=key), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body()])

    def test_missing_required_fields_never_pass(self):
        for key in valid_enrichment_json():
            raw = valid_enrichment_json()
            del raw[key]
            with self.subTest(key=key), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body()])

    def test_citation_url_and_excerpt_must_belong_to_the_same_fetched_source(self):
        second = valid_body("다른 출처의 별도 기사 내용. " + "나" * 1200, "https://other.example/b")
        for url in ("https://never-fetched.example/a", second.final_url, "javascript:alert(1)"):
            raw = valid_enrichment_json()
            raw["evidence"][0]["sourceUrl"] = url
            with self.subTest(url=url), self.assertRaisesRegex(enrichment.QualityRejected, "EVIDENCE_NOT_FOUND"):
                enrichment.validate_enrichment(raw, [valid_body(), second])

    def test_duplicate_or_unhashable_evidence_ids_are_rejected(self):
        for identifier in ("S1", [], {"a": 1}, "S1\nbad"):
            raw = valid_enrichment_json()
            second = dict(raw["evidence"][0], id=identifier)
            raw["evidence"].append(second)
            with self.subTest(identifier=identifier), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body()])

    def test_date_is_source_metadata_not_model_invention(self):
        raw = valid_enrichment_json()
        raw["evidence"][0]["publishedAt"] = "2099-01-01"
        with self.assertRaises(enrichment.QualityRejected):
            enrichment.validate_enrichment(raw, [valid_body()])

    def test_all_facts_need_existing_evidence_and_finite_confidence(self):
        for changes in ({"evidenceIds": []}, {"evidenceIds": ["S2"]},
                        {"evidenceIds": ["S1", "S1"]}, {"confidence": float("inf")}):
            raw = valid_enrichment_json()
            raw["keyFacts"][0].update(changes)
            with self.subTest(changes=changes), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body()])

    def test_numeric_claim_must_appear_in_its_cited_excerpt(self):
        raw = valid_enrichment_json()
        raw["keyFacts"][0]["fact"] = "투자액은 300억원이다"
        with self.assertRaisesRegex(enrichment.QualityRejected, "NUMERIC_CLAIM_UNGROUNDED"):
            enrichment.validate_enrichment(raw, [valid_body()])
        raw["evidence"][0]["supportingExcerpt"] = "투자액은 300억원이다."
        enrichment.validate_enrichment(raw, [valid_body("투자액은 300억원이다. " + "가" * 1200)])

    def test_count_and_total_excerpt_limits(self):
        variants = []
        raw = valid_enrichment_json()
        raw["keyFacts"] = raw["keyFacts"][:2]
        variants.append(raw)
        raw = valid_enrichment_json()
        raw["meetingQuestions"], raw["recommendedActions"] = [], []
        variants.append(raw)
        raw = valid_enrichment_json(evidence_excerpt="가" * 181)
        variants.append(raw)
        raw = valid_enrichment_json()
        raw["evidence"] = [dict(raw["evidence"][0], id=f"S{i}", supportingExcerpt="가" * 170)
                           for i in range(1, 7)]
        variants.append(raw)
        for raw in variants:
            with self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body()])

    def test_long_quote_is_detected_between_old_sample_boundaries(self):
        source = "".join(chr(0x4e00 + i) for i in range(1200))
        raw = valid_enrichment_json(executive_summary="요" * 400 + source[13:214])
        raw["evidence"][0]["supportingExcerpt"] = source[:30]
        with self.assertRaises(enrichment.QualityRejected):
            enrichment.validate_enrichment(raw, [valid_body(source)])

    def test_whitespace_changes_cannot_disguise_rendered_verbatim_copying(self):
        source = " ".join(chr(0x4e00 + index) * 6 for index in range(200))
        for separator in ("\n", "\t", "\r\n", "\u2003", " \n\t "):
            raw = valid_enrichment_json(executive_summary=source[:700].replace(" ", separator))
            raw["evidence"][0]["supportingExcerpt"] = source[:30]
            with self.subTest(separator=separator), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(raw, [valid_body(source)])
        body = source.replace(" ", "\n\t")
        raw = valid_enrichment_json(executive_summary=source[:700])
        raw["evidence"][0]["supportingExcerpt"] = body[:30]
        with self.assertRaises(enrichment.QualityRejected):
            enrichment.validate_enrichment(raw, [valid_body(body)])

    def test_custom_urls_skip_only_relevance(self):
        raw = valid_enrichment_json(relevant=False, relevanceConfidence=0.1)
        with self.assertRaisesRegex(enrichment.QualityRejected, "NOT_RELEVANT"):
            enrichment.validate_enrichment(raw, [valid_body()])
        enrichment.validate_enrichment(raw, [valid_body()], require_relevance=False)
        raw["evidence"][0]["supportingExcerpt"] = "not in body"
        with self.assertRaisesRegex(enrichment.QualityRejected, "EVIDENCE_NOT_FOUND"):
            enrichment.validate_enrichment(raw, [valid_body()], require_relevance=False)

    def test_input_requires_bounded_fetched_identity(self):
        for bodies in ([], [valid_body()] * 4, [{"body": "untyped"}],
                       [article_body("short")], [valid_body(), valid_body()]):
            with self.subTest(count=len(bodies)), self.assertRaises(enrichment.QualityRejected):
                enrichment.validate_enrichment(valid_enrichment_json(), bodies)


class TestSynthesis(unittest.TestCase):
    def test_invalid_unicode_prompt_inputs_are_rejected_before_model_calls(self):
        client = mock.Mock()
        candidates = [candidate_fixture(**{key: "\ud800"}) for key in ("sourceName", "title")]
        candidates += [candidate_fixture(**{key: ["\udfff"]}) for key in ("aliases", "keywords")]
        candidates += [None, [], "invalid"]
        for candidate in candidates:
            with self.subTest(candidate=candidate), self.assertRaisesRegex(
                    enrichment.QualityRejected, "INVALID_SOURCE"):
                enrichment.enrich_article(candidate, [valid_body()], client)
        for body in (replace(valid_body(), body=valid_body().body + "\ud800"),
                     replace(valid_body(), published_at="\udfff"),
                     replace(valid_body(), final_url="https://example.com/\ud800"),
                     replace(valid_body(), final_url="https://example.com/a#\udfff")):
            with self.subTest(body_field="invalid UTF-8"), self.assertRaisesRegex(
                    enrichment.QualityRejected, "INVALID_SOURCE"):
                enrichment.enrich_article(candidate_fixture(), [body], client)
        client.converse.assert_not_called()

    def test_valid_unicode_prompt_context_remains_unchanged(self):
        candidate = candidate_fixture(sourceName="우리은행", title="새 소식 😀",
                                      aliases=["école-١"], keywords=["日本語"])
        value = json.loads(enrichment.build_prompt(candidate, [valid_body()], True))
        for key in ("sourceName", "title", "aliases", "keywords"):
            self.assertEqual(value["context"][key], candidate[key])

    def test_strict_json_rejects_trailing_data_duplicate_fields_and_nonfinite_values(self):
        for text in ('{"a":1} trailing', '{"a":1} {"a":2}', '{"a":1,"a":2}',
                     '{"a":NaN}', '[]', "x" * (enrichment.MAX_RESPONSE_BYTES + 1),
                     '{"a":' + "[" * 2000 + "0" + "]" * 2000 + "}"):
            with self.subTest(text=text[:40]), self.assertRaises(ValueError):
                enrichment.parse_json_object(text)
        self.assertEqual(enrichment.parse_json_object('```json\n{"a":1}\n```'), {"a": 1})
        self.assertEqual(enrichment.parse_json_object(json.dumps({"text": '["\\' * 100})),
                         {"text": '["\\' * 100})

    def test_first_success_has_bounded_tokens_and_source_urls_in_prompt(self):
        client = mock.Mock()
        client.converse.return_value = bedrock_response()
        _, usage = enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        self.assertEqual(usage, {"inputTokens": 100, "outputTokens": 50, "repairCount": 0})
        args = client.converse.call_args.kwargs
        self.assertEqual(args["inferenceConfig"]["maxTokens"], 6000)
        self.assertNotIn("temperature", args["inferenceConfig"])
        self.assertEqual(args["additionalModelRequestFields"], {"thinking": {"type": "disabled"}})
        value = json.loads(args["messages"][0]["content"][0]["text"])
        self.assertEqual(value["sources"][0]["url"], valid_body().final_url)
        self.assertEqual(value["sources"][0]["body"], valid_body().body)
        self.assertTrue(args["system"])

    def test_repair_uses_same_complete_bodies_and_accumulates_tokens(self):
        client = mock.Mock()
        client.converse.side_effect = [bedrock_response("not json"), bedrock_response()]
        _, usage = enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        self.assertEqual(usage, {"inputTokens": 200, "outputTokens": 100, "repairCount": 1})
        first, second = [call.kwargs["messages"][0]["content"][0]["text"]
                         for call in client.converse.call_args_list]
        self.assertTrue(second.startswith(first))
        self.assertIn("invalidOutput", second)

    def test_repair_includes_measured_length_error(self):
        client = mock.Mock()
        client.converse.side_effect = [
            bedrock_response(valid_enrichment_json(executive_summary="가" * 599)), bedrock_response()]
        enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        prompt = client.converse.call_args.kwargs["messages"][0]["content"][0]["text"]
        self.assertIn("characters=599; required=600..4000", prompt)

    def test_oversized_repair_keeps_consumed_tokens_without_another_call(self):
        client = mock.Mock()
        client.converse.return_value = bedrock_response("🙂" * 8000)
        candidate = candidate_fixture(
            sourceName="가" * 500, title="가" * 500,
            aliases=["가" * 200] * 28, keywords=["가" * 200] * 28)
        sources = [valid_body("가" * 29000, f"https://example.com/{n}") for n in range(3)]
        prompt = enrichment.build_prompt(candidate, sources, True)
        self.assertLessEqual(len(prompt.encode()), enrichment.MAX_PROMPT_BYTES)
        with self.assertRaisesRegex(enrichment.QualityRejected, "INVALID_SOURCE") as error:
            enrichment.enrich_article(candidate, sources, client)
        client.converse.assert_called_once()
        self.assertEqual(error.exception.usage,
                         {"inputTokens": 100, "outputTokens": 50, "repairCount": 1})

    def test_failed_repair_call_retains_known_usage_and_marks_unknown_charge(self):
        errors = [
            ReadTimeoutError(endpoint_url="https://bedrock.invalid"),
            ClientError({"Error": {"Code": "ThrottlingException", "Message": "fixture"}}, "Converse"),
        ]
        for failure in errors:
            client = mock.Mock()
            client.converse.side_effect = [bedrock_response("not JSON"), failure]
            with self.subTest(failure=type(failure)), self.assertRaisesRegex(
                    enrichment.QualityRejected, "MODEL_ERROR") as error:
                enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
            self.assertEqual(client.converse.call_count, 2)
            self.assertEqual(error.exception.usage,
                             {"inputTokens": 100, "outputTokens": 50,
                              "repairCount": 1, "usageComplete": False})

    def test_incomplete_responses_never_publish_even_when_json_looks_valid(self):
        for stop in ("max_tokens", "tool_use", "content_filtered", "stop_sequence", None):
            client = mock.Mock()
            client.converse.return_value = bedrock_response(stop=stop)
            with self.subTest(stop=stop), self.assertRaisesRegex(enrichment.QualityRejected, "INCOMPLETE_RESPONSE") as error:
                enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
            self.assertEqual(client.converse.call_count, 2)
            self.assertEqual(error.exception.usage["outputTokens"], 100)

    def test_rejected_schema_keeps_usage_and_does_not_make_third_call(self):
        client = mock.Mock()
        client.converse.return_value = bedrock_response("not JSON")
        with self.assertRaises(enrichment.QualityRejected) as error:
            enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        self.assertEqual(client.converse.call_count, 2)
        self.assertEqual(error.exception.usage["inputTokens"], 200)

    def test_unrelated_article_is_not_repaired_to_change_its_verdict(self):
        client = mock.Mock()
        client.converse.return_value = bedrock_response(valid_enrichment_json(relevant=False))
        with self.assertRaisesRegex(enrichment.QualityRejected, "NOT_RELEVANT"):
            enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        client.converse.assert_called_once()

    def test_negative_or_low_confidence_verdict_survives_malformed_other_fields(self):
        for raw in (valid_enrichment_json(relevant=False, executive_summary="short"),
                    valid_enrichment_json(relevanceConfidence=0.1, executive_summary="short"),
                    {"relevant": False}):
            client = mock.Mock()
            client.converse.side_effect = [bedrock_response(raw), bedrock_response()]
            with self.subTest(raw=raw), self.assertRaisesRegex(
                    enrichment.QualityRejected, "NOT_RELEVANT"):
                enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
            client.converse.assert_called_once()

    def test_low_confidence_is_terminal_with_missing_or_malformed_relevant(self):
        for flag in (None, "", "true", "false", 0, 1, [], {}):
            for missing in (False, True):
                raw = valid_enrichment_json(relevanceConfidence=0.1)
                if missing:
                    del raw["relevant"]
                else:
                    raw["relevant"] = flag
                client = mock.Mock()
                client.converse.side_effect = [bedrock_response(raw), bedrock_response()]
                with self.subTest(flag=flag, missing=missing), self.assertRaisesRegex(
                        enrichment.QualityRejected, "NOT_RELEVANT"):
                    enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
                client.converse.assert_called_once()

    def test_relevance_gate_requires_an_actual_boolean_before_model_calls(self):
        for flag in (None, 0, 1, "", "false", [], {}):
            client = mock.Mock()
            with self.subTest(flag=flag), self.assertRaisesRegex(
                    enrichment.QualityRejected, "INVALID_SOURCE"):
                enrichment.enrich_article(
                    candidate_fixture(requireRelevance=flag), [valid_body()], client)
            client.converse.assert_not_called()

    def test_evidence_uses_exact_fetched_url_without_mutating_model_output(self):
        url = "https://example.com/a?z=2&tag=b&tag=a&signed=x%2fy&utm_source=receipt"
        body = article_body(valid_body().body, url)
        raw = valid_enrichment_json()
        raw["evidence"][0]["sourceUrl"] = enrichment.canonicalize_url(url)
        accepted = enrichment.validate_enrichment(raw, [body])
        self.assertEqual(accepted["evidence"][0]["sourceUrl"], url)
        self.assertNotEqual(raw["evidence"][0]["sourceUrl"], url)

    def test_missing_usage_is_unknown_not_free(self):
        client = mock.Mock()
        client.converse.return_value = bedrock_response(usage={})
        with self.assertRaisesRegex(enrichment.QualityRejected, "USAGE_INVALID") as error:
            enrichment.enrich_article(candidate_fixture(), [valid_body()], client)
        self.assertFalse(error.exception.usage["usageComplete"])

    def test_joined_text_blocks_cannot_hide_trailing_model_output(self):
        client = mock.Mock()
        value = bedrock_response()
        value["output"]["message"]["content"].append({"text": " trailing untrusted content"})
        client.converse.return_value = value
        with self.assertRaises(enrichment.QualityRejected):
            enrichment.enrich_article(candidate_fixture(), [valid_body()], client)

    def test_estimates_require_explicit_verified_prices(self):
        usage = {"inputTokens": 1000, "outputTokens": 200}
        self.assertIsNone(enrichment.estimate_cost(usage))
        self.assertEqual(enrichment.estimate_cost(usage, "2", "10"), Decimal("0.004"))
        with self.assertRaises(ValueError):
            enrichment.estimate_cost(usage, "NaN", "10")


class TestRendering(unittest.TestCase):
    def test_expected_sections_and_citation_are_rendered(self):
        output = news_renderer.render_news_markdown(valid_enrichment_json(), candidate_fixture(), 1)
        for heading in ("핵심 요약", "주요 사실", "배경과 맥락", "영향", "리스크와 기회",
                        "AWS 관련성", "다음 미팅 질문", "권장 액션", "근거 및 출처"):
            self.assertIn("## " + heading, output)
        self.assertIn("[S1]", output)
        self.assertIn("[원문](<https://example.com/a>)", output)

    def test_untrusted_prose_cannot_add_html_or_markdown_structure(self):
        raw = valid_enrichment_json(title="<img src=x onerror=alert(1)>",
                                    context="safe\n# injected\n[x](javascript:alert(1))")
        raw["evidence"][0]["sourceUrl"] = "https://example.com/a)![x](https://evil.example)"
        output = news_renderer.render_news_markdown(raw, candidate_fixture(sourceName="\n# injected"), 1)
        self.assertNotIn("<img", output)
        self.assertNotIn("\n# injected", output)
        self.assertNotIn("[x](javascript:", output)
        self.assertNotIn("![x]", output)
        self.assertIn("[원문](<https://example.com/a)!%5Bx%5D(https://evil.example)>)", output)

    def test_source_links_preserve_query_order_encoding_and_ipv6_authority(self):
        for url in ("https://example.com/a?z=2&tag=b&tag=a&signed=x%2fy&utm_source=receipt",
                    "https://[2606:4700:4700::1111]/a",
                    "https://example.com/a?literal=&copy;&x=1"):
            raw = valid_enrichment_json()
            raw["evidence"][0]["sourceUrl"] = url
            output = news_renderer.render_news_markdown(raw, candidate_fixture(), 1)
            self.assertIn(f"[원문](<{url}>)", html.unescape(output))
            if "&copy;" in url:
                self.assertIn("literal=&amp;copy;&amp;x=1", output)

    def test_renderer_rejects_invalid_url_and_oversized_output(self):
        raw = valid_enrichment_json()
        raw["evidence"][0]["sourceUrl"] = "javascript:alert(1)"
        with self.assertRaises(ValueError):
            news_renderer.render_news_markdown(raw, candidate_fixture(), 1)
        raw = valid_enrichment_json(executive_summary="가" * 30000)
        with self.assertRaises(ValueError):
            news_renderer.render_news_markdown(raw, candidate_fixture(), 1)
