#!/usr/bin/env python3
"""One-time backfill for CrawlerSource.OwnerID (ADR-026).

DELETE /api/insights/{sourceId}/{docHash} authorizes on source.OwnerID,
which is only set by AddSource's new-source branch -- any CrawlerSource
created before that field existed has OwnerID == "" and is admin-only-
deletable until backfilled here. Best-effort choice: the first (oldest)
subscriber becomes the owner, since Subscribers is append-only and index 0
is whoever originally called AddSource for a brand-new sourceId.

Usage:
  python3 scripts/insights-backfill-owner.py         # dry-run: list sources that would be updated
  python3 scripts/insights-backfill-owner.py --run   # actually write ownerId
"""
import argparse
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
    parser = argparse.ArgumentParser()
    parser.add_argument("--run", action="store_true", help="Actually write ownerId")
    args = parser.parse_args()

    sources = find_sources_without_owner()
    print(f"Found {len(sources)} source(s) without ownerId")

    for src in sources:
        owner = src["subscribers"][0] if src["subscribers"] else ""
        print(f"  {src['sourceId']}: ownerId -> {owner!r} (from subscribers[0])")

    if not args.run:
        print("Dry-run only -- pass --run to actually write ownerId.")
        return

    written = 0
    for src in sources:
        owner = src["subscribers"][0] if src["subscribers"] else ""
        if not owner:
            print(f"  Skipping {src['sourceId']}: no subscribers to backfill from (stays admin-only)")
            continue
        ddb.update_item(
            TableName=TABLE,
            Key={"PK": {"S": f"CRAWLER#{src['sourceId']}"}, "SK": {"S": "CONFIG"}},
            UpdateExpression="SET ownerId = :o",
            ConditionExpression="attribute_not_exists(ownerId)",
            ExpressionAttributeValues={":o": {"S": owner}},
        )
        written += 1
    print(f"Backfilled ownerId on {written} source(s).")


if __name__ == "__main__":
    main()
