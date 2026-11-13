import unittest
from datetime import datetime, timedelta, timezone

from update_feeds import FeedEntry, FeedError, discover_feed_url, page_links, parse_feed, recent

RSS = """<?xml version="1.0"?>
<rss version="2.0"><channel><title>t</title>
<item><title>  Hello
   World </title><link>https://example.com/a</link>
<pubDate>Mon, 10 Nov 2026 09:00:00 GMT</pubDate></item>
<item><title>Insecure</title><link>http://example.com/b</link></item>
<item><title>No link</title></item>
<item><title>Bad date</title><link>https://example.com/c</link><pubDate>garbage</pubDate></item>
</channel></rss>"""

ATOM = """<feed xmlns="http://www.w3.org/2005/Atom">
<entry><title>Atom One</title><link rel="self" href="https://example.com/self"/>
<link rel="alternate" href="https://example.com/atom1"/>
<updated>2026-11-10T09:00:00Z</updated></entry>
<entry><title>Atom Two</title><link href="https://example.com/atom2"/>
<published>2026-11-09T09:00:00+09:00</published><updated>2026-11-11T00:00:00Z</updated></entry>
<entry><title>Naive</title><link href="https://example.com/atom3"/><updated>2026-11-09T09:00:00</updated></entry>
</feed>"""

YT = """<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015">
<entry><id>yt:video:abc</id><yt:videoId>abc</yt:videoId><title>Vid</title>
<link rel="alternate" href="https://www.youtube.com/watch?v=abc"/>
<published>2026-11-10T01:02:03+00:00</published></entry></feed>"""


class ParseFeedTest(unittest.TestCase):
    def test_rss(self):
        e = parse_feed(RSS)
        self.assertEqual([x.url for x in e], ["https://example.com/a", "https://example.com/c"])
        self.assertEqual(e[0].title, "Hello World")
        self.assertEqual(e[0].published_at, datetime(2026, 11, 10, 9, tzinfo=timezone.utc))
        self.assertIsNone(e[1].published_at)

    def test_atom(self):
        e = parse_feed(ATOM)
        self.assertEqual([x.url for x in e],
                         ["https://example.com/atom1", "https://example.com/atom2", "https://example.com/atom3"])
        self.assertEqual(e[0].published_at, datetime(2026, 11, 10, 9, tzinfo=timezone.utc))
        self.assertEqual(e[1].published_at.utcoffset(), timedelta(hours=9))
        self.assertEqual(e[2].published_at, datetime(2026, 11, 9, 9, tzinfo=timezone.utc))

    def test_youtube(self):
        e = parse_feed(YT)
        self.assertEqual(e[0].url, "https://www.youtube.com/watch?v=abc")
        self.assertEqual(e[0].title, "Vid")

    def test_refuses_dtd_and_entities_case_insensitive(self):
        for text in ('<!DOCTYPE x><rss/>', '<!doctype x><rss/>', '<!Entity a "b"><rss/>',
                     '<?xml version="1.0"?><!DOCTYPE r [<!ENTITY a "b">]><rss/>'):
            with self.assertRaises(FeedError) as cm:
                parse_feed(text)
            self.assertEqual(str(cm.exception), "parse")

    def test_malformed_and_unexpected_root(self):
        for text in ("<rss><channel>", "", "not xml", "<html><body/></html>"):
            with self.assertRaises(FeedError):
                parse_feed(text)

    def test_title_capped_and_entry_cap(self):
        items = "".join(
            f"<item><title>{'x' * 400}</title><link>https://e.com/{i}</link></item>" for i in range(250))
        e = parse_feed(f"<rss><channel>{items}</channel></rss>")
        self.assertEqual(len(e), 200)
        self.assertEqual(len(e[0].title), 300)


class DiscoverTest(unittest.TestCase):
    def test_youtube_channel(self):
        html = ('<html><head><link rel="alternate" type="application/rss+xml" title="RSS" '
                'href="https://www.youtube.com/feeds/videos.xml?channel_id=UC123"></head></html>')
        self.assertEqual(discover_feed_url(html, "https://www.youtube.com/@x"),
                         "https://www.youtube.com/feeds/videos.xml?channel_id=UC123")

    def test_relative_multi_token_case(self):
        html = '<link REL="Alternate home" TYPE="Application/Atom+XML" href="/feed.atom">'
        self.assertEqual(discover_feed_url(html, "https://e.com/blog/"), "https://e.com/feed.atom")

    def test_rejects_http_and_other_types(self):
        html = ('<link rel="alternate" type="text/html" href="/x">'
                '<link rel="alternate" type="application/rss+xml" href="http://e.com/f">')
        self.assertIsNone(discover_feed_url(html, "https://e.com/"))

    def test_malformed(self):
        self.assertIsNone(discover_feed_url("<<<link rel=", "https://e.com/"))


class PageLinksTest(unittest.TestCase):
    def test_filters(self):
        html = """<a href="/a#frag">1</a><a href="https://E.com/a">dup</a><a href="http://e.com/b">no</a>
        <a href="https://other.com/c">no</a><a href="mailto:x@e.com">no</a><a href="javascript:void(0)">no</a>
        <a href="https://e.com/page#top">self</a><a href="rel/d">4</a><a>nohref</a>
        <a href="https://sub.e.com/x">sub</a>"""
        self.assertEqual(page_links(html, "https://e.com/page"),
                         ["https://e.com/a", "https://e.com/rel/d"])

    def test_cap_and_malformed(self):
        html = "".join(f'<a href="/p{i}">x</a>' for i in range(400))
        self.assertEqual(len(page_links(html, "https://e.com/")), 300)
        self.assertEqual(page_links("<a href=<<", "https://e.com/"), [])


class RecentTest(unittest.TestCase):
    NOW = datetime(2026, 11, 13, 12, tzinfo=timezone.utc)

    def _e(self, days):
        return FeedEntry(f"https://e.com/x{days}", "t", self.NOW - timedelta(days=days))

    def test_window_sort_and_undated(self):
        entries = [self._e(2), FeedEntry("https://e.com/u", "t", None), self._e(4), self._e(1)]
        got = recent(entries, self.NOW)
        self.assertEqual([x.url for x in got], ["https://e.com/x1", "https://e.com/x2"])

    def test_future(self):
        ok = FeedEntry("https://e.com/f1", "t", self.NOW + timedelta(hours=12))
        bad = FeedEntry("https://e.com/f2", "t", self.NOW + timedelta(days=2))
        self.assertEqual(recent([bad, ok], self.NOW), [ok])

    def test_cap(self):
        entries = [FeedEntry(f"https://e.com/{i}", "t", self.NOW - timedelta(minutes=i)) for i in range(60)]
        got = recent(entries, self.NOW)
        self.assertEqual(len(got), 50)
        self.assertEqual(got[0].url, "https://e.com/0")


if __name__ == "__main__":
    unittest.main()
