#!/usr/bin/env python3
"""Re-run Whisper ECS batch transcription for existing meetings.

Finds meetings that were transcribed by AWS Transcribe (not Whisper),
then triggers Whisper ECS tasks for each. The existing pipeline handles
the rest: Whisper → S3 transcripts/ → EventBridge → summarize Lambda.

Usage:
  python3 scripts/whisper-rebatch.py              # dry-run: list meetings
  python3 scripts/whisper-rebatch.py --run         # trigger ECS tasks
  python3 scripts/whisper-rebatch.py --run <id>    # single meeting
"""
import argparse
import json
import os
import sys
import time
import unicodedata

import boto3

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
BUCKET = os.environ.get("BUCKET_NAME", "ttobak-assets-180294183052")
TABLE = os.environ.get("TABLE_NAME", "ttobak-main")
CLUSTER = os.environ.get("WHISPER_CLUSTER", "ttobak-whisper")
TASK_DEF = os.environ.get("WHISPER_TASK_DEF", "ttobak-whisper")
CONTAINER = os.environ.get("WHISPER_CONTAINER", "whisper")
CAPACITY_PROVIDER = os.environ.get("WHISPER_CAPACITY_PROVIDER", "ttobak-whisper-spot")
SUBNET_IDS = os.environ.get("WHISPER_SUBNETS", "").split(",")

s3 = boto3.client("s3", region_name=REGION)
ddb = boto3.client("dynamodb", region_name=REGION)
ecs = boto3.client("ecs", region_name=REGION)


def find_audio_files():
    """Find all audio files in S3 with their meeting IDs."""
    paginator = s3.get_paginator("list_objects_v2")
    files = {}
    for page in paginator.paginate(Bucket=BUCKET, Prefix="audio/"):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            if "recording_progress" in key or "checkpoint" in key:
                continue
            if obj["Size"] < 10000:
                continue
            parts = key.split("/")
            if len(parts) >= 4:
                meeting_id = parts[2]
                user_id = parts[1]
                if meeting_id not in files or obj["Size"] > files[meeting_id]["size"]:
                    files[meeting_id] = {
                        "key": key,
                        "meeting_id": meeting_id,
                        "user_id": user_id,
                        "size": obj["Size"],
                        "size_mb": round(obj["Size"] / 1048576, 1),
                    }
    return files


def get_meeting(meeting_id):
    """Get meeting record via GSI3."""
    resp = ddb.query(
        TableName=TABLE,
        IndexName="GSI3",
        KeyConditionExpression="meetingId = :mid",
        FilterExpression="entityType = :et",
        ExpressionAttributeValues={
            ":mid": {"S": meeting_id},
            ":et": {"S": "MEETING"},
        },
    )
    for item in resp.get("Items", []):
        return {
            "PK": item["PK"]["S"],
            "SK": item["SK"]["S"],
            "meetingId": item["meetingId"]["S"],
            "title": item.get("title", {}).get("S", ""),
            "status": item.get("status", {}).get("S", ""),
            "userId": item.get("userId", {}).get("S", ""),
            "sttProvider": item.get("sttProvider", {}).get("S", ""),
        }
    return None


def run_whisper_task(meeting_id, user_id, audio_key):
    """Trigger ECS Whisper task for a single meeting."""
    nfc_key = unicodedata.normalize("NFC", audio_key)
    overrides = {
        "containerOverrides": [{
            "name": CONTAINER,
            "environment": [
                {"name": "AUDIO_KEY", "value": nfc_key},
                {"name": "MEETING_ID", "value": meeting_id},
                {"name": "USER_ID", "value": user_id},
                {"name": "BUCKET_NAME", "value": BUCKET},
                {"name": "TABLE_NAME", "value": TABLE},
            ],
        }],
    }

    resp = ecs.run_task(
        cluster=CLUSTER,
        taskDefinition=TASK_DEF,
        overrides=overrides,
        capacityProviderStrategy=[{
            "capacityProvider": CAPACITY_PROVIDER,
            "weight": 1,
        }],
        count=1,
    )

    failures = resp.get("failures", [])
    if failures:
        print(f"  FAILED: {failures[0].get('reason', 'unknown')}")
        return False

    task_arn = resp["tasks"][0]["taskArn"]
    print(f"  Task started: {task_arn.split('/')[-1]}")
    return True


def main():
    parser = argparse.ArgumentParser(description="Re-batch existing meetings through Whisper ECS")
    parser.add_argument("--run", action="store_true", help="Actually trigger ECS tasks (default: dry-run)")
    parser.add_argument("meeting_id", nargs="?", help="Process single meeting ID")
    args = parser.parse_args()

    print(f"Whisper Re-Batch — {'RUN' if args.run else 'DRY RUN'}")
    print(f"Cluster: {CLUSTER} | Task: {TASK_DEF}")
    print()

    audio_files = find_audio_files()
    print(f"Found {len(audio_files)} meetings with audio files")

    targets = []
    for mid, af in sorted(audio_files.items()):
        if args.meeting_id and mid != args.meeting_id:
            continue
        meeting = get_meeting(mid)
        if not meeting:
            continue
        if meeting["status"] not in ("done", "error", "transcribing", "summarizing"):
            continue
        targets.append({**af, "title": meeting["title"], "status": meeting["status"], "sttProvider": meeting["sttProvider"]})

    print(f"\nTargets: {len(targets)} meetings")
    print(f"{'ID':>10s}  {'Size':>6s}  {'Provider':>12s}  {'Status':>12s}  Title")
    print("-" * 80)
    for t in targets:
        print(f"{t['meeting_id'][:10]}  {t['size_mb']:>5.1f}M  {t['sttProvider'] or 'transcribe':>12s}  {t['status']:>12s}  {t['title'][:30]}")

    if not args.run:
        print(f"\nDry run complete. Use --run to trigger {len(targets)} ECS tasks.")
        return

    print(f"\nTriggering {len(targets)} Whisper ECS tasks...")
    success = 0
    for i, t in enumerate(targets):
        print(f"\n[{i+1}/{len(targets)}] {t['meeting_id'][:10]} — {t['title'][:40]}")

        # Mark as transcribing
        ddb_resource = boto3.resource("dynamodb", region_name=REGION)
        ddb_resource.Table(TABLE).update_item(
            Key={"PK": f"USER#{t['user_id']}", "SK": f"MEETING#{t['meeting_id']}"},
            UpdateExpression="SET #s = :s, sttProvider = :p",
            ExpressionAttributeNames={"#s": "status"},
            ExpressionAttributeValues={":s": "transcribing", ":p": "whisper"},
        )

        if run_whisper_task(t["meeting_id"], t["user_id"], t["key"]):
            success += 1
        time.sleep(2)

    print(f"\n{'=' * 60}")
    print(f"Triggered: {success}/{len(targets)} tasks")
    print("Monitor progress: aws ecs list-tasks --cluster ttobak-whisper")


if __name__ == "__main__":
    main()
