#!/usr/bin/env python3
"""Re-score existing crawler news insights against the new relevance gate
(backend/python/crawler/news_crawler.py's _summarize_and_tag) and purge
docs that fall below threshold. One-time backfill for docs ingested before
the gate existed -- new docs are already filtered at crawl time.

Re-scores from the title + already-stored summary (not the original
snippet, which isn't persisted) -- good enough to catch the reported bug
(an article that's clearly unrelated to the customer) without re-fetching
anything.

Skips (never purges) two categories instead of treating them as "confirmed
irrelevant": docs whose rescore call itself failed (Bedrock error/unparseable
response -- an infra hiccup, not a verdict; this script's delete is
irreversible, unlike crawl-time's skip/retry-next-crawl), and docs ingested
via customUrls, which bypass the relevance gate by design since they're
explicit user-requested URLs. Docs written after this PR mark that directly
(`ingestSource: "custom"`); older docs predate the field but the customUrls
path itself is not new, so a missing field is resolved by matching the doc's
stored URL against the source's current `customUrls` list (see
`is_custom_ingest`), not assumed to be "search".

Usage:
  python3 scripts/insights-rescore.py                          # dry-run: list docs that would be purged
  python3 scripts/insights-rescore.py --run --yes               # purge + trigger one KB ingestion job
  python3 scripts/insights-rescore.py --source hanabank         # limit to one crawler source
  python3 scripts/insights-rescore.py --threshold 0.6           # override RELEVANCE_THRESHOLD
  python3 scripts/insights-rescore.py --run --yes --kb-id X --ds-id Y --bucket Y # override real KB/DataSource IDs + bucket
"""
import argparse
import json
import os
import sys
from datetime import datetime, timezone

import boto3

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "backend", "python", "crawler"))
import news_crawler  # noqa: E402  (path insert must happen first)

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
TABLE = os.environ.get("TABLE_NAME", "ttobak-main")
# NOTE: there is no correct hardcoded default here -- the CDK-created bucket
# is named `ttobak-kb-${ACCOUNT_ID}` (infra/lib/knowledge-stack.ts), which
# varies per account/stage. Always pass --bucket or set KB_BUCKET_NAME.
KB_BUCKET = os.environ.get("KB_BUCKET_NAME", "")
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
                "url": item.get("url", {}).get("S", ""),
                "ingestSource": item.get("ingestSource", {}).get("S", ""),
            })
    return docs


def is_custom_ingest(doc, source_custom_urls):
    """customUrls-ingested docs bypass the relevance gate by design (explicit
    user-requested URL) -- never candidates for purge. Docs written after
    this PR carry ingestSource == "custom" directly. Docs written before it
    predate that field, but the customUrls ingest path itself already
    existed in base news_crawler.py -- so a missing field does NOT mean
    "search" (an earlier version of this script assumed that and would have
    purged legacy custom docs). Fall back to matching the doc's stored URL
    against the source's current customUrls list; only treat it as
    "search-ingested, reviewable" if the URL isn't a known custom one."""
    if doc["ingestSource"] == "custom":
        return True
    if doc["ingestSource"] == "":
        return doc["url"] in source_custom_urls
    return False


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
            "customUrls": {v["S"] for v in item.get("customUrls", {}).get("L", [])} if item else set(),
        }
    return _source_cache[source_id]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run", action="store_true", help="Actually delete below-threshold docs")
    parser.add_argument("--yes", action="store_true", help="Skip the confirmation prompt (required with --run)")
    parser.add_argument("--source", help="Limit to one crawler sourceId")
    parser.add_argument("--threshold", type=float, default=news_crawler.RELEVANCE_THRESHOLD)
    parser.add_argument("--kb-id", default=DEFAULT_KB_ID)
    parser.add_argument("--ds-id", default=DEFAULT_DS_ID)
    parser.add_argument("--bucket", default=KB_BUCKET, help="KB S3 bucket name (required; no safe default -- see KB_BUCKET_NAME note above)")
    args = parser.parse_args()

    if args.run and not args.bucket:
        sys.exit("--bucket (or KB_BUCKET_NAME) is required with --run -- there is no safe default "
                  "bucket name (it's account-suffixed, see infra/lib/knowledge-stack.ts).")

    docs = find_news_docs(args.source)
    print(f"Scanning {len(docs)} news doc(s)" + (f" (source={args.source})" if args.source else ""))

    to_purge = []
    unscorable = []
    skipped_custom = 0
    for doc in docs:
        cfg = get_source_config(doc["sourceId"])
        if is_custom_ingest(doc, cfg["customUrls"]):
            skipped_custom += 1
            continue
        summary, tags, relevant, confidence = news_crawler._summarize_and_tag(
            doc["title"], doc["summary"], cfg["sourceName"], cfg["newsQueries"])
        # _summarize_and_tag's fail-closed error path returns ('', [], False,
        # 0.0) -- indistinguishable from a genuine low-confidence verdict by
        # the return value alone, but a real classification almost always
        # carries non-empty summary/tags (the model is instructed to
        # produce them regardless of the relevant flag). Treat the
        # all-empty signature as "couldn't score", not "confirmed
        # irrelevant" -- this script's delete is irreversible, unlike
        # crawl-time's skip/retry-next-crawl for the same fail-closed case.
        if not summary and not tags:
            unscorable.append(doc)
            print(f"  [SKIP-UNSCORABLE] {doc['sourceId']}/{doc['docHash']} title={doc['title']!r}")
            continue
        if not relevant or confidence < args.threshold:
            to_purge.append((doc, confidence))
            print(f"  [PURGE] {doc['sourceId']}/{doc['docHash']} "
                  f"confidence={confidence:.2f} title={doc['title']!r}")

    print(f"\n{len(to_purge)}/{len(docs)} doc(s) below threshold {args.threshold} "
          f"({len(unscorable)} skipped as unscorable, {skipped_custom} skipped as custom-ingested)")

    if not args.run:
        print("Dry-run only -- pass --run --yes to actually delete these and trigger KB re-ingestion.")
        return

    if not to_purge:
        print("Nothing to delete.")
        return

    if not args.yes:
        sys.exit(f"Refusing to delete {len(to_purge)} doc(s) without --yes "
                  f"(irreversible bulk delete). Re-run with --run --yes.")

    backup_path = f"/tmp/insights-rescore-purged-{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')}.json"
    with open(backup_path, "w") as f:
        json.dump([{"sourceId": d["sourceId"], "docHash": d["docHash"], "title": d["title"],
                    "s3Key": d["s3Key"], "confidence": c} for d, c in to_purge], f, indent=2, ensure_ascii=False)
    print(f"Wrote purge list to {backup_path} before deleting.")

    for doc, _ in to_purge:
        s3_key = doc["s3Key"] or f"shared/news/{doc['sourceId']}/{doc['docHash']}.md"
        s3.delete_object(Bucket=args.bucket, Key=s3_key)
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
