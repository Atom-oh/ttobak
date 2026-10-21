"""Private Lambda entry points for bounded, publication-aware news runs.

Writers are off unless deployment explicitly enables a canary source or all
sources. IAM and Step Functions own ingress; these are not application routes.
"""

from datetime import date, datetime, timezone
from decimal import Decimal
import json
import os
import re
import time

import boto3
from botocore.config import Config

from news_discovery import discover, source_revision, valid_source_id
from news_lifecycle import NewsRuns, Run
from news_model import NewsModel
from news_search import NewsSearch
from news_store import NewsStore, StateRejected, _money
from news_worker import ModelPolicy, WorkerDeps, _candidate, process_candidate

MAX_EVENT_BYTES = 224000
MAX_RESULT_BYTES = 200000
SDK_CONFIG = Config(connect_timeout=2, read_timeout=3,
                    retries={"total_max_attempts": 1}, tcp_keepalive=True)
S3_CONFIG = Config(connect_timeout=2, read_timeout=5,
                   retries={"total_max_attempts": 1}, tcp_keepalive=True)
MODEL_CONFIG = Config(connect_timeout=5, read_timeout=120,
                      retries={"total_max_attempts": 1}, tcp_keepalive=True)


def _encode(value):
    def normalize(item):
        if isinstance(item, dict):
            return {key: str(child) if key in ("chargedUsd", "reservedUsd")
                    and isinstance(child, Decimal) and child.is_finite()
                    else normalize(child) for key, child in item.items()}
        if isinstance(item, (list, tuple)):
            return [normalize(child) for child in item]
        return item

    def decimal(item):
        if not isinstance(item, Decimal) or not item.is_finite() or item != int(item):
            raise ValueError("INVALID_JSON")
        # DynamoDB replay must retain the original integer counter shape.
        return int(item)
    return json.dumps(normalize(value), ensure_ascii=False, allow_nan=False,
                      separators=(",", ":"), default=decimal).encode("utf-8")


def _event(value, keys=None):
    try:
        if (not isinstance(value, dict) or len(_encode(value)) > MAX_EVENT_BYTES
                or (keys is not None and set(value) != set(keys))):
            raise ValueError()
    except (ValueError, TypeError, UnicodeError, RecursionError):
        raise StateRejected("INVALID_REQUEST") from None
    return value


def _result(value):
    if len(_encode(value)) > MAX_RESULT_BYTES:
        raise StateRejected("RESULT_TOO_LARGE")
    return json.loads(_encode(value))


def _identity(source_id, run_id=None):
    if (not valid_source_id(source_id) or (run_id is not None
            and (not isinstance(run_id, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", run_id)))):
        raise StateRejected("INVALID_IDENTITY")


def _enabled(source_id):
    mode = os.environ.get("NEWS_PIPELINE_MODE", "off")
    if mode == "all":
        if os.environ.get("NEWS_LEGACY_WRITES") != "off":
            raise StateRejected("LEGACY_WRITER_ENABLED")
        return
    if mode == "canary" and source_id == os.environ.get("NEWS_CANARY_SOURCE_ID") and valid_source_id(source_id):
        return
    raise StateRejected("PIPELINE_DISABLED")


def _remaining(context):
    # Freeze an invocation-local monotonic deadline; a faulty/mutable callback
    # cannot extend the execution budget between SDK calls.
    milliseconds = context.get_remaining_time_in_millis()
    if type(milliseconds) is not int or not 0 < milliseconds <= 900000:
        raise StateRejected("INVALID_CONTEXT")
    deadline = time.monotonic() + milliseconds / 1000
    return lambda: max(0, deadline - time.monotonic())


class DeadlineClient:
    def __init__(self, client, remaining, minimum=10):
        self.client, self.remaining, self.minimum = client, remaining, minimum

    def __getattr__(self, name):
        operation = getattr(self.client, name)

        def invoke(**kwargs):
            if self.remaining() < self.minimum:
                raise StateRejected("RUN_BUDGET")
            return operation(**kwargs)
        return invoke


def _store(session, remaining):
    table_name = os.environ.get("TABLE_NAME", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]{3,255}", table_name):
        raise StateRejected("INVALID_CONFIGURATION")
    # Resource.meta.client intentionally retains boto3's native-value DynamoDB
    # transformations for the NewsStore transaction contract.
    client = session.resource("dynamodb", config=SDK_CONFIG).meta.client
    return NewsStore(DeadlineClient(client, remaining), table_name)


def _policy(now):
    try:
        verified = date.fromisoformat(os.environ["NEWS_PRICE_VERIFIED_ON"])
        today = datetime.fromtimestamp(now, timezone.utc).date()
        if not 0 <= (today - verified).days <= 90:
            raise ValueError()
        policy = ModelPolicy(os.environ["NEWS_INPUT_USD_PER_MTOK"],
                             os.environ["NEWS_OUTPUT_USD_PER_MTOK"])
        run_limit = _money(os.environ["NEWS_RUN_LIMIT_USD"])
        month_limit = _money(os.environ["NEWS_MONTH_LIMIT_USD"])
        if not policy.reservation <= run_limit <= month_limit:
            raise ValueError()
        return policy, run_limit, month_limit
    except (KeyError, TypeError, ValueError, StateRejected):
        raise StateRejected("INVALID_PRICING") from None


def discover_handler(event, context):
    event = _event(event, ("sourceId", "runId"))
    source_id, run_id = event["sourceId"], event["runId"]
    _identity(source_id, run_id)
    _enabled(source_id)
    remaining = _remaining(context)
    now = int(time.time())
    _policy(now)  # Do not start a run which cannot reserve a priced attempt.
    session = boto3.Session()
    store = _store(session, remaining)
    config = store._get("CRAWLER#" + source_id, "CONFIG")
    if not config or config.get("sourceId") != source_id:
        raise StateRejected("SOURCE_CHANGED")
    try:
        NewsSearch.validate_url(os.environ.get("WEB_SEARCH_GATEWAY_URL", ""))
    except ValueError:
        raise StateRejected("INVALID_CONFIGURATION") from None
    revision = source_revision(config)
    run = Run(source_id, run_id, revision)
    existing = NewsRuns(store).begin(run, now)
    if existing["runState"] == "FINALIZED":
        return _result({"sourceId": source_id, "runId": run_id,
                        "sourceRevision": revision, "candidates": [], "status": "FINALIZED"})
    if existing.get("acceptUntil", 0) <= now:
        raise StateRejected("RUN_CLOSED")
    if remaining() < 35:
        raise StateRejected("RUN_BUDGET")
    gateway = NewsSearch(os.environ.get("WEB_SEARCH_GATEWAY_URL", ""),
                         session.get_credentials, remaining)
    result = discover(config, gateway, store.published_hashes,
                      datetime.fromtimestamp(now, timezone.utc), run_id)
    # Recheck before candidates leave this stage. Workers also recheck before
    # any fetch and transactionally before publication.
    store.current_config(source_id, revision)
    return _result(result)


def worker_handler(event, context):
    candidate = _event(event)
    source_id, run_id, *_ = _candidate(candidate)
    _enabled(source_id)
    remaining = _remaining(context)
    now = int(time.time())
    policy, run_limit, month_limit = _policy(now)
    session = boto3.Session()
    store = _store(session, remaining)
    artifact_bucket = os.environ.get("NEWS_ARTIFACT_BUCKET", "")
    kb_bucket = os.environ.get("KB_BUCKET_NAME", "")
    if not artifact_bucket or not kb_bucket:
        raise StateRejected("INVALID_CONFIGURATION")
    deps = WorkerDeps(
        store=store,
        s3=DeadlineClient(session.client("s3", config=S3_CONFIG), remaining, 12),
        bedrock=NewsModel(session.client("bedrock-runtime", region_name="ap-northeast-2",
                                        config=MODEL_CONFIG), session.get_credentials),
        artifact_bucket=artifact_bucket, kb_bucket=kb_bucket,
        policy=policy, month_limit=month_limit, run_limit=run_limit,
        remaining=remaining, now=lambda: int(time.time()))
    try:
        return _result(process_candidate(candidate, deps))
    except StateRejected as error:
        # A state rejection before/after reservation is visible. Existing owned
        # claims remain recoverable; never invent a success or release unknown cost.
        return _result({"sourceId": source_id, "runId": run_id, "status": "FAILED",
                        "reason": error.reason, "docsAdded": 0, "docsUpdated": 0})


def finalize_handler(event, context):
    event = _event(event, ("discovery", "outcomes"))
    discovery = event["discovery"]
    if not isinstance(discovery, dict):
        raise StateRejected("INVALID_REQUEST")
    source_id, run_id = discovery.get("sourceId"), discovery.get("runId")
    _identity(source_id, run_id)
    # Finalization/recovery remains available after writers are disabled.
    run = Run(source_id, run_id, discovery.get("sourceRevision"))
    remaining = _remaining(context)
    store = _store(boto3.Session(), remaining)
    return _result(NewsRuns(store).finish(
        run, int(time.time()), discovery=discovery, outcomes=event["outcomes"]))
