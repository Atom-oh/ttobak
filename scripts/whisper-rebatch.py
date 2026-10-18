#!/usr/bin/env python3
"""SUPERSEDED operator tool: re-run Whisper for ONE explicitly named meeting.

Stuck single-file meetings are now re-driven automatically, with durable
conditional claims and a two-attempt budget, by the scheduled STT recovery
tick (ttobak-stt-recovery -> ttobak-transcribe; ADR-031 amendment and
docs/runbooks/stt-pipeline-troubleshooting.md). Prefer that path.

This script remains for a deliberate one-off. It refuses to run without an
explicit meeting ID, rejects done/summarizing meetings, changes status only
through a conditional update on the observed status and raw updatedAt (and
bumps updatedAt), and never prints meeting titles. The existing pipeline then
handles Whisper -> S3 transcripts/ -> EventBridge -> summarize.

Usage:
  python3 scripts/whisper-rebatch.py <id>                               # dry-run for one meeting
  python3 scripts/whisper-rebatch.py --run <id>                         # trigger one ECS task
  python3 scripts/whisper-rebatch.py --run --num-speakers 8 <id>        # hint a max speaker count
                                                                         # (upper bound; pyannote still
                                                                         # auto-detects within it)
"""
import argparse
import json
import os
import sys
import unicodedata
from datetime import datetime, timezone

import boto3
from botocore.exceptions import ClientError

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
        # entityType is GSI3's sort key, so it belongs in KeyConditionExpression --
        # DynamoDB rejects a FilterExpression that references an index key attribute.
        KeyConditionExpression="meetingId = :mid AND entityType = :et",
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
            "updatedAt": item.get("updatedAt", {}).get("S", ""),
            "status": item.get("status", {}).get("S", ""),
            "userId": item.get("userId", {}).get("S", ""),
            "sttProvider": item.get("sttProvider", {}).get("S", ""),
        }
    return None


def run_whisper_task(meeting_id, user_id, audio_key, num_speakers=None):
    """Trigger ECS Whisper task for a single meeting."""
    nfc_key = unicodedata.normalize("NFC", audio_key)
    environment = [
        {"name": "AUDIO_KEY", "value": nfc_key},
        {"name": "MEETING_ID", "value": meeting_id},
        {"name": "USER_ID", "value": user_id},
        {"name": "BUCKET_NAME", "value": BUCKET},
        {"name": "TABLE_NAME", "value": TABLE},
    ]
    if num_speakers:
        # Passed to the container as max_speakers -- an upper bound pyannote
        # auto-detects within, not an exact count (see transcribe.py).
        environment.append({"name": "NUM_SPEAKERS", "value": str(num_speakers)})
    overrides = {
        "containerOverrides": [{
            "name": CONTAINER,
            "environment": environment,
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


def _now():
    """Go RFC3339Nano UTC, matching the Go writers' updatedAt format."""
    value = datetime.now(timezone.utc).isoformat(timespec="microseconds").removesuffix("+00:00")
    return value.rstrip("0").rstrip(".") + "Z"


def claim_meeting(target):
    """Conditionally moves the observed meeting to transcribing. Returns False
    when anything changed since it was read."""
    table = boto3.resource("dynamodb", region_name=REGION).Table(TABLE)
    try:
        table.update_item(
            Key={"PK": f"USER#{target['user_id']}", "SK": f"MEETING#{target['meeting_id']}"},
            UpdateExpression="SET #s = :s, sttProvider = :p, updatedAt = :now",
            ConditionExpression="#s = :observed AND updatedAt = :updated AND attribute_not_exists(audioCrop)",
            ExpressionAttributeNames={"#s": "status"},
            ExpressionAttributeValues={":s": "transcribing", ":p": "whisper", ":now": _now(),
                                       ":observed": target["status"], ":updated": target["updatedAt"]},
        )
        return True
    except ClientError as e:
        if e.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            return False
        raise


def main():
    parser = argparse.ArgumentParser(description="Re-run Whisper ECS for one explicitly named meeting (superseded by the recovery tick)")
    parser.add_argument("--run", action="store_true", help="Actually trigger the ECS task (default: dry-run)")
    parser.add_argument("--num-speakers", type=int, default=None,
                         help="Hint a maximum speaker count to the diarization pipeline -- "
                              "an upper bound pyannote still auto-detects within, not an exact count")
    parser.add_argument("meeting_id", help="The single meeting ID to re-run (required)")
    args = parser.parse_args()

    print(f"Whisper Re-Batch (superseded; see ADR-031 amendment) — {'RUN' if args.run else 'DRY RUN'}")
    audio_files = find_audio_files()
    af = audio_files.get(args.meeting_id)
    meeting = get_meeting(args.meeting_id) if af else None
    if not af or not meeting:
        print("No audio or meeting found for that ID", file=sys.stderr)
        sys.exit(1)
    if meeting["status"] not in ("error", "transcribing"):
        print(f"Refusing: status={meeting['status']} (only error/transcribing meetings are re-run)", file=sys.stderr)
        sys.exit(1)
    if not meeting["updatedAt"]:
        print("Refusing: meeting has no updatedAt to fence on", file=sys.stderr)
        sys.exit(1)
    target = {**af, "status": meeting["status"], "updatedAt": meeting["updatedAt"]}
    print(f"Target: {target['meeting_id']} size={target['size_mb']:.1f}M status={target['status']} "
          f"provider={meeting['sttProvider'] or 'transcribe'}")
    if not args.run:
        print("Dry run complete. Use --run to trigger the ECS task.")
        return
    if not claim_meeting(target):
        print("Refusing: the meeting changed since it was read", file=sys.stderr)
        sys.exit(1)
    ok = run_whisper_task(target["meeting_id"], target["user_id"], target["key"], args.num_speakers)
    print("Triggered" if ok else "Task did not start; the meeting stays transcribing until the recovery tick or expiry")
    print("Monitor progress: aws ecs list-tasks --cluster ttobak-whisper")


if __name__ == "__main__":
    main()
