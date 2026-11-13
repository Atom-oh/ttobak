"""Collector tests with a dict-backed fake table and a patched feed fetcher."""
import hashlib
import re
import unittest
from datetime import datetime, timedelta, timezone
from unittest import mock

import update_collector as uc
from article_fetcher import FeedDocument, FetchRejected

NOW = datetime(2026, 11, 13, 10, 20, 47, tzinfo=timezone.utc)
RSS_TYPE = "application/rss+xml"


class CondFailed(Exception):
    response = {"Error": {"Code": "ConditionalCheckFailedException"}}


class FakeTable:
    """Supports only the DynamoDB calls the collector's IAM role allows."""

    def __init__(self, page=2):
        self.rows = {}
        self.page = page
        self.ops = []

    def put_item(self, Item, ConditionExpression=None):
        self.ops.append("put_item")
        key = (Item["PK"], Item["SK"])
        if ConditionExpression == "attribute_not_exists(PK)" and key in self.rows:
            raise CondFailed()
        self.rows[key] = dict(Item)

    def get_item(self, Key):
        self.ops.append("get_item")
        row = self.rows.get((Key["PK"], Key["SK"]))
        return {"Item": dict(row)} if row else {}

    def update_item(self, Key, UpdateExpression, ExpressionAttributeNames,
                    ExpressionAttributeValues):
        self.ops.append("update_item")
        row = self.rows.setdefault((Key["PK"], Key["SK"]), dict(Key))
        m = re.match(r"SET (.*?)(?: REMOVE (.*))?$", UpdateExpression)
        for part in re.split(r", (?![^(]*\))", m.group(1)):
            name, expr = part.split(" = ", 1)
            attr = ExpressionAttributeNames[name]
            inc = re.match(r"#f = if_not_exists\(#f, (:z)\) \+ (:one)", part)
            if inc:
                row[attr] = row.get(attr, 0) + ExpressionAttributeValues[":one"]
            else:
                row[attr] = ExpressionAttributeValues[expr]
        for name in (m.group(2) or "").split(", "):
            if name:
                row.pop(ExpressionAttributeNames[name], None)

    def query(self, KeyConditionExpression, ExpressionAttributeValues,
              ExclusiveStartKey=None):
        self.ops.append("query")
        assert KeyConditionExpression == "PK = :pk"
        pk = ExpressionAttributeValues[":pk"]
        items = sorted((r for (p, _), r in self.rows.items() if p == pk),
                       key=lambda r: r["SK"])
        start = 0
        if ExclusiveStartKey:
            start = [r["SK"] for r in items].index(ExclusiveStartKey["SK"]) + 1
        chunk = items[start:start + self.page]
        resp = {"Items": [dict(r) for r in chunk]}
        if start + self.page < len(items):
            resp["LastEvaluatedKey"] = {"PK": pk, "SK": chunk[-1]["SK"]}
        return resp

    def state(self, feed_hash):
        return self.rows.get(("CUFEED#" + feed_hash, "STATE"), {})

    def entries(self, feed_hash):
        return [r for (p, s), r in self.rows.items()
                if p == "CUFEED#" + feed_hash and s.startswith("ENTRY#")]


def register(table, feed_hash, url, kind="rss", user="u1", source="s1", status="active"):
    table.rows[("CUFEEDS", "%s#%s#%s" % (feed_hash, user, source))] = {
        "PK": "CUFEEDS", "SK": "%s#%s#%s" % (feed_hash, user, source),
        "feedHash": feed_hash, "url": url, "kind": kind, "userId": user,
        "sourceId": source, "status": status}


def rss(*items):
    body = "".join("<item><link>%s</link><title>%s</title><pubDate>%s</pubDate></item>" % i
                   for i in items)
    return "<rss><channel>%s</channel></rss>" % body


def pub(days):
    return (NOW - timedelta(days=days)).strftime("%a, %d %b %Y %H:%M:%S +0000")


def doc(text, ctype=RSS_TYPE, url="https://example.com/feed"):
    return FeedDocument(url, ctype, text)


def html(*links, feed=None):
    head = ('<link rel="alternate" type="application/rss+xml" href="%s">' % feed) if feed else ""
    return "<html><head>%s</head><body>%s</body></html>" % (
        head, "".join('<a href="%s">x</a>' % l for l in links))


class Ctx:
    def __init__(self, ms):
        self.ms = ms

    def get_remaining_time_in_millis(self):
        return self.ms


def run(table, docs, now=NOW, context=None):
    """docs: {url: FeedDocument | Exception | callable}."""
    calls = []

    def fake(url):
        calls.append(url)
        d = docs[url]
        if isinstance(d, Exception):
            raise d
        return d
    with mock.patch.object(uc, "fetch_feed_document", fake):
        result = uc.collect(uc.FeedStore(table), now, context)
    return result, calls


def assert_no_empty_time(case, table):
    for row in table.rows.values():
        for key in ("publishedAt", "seenAt", "updatedAt", "lastFetchedAt"):
            if key in row:
                case.assertRegex(row[key], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")


class TestCollector(unittest.TestCase):
    def test_two_rows_one_hash_fetch_once(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/feed", user="u1")
        register(t, "h1", "https://a.example/feed", user="u2")
        result, calls = run(t, {"https://a.example/feed": doc(rss())})
        self.assertEqual(calls, ["https://a.example/feed"])
        self.assertEqual(result, {"feeds": 1, "fetched": 1, "entries": 0, "skipped": 0, "failed": 0})

    def test_disabled_rows_ignored(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/feed", status="disabled")
        result, calls = run(t, {})
        self.assertEqual(calls, [])
        self.assertEqual(result["feeds"], 0)

    def test_rss_recent_entries_and_second_run(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/feed")
        feed = rss(("https://a.example/new", "New", pub(1)),
                   ("https://a.example/old", "Old", pub(5)))
        result, _ = run(t, {"https://a.example/feed": doc(feed)})
        self.assertEqual(result["entries"], 1)
        entries = t.entries("h1")
        self.assertEqual(len(entries), 1)
        e = entries[0]
        self.assertEqual(e["SK"], "ENTRY#" + hashlib.sha256(b"https://a.example/new").hexdigest())
        self.assertEqual(e["pendingShareExpiresAt"], int(NOW.timestamp()) + 7 * 86400)
        self.assertEqual(e["seenAt"], "2026-11-13T10:20:47Z")
        self.assertIn("publishedAt", e)
        st = t.state("h1")
        self.assertEqual(st["updatedAt"], "2026-11-13T10:20:47Z")
        self.assertEqual(st["failures"], 0)
        self.assertNotIn("lastError", st)
        later = NOW + timedelta(hours=1)
        result, _ = run(t, {"https://a.example/feed": doc(feed)}, now=later)
        self.assertEqual(result["entries"], 0)
        st = t.state("h1")
        self.assertEqual(st["updatedAt"], "2026-11-13T10:20:47Z")
        self.assertEqual(st["lastFetchedAt"], "2026-11-13T11:20:47Z")
        assert_no_empty_time(self, t)

    def test_html_with_feed_link_stores_resolved_url(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        feed = rss(("https://a.example/p", "P", pub(1)))
        docs = {"https://a.example/": doc(html(feed="/feed.xml"), "text/html", "https://a.example/"),
                "https://a.example/feed.xml": doc(feed)}
        result, calls = run(t, docs)
        self.assertEqual(t.state("h1")["resolvedUrl"], "https://a.example/feed.xml")
        self.assertEqual(result["entries"], 1)
        _, calls = run(t, docs, now=NOW + timedelta(hours=1))
        self.assertEqual(calls, ["https://a.example/feed.xml"])

    def test_site_baseline_then_new_links(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        first = doc(html("/a", "/b"), "text/html", base)
        result, _ = run(t, {base: first})
        self.assertEqual(result["entries"], 0)
        self.assertEqual(t.entries("h1"), [])
        self.assertEqual(sorted(t.state("h1")["links"]),
                         ["https://a.example/a", "https://a.example/b"])
        self.assertNotIn("updatedAt", t.state("h1"))
        second = doc(html("/a", "/b", "/c", "/d"), "text/html", base)
        result, _ = run(t, {base: second}, now=NOW + timedelta(hours=1))
        self.assertEqual(result["entries"], 2)
        urls = sorted(e["url"] for e in t.entries("h1"))
        self.assertEqual(urls, ["https://a.example/c", "https://a.example/d"])
        for e in t.entries("h1"):
            self.assertNotIn("publishedAt", e)
        self.assertIn("updatedAt", t.state("h1"))
        assert_no_empty_time(self, t)

    def test_site_writes_at_most_30(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        run(t, {base: doc(html("/a"), "text/html", base)})
        many = doc(html(*["/p%d" % i for i in range(60)]), "text/html", base)
        result, _ = run(t, {base: many}, now=NOW + timedelta(hours=1))
        self.assertEqual(result["entries"], 30)

    def test_youtube_html_without_feed_is_no_feed(self):
        t = FakeTable()
        register(t, "h1", "https://www.youtube.com/@x", kind="youtube")
        url = "https://www.youtube.com/@x"
        result, _ = run(t, {url: doc(html("/a"), "text/html", url)})
        self.assertEqual(result["failed"], 1)
        st = t.state("h1")
        self.assertEqual(st["lastError"], "no_feed")
        self.assertEqual(st["failures"], 1)
        self.assertNotIn("links", st)
        self.assertEqual(t.entries("h1"), [])

    def test_fetch_rejected_reasons_and_loop_continues(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        register(t, "h2", "https://a.example/2")
        register(t, "h3", "https://a.example/3")
        docs = {"https://a.example/1": FetchRejected("content_type"),
                "https://a.example/2": FetchRejected("TIMEOUT"),
                "https://a.example/3": doc(rss(("https://a.example/p", "P", pub(1))))}
        result, calls = run(t, docs)
        self.assertEqual(len(calls), 3)
        self.assertEqual(result["failed"], 2)
        self.assertEqual(result["fetched"], 1)
        self.assertEqual(t.state("h1")["lastError"], "content_type")
        self.assertEqual(t.state("h2")["lastError"], "fetch")
        self.assertEqual(t.state("h2")["failures"], 1)
        self.assertNotIn("updatedAt", t.state("h1"))
        docs["https://a.example/1"] = FetchRejected("too_large")
        run(t, docs, now=NOW + timedelta(hours=1))
        self.assertEqual(t.state("h1")["lastError"], "too_large")
        self.assertEqual(t.state("h1")["failures"], 2)

    def test_parse_error_and_success_resets(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/feed")
        run(t, {"https://a.example/feed": doc("<not-a-feed/>")})
        self.assertEqual(t.state("h1")["lastError"], "parse")
        run(t, {"https://a.example/feed": doc(rss())}, now=NOW + timedelta(hours=1))
        st = t.state("h1")
        self.assertEqual(st["failures"], 0)
        self.assertNotIn("lastError", st)

    def test_backoff_skips_recent_after_five_failures(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/feed")
        t.rows[("CUFEED#h1", "STATE")] = {
            "PK": "CUFEED#h1", "SK": "STATE", "failures": 5,
            "lastFetchedAt": "2026-11-13T00:00:00Z"}
        result, calls = run(t, {})
        self.assertEqual(calls, [])
        self.assertEqual(result["skipped"], 1)
        t.rows[("CUFEED#h1", "STATE")]["lastFetchedAt"] = "2026-11-11T00:00:00Z"
        result, calls = run(t, {"https://a.example/feed": doc(rss())})
        self.assertEqual(len(calls), 1)
        self.assertEqual(result["fetched"], 1)

    def test_unexpected_exception_does_not_stop_loop(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        register(t, "h2", "https://a.example/2")
        docs = {"https://a.example/1": RuntimeError("boom with https://secret.example"),
                "https://a.example/2": doc(rss())}
        result, _ = run(t, docs)
        self.assertEqual(result["failed"], 1)
        self.assertEqual(result["fetched"], 1)
        self.assertEqual(t.state("h1")["lastError"], "fetch")

    def test_state_read_failure_is_contained_to_that_feed(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        register(t, "h2", "https://a.example/2")
        real = t.get_item

        def flaky(Key):
            if Key["PK"] == "CUFEED#h1":
                raise RuntimeError("throttled")
            return real(Key)
        t.get_item = flaky
        result, calls = run(t, {"https://a.example/2": doc(rss())})
        self.assertEqual(calls, ["https://a.example/2"])
        self.assertEqual(result["failed"], 1)
        self.assertEqual(result["fetched"], 1)

    def test_malformed_state_values_do_not_abort(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        register(t, "h2", "https://a.example/2")
        t.rows[("CUFEED#h1", "STATE")] = {"PK": "CUFEED#h1", "SK": "STATE",
                                          "failures": "abc", "lastFetchedAt": "garbage"}
        t.rows[("CUFEED#h2", "STATE")] = {"PK": "CUFEED#h2", "SK": "STATE",
                                          "failures": 9, "lastFetchedAt": 12345}
        docs = {"https://a.example/1": doc(rss()), "https://a.example/2": doc(rss())}
        result, _ = run(t, docs)
        self.assertEqual(result["fetched"], 2)

    def test_failure_write_throttled_is_contained(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        register(t, "h2", "https://a.example/2")
        real = t.update_item

        def flaky(Key, **kw):
            if Key["PK"] == "CUFEED#h1":
                raise RuntimeError("throttled")
            return real(Key, **kw)
        t.update_item = flaky
        docs = {"https://a.example/1": FetchRejected("TIMEOUT"),
                "https://a.example/2": doc(rss())}
        result, _ = run(t, docs)
        self.assertEqual(result["failed"], 1)
        self.assertEqual(result["fetched"], 1)

    def test_stops_when_time_is_short(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/1")
        result, calls = run(t, {}, context=Ctx(59000))
        self.assertEqual(calls, [])
        self.assertEqual(result["skipped"], 1)

    def test_registry_pagination_and_allowed_operations_only(self):
        t = FakeTable(page=2)
        for i in range(5):
            register(t, "h%d" % i, "https://a.example/%d" % i)
        docs = {"https://a.example/%d" % i: doc(rss()) for i in range(5)}
        result, _ = run(t, docs)
        self.assertEqual(result["feeds"], 5)
        self.assertLessEqual(set(t.ops), {"query", "get_item", "put_item", "update_item"})

    def test_logs_contain_no_urls(self):
        t = FakeTable()
        register(t, "h" * 40, "https://secret.example/feed")
        with self.assertLogs(uc.logger, level="INFO") as logs:
            run(t, {"https://secret.example/feed": FetchRejected("TIMEOUT")})
        text = "\n".join(logs.output)
        self.assertNotIn("secret.example", text)
        self.assertIn("h" * 12, text)


if __name__ == "__main__":
    unittest.main()
