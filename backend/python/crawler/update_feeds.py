"""Pure parsing helpers for customer update feeds (RSS, Atom, HTML pages).

Input is untrusted text. No network, no I/O, stdlib only.
"""
import re
import xml.etree.ElementTree as ET
from collections import namedtuple
from datetime import datetime, timedelta, timezone
from email.utils import parsedate_to_datetime
from html.parser import HTMLParser
from urllib.parse import urldefrag, urljoin, urlsplit

FeedEntry = namedtuple("FeedEntry", "url title published_at")

MAX_ENTRIES = 200
MAX_TITLE = 300
MAX_PAGE_LINKS = 300
_FEED_TYPES = ("application/rss+xml", "application/atom+xml")
_ATOM = "{http://www.w3.org/2005/Atom}"
_DTD_RE = re.compile(r"<!\s*(doctype|entity)", re.IGNORECASE)


class FeedError(Exception):
    """Raised with a short reason code, e.g. FeedError("parse")."""


def _clean_title(text):
    return " ".join((text or "").split())[:MAX_TITLE]


def _https_url(value):
    value = (value or "").strip()
    try:
        parts = urlsplit(value)
    except ValueError:
        return None
    if parts.scheme.lower() != "https" or not parts.hostname:
        return None
    return value


def _parse_date(value):
    value = (value or "").strip()
    if not value:
        return None
    dt = None
    try:
        dt = parsedate_to_datetime(value)
    except (TypeError, ValueError, IndexError, OverflowError):
        dt = None
    if dt is None:
        try:
            dt = datetime.fromisoformat(value.replace("Z", "+00:00").replace("z", "+00:00"))
        except (ValueError, OverflowError):
            return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def _text(el, tag):
    child = el.find(tag)
    return child.text if child is not None else None


def _atom_link(entry):
    for link in entry.findall(_ATOM + "link"):
        rel = (link.get("rel") or "alternate").strip().lower()
        if rel == "alternate":
            url = _https_url(link.get("href"))
            if url:
                return url
    return None


def parse_feed(xml_text):
    if not isinstance(xml_text, str) or _DTD_RE.search(xml_text):
        raise FeedError("parse")
    try:
        root = ET.fromstring(xml_text)
    except (ET.ParseError, ValueError, RecursionError):
        raise FeedError("parse") from None
    entries = []
    if root.tag == "rss" or root.tag == "channel" or root.find("channel") is not None:
        channel = root if root.tag == "channel" else root.find("channel")
        if channel is None:
            raise FeedError("parse")
        for item in channel.findall("item"):
            url = _https_url(_text(item, "link"))
            if not url:
                continue
            entries.append(FeedEntry(url, _clean_title(_text(item, "title")),
                                     _parse_date(_text(item, "pubDate"))))
            if len(entries) >= MAX_ENTRIES:
                break
    elif root.tag == _ATOM + "feed":
        for entry in root.findall(_ATOM + "entry"):
            url = _atom_link(entry)
            if not url:
                continue
            date = _parse_date(_text(entry, _ATOM + "published"))
            if date is None:
                date = _parse_date(_text(entry, _ATOM + "updated"))
            entries.append(FeedEntry(url, _clean_title(_text(entry, _ATOM + "title")), date))
            if len(entries) >= MAX_ENTRIES:
                break
    else:
        raise FeedError("parse")
    return entries


class _LinkCollector(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.links = []  # (tag, attrs dict)

    def handle_starttag(self, tag, attrs):
        if tag in ("link", "a"):
            self.links.append((tag, {k.lower(): (v or "") for k, v in attrs}))


def _collect(html):
    parser = _LinkCollector()
    try:
        parser.feed(html or "")
        parser.close()
    except Exception:  # malformed markup: keep what was parsed so far
        pass
    return parser.links


def discover_feed_url(html, base_url):
    for tag, attrs in _collect(html):
        if tag != "link":
            continue
        if "alternate" not in attrs.get("rel", "").lower().split():
            continue
        if attrs.get("type", "").strip().lower() not in _FEED_TYPES:
            continue
        href = attrs.get("href", "").strip()
        if not href:
            continue
        try:
            url = _https_url(urljoin(base_url, href))
        except ValueError:
            continue
        if url:
            return url
    return None


def _normalize(parts):
    """Lowercase scheme/host and make an empty path "/" so equal pages compare equal."""
    return parts._replace(scheme=parts.scheme.lower(), netloc=parts.netloc.lower(),
                          path=parts.path or "/").geturl()


def page_links(html, base_url):
    try:
        base_host = (urlsplit(base_url).hostname or "").lower()
        base_page = _normalize(urlsplit(urldefrag(base_url)[0]))
    except ValueError:
        return []
    if not base_host:
        return []
    seen = set()
    out = []
    for tag, attrs in _collect(html):
        if tag != "a" or not attrs.get("href", "").strip():
            continue
        try:
            absolute = urldefrag(urljoin(base_url, attrs["href"].strip()))[0]
            parts = urlsplit(absolute)
            host = (parts.hostname or "").lower()
        except ValueError:
            continue
        if parts.scheme.lower() != "https" or host != base_host:
            continue
        absolute = _normalize(parts)
        if absolute == base_page or absolute in seen:
            continue
        seen.add(absolute)
        out.append(absolute)
        if len(out) >= MAX_PAGE_LINKS:
            break
    return out


def recent(entries, now, days=3, cap=50):
    oldest = now - timedelta(days=days)
    newest = now + timedelta(days=1)
    kept = [e for e in entries
            if e.published_at is not None and oldest <= e.published_at <= newest]
    kept.sort(key=lambda e: e.published_at, reverse=True)
    return kept[:cap]
