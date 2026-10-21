"""Bounded daily discovery of source identities; no model or publication writes."""

from datetime import datetime, timezone
import hashlib
import os
import time

import boto3
from boto3.dynamodb.conditions import Attr

from news_discovery import source_snapshot, valid_source_id
from news_runtime import SDK_CONFIG, _encode, _event, _policy, _remaining
from news_store import StateRejected

MAX_PAGES = 1000
# Thirty sequential 45-minute children fit within the 23-hour daily workflow.
MAX_SOURCES = 30
MAX_OUTPUT_BYTES = 64000


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
    for number in range(MAX_PAGES + 1):
        # Check before advancing the lazy paginator: next() performs network IO.
        if number == MAX_PAGES or remaining() < 10:
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
            if (not valid_source_id(source_id) or row.get("SK") != "CONFIG"
                    or row.get("sourceId") != source_id):
                raise StateRejected("INVALID_SCHEDULE_SOURCE")
            source_snapshot(row)  # Reject malformed canonical configuration before dispatch.
            if source_id in sources:
                raise StateRejected("DUPLICATE_SCHEDULE_SOURCE")
            sources[source_id] = {
                "sourceId": source_id, "runId": f"daily-{day}",
                "executionName": f"news-{day}-{hashlib.sha256(source_id.encode('utf-8')).hexdigest()[:32]}",
            }
            if len(sources) > MAX_SOURCES:
                raise StateRejected("SCHEDULE_SOURCE_LIMIT")
    result = {"mode": mode, "sources": [sources[key] for key in sorted(sources)]}
    if len(_encode(result)) > MAX_OUTPUT_BYTES:
        raise StateRejected("SCHEDULE_OUTPUT_LIMIT")
    return result


def handler(event, context):
    _event(event, ())
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
    _policy(now)
    name = os.environ.get("TABLE_NAME", "")
    if not name:
        raise StateRejected("INVALID_CONFIGURATION")
    table = boto3.Session().resource("dynamodb", config=SDK_CONFIG).Table(name)
    return list_sources(table, mode, canary, now, remaining)
