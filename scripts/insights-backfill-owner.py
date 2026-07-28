#!/usr/bin/env python3
"""Report on CrawlerSource rows missing OwnerID (ADR-026).

DELETE /api/insights/{sourceId}/{docHash} authorizes on source.OwnerID,
which is only set by AddSource's new-source branch -- any CrawlerSource
created before that field existed has OwnerID == "" and stays admin-only-
deletable. This script does NOT write ownerId: Subscribers is not
append-only (Unsubscribe removes entries -- see crawler.go's Unsubscribe),
so subscribers[0] is not reliably the original creator, and there is no
other recorded creation history to promote from. Auto-promoting the wrong
person would grant them destructive delete rights over a shared source.

This is intentionally report-only. If a source's real creator is known out
of band (e.g. from deploy history or asking the team), set ownerId for that
one source by hand; otherwise leave it admin-only-deletable indefinitely.

Usage:
  python3 scripts/insights-backfill-owner.py   # list sources with no ownerId (admin-only until known)
"""
import os

import boto3

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
TABLE = os.environ.get("TABLE_NAME", "ttobak-main")

ddb = boto3.client("dynamodb", region_name=REGION)


def find_sources_without_owner():
    paginator = ddb.get_paginator("scan")
    sources = []
    for page in paginator.paginate(
        TableName=TABLE,
        FilterExpression="begins_with(PK, :pfx) AND SK = :sk AND attribute_not_exists(ownerId)",
        ExpressionAttributeValues={":pfx": {"S": "CRAWLER#"}, ":sk": {"S": "CONFIG"}},
    ):
        for item in page.get("Items", []):
            subscribers = [v["S"] for v in item.get("subscribers", {}).get("L", [])]
            sources.append({
                "sourceId": item.get("sourceId", {}).get("S", ""),
                "subscribers": subscribers,
            })
    return sources


def main():
    sources = find_sources_without_owner()
    print(f"Found {len(sources)} source(s) without ownerId (admin-only delete until an owner is set)")
    for src in sources:
        print(f"  {src['sourceId']}: subscribers={src['subscribers']!r} -- no reliable owner to infer")


if __name__ == "__main__":
    main()
