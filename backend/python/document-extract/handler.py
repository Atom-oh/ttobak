"""Lambda parent: authorize canonical source, run isolated parser, publish by CAS."""
import os
import re
import time
from dataclasses import replace
from decimal import Decimal

import boto3
from botocore.config import Config
from botocore.exceptions import BotoCoreError, ClientError

from aws_state import ConditionRejected, StateStore, timestamp
from contract import Limits, encoded
from worker import run_parser, valid_result


class ExtractionFailure(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


class Budget:
    def __init__(self, context):
        self.deadline = time.monotonic() + max(0, context.get_remaining_time_in_millis()) / 1000

    def remaining(self):
        return max(0, self.deadline - time.monotonic())

    def require(self, seconds=15):
        if self.remaining() <= seconds:
            raise ExtractionFailure("TIMEOUT")


def event_job(event, bucket):
    if (not isinstance(event, dict) or event.get("source") != "ttobak.upload" or
            event.get("detail-type") != "DocumentUploadCompleted"):
        return None
    job = event.get("detail")
    if not isinstance(job, dict) or job.get("bucket") != bucket:
        return None
    for key in ("meetingId", "ownerId", "userId", "attachmentId", "runId"):
        if not isinstance(job.get(key), str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", job[key]):
            return None
    # key is intentionally never used as a source authority.
    return {key: job[key] for key in ("meetingId", "ownerId", "userId", "attachmentId", "runId")}


def valid_source(key, job):
    if not isinstance(key, str):
        return False
    try:
        if len(key.encode("utf-8")) > 1024:
            return False
    except UnicodeError:
        return False
    parts = key.split("/")
    return (len(parts) == 4 and parts[:3] == ["files", job["userId"], job["meetingId"]] and
            parts[3] not in ("", ".", "..") and "\\" not in key and
            all(ord(char) >= 32 and ord(char) != 127 for char in key))


def queued_for_job(state, job, now_ms):
    lease = state.get("leaseUntil")
    return (state.get("runId") == job["runId"] and state.get("status") == "queued" and
            state.get("ownerId") == job["ownerId"] and state.get("uploaderId") == job["userId"] and
            isinstance(lease, (int, Decimal)) and not isinstance(lease, bool) and
            lease == int(lease) and now_ms < lease and isinstance(state.get("sourceKey"), str))


def check_canonical(parent, attachment, state, job):
    if not parent or not attachment:
        raise ExtractionFailure("SOURCE_UNAVAILABLE")
    if (parent.get("meetingId") != job["meetingId"] or parent.get("userId") != job["ownerId"] or
            attachment.get("meetingId") != job["meetingId"] or attachment.get("userId") != job["userId"] or
            attachment.get("attachmentId") != job["attachmentId"] or
            attachment.get("originalKey") != state["sourceKey"]):
        raise ExtractionFailure("SOURCE_CHANGED")
    if not valid_source(state["sourceKey"], job):
        raise ExtractionFailure("INVALID_SOURCE")


def head_source(s3, bucket, key, budget):
    budget.require()
    head = s3.head_object(Bucket=bucket, Key=key)
    size, etag = head.get("ContentLength"), head.get("ETag")
    if type(size) is not int or size < 0 or not isinstance(etag, str) or not 0 < len(etag) <= 256:
        raise ExtractionFailure("SOURCE_UNAVAILABLE")
    if size > Limits().max_input_bytes:
        raise ExtractionFailure("SOURCE_TOO_LARGE")
    return size, etag


def read_source(s3, bucket, key, size, etag, budget):
    budget.require()
    response = s3.get_object(Bucket=bucket, Key=key, IfMatch=etag)
    # Manual streaming is deliberate: transfers cannot enforce both IfMatch
    # and an actual-byte limit before an entire download.
    body = response["Body"]
    try:
        if response.get("ETag") != etag or response.get("ContentLength") != size:
            raise ExtractionFailure("SOURCE_CHANGED")
        data = bytearray()
        while True:
            budget.require()
            chunk = body.read(min(65536, size + 1 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
            if len(data) > size:
                raise ExtractionFailure("SOURCE_TOO_LARGE")
        if len(data) != size:
            raise ExtractionFailure("SOURCE_CHANGED")
        return bytes(data)
    finally:
        body.close()


def parse_and_store(s3, bucket, job, state, budget, parser):
    key = state["sourceKey"]
    fmt = key.rsplit(".", 1)[-1].lower()
    if fmt not in ("pdf", "pptx", "docx", "md"):
        raise ExtractionFailure("UNSUPPORTED_FORMAT")
    phase = "read"
    try:
        size, etag = head_source(s3, bucket, key, budget)
        data = read_source(s3, bucket, key, size, etag, budget)
        budget.require(20)
        # Reserve room for fixed-size source provenance and terminal AWS calls.
        limits = replace(Limits(), max_result_bytes=Limits().max_result_bytes - 4096,
                         wall_seconds=min(Limits().wall_seconds, budget.remaining() - 20))
        result = parser(data, fmt, limits)
        if not valid_result(result, fmt, limits):
            raise ExtractionFailure("WORKER_FAILED")
        if result["status"] == "failed":
            raise ExtractionFailure(result["error"]["code"])
        if head_source(s3, bucket, key, budget) != (size, etag):
            raise ExtractionFailure("SOURCE_CHANGED")
        result["source"] = {**job, "uploaderId": job["userId"], "bucket": bucket, "key": key, "eTag": etag}
        del result["source"]["userId"]
        output = encoded(result)
        if len(output) > Limits().max_result_bytes:
            raise ExtractionFailure("WORKER_OUTPUT_LIMIT")
        result_key = f'files/{job["userId"]}/{job["meetingId"]}/text/{job["attachmentId"]}/{job["runId"]}.json'
        phase = "write"
        budget.require()
        s3.put_object(Bucket=bucket, Key=result_key, Body=output, ContentType="application/json",
                      IfNoneMatch="*")
        phase = "read"
        if head_source(s3, bucket, key, budget) != (size, etag):
            raise ExtractionFailure("SOURCE_CHANGED")
        return {
            "status": result["status"], "complete": result["complete"], "sourceETag": etag,
            "resultKey": result_key, "unitCount": len(result["units"]),
            "errorCode": "" if result["complete"] else "PARTIAL_EXTRACTION",
        }
    except ClientError as error:
        code = error.response.get("Error", {}).get("Code")
        if phase == "read" and code in ("PreconditionFailed", "412"):
            raise ExtractionFailure("SOURCE_CHANGED") from None
        raise ExtractionFailure("RESULT_WRITE_FAILED" if phase == "write" else "SOURCE_UNAVAILABLE") from None
    except BotoCoreError:
        raise ExtractionFailure("RESULT_WRITE_FAILED" if phase == "write" else "SOURCE_UNAVAILABLE") from None


def handle_event(event, context, *, s3, ddb, bucket, table, parser=run_parser, clock_ms=None):
    job = event_job(event, bucket)
    if job is None:
        return {"status": "ignored"}
    clock_ms = clock_ms or (lambda: time.time_ns() // 1_000_000)
    store = StateStore(ddb, table, job)
    state = store.get(store.state_key)
    if not queued_for_job(state, job, clock_ms()):
        return {"status": "ignored"}
    budget = Budget(context)
    try:
        check_canonical(store.get(store.parent_key), store.get(store.attachment_key), state, job)
        budget.require(25)
    except ExtractionFailure as error:
        return store.fail(state, error.code, clock_ms())
    now = clock_ms()
    running = {**state, "status": "running", "leaseUntil": now + min(120_000, int(budget.remaining() * 1000))}
    try:
        store.transact(state, {"status": "running", "leaseUntil": running["leaseUntil"],
                               "errorCode": "", "updatedAt": timestamp(now)}, now)
    except ConditionRejected:
        # A duplicate cannot fail an already-claimed run. If the source vanished,
        # fail only this still-queued version so status does not falsely linger.
        return store.fail(state, "SOURCE_CHANGED", clock_ms())
    try:
        updates = parse_and_store(s3, bucket, job, running, budget, parser)
    except ExtractionFailure as error:
        return store.fail(running, error.code, clock_ms())
    except Exception:
        # Never expose library/SDK response bodies or extracted content in logs.
        return store.fail(running, "WORKER_FAILED", clock_ms())
    now = clock_ms()
    updates.update({"leaseUntil": 0, "updatedAt": timestamp(now)})
    try:
        store.transact(running, updates, now)
    except ConditionRejected:
        return store.fail(running, "SOURCE_CHANGED", clock_ms())
    return {"status": updates["status"]}


_clients = None


def lambda_handler(event, context):
    bucket, table = os.environ.get("BUCKET_NAME"), os.environ.get("TABLE_NAME")
    if not bucket or not table:
        raise RuntimeError("CONFIGURATION_ERROR")
    if event_job(event, bucket) is None:
        return {"status": "ignored"}
    global _clients
    if _clients is None:
        # One attempt preserves the meaning of conditional rejection after an
        # ambiguous write. Warm invocations reuse connections. No import-time AWS.
        config = Config(connect_timeout=2, read_timeout=4, retries={"mode": "standard", "total_max_attempts": 1})
        session = boto3.Session()
        _clients = (session.client("s3", config=config), session.client("dynamodb", config=config))
    return handle_event(event, context, s3=_clients[0], ddb=_clients[1], bucket=bucket, table=table)
