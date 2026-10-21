"""Bounded scheduled recovery with a leased, durable DynamoDB scan cursor.

Only expired NEWSRUN rows are finalized. The cursor advances after a full page
is handled; interruption retries that page and finalization receipts are stable.
No new table index, source-status mutation or speculative charge release occurs.
"""

import logging
import sys
import time
import uuid

import boto3
from botocore.exceptions import ClientError

from news_lifecycle import NewsRuns, Run
from news_runtime import DeadlineClient, _event, _remaining, _result, _store
from news_store import NewsStore, StateRejected

CHECKPOINT = {"PK": "CRAWLER#__news_runtime__", "SK": "RECOVERY"}
MAX_PAGES = 10
PAGE_SIZE = 100
LEASE_SECONDS = 300
WORK_SECONDS = 240
logger = logging.getLogger(__name__)


def _cursor(value):
    if value is None:
        return None
    if (not isinstance(value, dict) or set(value) != {"PK", "SK"}
            or any(not isinstance(value[key], str) or not value[key]
                   or len(value[key].encode("utf-8")) > limit
                   for key, limit in (("PK", 2048), ("SK", 1024)))):
        raise StateRejected("INVALID_RECOVERY_CURSOR")
    return value


def recover(store, remaining, now, token):
    started = time.monotonic()
    deadline = started + min(WORK_SECONDS, remaining())
    lease_remaining = lambda: min(remaining(), max(0, deadline - time.monotonic()))
    original_client = store.client
    client = DeadlineClient(original_client, lease_remaining)
    # This also bounds every nested read/transaction inside NewsRuns.finish.
    store = NewsStore(client, store.table_name)
    try:
        claimed = client.update_item(
            TableName=store.table_name, Key=CHECKPOINT,
            UpdateExpression="SET leaseUntil = :until, leaseToken = :token",
            ConditionExpression="attribute_not_exists(leaseUntil) OR leaseUntil <= :now",
            ExpressionAttributeValues={":until": now + LEASE_SECONDS, ":now": now, ":token": token},
            ReturnValues="ALL_NEW")["Attributes"]
    except ClientError as error:
        if error.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            return {"status": "BUSY", "pages": 0, "recovered": 0}
        raise
    pages = recovered = busy = conflicts = 0
    complete = False
    try:
        cursor = _cursor(claimed.get("scanCursor"))
        for _ in range(MAX_PAGES):
            if lease_remaining() < 30:
                break
            args = {
                "TableName": store.table_name, "ConsistentRead": True, "Limit": PAGE_SIZE,
                "ProjectionExpression": "PK, SK, sourceId, runId, sourceRevision",
                "FilterExpression": "begins_with(PK, :prefix) AND begins_with(SK, :run)"
                                    " AND runState = :open AND acceptUntil <= :now",
                "ExpressionAttributeValues": {
                    ":prefix": "CRAWLER#", ":run": "NEWSRUN#", ":open": "OPEN", ":now": now},
            }
            if cursor:
                args["ExclusiveStartKey"] = cursor
            response = client.scan(**args)
            rows = response.get("Items")
            if not isinstance(rows, list) or len(rows) > PAGE_SIZE:
                raise StateRejected("INVALID_RUN_STATE")
            following = _cursor(response.get("LastEvaluatedKey"))
            if following is not None and following == cursor:
                raise StateRejected("STATE_UNAVAILABLE")
            for row in rows:
                if lease_remaining() < 20:
                    # Leave this page's cursor unchanged. Already finalized rows
                    # disappear from the filter on replay.
                    raise StateRejected("RUN_BUDGET")
                run = Run(row.get("sourceId"), row.get("runId"), row.get("sourceRevision"))
                if row.get("PK") != run.pk or row.get("SK") != run.sk:
                    raise StateRejected("INVALID_RUN_STATE")
                try:
                    NewsRuns(store).finish(run, int(time.time()), recovery=True)
                    recovered += 1
                except StateRejected as error:
                    if error.reason == "RUN_BUSY":
                        busy += 1
                    elif error.reason == "STATE_CONFLICT":
                        conflicts += 1
                    else:
                        raise
            update = "SET lastPageAt = :now"
            values = {":token": token, ":now": now}
            if following:
                update += ", scanCursor = :cursor"
                values[":cursor"] = following
            else:
                update += " REMOVE scanCursor"
            client.update_item(
                TableName=store.table_name, Key=CHECKPOINT,
                UpdateExpression=update, ConditionExpression="leaseToken = :token",
                ExpressionAttributeValues=values)
            pages += 1
            cursor = following
            if not cursor:
                complete = True
                break
        return {"status": "COMPLETE" if complete else "CONTINUE", "pages": pages,
                "recovered": recovered, "busy": busy, "conflicts": conflicts}
    finally:
        # On timeout Lambda cannot execute finally; the short lease expires.
        # An ambiguous release never deletes state or another invocation's lease.
        primary_failure = sys.exc_info()[0] is not None
        if remaining() >= 10 and time.monotonic() < started + LEASE_SECONDS - 10:
            try:
                original_client.update_item(
                    TableName=store.table_name, Key=CHECKPOINT,
                    UpdateExpression="REMOVE leaseToken, leaseUntil",
                    ConditionExpression="leaseToken = :token",
                    ExpressionAttributeValues={":token": token})
            except Exception as error:
                lost = isinstance(error, ClientError) and error.response.get(
                    "Error", {}).get("Code") == "ConditionalCheckFailedException"
                if not lost:
                    if not primary_failure:
                        raise
                    logger.warning("News recovery lease release failed; original failure retained")


def handler(event, context):
    _event(event, ())
    remaining = _remaining(context)
    store = _store(boto3.Session(), remaining)
    return _result(recover(store, remaining, int(time.time()), uuid.uuid4().hex))
