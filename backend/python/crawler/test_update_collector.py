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
        self.consistent_reads = []

    def put_item(self, Item, ConditionExpression=None):
        self.ops.append("put_item")
        key = (Item["PK"], Item["SK"])
        if ConditionExpression == "attribute_not_exists(PK)" and key in self.rows:
            raise CondFailed()
        self.rows[key] = dict(Item)

    def get_item(self, Key, ConsistentRead=False):
        self.ops.append("get_item")
        self.consistent_reads.append(ConsistentRead)
        row = self.rows.get((Key["PK"], Key["SK"]))
        return {"Item": dict(row)} if row else {}

    def update_item(self, Key, UpdateExpression, ExpressionAttributeNames,
                    ExpressionAttributeValues, ConditionExpression=None):
        self.ops.append("update_item")
        current = self.rows.get((Key["PK"], Key["SK"]), {})
        if ConditionExpression is not None:
            ok = False
            for clause in ConditionExpression.split(" OR "):
                absent = re.fullmatch(r"attribute_not_exists\((#\w+)\)", clause)
                equal = re.fullmatch(r"(#\w+) = (:\w+)", clause)
                if absent:
                    ok = ok or ExpressionAttributeNames[absent.group(1)] not in current
                elif equal:
                    attr = ExpressionAttributeNames[equal.group(1)]
                    ok = ok or (attr in current and
                                current[attr] == ExpressionAttributeValues[equal.group(2)])
                else:
                    raise AssertionError("unsupported condition " + clause)
            if not ok:
                raise CondFailed()
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


def run(table, docs, now=NOW, context=None, shuffle=lambda order: None):
    """docs: {url: FeedDocument | Exception | callable}. Feed order is kept."""
    calls = []

    def fake(url):
        calls.append(url)
        d = docs[url]
        if isinstance(d, Exception):
            raise d
        return d
    with mock.patch.object(uc, "fetch_feed_document", fake):
        result = uc.collect(uc.FeedStore(table), now, context, shuffle=shuffle)
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

    def test_large_page_makes_progress_and_never_redelivers(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        # 298 links (+ /seed and /late = page_links' 300 cap) of ~1.5 KB each: well over 200,000 bytes in total.
        pad = "x" * 1500
        paths = ["/%03d-%s" % (i, pad) for i in range(298)]
        run(t, {base: doc(html("/seed"), "text/html", base)})
        page = doc(html("/seed", *paths), "text/html", base)
        self.assertGreater(sum(len(p) for p in paths), 200000)
        delivered = []
        for n in range(1, 13):
            result, _ = run(t, {base: page}, now=NOW + timedelta(hours=n))
            delivered.append(result["entries"])
        self.assertEqual(delivered, [30] * 9 + [28, 0, 0])
        urls = [e["url"] for e in t.entries("h1")]
        self.assertEqual(len(urls), 298)
        self.assertEqual(len(set(urls)), 298)
        self.assertEqual(len(t.state("h1")["links"]), 299)
        # A link appended later is collected.
        page = doc(html("/seed", *paths, "/late"), "text/html", base)
        result, _ = run(t, {base: page}, now=NOW + timedelta(hours=20))
        self.assertEqual(result["entries"], 1)
        self.assertIn("https://a.example/late", [e["url"] for e in t.entries("h1")])

    def test_stored_links_cap_keeps_every_current_page_link(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        old = ["%016x" % i for i in range(uc.MAX_STORED_LINK_IDS + 500)]
        uc.FeedStore(t).update_state("h1", NOW, links=old)
        current = ["/p%d" % i for i in range(300)]
        result, _ = run(t, {base: doc(html(*current), "text/html", base)},
                        now=NOW + timedelta(hours=1))
        self.assertEqual(result["entries"], 30)
        stored = t.state("h1")["links"]
        self.assertEqual(len(stored), uc.MAX_STORED_LINK_IDS)
        ids = set(stored)
        # Delivered links stay; undelivered current links stay new.
        delivered = [e["url"] for e in t.entries("h1")]
        self.assertTrue(all(uc.link_id(u) in ids for u in delivered))
        self.assertEqual(sum(uc.link_id("https://a.example" + p) in ids for p in current), 30)

    def test_unusable_stored_link_values_do_not_crash(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        uc.FeedStore(t).update_state(
            "h1", NOW, links=["https://a.example/a", "", None, 7, "zz"])
        result, _ = run(t, {base: doc(html("/a", "/b"), "text/html", base)},
                        now=NOW + timedelta(hours=1))
        self.assertEqual(result["failed"], 0)
        # A legacy URL value is treated as that link's identity.
        self.assertEqual([e["url"] for e in t.entries("h1")], ["https://a.example/b"])

    def test_site_baseline_then_new_links(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        first = doc(html("/a", "/b"), "text/html", base)
        result, _ = run(t, {base: first})
        self.assertEqual(result["entries"], 0)
        self.assertEqual(t.entries("h1"), [])
        self.assertEqual(sorted(t.state("h1")["links"]),
                         sorted(uc.link_id(u) for u in ("https://a.example/a", "https://a.example/b")))
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

    def test_site_writes_at_most_30_and_rest_later(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        base = "https://a.example/"
        run(t, {base: doc(html("/a"), "text/html", base)})
        many = doc(html(*["/p%d" % i for i in range(60)]), "text/html", base)
        result, _ = run(t, {base: many}, now=NOW + timedelta(hours=1))
        self.assertEqual(result["entries"], 30)
        self.assertNotIn(uc.link_id("https://a.example/p30"), t.state("h1")["links"])
        self.assertIn(uc.link_id("https://a.example/a"), t.state("h1")["links"])
        result, _ = run(t, {base: many}, now=NOW + timedelta(hours=2))
        self.assertEqual(result["entries"], 30)
        self.assertEqual(len(t.entries("h1")), 60)
        result, _ = run(t, {base: many}, now=NOW + timedelta(hours=3))
        self.assertEqual(result["entries"], 0)

    def test_site_kind_wins_when_subscribers_disagree(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="rss", user="u1")
        register(t, "h1", "https://a.example/", kind="site", user="u2")
        self.assertEqual(uc.FeedStore(t).active_feeds(), {"h1": ("https://a.example/", "site")})

    def test_failing_resolved_url_is_dropped_then_rediscovered(self):
        t = FakeTable()
        register(t, "h1", "https://a.example/", kind="site")
        page = doc(html(feed="/feed.xml"), "text/html", "https://a.example/")
        feed = doc(rss(("https://a.example/p", "P", pub(1))))
        run(t, {"https://a.example/": page, "https://a.example/feed.xml": feed})
        self.assertEqual(t.state("h1")["resolvedUrl"], "https://a.example/feed.xml")
        docs = {"https://a.example/": page, "https://a.example/feed.xml": FetchRejected("HTTP_404")}
        result, calls = run(t, docs, now=NOW + timedelta(hours=1))
        self.assertEqual((result["failed"], calls), (1, ["https://a.example/feed.xml"]))
        self.assertEqual(t.state("h1")["failures"], 1)
        self.assertNotIn("resolvedUrl", t.state("h1"))
        docs["https://a.example/feed.xml"] = feed
        result, calls = run(t, docs, now=NOW + timedelta(hours=2))
        self.assertEqual(calls, ["https://a.example/", "https://a.example/feed.xml"])
        self.assertEqual(t.state("h1")["resolvedUrl"], "https://a.example/feed.xml")

    def test_feed_order_is_shuffled(self):
        t = FakeTable()
        for i in range(3):
            register(t, "h%d" % i, "https://a.example/%d" % i)
        docs = {"https://a.example/%d" % i: doc(rss()) for i in range(3)}
        _, calls = run(t, docs, shuffle=lambda order: order.reverse())
        self.assertEqual(calls, ["https://a.example/2", "https://a.example/1", "https://a.example/0"])

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

        def flaky(Key, ConsistentRead=False):
            if Key["PK"] == "CUFEED#h1":
                raise RuntimeError("throttled")
            return real(Key, ConsistentRead)
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
        result, calls = run(t, {}, context=Ctx(269999))
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


class FlakyTable(FakeTable):
    """Raises a throttle-like error for chosen operations."""

    def __init__(self, fail=(), **kw):
        super().__init__(**kw)
        self.fail = set(fail)
        self.update_calls = 0

    def get_item(self, Key, ConsistentRead=False):
        if "get" in self.fail:
            raise RuntimeError("throttled")
        return super().get_item(Key, ConsistentRead)

    def put_item(self, Item, ConditionExpression=None):
        if "put" in self.fail:
            raise RuntimeError("throttled")
        return super().put_item(Item, ConditionExpression)

    def update_item(self, **kw):
        self.update_calls += 1
        if "update" in self.fail or ("update_after_marker" in self.fail and self.update_calls > 1):
            raise RuntimeError("throttled")
        return super().update_item(**kw)


URL = "https://a.example/feed"
LATER = NOW + timedelta(hours=1)


class TestHardening(unittest.TestCase):
    def feed(self):
        return doc(rss(("https://a.example/new", "New", pub(1))))

    def test_state_read_is_consistent(self):
        t = FakeTable()
        register(t, "h1", URL)
        run(t, {URL: self.feed()})
        self.assertEqual(t.consistent_reads, [True])

    def test_unacknowledged_run_published_by_next_run(self):
        t = FlakyTable(fail={"update_after_marker"})
        register(t, "h1", URL)
        result, _ = run(t, {URL: self.feed()})
        self.assertEqual(result["failed"], 1)
        self.assertEqual(len(t.entries("h1")), 1)
        self.assertEqual(t.state("h1")["writingAt"], "2026-11-13T10:20:47Z")
        self.assertNotIn("updatedAt", t.state("h1"))
        t.fail = set()
        result, _ = run(t, {URL: self.feed()}, now=LATER)
        self.assertEqual(result["entries"], 0)
        st = t.state("h1")
        self.assertEqual(st["updatedAt"], "2026-11-13T10:20:47Z")
        self.assertNotIn("writingAt", st)

    def test_normal_run_leaves_no_marker(self):
        t = FakeTable()
        register(t, "h1", URL)
        run(t, {URL: self.feed()})
        self.assertNotIn("writingAt", t.state("h1"))
        self.assertIn("updatedAt", t.state("h1"))

    def test_nothing_to_write_sets_no_marker(self):
        t = FakeTable()
        register(t, "h1", URL)
        run(t, {URL: doc(rss(("https://a.example/old", "Old", pub(9))))})
        self.assertNotIn("writingAt", t.state("h1"))
        self.assertNotIn("updatedAt", t.state("h1"))
        self.assertEqual(t.ops.count("update_item"), 1)

    def test_pending_marker_published_on_fetch_failure(self):
        t = FakeTable()
        register(t, "h1", URL)
        t.rows[("CUFEED#h1", "STATE")] = {"PK": "CUFEED#h1", "SK": "STATE",
                                          "writingAt": "2026-11-13T09:00:00Z"}
        run(t, {URL: FetchRejected("TIMEOUT")})
        st = t.state("h1")
        self.assertEqual(st["updatedAt"], "2026-11-13T09:00:00Z")
        self.assertNotIn("writingAt", st)
        self.assertEqual(st["lastError"], "fetch")

    def test_acknowledged_marker_does_not_move_updated_at(self):
        t = FakeTable()
        register(t, "h1", URL)
        t.rows[("CUFEED#h1", "STATE")] = {
            "PK": "CUFEED#h1", "SK": "STATE", "writingAt": "2026-11-13T09:00:00Z",
            "updatedAt": "2026-11-13T09:30:00Z"}
        run(t, {URL: doc(rss())})
        self.assertEqual(t.state("h1")["updatedAt"], "2026-11-13T09:30:00Z")
        self.assertNotIn("writingAt", t.state("h1"))

    def test_store_errors_are_not_feed_failures(self):
        for fail in ({"get"}, {"put"}, {"update"}):
            t = FlakyTable(fail=fail)
            register(t, "h1", URL)
            t.rows[("CUFEED#h1", "STATE")] = {"PK": "CUFEED#h1", "SK": "STATE",
                                              "failures": 2}
            result, _ = run(t, {URL: self.feed()})
            self.assertEqual(result["failed"], 1, fail)
            st = t.state("h1")
            self.assertEqual(st.get("failures"), 2, fail)
            self.assertNotIn("lastError", st, fail)

    def test_store_error_contained_to_feed(self):
        t = FlakyTable(fail={"get"})
        register(t, "h1", URL)
        result, _ = run(t, {URL: self.feed()})
        self.assertEqual(result["failed"], 1)
        self.assertEqual(result["feeds"], 1)

    def test_fetch_and_parse_failures_still_count(self):
        for exc_doc, reason in ((FetchRejected("TIMEOUT"), "fetch"),
                                (doc("not a feed at all"), "parse")):
            t = FakeTable()
            register(t, "h1", URL)
            result, _ = run(t, {URL: exc_doc})
            self.assertEqual(result["failed"], 1)
            self.assertEqual(t.state("h1")["lastError"], reason)
            self.assertEqual(t.state("h1")["failures"], 1)


class AwsError(Exception):
    """botocore ClientError lookalike with a code and a message."""

    def __init__(self, code, message="secret https://secret.example/key"):
        super().__init__(message)
        self.response = {"Error": {"Code": code, "Message": message}}


def state_row(**attrs):
    row = {"PK": "CUFEED#h1", "SK": "STATE"}
    row.update(attrs)
    return row


class TestMarkerFence(unittest.TestCase):
    def feed(self):
        return doc(rss(("https://a.example/new", "New", pub(1))))

    def test_young_foreign_marker_and_fetch_failure_kept(self):
        t = FakeTable()
        register(t, "h1", URL)
        young = uc._iso(NOW - timedelta(minutes=5))
        t.rows[("CUFEED#h1", "STATE")] = state_row(writingAt=young)
        result, _ = run(t, {URL: FetchRejected("TIMEOUT")})
        self.assertEqual(result["skipped"], 1)
        st = t.state("h1")
        self.assertEqual(st["writingAt"], young)
        self.assertNotIn("updatedAt", st)
        self.assertNotIn("lastError", st)

    def test_young_foreign_marker_skips_feed(self):
        t = FakeTable()
        register(t, "h1", URL)
        young = uc._iso(NOW - timedelta(minutes=20))
        t.rows[("CUFEED#h1", "STATE")] = state_row(writingAt=young)
        result, calls = run(t, {URL: self.feed()})
        self.assertEqual((result["skipped"], result["entries"], calls), (1, 0, []))
        self.assertEqual(t.entries("h1"), [])
        self.assertEqual(t.state("h1")["writingAt"], young)
        self.assertNotIn("update_item", t.ops)

    def test_old_marker_published(self):
        t = FakeTable()
        register(t, "h1", URL)
        old = uc._iso(NOW - timedelta(minutes=21))
        t.rows[("CUFEED#h1", "STATE")] = state_row(writingAt=old)
        result, _ = run(t, {URL: FetchRejected("TIMEOUT")})
        self.assertEqual(result["failed"], 1)
        st = t.state("h1")
        self.assertEqual(st["updatedAt"], old)
        self.assertNotIn("writingAt", st)

    def test_marker_set_after_read_is_not_published_or_cleared(self):
        t = FakeTable()
        register(t, "h1", URL)
        foreign = uc._iso(NOW - timedelta(minutes=1))

        def fake(url):
            t.rows[("CUFEED#h1", "STATE")] = state_row(writingAt=foreign)
            raise FetchRejected("TIMEOUT")
        with mock.patch.object(uc, "fetch_feed_document", fake):
            result = uc.collect(uc.FeedStore(t), NOW, None, shuffle=lambda o: None)
        self.assertEqual(result["failed"], 1)
        st = t.state("h1")
        self.assertEqual(st["writingAt"], foreign)
        self.assertNotIn("updatedAt", st)
        self.assertEqual(st["failures"], 1)

    def test_marker_set_after_read_blocks_own_marker(self):
        t = FakeTable()
        register(t, "h1", URL)
        foreign = uc._iso(NOW - timedelta(minutes=1))
        feed = self.feed()

        def fake(url):
            t.rows[("CUFEED#h1", "STATE")] = state_row(writingAt=foreign)
            return feed
        with mock.patch.object(uc, "fetch_feed_document", fake):
            result = uc.collect(uc.FeedStore(t), NOW, None, shuffle=lambda o: None)
        self.assertEqual((result["skipped"], result["entries"]), (1, 0))
        self.assertEqual(t.entries("h1"), [])
        self.assertEqual(t.state("h1")["writingAt"], foreign)


class EntryPutFails(FakeTable):
    def __init__(self, exc):
        super().__init__()
        self.exc = exc

    def put_item(self, Item, ConditionExpression=None):
        if self.exc is not None:
            self.ops.append("put_item")
            raise self.exc
        return super().put_item(Item, ConditionExpression)


class TestAmbiguousEntryPut(unittest.TestCase):
    def test_put_error_keeps_marker_then_fenced_publish(self):
        t = EntryPutFails(AwsError("InternalServerError"))
        register(t, "h" * 40, URL)
        feed = doc(rss(("https://a.example/new", "New", pub(1))))
        with self.assertLogs(uc.logger, level="INFO") as logs:
            result, _ = run(t, {URL: feed})
        text = "\n".join(logs.output)
        self.assertIn("h" * 12 + " store error code=InternalServerError", text)
        self.assertNotIn("secret", text)
        self.assertEqual(result["failed"], 1)
        st = t.state("h" * 40)
        self.assertEqual(st["writingAt"], uc._iso(NOW))
        self.assertNotIn("updatedAt", st)
        # Before the fence the marker is another run's: left alone.
        t.exc = None
        result, _ = run(t, {URL: feed}, now=NOW + timedelta(minutes=10))
        self.assertEqual(result["skipped"], 1)
        self.assertNotIn("updatedAt", t.state("h" * 40))
        # After the fence it is published; the entry is written and covered.
        result, _ = run(t, {URL: feed}, now=LATER)
        st = t.state("h" * 40)
        self.assertEqual(st["updatedAt"], uc._iso(LATER))
        self.assertNotIn("writingAt", st)
        self.assertEqual(len(t.entries("h" * 40)), 1)

    def test_store_error_keeps_botocore_code(self):
        for code in ("AccessDeniedException", "ProvisionedThroughputExceededException"):
            t = EntryPutFails(AwsError(code))
            with self.assertRaises(uc.StoreError) as cm:
                uc.FeedStore(t).put_entry("h1", "https://a.example/x", "", None, NOW)
            self.assertEqual(cm.exception.code, code)
            self.assertNotIn("secret", str(cm.exception))

    def test_conditional_failure_still_means_exists(self):
        t = FakeTable()
        store = uc.FeedStore(t)
        self.assertTrue(store.put_entry("h1", "https://a.example/x", "", None, NOW))
        self.assertFalse(store.put_entry("h1", "https://a.example/x", "", None, NOW))

    def test_entry_puts_use_separate_table(self):
        state_table, entry_table = FakeTable(), FakeTable()
        uc.FeedStore(state_table, entry_table).put_entry("h1", "https://a.example/x", "", None, NOW)
        self.assertEqual(len(entry_table.entries("h1")), 1)
        self.assertEqual(state_table.rows, {})

    def test_handler_builds_no_retry_entry_table(self):
        tables = []

        class Resource:
            def __init__(self, config):
                self.config = config

            def Table(self, name):
                t = FakeTable()
                t.config, t.name = self.config, name
                tables.append(t)
                return t

        def resource(service, config=None):
            self.assertEqual(service, "dynamodb")
            return Resource(config)
        stores = []
        with mock.patch("boto3.resource", resource), \
                mock.patch.dict("os.environ", {"TABLE_NAME": "tbl"}), \
                mock.patch.object(uc, "collect", lambda store, now, ctx: stores.append(store) or {}):
            uc.handler({}, None)
        store = stores[0]
        self.assertIsNone(store.table.config)
        cfg = store.entry_table.config
        self.assertIsNotNone(cfg)
        self.assertEqual(cfg.retries, {"max_attempts": 1, "mode": "standard"})
        self.assertEqual({t.name for t in tables}, {"tbl"})
        self.assertIsNot(store.table, store.entry_table)


if __name__ == "__main__":
    unittest.main()
