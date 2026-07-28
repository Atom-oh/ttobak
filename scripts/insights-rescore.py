#!/usr/bin/env python3
"""Re-score existing crawler news insights against the new relevance gate
(backend/python/crawler/news_crawler.py's _summarize_and_tag) and purge
docs that fall below threshold. One-time backfill for docs ingested before
the gate existed -- new docs are already filtered at crawl time.

Re-scores from the title + already-stored summary (not the original
snippet, which isn't persisted) -- good enough to catch the reported bug
(an article that's clearly unrelated to the customer) without re-fetching
anything.

Usage:
  python3 scripts/insights-rescore.py                          # dry-run: list docs that would be purged
  python3 scripts/insights-rescore.py --run                    # purge + trigger one KB ingestion job
  python3 scripts/insights-rescore.py --source hanabank         # limit to one crawler source
  python3 scripts/insights-rescore.py --threshold 0.6           # override RELEVANCE_THRESHOLD
  python3 scripts/insights-rescore.py --run --kb-id X --ds-id Y # override the real KB/DataSource IDs
"""
import argparse
import os
import sys

import boto3

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "backend", "python", "crawler"))
import news_crawler  # noqa: E402  (path insert must happen first)

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
TABLE = os.environ.get("TABLE_NAME", "ttobak-main")
KB_BUCKET = os.environ.get("KB_BUCKET_NAME", "ttobak-kb")
# Real values (not the 'PENDING' placeholder) -- see knowledge-stack.ts / CLAUDE.md Known Issues.
DEFAULT_KB_ID = "BJJLVLFTOR"
DEFAULT_DS_ID = "3AVMMT3RF3"

ddb = boto3.client("dynamodb", region_name=REGION)
s3 = boto3.client("s3", region_name=REGION)
bedrock_agent = boto3.client("bedrock-agent", region_name=REGION)

_source_cache = {}


def find_news_docs(source_filter=None):
    """All CRAWLER#{sourceId}/DOC#{docHash} items of type 'news', via GSI4
    (GSI4PK='DOC#news') -- avoids a full table scan."""
    paginator = ddb.get_paginator("query")
    docs = []
    for page in paginator.paginate(
        TableName=TABLE,
        IndexName="GSI4",
        KeyConditionExpression="GSI4PK = :pk",
        ExpressionAttributeValues={":pk": {"S": "DOC#news"}},
    ):
        for item in page.get("Items", []):
            source_id = item.get("sourceId", {}).get("S", "")
            if source_filter and source_id != source_filter:
                continue
            docs.append({
                "sourceId": source_id,
                "docHash": item.get("docHash", {}).get("S", ""),
                "title": item.get("title", {}).get("S", ""),
                "summary": item.get("summary", {}).get("S", ""),
                "s3Key": item.get("s3Key", {}).get("S", ""),
            })
    return docs


def get_source_config(source_id):
    if source_id not in _source_cache:
        resp = ddb.get_item(
            TableName=TABLE,
            Key={"PK": {"S": f"CRAWLER#{source_id}"}, "SK": {"S": "CONFIG"}},
        )
        item = resp.get("Item")
        _source_cache[source_id] = {
            "sourceName": item.get("sourceName", {}).get("S", "") if item else "",
            "newsQueries": [v["S"] for v in item.get("newsQueries", {}).get("L", [])] if item else [],
        }
    return _source_cache[source_id]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run", action="store_true", help="Actually delete below-threshold docs")
    parser.add_argument("--source", help="Limit to one crawler sourceId")
    parser.add_argument("--threshold", type=float, default=news_crawler.RELEVANCE_THRESHOLD)
    parser.add_argument("--kb-id", default=DEFAULT_KB_ID)
    parser.add_argument("--ds-id", default=DEFAULT_DS_ID)
    args = parser.parse_args()

    docs = find_news_docs(args.source)
    print(f"Scanning {len(docs)} news doc(s)" + (f" (source={args.source})" if args.source else ""))

    to_purge = []
    for doc in docs:
        cfg = get_source_config(doc["sourceId"])
        _, _, relevant, confidence = news_crawler._summarize_and_tag(
            doc["title"], doc["summary"], cfg["sourceName"], cfg["newsQueries"])
        if not relevant or confidence < args.threshold:
            to_purge.append((doc, confidence))
            print(f"  [PURGE] {doc['sourceId']}/{doc['docHash']} "
                  f"confidence={confidence:.2f} title={doc['title']!r}")

    print(f"\n{len(to_purge)}/{len(docs)} doc(s) below threshold {args.threshold}")

    if not args.run:
        print("Dry-run only -- pass --run to actually delete these and trigger KB re-ingestion.")
        return

    for doc, _ in to_purge:
        s3_key = doc["s3Key"] or f"shared/news/{doc['sourceId']}/{doc['docHash']}.md"
        s3.delete_object(Bucket=KB_BUCKET, Key=s3_key)
        ddb.delete_item(
            TableName=TABLE,
            Key={"PK": {"S": f"CRAWLER#{doc['sourceId']}"}, "SK": {"S": f"DOC#{doc['docHash']}"}},
        )
    print(f"Deleted {len(to_purge)} doc(s).")

    if to_purge:
        job = bedrock_agent.start_ingestion_job(knowledgeBaseId=args.kb_id, dataSourceId=args.ds_id)
        job_id = job.get("ingestionJob", {}).get("ingestionJobId", "?")
        print(f"Started KB ingestion job {job_id} to reconcile deleted vectors.")


if __name__ == "__main__":
    main()
