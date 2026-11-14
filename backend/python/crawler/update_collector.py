"""Customer update collector Lambda.

Fetches each distinct registered feed once per run and stores raw entries under
the shared ``CUFEED#{feedHash}`` partition. A separate Go worker fans entries
out to users; this module never touches ``USER#`` rows. IAM allows only Query on
``CUFEEDS`` and GetItem/PutItem/UpdateItem/Query on ``CUFEED#*``.

Logs carry feedHash[:12], counts and DynamoDB error codes only, never URLs,
titles, keys, error messages or user data.

Write-intent marker (``writingAt``): a run sets it, compare-and-set on the value
it observed, before its first entry put, and clears it (again compare-and-set)
when it publishes ``updatedAt``. A marker not acknowledged by ``updatedAt`` is
publishable by a later run only once it is older than MARKER_FENCE (twice the
10-minute function timeout); a younger one belongs to an overlapping invocation,
so the feed is skipped and the marker left untouched. Entry puts use a table
without SDK retries: an ambiguous put surfaces as a StoreError and leaves the
marker for a later run to publish instead of reading as "already exists".
"""
import hashlib
import logging
import os
import random
import re
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
MAX_STORED_LINK_IDS = 2000  # 16-hex ids: 32 KB, far below DynamoDB's 400 KB item limit
BACKOFF_FAILURES = 5
BACKOFF_SECONDS = 24 * 3600
MARKER_FENCE = timedelta(minutes=20)  # function timeout is 10 minutes
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


_LINK_ID = re.compile(r"[0-9a-f]{16}")


def link_id(url):
    """Bounded identity of a link URL: the first 16 hex characters of its SHA-256."""
    return _sha256(url)[:16]


def _stored_id(value):
    """Normalize a stored baseline entry; None when it is not usable."""
    if not isinstance(value, str) or not value:
        return None
    return value if _LINK_ID.fullmatch(value) else link_id(value)


def merge_link_ids(page_known, old_ids):
    """Known ids on the current page first (all of them), then older ids no
    longer on the page, deduplicated, at most MAX_STORED_LINK_IDS."""
    out, seen = [], set()
    for i in list(page_known) + list(old_ids):
        if i not in seen:
            seen.add(i)
            out.append(i)
    return out[:max(MAX_STORED_LINK_IDS, len(list(page_known)))]


class _Failure(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


class StoreError(Exception):
    """A DynamoDB failure. It is contained to one feed and counted in the run
    stats, but it says nothing about the feed itself, so it never reaches the
    feed's failure state. ``code`` is the botocore error code (or the exception
    type); the message is never kept."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


class MarkerConflict(Exception):
    """Another invocation's write-intent marker appeared after this run read the
    feed state; the run leaves it untouched."""


def _error_code(exc):
    code = None
    response = getattr(exc, "response", None)
    if isinstance(response, dict):
        code = (response.get("Error") or {}).get("Code")
    return code if isinstance(code, str) and code else type(exc).__name__


def _guarded(fn):
    def wrapper(*args, **kwargs):
        try:
            return fn(*args, **kwargs)
        except (StoreError, MarkerConflict):
            raise
        except Exception as exc:  # botocore ClientError, without importing it here
            raise StoreError(_error_code(exc)) from None
    return wrapper


def _marker_condition(names, values, allowed):
    """Condition: writingAt absent or equal to one of the allowed values."""
    names["#cw"] = "writingAt"
    parts = ["attribute_not_exists(#cw)"]
    for i, v in enumerate(v for v in allowed if v is not None):
        values[":cw%d" % i] = v
        parts.append("#cw = :cw%d" % i)
    return " OR ".join(parts)


class FeedStore:
    """All DynamoDB access for the collector. Per-feed methods raise StoreError."""

    def __init__(self, table, entry_table=None):
        self.table = table
        # Entry puts go through a table without SDK retries (see handler).
        self.entry_table = entry_table if entry_table is not None else table

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

    @_guarded
    def get_state(self, feed_hash):
        # Strongly consistent: a stale read could miss a just-committed baseline.
        resp = self.table.get_item(Key={"PK": "CUFEED#" + feed_hash, "SK": STATE_SK},
                                   ConsistentRead=True)
        return resp.get("Item") or {}

    @_guarded
    def mark_writing(self, feed_hash, now, observed):
        """Write-intent marker, set before the first entry put of a run, only
        while the stored marker is still the one this run observed (or this
        run's own value, for an SDK retry). Raises MarkerConflict otherwise."""
        names = {"#h": "feedHash", "#t": "entityType", "#w": "writingAt"}
        values = {":h": feed_hash, ":t": STATE_ENTITY, ":w": _iso(now)}
        cond = _marker_condition(names, values, [observed, _iso(now)])
        try:
            self.table.update_item(
                Key={"PK": "CUFEED#" + feed_hash, "SK": STATE_SK},
                UpdateExpression="SET #h = :h, #t = :t, #w = :w",
                ConditionExpression=cond,
                ExpressionAttributeNames=names, ExpressionAttributeValues=values)
        except Exception as exc:
            if _error_code(exc) == "ConditionalCheckFailedException":
                raise MarkerConflict() from None
            raise

    @_guarded
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
            self.entry_table.put_item(Item=item, ConditionExpression="attribute_not_exists(PK)")
        except Exception as exc:  # botocore ClientError, without importing it here
            code = getattr(exc, "response", {}).get("Error", {}).get("Code")
            if code == "ConditionalCheckFailedException":
                return False
            raise
        return True

    @_guarded
    def update_state(self, feed_hash, now, *, failed=False, error=None,
                     updated_at=None, resolved_url=None, links=None, drop_resolved=False,
                     marker=None):
        """Attribute-level update; never replaces the whole state item.

        It publishes updated_at and removes the write-intent marker only while
        the stored marker is absent or equal to ``marker`` (the value this run
        set or observed as publishable). When another invocation's marker
        appeared meanwhile, it retries without publishing or removing it."""
        try:
            self._update_state(feed_hash, now, failed, error, updated_at, resolved_url,
                               links, drop_resolved, marker, True)
        except Exception as exc:
            if _error_code(exc) != "ConditionalCheckFailedException":
                raise
            self._update_state(feed_hash, now, failed, error, None, resolved_url,
                               links, drop_resolved, marker, False)

    def _update_state(self, feed_hash, now, failed, error, updated_at, resolved_url,
                      links, drop_resolved, marker, acknowledge):
        sets = {"feedHash": feed_hash, "entityType": STATE_ENTITY,
                "lastFetchedAt": _iso(now)}
        removes = (["writingAt"] if acknowledge else []) + (["resolvedUrl"] if drop_resolved else [])
        if updated_at is not None:
            sets["updatedAt"] = _iso(updated_at)
        if resolved_url:
            sets["resolvedUrl"] = resolved_url
        if links is not None:
            sets["links"] = list(links)
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
        kwargs = {}
        if acknowledge:
            kwargs["ConditionExpression"] = _marker_condition(names, values, [marker])
        self.table.update_item(
            Key={"PK": "CUFEED#" + feed_hash, "SK": STATE_SK},
            UpdateExpression=expr, ExpressionAttributeNames=names,
            ExpressionAttributeValues=values, **kwargs)


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


def _publish_at(pending, now, written):
    """updatedAt to publish: this run's time when it wrote entries, never earlier
    than an earlier unacknowledged run's write-intent time; None to publish nothing."""
    times = [t for t in (pending, now if written > 0 else None) if t is not None]
    return max(times) if times else None


def process_feed(store, feed_hash, url, kind, now):
    """Collect one feed. Returns (outcome, written); outcome is fetched|skipped|failed."""
    written = 0
    pending = None  # an earlier run's unacknowledged, fenced write-intent time
    marker = None  # stored marker value this run may clear (observed or its own)
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
        observed = state.get("writingAt")
        w = _parse_iso(observed)
        u = _parse_iso(state.get("updatedAt"))
        if w is not None and (u is None or u < w):
            if now - w <= MARKER_FENCE:
                # An overlapping invocation may still be writing: leave its
                # marker alone and skip the feed for this run.
                return "skipped", 0
            pending = w
        marker = observed
        drop_resolved = bool(state.get("resolvedUrl"))
        entries, links, resolved = _collect(url, kind, state)
        drop_resolved = False
        stored = None
        if entries is not None:
            fresh_entries = list(recent(entries, now))
            if fresh_entries:
                store.mark_writing(feed_hash, now, observed)
                marker = _iso(now)
            for e in fresh_entries:
                if store.put_entry(feed_hash, e.url, e.title, e.published_at, now):
                    written += 1
        else:
            old = state.get("links")
            ids = [link_id(l) for l in links]
            # The first run records the page as the baseline.
            stored = merge_link_ids(ids, [])
            if isinstance(old, (list, tuple, set)):
                old_ids = [i for i in (_stored_id(v) for v in old) if i]
                known = set(old_ids)
                # Only links delivered now become known; the rest of the page
                # stays new and is delivered on later runs.
                fresh = [(l, i) for l, i in zip(links, ids) if i not in known][:MAX_SITE_ENTRIES]
                if fresh:
                    store.mark_writing(feed_hash, now, observed)
                    marker = _iso(now)
                for link, i in fresh:
                    if store.put_entry(feed_hash, link, "", None, now):
                        written += 1
                    known.add(i)
                on_page = set(ids)
                stored = merge_link_ids([i for i in ids if i in known],
                                        [i for i in old_ids if i not in on_page])
        store.update_state(feed_hash, now, updated_at=_publish_at(pending, now, written),
                           resolved_url=resolved, links=stored, marker=marker)
        return "fetched", written
    except MarkerConflict:
        # Another invocation set its marker after this run read the state.
        logger.warning("feed=%s skipped: concurrent marker", feed_hash[:12])
        return "skipped", written
    except StoreError as exc:
        # Not a feed failure: no lastError, no failure count. Any marker stays
        # (including after an ambiguous entry put) and a run after the fence
        # publishes the unacknowledged entries.
        logger.error("feed=%s store error code=%s", feed_hash[:12], exc.code)
        return "failed", written
    except Exception as exc:
        reason = exc.reason if isinstance(exc, _Failure) else "fetch"
        try:
            store.update_state(feed_hash, now, failed=True, error=reason,
                               updated_at=_publish_at(pending, now, written),
                               drop_resolved=drop_resolved, marker=marker)
        except StoreError as store_exc:
            logger.error("feed=%s state update failed code=%s", feed_hash[:12], store_exc.code)
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


# One attempt in total: a retried conditional put whose first attempt committed
# would read as "already exists" and the entry would never be published.
# botocore's `max_attempts` counts retries after the first request, so the
# total is pinned with `total_max_attempts`.
ENTRY_PUT_RETRIES = {"total_max_attempts": 1, "mode": "standard"}


def handler(event, context):
    import boto3
    from botocore.config import Config
    name = os.environ.get("TABLE_NAME", "ttobak-main")
    table = boto3.resource("dynamodb").Table(name)
    entry_table = boto3.resource(
        "dynamodb", config=Config(retries=dict(ENTRY_PUT_RETRIES))).Table(name)
    result = collect(FeedStore(table, entry_table), datetime.now(timezone.utc), context)
    logger.info("collector done %s", result)
    return result
