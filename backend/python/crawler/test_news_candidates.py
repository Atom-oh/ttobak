"""Candidate identity and public-source query tests."""

import unittest
from datetime import date, datetime, timezone

import news_pipeline


def candidate(title, url, published_at, source_id='woori', source_name='우리은행'):
    """Minimal candidate dict fixture with fixed dates (no wall-clock
    dependency) for TestCandidateDiscovery's grouping test."""
    return {
        'candidateId': f'{source_id}:{url}',
        'kind': 'news',
        'sourceId': source_id,
        'sourceName': source_name,
        'aliases': [],
        'keywords': [],
        'title': title,
        'url': url,
        'canonicalUrl': url,
        'publishedAt': published_at,
        'discoveredAt': '2026-08-09T00:00:00Z',
        'queryClass': 'identity',
        'searchResultRank': 1,
        'ingestSource': 'search',
        'requireRelevance': True,
        'corroboratingSources': [],
    }

class TestCandidateDiscovery(unittest.TestCase):
    def test_non_utf8_query_octets_never_collapse_into_replacement_identity(self):
        for query in ("%B0%A1", "%B3%AA"):
            with self.subTest(query=query), self.assertRaises(UnicodeError):
                news_pipeline.canonicalize_url("https://example.com/article?id=" + query)
        first = news_pipeline.canonicalize_url("https://example.com/article?id=%EA%B0%80")
        second = news_pipeline.canonicalize_url("https://example.com/article?id=%EB%82%98")
        self.assertNotEqual(first, second)
        self.assertNotEqual(news_pipeline.document_hash(first), news_pipeline.document_hash(second))

    def test_malformed_group_member_does_not_discard_valid_neighbor(self):
        good = candidate("valid article", "https://example.com/a?utm_source=x", "2026-08-08")
        for bad in (None, {**good, "url": "ftp://example.com"},
                    {**good, "aliases": [3]}, {**good, "title": []},
                    {**good, "url": "https://example.com/?id=%B0%A1"}):
            with self.subTest(bad=bad):
                groups = news_pipeline.group_near_duplicates([bad, good])
                self.assertEqual(len(groups), 1)
                self.assertEqual(groups[0]["canonicalUrl"], "https://example.com/a")

    def test_empty_root_and_slash_have_the_same_identity(self):
        bare = news_pipeline.canonicalize_url("https://example.com")
        slash = news_pipeline.canonicalize_url("https://example.com/")
        self.assertEqual(bare, slash)
        self.assertEqual(news_pipeline.document_hash(bare), news_pipeline.document_hash(slash))
        self.assertEqual(news_pipeline.candidate_id("source", bare),
                         news_pipeline.candidate_id("source", slash))

    def test_grouping_does_not_forward_search_snippets_or_nested_payloads(self):
        lead = candidate("same title", "https://example.com/a", "2026-08-08")
        lead.update(snippet="search snippet", body="not fetched", unexpected={"raw": "payload"})
        lead["corroboratingSources"] = [{"url": "https://example.com/b", "snippet": "snippet"}]
        group = news_pipeline.group_near_duplicates([lead])[0]
        self.assertNotIn("snippet", group)
        self.assertNotIn("body", group)
        self.assertNotIn("unexpected", group)
        self.assertEqual(group["corroboratingSources"], [])

    def test_unsafe_or_ambiguous_urls_are_rejected(self):
        for url in ("https://user:pass@example.com/x", "https://example.com/\nx",
                    "https://example.com:0/", "https://example.com:99999/",
                    "https://example.com\\@127.0.0.1/", "https://%31%32%37.0.0.1/",
                    "https://[fe80::1%25eth0]/", "https://example.com/" + "a" * 4096):
            with self.subTest(url=url[:60]), self.assertRaises(ValueError):
                news_pipeline.canonicalize_url(url)

    def test_ipv6_identity_keeps_brackets_and_idna_is_stable(self):
        self.assertEqual(news_pipeline.canonicalize_url("https://[2606:4700:4700::1111]:443/a"),
                         "https://[2606:4700:4700::1111]/a")
        self.assertEqual(news_pipeline.canonicalize_url("https://bücher.example/a"),
                         "https://xn--bcher-kva.example/a")

    def test_unknown_or_future_dates_do_not_establish_freshness_or_duplicates(self):
        now = datetime(2026, 8, 9, tzinfo=timezone.utc)
        self.assertFalse(news_pipeline.is_within_days("2026-08-10", 7, now))
        for second_date in (None, "2026-08-15T00:00:01Z"):
            grouped = news_pipeline.group_near_duplicates([
                candidate("same title", "https://example.com/1", "2026-08-08"),
                candidate("same title", "https://example.com/2", second_date),
            ])
            self.assertEqual(len(grouped), 2)

    def test_alias_overlap_does_not_merge_separate_source_partitions(self):
        a = candidate("same title", "https://example.com/a", "2026-08-08", source_id="a")
        b = candidate("same title", "https://example.com/b", "2026-08-08", source_id="b")
        self.assertEqual(len(news_pipeline.group_near_duplicates([a, b])), 2)

    def test_public_terms_and_quadratic_grouping_are_bounded(self):
        with self.assertRaises(ValueError):
            news_pipeline.expand_queries("name", ["alias"] * 100, [], date(2026, 8, 9))
        with self.assertRaises(ValueError):
            news_pipeline.expand_queries("name", [], ["a" * 201], date(2026, 8, 9))
        with self.assertRaises(ValueError):
            news_pipeline.group_near_duplicates([{}] * 301)

    def test_canonicalize_url_removes_tracking_and_sorts_query(self):
        got = news_pipeline.canonicalize_url(
            "HTTPS://Example.COM:443/a?utm_source=x&id=7&b=2#top")
        self.assertEqual(got, "https://example.com/a?b=2&id=7")

    def test_canonicalize_url_removes_configured_tracking_params(self):
        got = news_pipeline.canonicalize_url(
            "https://example.com/a?gclid=abc&fbclid=def&msclkid=ghi&x=1")
        self.assertEqual(got, "https://example.com/a?x=1")

    def test_canonicalize_url_keeps_non_default_port(self):
        got = news_pipeline.canonicalize_url("http://example.com:8080/a")
        self.assertEqual(got, "http://example.com:8080/a")

    def test_canonicalize_url_rejects_non_http_scheme(self):
        with self.assertRaises(ValueError):
            news_pipeline.canonicalize_url("ftp://example.com/a")

    def test_query_expansion_uses_only_public_config(self):
        queries = news_pipeline.expand_queries(
            source_name="우리은행",
            aliases=["Woori Bank"],
            keywords=["AI"],
            after_date=date(2026, 8, 2),
        )
        query_text = "\n".join(q.text for q in queries)
        self.assertIn('"우리은행"', query_text)
        self.assertIn('"Woori Bank"', query_text)
        self.assertIn("after:2026-08-02", query_text)
        self.assertNotIn("meeting", query_text.lower())
        self.assertLessEqual(len(queries), news_pipeline.MAX_QUERIES_PER_SOURCE)

    def test_query_expansion_caps_at_max_queries_per_source(self):
        queries = news_pipeline.expand_queries(
            source_name="우리은행",
            aliases=["Woori Bank", "우리금융", "WooriFG"],
            keywords=["AI", "클라우드", "빅데이터", "핀테크", "보안"],
            after_date=date(2026, 8, 2),
        )
        self.assertLessEqual(len(queries), news_pipeline.MAX_QUERIES_PER_SOURCE)

    def test_query_expansion_with_no_source_name_uses_keywords(self):
        queries = news_pipeline.expand_queries(
            source_name="", aliases=[], keywords=["클라우드"],
            after_date=date(2026, 8, 2))
        self.assertTrue(any("클라우드" in q.text for q in queries))

    def test_is_within_days_seven_day_freshness(self):
        now = datetime(2026, 8, 9, tzinfo=timezone.utc)
        self.assertTrue(news_pipeline.is_within_days(
            "2026-08-05T00:00:00Z", 7, now))
        self.assertFalse(news_pipeline.is_within_days(
            "2026-07-01T00:00:00Z", 7, now))

    def test_is_within_days_thirty_day_fallback(self):
        now = datetime(2026, 8, 9, tzinfo=timezone.utc)
        self.assertTrue(news_pipeline.is_within_days(
            "2026-07-15T00:00:00Z", 30, now))

    def test_is_within_days_unparseable_fails_closed(self):
        now = datetime(2026, 8, 9, tzinfo=timezone.utc)
        self.assertFalse(news_pipeline.is_within_days("not-a-date", 7, now))
        for value in ("0001-01-01T00:00:00+14:00", "9999-12-31T23:59:59-14:00"):
            with self.subTest(value=value):
                self.assertIsNone(news_pipeline.parse_published_at(value))
                self.assertFalse(news_pipeline.is_within_days(value, 7, now))

    def test_exact_url_dedup_identity_is_stable_for_same_canonical_url(self):
        a = news_pipeline.document_hash(
            news_pipeline.canonicalize_url("https://example.com/a?b=1&utm_source=x"))
        b = news_pipeline.document_hash(
            news_pipeline.canonicalize_url("https://EXAMPLE.com/a?utm_source=y&b=1"))
        self.assertEqual(a, b)

    def test_near_duplicates_become_one_event_group(self):
        grouped = news_pipeline.group_near_duplicates([
            candidate("우리은행 AI 플랫폼 확대", "https://a.example/1", "2026-08-08"),
            candidate("우리은행 AI 플랫폼 확대 발표", "https://b.example/2", "2026-08-09"),
        ])
        self.assertEqual(len(grouped), 1)
        self.assertEqual(len(grouped[0]["corroboratingSources"]), 1)

    def test_dissimilar_titles_stay_separate_groups(self):
        grouped = news_pipeline.group_near_duplicates([
            candidate("우리은행 AI 플랫폼 확대", "https://a.example/1", "2026-08-08"),
            candidate("완전히 다른 기사 제목입니다", "https://b.example/2", "2026-08-09"),
        ])
        self.assertEqual(len(grouped), 2)

    def test_far_apart_dates_stay_separate_groups(self):
        grouped = news_pipeline.group_near_duplicates([
            candidate("우리은행 AI 플랫폼 확대", "https://a.example/1", "2026-08-08"),
            candidate("우리은행 AI 플랫폼 확대 발표", "https://b.example/2", "2026-09-01"),
        ])
        self.assertEqual(len(grouped), 2)

    def test_different_sources_never_grouped(self):
        grouped = news_pipeline.group_near_duplicates([
            candidate("우리은행 AI 플랫폼 확대", "https://a.example/1", "2026-08-08",
                      source_id='woori', source_name='우리은행'),
            candidate("우리은행 AI 플랫폼 확대 발표", "https://b.example/2", "2026-08-09",
                      source_id='other', source_name='다른회사'),
        ])
        self.assertEqual(len(grouped), 2)

    def test_custom_url_bypasses_only_relevance(self):
        item = news_pipeline.custom_url_candidate(
            source_id="woori", source_name="우리은행",
            url="https://example.com/curated", run_id="run-1")
        self.assertFalse(item["requireRelevance"])
        self.assertEqual(item["ingestSource"], "custom")

    def test_custom_url_candidate_still_has_full_contract_fields(self):
        item = news_pipeline.custom_url_candidate(
            source_id="woori", source_name="우리은행",
            url="https://example.com/curated", run_id="run-1")
        for field in ("candidateId", "kind", "sourceId", "sourceName",
                      "canonicalUrl", "discoveredAt", "queryClass",
                      "corroboratingSources"):
            self.assertIn(field, item)
