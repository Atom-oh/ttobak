"""Bounded daily discovery of source identities; no model or publication writes."""

from datetime import datetime, timezone
import hashlib
import json
import os
import re
import time

import boto3
from boto3.dynamodb.conditions import Attr

from news_discovery import valid_source_id
from news_runtime import SDK_CONFIG, _encode, _event, _policy, _remaining
from news_store import StateRejected

MAX_PAGES = 1000
# Thirty sequential 45-minute children fit within the 23-hour daily workflow.
MAX_SOURCES = 30
MAX_OUTPUT_BYTES = 64000
MAX_REPLAY_SECONDS = 14 * 86400


def sources_sha256(result):
    """Manual replay pins every bounded entry, independent of scan order."""
    def canonical(value):
        return json.dumps(value, ensure_ascii=False, sort_keys=True,
                          separators=(",", ":"), allow_nan=False)
    payload = {"mode": result["mode"], "sources": sorted(result["sources"], key=canonical)}
    return hashlib.sha256(canonical(payload).encode("utf-8")).hexdigest()


def list_sources(table, mode, canary, now, remaining):
    day = datetime.fromtimestamp(now, timezone.utc).strftime("%Y%m%d")
    sources = {}
    pages = table.meta.client.get_paginator("scan").paginate(
        TableName=table.name, ConsistentRead=True,
        FilterExpression=Attr("PK").begins_with("CRAWLER#") & Attr("SK").eq("CONFIG"),
        ProjectionExpression="PK, SK, sourceId, sourceName, aliases, newsQueries, customUrls, #s, ownerId",
        ExpressionAttributeNames={"#s": "status"},
        PaginationConfig={"PageSize": 100})
    pages = iter(pages)
    for _ in range(MAX_PAGES):
        # Check before advancing the lazy paginator: next() performs network IO.
        if remaining() < 10:
            raise StateRejected("SCHEDULE_SCAN_BUDGET")
        try:
            page = next(pages)
        except StopIteration:
            break
        for row in page.get("Items", []):
            pk = row.get("PK", "")
            source_id = pk[8:] if isinstance(pk, str) and pk.startswith("CRAWLER#") else ""
            if source_id.startswith("__") or row.get("status") == "disabled":
                continue
            if mode == "canary" and source_id != canary:
                continue
            if source_id in sources:
                raise StateRejected("DUPLICATE_SCHEDULE_SOURCE")
            if (not valid_source_id(source_id) or row.get("SK") != "CONFIG"
                    or row.get("sourceId") != source_id):
                # Count rejected identities in the same bounded list. Never send
                # them to the child or expose a potentially oversized raw key.
                sources[source_id] = {
                    "schedulingError": "INVALID_SCHEDULE_SOURCE",
                    "sourceKeySha256": hashlib.sha256(
                        str(pk).encode("utf-8", errors="surrogatepass")).hexdigest(),
                }
            else:
                # The child still validates its current canonical configuration.
                sources[source_id] = {
                    "sourceId": source_id, "runId": f"daily-{day}",
                    "executionName": f"news-{day}-{hashlib.sha256(source_id.encode('utf-8')).hexdigest()[:32]}",
                }
            if len(sources) > MAX_SOURCES:
                raise StateRejected("SCHEDULE_SOURCE_LIMIT")
        if not page.get("LastEvaluatedKey"):
            break
    else:
        raise StateRejected("SCHEDULE_SCAN_BUDGET")
    result = {"mode": mode, "sources": [sources[key] for key in sorted(sources)]}
    if len(_encode(result)) > MAX_OUTPUT_BYTES:
        raise StateRejected("SCHEDULE_OUTPUT_LIMIT")
    return result


def handler(event, context):
    _event(event)
    if set(event) not in (set(), {"scheduledAt"}, {"scheduledAt", "expectedSourcesSha256"}):
        raise StateRejected("INVALID_REQUEST")
    expected = event.get("expectedSourcesSha256")
    if ("expectedSourcesSha256" in event
            and (not isinstance(expected, str) or not re.fullmatch(r"[0-9a-f]{64}", expected))):
        raise StateRejected("INVALID_REQUEST")
    mode = os.environ.get("NEWS_PIPELINE_MODE", "off")
    if mode == "off":
        return {"mode": "off", "sources": []}
    canary = os.environ.get("NEWS_CANARY_SOURCE_ID", "")
    if mode not in ("canary", "all") or (mode == "canary" and not valid_source_id(canary)):
        raise StateRejected("INVALID_SCHEDULE_MODE")
    if mode == "all" and os.environ.get("NEWS_LEGACY_WRITES") != "off":
        raise StateRejected("LEGACY_WRITER_ENABLED")
    remaining = _remaining(context)
    now = int(time.time())
    scheduled_at = event.get("scheduledAt")
    if (not isinstance(scheduled_at, str)
            or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z",
                                scheduled_at)):
        raise StateRejected("INVALID_SCHEDULE_TIME")
    try:
        scheduled = int(datetime.strptime(scheduled_at, "%Y-%m-%dT%H:%M:%SZ")
                        .replace(tzinfo=timezone.utc).timestamp())
    except (ValueError, OverflowError):
        raise StateRejected("INVALID_SCHEDULE_TIME") from None
    if not 0 <= now - scheduled <= MAX_REPLAY_SECONDS:
        raise StateRejected("INVALID_SCHEDULE_TIME")
    _policy(now)
    name = os.environ.get("TABLE_NAME", "")
    if not name:
        raise StateRejected("INVALID_CONFIGURATION")
    table = boto3.Session().resource("dynamodb", config=SDK_CONFIG).Table(name)
    result = list_sources(table, mode, canary, scheduled, remaining)
    if expected is not None and sources_sha256(result) != expected:
        raise StateRejected("SCHEDULE_SOURCES_CHANGED")
    return result
