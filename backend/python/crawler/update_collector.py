"""Customer update collector Lambda.

Fetches each distinct registered feed once per run and stores raw entries under
the shared ``CUFEED#{feedHash}`` partition. A separate Go worker fans entries
out to users; this module never touches ``USER#`` rows. IAM allows only Query on
``CUFEEDS`` and GetItem/PutItem/UpdateItem/Query on ``CUFEED#*``.

Logs carry feedHash[:12] and counts only, never URLs, titles or user data.
"""
import hashlib
import logging
import os
import random
from datetime import datetime, timedelta, timezone

from article_fetcher import FetchRejected, fetch_feed_document
from update_feeds import (FeedError, discover_feed_url, page_links, parse_feed,
                          recent)

logger = logging.getLogger()
logger.setLevel(logging.INFO)

PK_REGISTRY = "CUFEEDS"
STATE_SK = "STATE"
ENTRY_PREFIX = "ENTRY#"
STATE_ENTITY = "UPDATE_FEED_STATE"
ENTRY_ENTITY = "UPDATE_FEED_ENTRY"
ENTRY_TTL_SECONDS = 7 * 24 * 3600
MAX_SITE_ENTRIES = 30
MAX_STORED_LINKS = 300
MAX_STORED_LINK_BYTES = 200_000  # keeps the state item far below DynamoDB's 400 KB
BACKOFF_FAILURES = 5
BACKOFF_SECONDS = 24 * 3600
MIN_REMAINING_MS = 270000  # one feed can take about 250 s (retries and redirects)
HTML_TYPES = {"text/html", "application/xhtml+xml"}
FETCH_REASONS = {"content_type", "too_large"}


def _iso(dt):
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _parse_iso(value):
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except (TypeError, ValueError):
        return None


def _sha256(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def budget_links(links):
    """Keep links in order, at most MAX_STORED_LINKS and MAX_STORED_LINK_BYTES UTF-8."""
    out, total = [], 0
    for link in links:
        if len(out) >= MAX_STORED_LINKS:
            break
        size = len(link.encode("utf-8"))
        if total + size > MAX_STORED_LINK_BYTES:
            break
        out.append(link)
        total += size
    return out


class _Failure(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


class FeedStore:
    """All DynamoDB access for the collector."""

    def __init__(self, table):
        self.table = table

    def active_feeds(self):
        """Return {feedHash: (url, kind)} from active registry rows.

        When subscribers of one feed disagree on kind, ``site`` wins so the page
        can still be followed without a feed."""
        feeds = {}
        kwargs = {"KeyConditionExpression": "PK = :pk",
                  "ExpressionAttributeValues": {":pk": PK_REGISTRY}}
        while True:
            resp = self.table.query(**kwargs)
            for row in resp.get("Items", []):
                h = row.get("feedHash")
                if row.get("status") != "active" or not h or not row.get("url"):
                    continue
                kind = row.get("kind", "rss")
                if h not in feeds:
                    feeds[h] = (row["url"], kind)
                elif kind == "site":
                    feeds[h] = (feeds[h][0], "site")
            last = resp.get("LastEvaluatedKey")
            if not last:
                return feeds
            kwargs["ExclusiveStartKey"] = last

    def get_state(self, feed_hash):
        resp = self.table.get_item(Key={"PK": "CUFEED#" + feed_hash, "SK": STATE_SK})
        return resp.get("Item") or {}

    def put_entry(self, feed_hash, url, title, published_at, now):
        """Write one entry; False when it already exists."""
        entry_hash = _sha256(url)
        item = {
            "PK": "CUFEED#" + feed_hash, "SK": ENTRY_PREFIX + entry_hash,
            "entryHash": entry_hash, "url": url, "seenAt": _iso(now),
            "pendingShareExpiresAt": int(now.timestamp()) + ENTRY_TTL_SECONDS,
            "entityType": ENTRY_ENTITY,
        }
        if title:
            item["title"] = title
        if published_at is not None:
            item["publishedAt"] = _iso(published_at)
        try:
            self.table.put_item(Item=item, ConditionExpression="attribute_not_exists(PK)")
        except Exception as exc:  # botocore ClientError, without importing it here
            code = getattr(exc, "response", {}).get("Error", {}).get("Code")
            if code == "ConditionalCheckFailedException":
                return False
            raise
        return True

    def update_state(self, feed_hash, now, *, failed=False, error=None,
                     updated=False, resolved_url=None, links=None, drop_resolved=False):
        """Attribute-level update; never replaces the whole state item."""
        sets = {"feedHash": feed_hash, "entityType": STATE_ENTITY,
                "lastFetchedAt": _iso(now)}
        removes = ["resolvedUrl"] if drop_resolved else []
        if updated:
            sets["updatedAt"] = _iso(now)
        if resolved_url:
            sets["resolvedUrl"] = resolved_url
        if links is not None:
            sets["links"] = budget_links(links)
        if failed:
            sets["lastError"] = error
        else:
            sets["failures"] = 0
            removes.append("lastError")
        names, values, parts = {}, {}, []
        for i, (key, val) in enumerate(sets.items()):
            names["#s%d" % i] = key
            values[":s%d" % i] = val
            parts.append("#s%d = :s%d" % (i, i))
        if failed:
            names["#f"] = "failures"
            values[":z"] = 0
            values[":one"] = 1
            parts.append("#f = if_not_exists(#f, :z) + :one")
        expr = "SET " + ", ".join(parts)
        if removes:
            for i, key in enumerate(removes):
                names["#r%d" % i] = key
            expr += " REMOVE " + ", ".join("#r%d" % i for i in range(len(removes)))
        self.table.update_item(
            Key={"PK": "CUFEED#" + feed_hash, "SK": STATE_SK},
            UpdateExpression=expr, ExpressionAttributeNames=names,
            ExpressionAttributeValues=values)


def _fetch(url):
    try:
        return fetch_feed_document(url)
    except FetchRejected as exc:
        raise _Failure(exc.reason if exc.reason in FETCH_REASONS else "fetch") from None


def _parse(text):
    try:
        return parse_feed(text)
    except FeedError:
        raise _Failure("parse") from None


def _collect(url, kind, state):
    """Return (feed entries or None, site links or None, resolved url or None)."""
    resolved = state.get("resolvedUrl")
    doc = _fetch(resolved or url)
    if doc.content_type not in HTML_TYPES:
        return _parse(doc.text), None, None
    if resolved:  # a stored feed address that now serves HTML is not a feed
        raise _Failure("no_feed")
    feed_url = discover_feed_url(doc.text, doc.final_url)
    if feed_url:
        feed_doc = _fetch(feed_url)
        if feed_doc.content_type in HTML_TYPES:
            raise _Failure("no_feed")
        return _parse(feed_doc.text), None, feed_url
    if kind != "site":
        raise _Failure("no_feed")
    return None, page_links(doc.text, doc.final_url), None


def process_feed(store, feed_hash, url, kind, now):
    """Collect one feed. Returns (outcome, written); outcome is fetched|skipped|failed."""
    written = 0
    # A stored resolvedUrl that fails or stops yielding a feed is dropped so the
    # next run rediscovers from the subscription URL.
    drop_resolved = False
    try:
        state = store.get_state(feed_hash)
        last = _parse_iso(state.get("lastFetchedAt"))
        try:
            failures = int(state.get("failures", 0))
        except (TypeError, ValueError):
            failures = 0
        if (failures >= BACKOFF_FAILURES and last is not None
                and now - last <= timedelta(seconds=BACKOFF_SECONDS)):
            return "skipped", 0
        drop_resolved = bool(state.get("resolvedUrl"))
        entries, links, resolved = _collect(url, kind, state)
        drop_resolved = False
        stored = None
        if entries is not None:
            for e in recent(entries, now):
                if store.put_entry(feed_hash, e.url, e.title, e.published_at, now):
                    written += 1
        else:
            old = state.get("links")
            stored = list(links)  # the first run records the page as the baseline
            if old is not None:
                known = set(old)
                current = set(links)
                # Only links delivered now become known; the rest of the page
                # stays new and is delivered on later runs.
                for link in [l for l in links if l not in known][:MAX_SITE_ENTRIES]:
                    if store.put_entry(feed_hash, link, "", None, now):
                        written += 1
                    known.add(link)
                stored = [l for l in links if l in known] + [l for l in old if l not in current]
        store.update_state(feed_hash, now, updated=written > 0,
                           resolved_url=resolved, links=stored)
        return "fetched", written
    except Exception as exc:
        reason = exc.reason if isinstance(exc, _Failure) else "fetch"
        try:
            store.update_state(feed_hash, now, failed=True, error=reason,
                               updated=written > 0, drop_resolved=drop_resolved)
        except Exception:
            logger.error("feed=%s state update failed", feed_hash[:12])
        logger.warning("feed=%s failed reason=%s", feed_hash[:12], reason)
        return "failed", written


def collect(store, now, context=None, shuffle=random.shuffle):
    """Collect every active feed once, in a shuffled order so feeds left over
    when time runs short differ from run to run."""
    feeds = store.active_feeds()
    result = {"feeds": len(feeds), "fetched": 0, "entries": 0, "skipped": 0, "failed": 0}
    remaining = getattr(context, "get_remaining_time_in_millis", None)
    order = list(feeds.items())
    shuffle(order)
    for feed_hash, (url, kind) in order:
        if remaining is not None and remaining() < MIN_REMAINING_MS:
            result["skipped"] += 1
            continue
        try:
            outcome, written = process_feed(store, feed_hash, url, kind, now)
        except Exception:  # last-resort containment; never stop the loop
            logger.error("feed=%s unexpected error", feed_hash[:12])
            outcome, written = "failed", 0
        result[outcome] += 1
        result["entries"] += written
    return result


def handler(event, context):
    import boto3
    table = boto3.resource("dynamodb").Table(os.environ.get("TABLE_NAME", "ttobak-main"))
    result = collect(FeedStore(table), datetime.now(timezone.utc), context)
    logger.info("collector done %s", result)
    return result
