"""Backfill ADR-062 seekable playback copies for existing meeting audio.

Scans ttobak-main meeting rows for their current audioKey/audioKeys, keeps the
WebM/Ogg keys that get a playback copy (same rule as
backend/internal/audioplayback.KeyFor), and invokes ttobak-audio-remux with
{"key": ...} for each. Orphaned S3 audio of deleted meetings is not touched.

Dry run by default; pass --apply to invoke. The worker is idempotent: a copy
already bound to the source's current version is reported as already_current.
Only key names and outcomes are printed; no meeting content is read.
"""

import argparse
import json
import os
import re
import sys
from concurrent.futures import ThreadPoolExecutor

SEGMENT = re.compile(r"^[^/\\\x00%]+$")
EXTENSIONS = (".webm", ".ogg")


def is_remux_source(key):
    """Mirror of audioplayback.IsRemuxSource."""
    if not isinstance(key, str):
        return False
    parts = key.split("/")
    if len(parts) != 4 or parts[0] != "audio":
        return False
    for segment in parts[1:]:
        if not SEGMENT.match(segment) or segment in (".", "..") or segment.strip() != segment:
            return False
    name = parts[3]
    if name.startswith("recording_progress."):
        return False
    return name.lower().endswith(EXTENSIONS)


def meeting_audio_keys(item):
    """Current audio keys of one meeting row, multi-part first, then legacy."""
    keys = [k for k in (item.get("audioKeys") or []) if k]
    if not keys and item.get("audioKey"):
        keys = [item["audioKey"]]
    return keys


def candidate_keys(items):
    seen, out = set(), []
    for item in items:
        if not str(item.get("SK", "")).startswith("MEETING#"):
            continue
        for key in meeting_audio_keys(item):
            if is_remux_source(key) and key not in seen:
                seen.add(key)
                out.append(key)
    return out


def scan_meetings(table):
    kwargs = {
        "FilterExpression": "begins_with(SK, :m) AND (attribute_exists(audioKey) OR attribute_exists(audioKeys))",
        "ExpressionAttributeValues": {":m": "MEETING#"},
        "ProjectionExpression": "SK, audioKey, audioKeys",
    }
    while True:
        page = table.scan(**kwargs)
        yield from page.get("Items", [])
        if "LastEvaluatedKey" not in page:
            return
        kwargs["ExclusiveStartKey"] = page["LastEvaluatedKey"]


def invoke(client, function_name, key):
    resp = client.invoke(FunctionName=function_name, InvocationType="RequestResponse",
                         Payload=json.dumps({"key": key}).encode())
    body = json.loads(resp["Payload"].read() or b"{}")
    if resp.get("FunctionError"):
        return key, "error", body.get("errorMessage", "")[:200]
    return key, body.get("outcome", "unknown"), ""


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--apply", action="store_true", help="invoke the worker (default: dry run)")
    parser.add_argument("--table", default=os.environ.get("TABLE_NAME", "ttobak-main"))
    parser.add_argument("--function", default="ttobak-audio-remux")
    parser.add_argument("--region", default=os.environ.get("AWS_REGION", "ap-northeast-2"))
    parser.add_argument("--concurrency", type=int, default=3)
    parser.add_argument("--limit", type=int, default=0, help="process at most N keys (0 = all)")
    args = parser.parse_args(argv)
    if not 1 <= args.concurrency <= 10:
        parser.error("--concurrency must be 1..10")

    import boto3
    from botocore.config import Config

    table = boto3.resource("dynamodb", region_name=args.region).Table(args.table)
    keys = candidate_keys(scan_meetings(table))
    if args.limit:
        keys = keys[:args.limit]
    print(f"candidates={len(keys)} apply={args.apply}")
    if not args.apply:
        for key in keys:
            print(f"would-remux {key}")
        return 0

    client = boto3.client("lambda", region_name=args.region,
                          config=Config(read_timeout=960, retries={"max_attempts": 0}))
    counts, failed = {}, 0
    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        for key, outcome, detail in pool.map(lambda k: invoke(client, args.function, k), keys):
            counts[outcome] = counts.get(outcome, 0) + 1
            print(f"{outcome} {key}" + (f" {detail}" if detail else ""), flush=True)
            failed += outcome == "error"
    print("summary " + json.dumps(counts, sort_keys=True))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
