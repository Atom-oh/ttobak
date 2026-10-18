#!/usr/bin/env python3
"""Operator tool: re-run Whisper ECS batch transcription for existing meetings.

Lists eligible meetings (dry run by default) and, with --run, triggers one
Whisper ECS task per selected meeting. The existing pipeline then handles
Whisper -> S3 transcripts/ -> EventBridge -> summarize. Nothing re-runs a task
automatically; this script is the authorized manual path
(docs/runbooks/stt-pipeline-troubleshooting.md).

Safety: only error/transcribing meetings are eligible (done/summarizing are
never overwritten); the audio key's user segment must match the meeting row's
owner; cropped meetings (audioCrop) are skipped; status changes only through a
conditional update on the observed status and raw updatedAt (which it bumps);
meeting titles are never printed. Re-running a transcribing meeting whose task
is still in flight starts a second task -- check `aws ecs list-tasks` first.

Usage:
  python3 scripts/whisper-rebatch.py                                    # dry-run: list eligible meetings
  python3 scripts/whisper-rebatch.py <id>                               # dry-run for one meeting
  python3 scripts/whisper-rebatch.py --run <id>                         # trigger one ECS task
  python3 scripts/whisper-rebatch.py --run --all                        # trigger every eligible meeting
  python3 scripts/whisper-rebatch.py --run --num-speakers 8 <id>        # hint a max speaker count
                                                                         # (upper bound; pyannote still
                                                                         # auto-detects within it)
"""
import argparse
import json
import os
import sys
import time
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
            "audioCrop": "audioCrop" in item,
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


ELIGIBLE_STATUSES = ("error", "transcribing")


def check_target(af, meeting):
    """Returns (target, None) for an eligible meeting or (None, reason)."""
    if not meeting:
        return None, "no meeting row"
    if meeting["status"] not in ELIGIBLE_STATUSES:
        return None, f"status={meeting['status']} (only error/transcribing are re-run)"
    # Bind the audio key to the meeting row: audio/{userId}/{meetingId}/...
    if (meeting["userId"] != af["user_id"] or meeting["PK"] != f"USER#{af['user_id']}"
            or meeting["meetingId"] != af["meeting_id"]):
        return None, "audio key owner does not match the meeting row"
    if meeting["audioCrop"]:
        return None, "meeting has an audioCrop (cropped re-runs are not supported here)"
    if not meeting["updatedAt"]:
        return None, "meeting has no updatedAt to fence on"
    return {**af, "status": meeting["status"], "updatedAt": meeting["updatedAt"],
            "sttProvider": meeting["sttProvider"]}, None


def main():
    parser = argparse.ArgumentParser(description="Re-batch existing meetings through Whisper ECS")
    parser.add_argument("--run", action="store_true", help="Actually trigger ECS tasks (default: dry-run)")
    parser.add_argument("--all", action="store_true",
                        help="With --run and no meeting_id, trigger every eligible meeting")
    parser.add_argument("--num-speakers", type=int, default=None,
                         help="Hint a maximum speaker count to the diarization pipeline -- "
                              "an upper bound pyannote still auto-detects within, not an exact "
                              "count (only meaningful with a single meeting_id)")
    parser.add_argument("meeting_id", nargs="?", help="Process single meeting ID")
    args = parser.parse_args()

    if args.num_speakers and not args.meeting_id:
        print("ERROR: --num-speakers requires a single meeting_id (it would apply the same "
              "speaker count to every re-batched meeting otherwise)", file=sys.stderr)
        sys.exit(1)
    if args.meeting_id and args.all:
        print("ERROR: --all cannot be combined with a meeting_id", file=sys.stderr)
        sys.exit(1)
    if args.run and not args.meeting_id and not args.all:
        print("ERROR: --run without a meeting_id requires --all", file=sys.stderr)
        sys.exit(1)

    print(f"Whisper Re-Batch — {'RUN' if args.run else 'DRY RUN'}")
    print(f"Cluster: {CLUSTER} | Task: {TASK_DEF}")
    print()

    audio_files = find_audio_files()
    print(f"Found {len(audio_files)} meetings with audio files")

    if args.meeting_id:
        af = audio_files.get(args.meeting_id)
        if not af:
            print("No audio found for that meeting ID", file=sys.stderr)
            sys.exit(1)
        candidates = [af]
    else:
        candidates = [af for _, af in sorted(audio_files.items())]

    targets = []
    for af in candidates:
        target, reason = check_target(af, get_meeting(af["meeting_id"]))
        if target:
            targets.append(target)
        elif args.meeting_id:
            print(f"Refusing: {reason}", file=sys.stderr)
            sys.exit(1)

    print(f"\nTargets: {len(targets)} meetings")
    print(f"{'ID':>10s}  {'Size':>6s}  {'Provider':>12s}  {'Status':>12s}")
    print("-" * 48)
    for t in targets:
        print(f"{t['meeting_id'][:10]}  {t['size_mb']:>5.1f}M  {t['sttProvider'] or 'transcribe':>12s}  {t['status']:>12s}")

    if not args.run:
        hint = "--run <id>" if args.meeting_id else "--run <id> (or --run --all)"
        print(f"\nDry run complete. Use {hint} to trigger ECS tasks.")
        return

    print(f"\nTriggering {len(targets)} Whisper ECS tasks...")
    success = 0
    for i, t in enumerate(targets):
        print(f"\n[{i+1}/{len(targets)}] {t['meeting_id'][:10]}")
        if not claim_meeting(t):
            print("  SKIPPED: status, updatedAt or audioCrop changed since it was read")
            continue
        if run_whisper_task(t["meeting_id"], t["user_id"], t["key"], args.num_speakers):
            success += 1
        else:
            print("  The meeting stays transcribing until the stale-transcribing expiry or another re-run")
        time.sleep(2)

    print(f"\n{'=' * 60}")
    print(f"Triggered: {success}/{len(targets)} tasks")
    print("Monitor progress: aws ecs list-tasks --cluster ttobak-whisper")


if __name__ == "__main__":
    main()
