#!/usr/bin/env python3
"""Inspect one retained failure; optionally resubmit its unchanged payload.

No queue deletion, table mutation, body logging, invented run IDs or automatic
mutation retry. See docs/runbooks/pipeline-failure-recovery.md.
"""
import argparse
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import sys
from urllib.parse import unquote_plus

MAX_BYTES = 1024 * 1024
ID = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
ROUTES = {
    ("ttobak.audio", "AudioCropRequested"): ("transcribe", "ttobak-audio-crop"),
    ("ttobak.upload", "ImageUploadCompleted"): ("process-image", "ttobak-image-upload"),
    ("ttobak.transcribe", "AllPartsTranscribed"): ("summarize", "ttobak-all-parts-transcribed"),
    ("ttobak.analysis", "SummaryRequested"): ("summarize", "ttobak-saved-summary-requested"),
    ("ttobak.analysis", "ActionItemsRequested"): ("summarize", "ttobak-action-items-requested"),
}
MEETING_FIELDS = (
    "meetingId", "userId", "status", "audioKey", "audioKeys", "audioPartCount",
    "audioPartsReadySet", "allPartsEmittedAt", "summarizeRetryClaimedAt",
    "summaryRetryPending", "summaryRetryAttempts", "updatedAt", "createdAt", "audioCrop",
)


class Rejected(Exception):
    """Only fixed, non-sensitive reason codes may be displayed."""


def require(condition, code):
    if not condition:
        raise Rejected(code)


def timestamp(value):
    require(isinstance(value, str), "INVALID_TIMESTAMP")
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        raise Rejected("INVALID_TIMESTAMP") from None
    require(result.tzinfo is not None, "INVALID_TIMESTAMP")
    return result.timestamp()


def normalize(value):
    if isinstance(value, Decimal):
        return str(value)
    if isinstance(value, datetime):
        return value.isoformat()
    if isinstance(value, set):
        return sorted(normalize(item) for item in value)
    if isinstance(value, dict):
        return {key: normalize(item) for key, item in value.items()}
    if isinstance(value, (tuple, list)):
        return [normalize(item) for item in value]
    return value


def digest(value):
    return hashlib.sha256(json.dumps(normalize(value), sort_keys=True, separators=(",", ":"),
                                     ensure_ascii=False).encode()).hexdigest()


def load_message(filename):
    with Path(filename).open("rb") as source:
        raw = source.read(MAX_BYTES + 1)
    require(len(raw) <= MAX_BYTES, "MESSAGE_TOO_LARGE")
    try:
        result = json.loads(raw)
    except (ValueError, UnicodeError):
        raise Rejected("INVALID_MESSAGE_JSON") from None
    require(isinstance(result, dict), "INVALID_MESSAGE")
    # Accept one SQS message, not a ReceiveMessage response that could silently
    # hide additional records. Export exactly the intended message to this file.
    require(isinstance(result.get("Body"), str), "SQS_MESSAGE_REQUIRED")
    return result


def unwrap(message, kind, account, region):
    try:
        body = json.loads(message["Body"])
    except (KeyError, TypeError, ValueError):
        raise Rejected("INVALID_MESSAGE_BODY") from None
    require(isinstance(body, dict), "INVALID_MESSAGE_BODY")
    if kind == "execution":
        require(body.get("version") == "1.0", "INVALID_DESTINATION_RECORD")
        context = body.get("requestContext", {})
        require(context.get("condition") in ("RetriesExhausted", "EventAgeExceeded"),
                "INVALID_DESTINATION_CONDITION")
        event = body.get("requestPayload")
    else:
        event = body
    require(isinstance(event, dict) and isinstance(event.get("detail"), dict), "INVALID_EVENT")
    require(event.get("account") == account and event.get("region") == region, "FOREIGN_EVENT")
    require(isinstance(event.get("id"), str) and bool(ID.fullmatch(event["id"])), "INVALID_EVENT_ID")
    require(len(json.dumps(event).encode()) <= 32 * 1024, "EVENT_TOO_LARGE")
    return event, body


class Inspector:
    def __init__(self, table, s3, *, account, region, bucket, owner_id,
                 meeting_id="", attachment_id="", document_id="", now=None):
        self.table, self.s3 = table, s3
        self.account, self.region, self.bucket = account, region, bucket
        self.owner, self.meeting = owner_id, meeting_id
        self.attachment, self.document = attachment_id, document_id
        self.fixed_now = now
        self.now = now if now is not None else datetime.now(timezone.utc).timestamp()
        require(bool(ID.fullmatch(owner_id)), "INVALID_OWNER_ID")
        for value in (meeting_id, attachment_id, document_id):
            require(not value or bool(ID.fullmatch(value)), "INVALID_RESOURCE_ID")
        self.observed = []

    def read(self, pk, sk, fields):
        names = {f"#f{i}": field for i, field in enumerate(fields)}
        item = self.table.get_item(
            Key={"PK": pk, "SK": sk}, ConsistentRead=True,
            ProjectionExpression=", ".join(names), ExpressionAttributeNames=names,
        ).get("Item")
        require(isinstance(item, dict) and bool(item), "CURRENT_RESOURCE_MISSING")
        self.observed.append({"key": [pk, sk], "metadata": item})
        return item

    def head(self, key, event_time, original=None):
        require(isinstance(key, str) and 0 < len(key.encode()) <= 1024 and "\\" not in key
                and all(piece not in ("", ".", "..") for piece in key.split("/")),
                "INVALID_OBJECT_KEY")
        current = self.s3.head_object(Bucket=self.bucket, Key=key)
        require(current.get("ETag") and isinstance(current.get("LastModified"), datetime),
                "INVALID_OBJECT_METADATA")
        require(current["LastModified"].timestamp() <= event_time, "SOURCE_REPLACED_AFTER_EVENT")
        if original is not None:
            require(isinstance(original.get("etag"), str) and
                    original["etag"].strip('"') == current["ETag"].strip('"'), "SOURCE_ETAG_CHANGED")
            if original.get("version-id"):
                require(original["version-id"] == current.get("VersionId"), "SOURCE_VERSION_CHANGED")
        self.observed.append({"key": key, "etag": current["ETag"],
                              "version": current.get("VersionId"), "modified": current["LastModified"]})
        return current

    def meeting_row(self, event_time):
        require(bool(self.meeting), "MEETING_ID_REQUIRED")
        meeting = self.read("USER#" + self.owner, "MEETING#" + self.meeting, MEETING_FIELDS)
        require(meeting.get("meetingId") == self.meeting and meeting.get("userId") == self.owner,
                "CURRENT_IDENTITY_MISMATCH")
        require(timestamp(meeting.get("createdAt")) <= event_time, "EVENT_PREDATES_RESOURCE")
        return meeting

    def summary_ready(self, meeting, event_time):
        status = meeting.get("status")
        require(status not in ("done", "error"), "USE_AUTHENTICATED_RESUMMARY")
        require(not meeting.get("summarizeRetryClaimedAt"), "SUMMARY_CLAIM_PRESENT")
        require(int(meeting.get("summaryRetryAttempts", 0)) < 2, "USE_AUTHENTICATED_RESUMMARY")
        if status == "summarizing":
            require(timestamp(meeting.get("updatedAt")) + 20 * 60 <= self.now, "SUMMARY_STILL_ACTIVE")
        else:
            require(status == "transcribing", "MEETING_NOT_REPLAYABLE")
            require(timestamp(meeting.get("updatedAt")) <= event_time, "MEETING_CHANGED_AFTER_EVENT")

    def plan(self, message, kind):
        self.now = self.fixed_now if self.fixed_now is not None else datetime.now(timezone.utc).timestamp()
        self.observed = []
        event, body = unwrap(message, kind, self.account, self.region)
        event_time = timestamp(event.get("time"))
        require(self.now - 7 * 86400 <= event_time <= self.now, "EVENT_OUTSIDE_RETENTION")
        detail = event["detail"]
        route = ROUTES.get((event.get("source"), event.get("detail-type")))
        if event.get("source") == "aws.s3" and event.get("detail-type") == "Object Created":
            require(detail.get("bucket", {}).get("name") == self.bucket, "FOREIGN_BUCKET")
            obj = detail.get("object", {})
            key = obj.get("key", "")
            if key.startswith("audio/"):
                route = ("transcribe", "ttobak-audio-upload")
                meeting = self.meeting_row(event_time)
                require(meeting.get("status") == "transcribing" and not meeting.get("audioCrop"),
                        "AUDIO_NOT_REPLAYABLE")
                require(timestamp(meeting.get("updatedAt")) <= event_time, "MEETING_CHANGED_AFTER_EVENT")
                keys = meeting.get("audioKeys") or [meeting.get("audioKey")]
                require(key in keys and key.startswith(f"audio/{self.owner}/{self.meeting}/"),
                        "AUDIO_BINDING_CHANGED")
                require(not any(marker in key for marker in ("checkpoint_", "recording_progress", "realtime_", "crop_result_")),
                        "NONFINAL_AUDIO")
            elif key.startswith("transcripts/"):
                route = ("summarize", "ttobak-transcript-upload")
                meeting = self.meeting_row(event_time)
                self.summary_ready(meeting, event_time)
                match = re.fullmatch(r"transcripts/" + re.escape(self.meeting) + r"(?:_part_([0-9]{3})|-nova)?\.json", key)
                require(match is not None, "TRANSCRIPT_IDENTITY_MISMATCH")
                if match.group(1) is not None:
                    part = int(match.group(1))
                    require(part < int(meeting.get("audioPartCount", 0)), "INVALID_TRANSCRIPT_PART")
                    require(part not in meeting.get("audioPartsReadySet", set()), "TRANSCRIPT_PART_ALREADY_APPLIED")
            elif key.startswith("docs/"):
                route = ("convert-doc", "ttobak-doc-slide-upload")
                require(self.document and key.startswith(f"docs/{self.owner}/") and
                        key.lower().endswith((".ppt", ".pptx")), "DOCUMENT_IDENTITY_REQUIRED")
                doc = self.read("USER#" + self.owner, "DOC#" + self.document,
                                ("docId", "fileKey", "createdAt", "updatedAt"))
                require(doc.get("docId") == self.document and doc.get("fileKey") == key,
                        "DOCUMENT_BINDING_CHANGED")
            else:
                raise Rejected("UNSUPPORTED_S3_EVENT")
            # Existing transcribe/summarize handlers decode form-style keys.
            # Do not replay a key they would interpret differently.
            require(route[0] == "convert-doc" or unquote_plus(key) == key, "AMBIGUOUS_LEGACY_KEY")
            self.head(key, event_time, obj)
        elif route:
            require(detail.get("meetingId") == self.meeting, "EVENT_MEETING_MISMATCH")
            meeting = self.meeting_row(event_time)
            event_type = event["detail-type"]
            if event_type in ("SummaryRequested", "ActionItemsRequested"):
                run = detail.get("runId")
                require(isinstance(run, str) and bool(ID.fullmatch(run)), "INVALID_RUN_ID")
                summary = event_type == "SummaryRequested"
                state = self.read("MEETING#" + self.meeting,
                                  "ANALYSIS#summary" if summary else "ANALYSIS#actionItems",
                                  ("runId", "status", "leaseUntil", "ownerId", "sourceHash", "updatedAt"))
                require(state.get("runId") == run, "STALE_RUN")
                require(state.get("status") == "queued" and int(state.get("leaseUntil", 0)) > self.now * 1000,
                        "USE_AUTHENTICATED_ANALYSIS_RETRY")
                require((state.get("ownerId") if summary else detail.get("ownerId")) == self.owner,
                        "EVENT_OWNER_MISMATCH")
            elif event_type == "AllPartsTranscribed":
                self.summary_ready(meeting, event_time)
                require(detail.get("userId") == self.owner and detail.get("bucket") == self.bucket,
                        "EVENT_OWNER_OR_BUCKET_MISMATCH")
                parts = detail.get("partCount")
                require(isinstance(parts, int) and 1 <= parts <= 10 and parts == meeting.get("audioPartCount")
                        and set(meeting.get("audioPartsReadySet", set())) == set(range(parts)),
                        "PART_SET_CHANGED")
                emitted = timestamp(meeting.get("allPartsEmittedAt"))
                require(emitted <= event_time <= emitted + 60, "STALE_PARTS_EVENT")
                for part in range(parts):
                    self.head(f"transcripts/{self.meeting}_part_{part:03d}.json", event_time)
            elif event_type == "AudioCropRequested":
                crop = meeting.get("audioCrop") or {}
                require(detail.get("userId") == self.owner and meeting.get("status") == "transcribing"
                        and crop.get("state") == "queued" and not crop.get("runId"), "CROP_NOT_REPLAYABLE")
                require(timestamp(meeting.get("updatedAt")) <= event_time, "MEETING_CHANGED_AFTER_EVENT")
                require(str(crop.get("sourceKey", "")).startswith(f"audio/{self.owner}/{crop.get('sourceMeetingId')}/"),
                        "FOREIGN_CROP_SOURCE")
                source = self.read("USER#" + self.owner, "MEETING#" + str(crop.get("sourceMeetingId")),
                                   ("meetingId", "userId"))
                require(source.get("meetingId") == crop.get("sourceMeetingId") and
                        source.get("userId") == self.owner, "CROP_SOURCE_IDENTITY_CHANGED")
                self.head(crop["sourceKey"], event_time, {"etag": crop.get("sourceETag")})
            else:  # ImageUploadCompleted
                require(detail.get("bucket") == self.bucket and self.attachment, "IMAGE_IDENTITY_REQUIRED")
                uploader, key = detail.get("userId"), detail.get("key")
                require(isinstance(uploader, str) and bool(ID.fullmatch(uploader)) and
                        isinstance(key, str) and key.startswith(f"images/{uploader}/{self.meeting}/"),
                        "FOREIGN_IMAGE")
                att = self.read("MEETING#" + self.meeting, "ATTACH#" + self.attachment,
                                ("attachmentId", "meetingId", "userId", "originalKey", "status", "type", "createdAt"))
                require(att.get("attachmentId") == self.attachment and att.get("meetingId") == self.meeting
                        and att.get("userId") == uploader and att.get("originalKey") == key, "IMAGE_BINDING_CHANGED")
                require(att.get("status") in ("uploaded", "processing"), "IMAGE_ALREADY_COMPLETED")
                require(timestamp(att.get("createdAt")) <= event_time, "EVENT_PREDATES_ATTACHMENT")
                self.head(key, event_time)
        else:
            raise Rejected("UNSUPPORTED_EVENT")
        function, rule = route
        target = f"arn:aws:lambda:{self.region}:{self.account}:function:ttobak-{function}"
        if kind == "execution":
            require(body["requestContext"].get("functionArn") in (target, target + ":$LATEST"),
                    "FOREIGN_FUNCTION")
        else:
            attributes = message.get("MessageAttributes", {})
            require(attributes.get("RULE_ARN", {}).get("StringValue") ==
                    f"arn:aws:events:{self.region}:{self.account}:rule/{rule}", "FOREIGN_RULE")
            require(attributes.get("TARGET_ARN", {}).get("StringValue") == target, "FOREIGN_TARGET")
        return {"target": target, "event": event,
                "planHash": digest({"target": target, "event": event, "state": self.observed})}


def execute(plan, client):
    # No SDK retry: a transport failure may occur after Lambda accepted work.
    response = client.invoke(FunctionName=plan["target"], InvocationType="Event",
                             Payload=json.dumps(plan["event"], ensure_ascii=False).encode())
    payload = response.get("Payload")
    if payload is not None:
        payload.close()
    require(response.get("StatusCode") == 202, "INVOCATION_NOT_ACCEPTED")
    return {"status": "accepted_not_completed", "planHash": plan["planHash"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--message-file", required=True)
    parser.add_argument("--kind", required=True, choices=("delivery", "execution"))
    parser.add_argument("--owner-id", required=True)
    parser.add_argument("--meeting-id", default="")
    parser.add_argument("--attachment-id", default="")
    parser.add_argument("--document-id", default="")
    parser.add_argument("--region", default="ap-northeast-2")
    parser.add_argument("--profile")
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--confirm-plan", default="")
    parser.add_argument("--workers-quiesced", action="store_true",
                        help="Operator verified no worker or concurrent source edit can race this replay.")
    args = parser.parse_args()
    try:
        import boto3
        from botocore.config import Config
        message = load_message(args.message_file)
        config = Config(connect_timeout=3, read_timeout=10, retries={"total_max_attempts": 1})
        session = boto3.Session(profile_name=args.profile, region_name=args.region)
        account = session.client("sts", config=config).get_caller_identity()["Account"]
        require(bool(re.fullmatch(r"[0-9]{12}", account)), "INVALID_ACCOUNT")
        inspector = Inspector(session.resource("dynamodb", config=config).Table("ttobak-main"),
                              session.client("s3", config=config), account=account, region=args.region,
                              bucket="ttobak-assets-" + account, owner_id=args.owner_id,
                              meeting_id=args.meeting_id, attachment_id=args.attachment_id,
                              document_id=args.document_id)
        plan = inspector.plan(message, args.kind)
        if args.execute:
            require(args.workers_quiesced, "WORKER_QUIESCENCE_REQUIRED")
            require(args.confirm_plan == plan["planHash"], "PLAN_CHANGED")
            # Re-read canonical metadata and object versions immediately before
            # submission. This is an operator guard, not an atomic worker claim.
            require(inspector.plan(message, args.kind)["planHash"] == plan["planHash"], "PLAN_CHANGED")
            result = execute(plan, session.client("lambda", config=config))
        else:
            result = {"status": "dry_run", "target": plan["target"], "planHash": plan["planHash"]}
        print(json.dumps(result, sort_keys=True))
        return 0
    except Rejected as error:
        print(json.dumps({"status": "rejected", "code": str(error)}), file=sys.stderr)
    except Exception:
        # SDK error messages can contain object keys, payloads or provider output.
        print(json.dumps({"status": "failed", "code": "OPERATION_FAILED_OR_OUTCOME_UNKNOWN"}), file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
