"""Conditional news publication and conservative, retry-safe cost accounting.

Staged storage adapter: no handler or client construction. Supply a DynamoDB
resource's ``meta.client`` (native values), with bounded SDK timeouts/retries.
S3 objects must be immutable and consumers must check the published row before
reading them. An uploaded object alone is deliberately not a publication.
"""

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation

from botocore.exceptions import ClientError

from news_discovery import source_revision, valid_source_id

_ID = re.compile(r"[A-Za-z0-9_-]{1,128}")
_HASH = re.compile(r"[0-9a-f]{16}")
_ATTEMPT = re.compile(r"[0-9a-f]{32}")
_REVISION = re.compile(r"[0-9a-f]{64}")
_MONTH = re.compile(r"\d{4}-(?:0[1-9]|1[0-2])")
CONFIG_FIELDS = ("sourceId", "sourceName", "aliases", "newsQueries",
                 "customUrls", "status", "ownerId")
MAX_ITEM_BYTES = 128000
LEASE_SECONDS = 1200


class StateRejected(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


def _match(value, pattern):
    if not isinstance(value, str) or not pattern.fullmatch(value):
        raise StateRejected("INVALID_IDENTITY")
    return value


def _money(value):
    # Reject float input rather than silently inheriting binary rounding.
    if isinstance(value, bool) or not isinstance(value, (str, int, Decimal)):
        raise StateRejected("INVALID_BUDGET")
    try:
        amount = Decimal(value)
    except InvalidOperation:
        raise StateRejected("INVALID_BUDGET") from None
    if not amount.is_finite() or not 0 <= amount <= 100000:
        raise StateRejected("INVALID_BUDGET")
    if amount.as_tuple().exponent < -12:
        raise StateRejected("INVALID_BUDGET")
    return amount


def _encoded(value):
    """Hash native values without merging numeric values with string values."""
    def typed(item):
        if item is None:
            return ["null"]
        if isinstance(item, bool):
            return ["bool", item]
        if isinstance(item, (int, Decimal)):
            number = Decimal(item)
            if not number.is_finite():
                raise StateRejected("INVALID_COMPLETION")
            sign, digits, exponent = number.as_tuple()
            digits = list(digits)
            while digits and digits[-1] == 0:
                digits.pop()
                exponent += 1
            return ["number", sign if digits else 0, "".join(map(str, digits)), exponent if digits else 0]
        if isinstance(item, str):
            return ["string", item]
        if isinstance(item, (list, tuple)):
            return ["list", [typed(member) for member in item]]
        if isinstance(item, dict) and all(isinstance(key, str) for key in item):
            return ["map", [[key, typed(item[key])] for key in sorted(item)]]
        raise StateRejected("INVALID_COMPLETION")
    return json.dumps(typed(value), ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _native(value):
    """Convert a bounded JSON artifact to DynamoDB-native decimal numbers."""
    try:
        data = json.dumps(value, ensure_ascii=False, allow_nan=False,
                          sort_keys=True, separators=(",", ":")).encode("utf-8")
    except (TypeError, ValueError, UnicodeError, RecursionError):
        raise StateRejected("INVALID_COMPLETION") from None
    if len(data) > MAX_ITEM_BYTES:
        raise StateRejected("ITEM_TOO_LARGE")
    return json.loads(data, parse_float=Decimal)


def published(row):
    return bool(row and type(row.get("schemaVersion")) in (int, Decimal)
                and row.get("schemaVersion") == 1
                and row.get("qualityStatus") == "PUBLISHED")


@dataclass(frozen=True)
class Attempt:
    source_id: str
    doc_hash: str
    run_id: str
    token: str
    month: str
    revision: str
    reserved: Decimal
    lease_until: int

    def __post_init__(self):
        if not valid_source_id(self.source_id):
            raise StateRejected("INVALID_IDENTITY")
        _match(self.doc_hash, _HASH)
        _match(self.run_id, _ID)
        _match(self.token, _ATTEMPT)
        _match(self.month, _MONTH)
        _match(self.revision, _REVISION)
        if type(self.lease_until) is not int or self.lease_until < LEASE_SECONDS:
            raise StateRejected("INVALID_LEASE")
        object.__setattr__(self, "reserved", _money(self.reserved))
        if self.reserved == 0:
            raise StateRejected("INVALID_BUDGET")

    @property
    def pk(self):
        return "CRAWLER#" + self.source_id

    @property
    def sk(self):
        return f"NEWSRUN#{self.run_id}#ATTEMPT#{self.token}"

    @property
    def markdown_key(self):
        return f"news/v1/{self.source_id}/{self.doc_hash}/{self.token}.md"

    @property
    def artifact_key(self):
        return f"knowledge-artifacts/news/v1/{self.source_id}/{self.doc_hash}/{self.token}.json"


class NewsStore:
    def __init__(self, client, table_name):
        self.client, self.table_name = client, table_name

    def _get(self, pk, sk):
        return self.client.get_item(
            TableName=self.table_name, Key={"PK": pk, "SK": sk},
            ConsistentRead=True).get("Item")

    def current_config(self, source_id, revision):
        if not valid_source_id(source_id):
            raise StateRejected("INVALID_IDENTITY")
        _match(revision, _REVISION)
        row = self._get("CRAWLER#" + source_id, "CONFIG")
        if (not row or row.get("sourceId") != source_id
                or row.get("status") == "disabled"
                or source_revision(row) != revision):
            raise StateRejected("SOURCE_CHANGED")
        return row

    def published_hashes(self, source_id, hashes):
        if not valid_source_id(source_id):
            raise StateRejected("INVALID_IDENTITY")
        if not isinstance(hashes, set) or len(hashes) > 300:
            raise StateRejected("INVALID_IDENTITY")
        for value in hashes:
            _match(value, _HASH)
        result = set()
        # Strong reads are explicit. Unprocessed keys are an incomplete answer,
        # never evidence that an article has not been published.
        ordered = sorted(hashes)
        for offset in range(0, len(ordered), 100):
            keys = [{"PK": "CRAWLER#" + source_id, "SK": "DOC#" + value}
                    for value in ordered[offset:offset + 100]]
            response = self.client.batch_get_item(RequestItems={
                self.table_name: {"Keys": keys, "ConsistentRead": True}})
            rows = response.get("Responses", {}).get(self.table_name)
            if response.get("UnprocessedKeys") or not isinstance(rows, list):
                raise StateRejected("STATE_UNAVAILABLE")
            requested = {(key["PK"], key["SK"]) for key in keys}
            for row in rows:
                if (row.get("PK"), row.get("SK")) not in requested:
                    raise StateRejected("STATE_UNAVAILABLE")
                if published(row):
                    result.add(row["SK"][4:])
        return result

    def _config_guard(self, attempt, row):
        names = {"#pk": "PK"}
        values = {}
        conditions = ["attribute_exists(#pk)"]
        for index, field in enumerate(CONFIG_FIELDS):
            alias = "#c" + str(index)
            names[alias] = field
            if field in row:
                value = ":c" + str(index)
                values[value] = row[field]
                conditions.append(alias + " = " + value)
            else:
                conditions.append("attribute_not_exists(" + alias + ")")
        return {"ConditionCheck": {
            "TableName": self.table_name, "Key": {"PK": attempt.pk, "SK": "CONFIG"},
            "ConditionExpression": " AND ".join(conditions),
            "ExpressionAttributeNames": names, "ExpressionAttributeValues": values}}

    def _transaction(self, operations):
        # Exact operation bytes select the token; SDK retries carry identical
        # operations. Durable row conditions remain necessary beyond token TTL.
        token = hashlib.sha256(_encoded(operations)).hexdigest()[:32]
        try:
            self.client.transact_write_items(
                TransactItems=operations, ClientRequestToken=token)
        except ClientError as error:
            if error.response.get("Error", {}).get("Code") == "TransactionCanceledException":
                reasons = error.response.get("CancellationReasons", [])
                if any(reason.get("Code") == "ConditionalCheckFailed" for reason in reasons):
                    raise StateRejected("STATE_CONFLICT") from None
            # Ambiguous/transport/throttle errors propagate. Never release a
            # reservation or delete immutable objects on an unknown outcome.
            raise

    def _budget_reservation(self, attempt, sk, limit):
        limit = _money(limit)
        if limit < attempt.reserved:
            raise StateRejected("BUDGET_PAUSED")
        return {"Update": {
            "TableName": self.table_name, "Key": {"PK": attempt.pk, "SK": sk},
            "UpdateExpression": "ADD #charge :reservation",
            "ConditionExpression": "(attribute_not_exists(#charge) OR #charge <= :remaining)"
                                   " AND attribute_not_exists(#blocked)"
                                   " AND attribute_not_exists(#finalized)",
            "ExpressionAttributeNames": {"#charge": "chargedUsd", "#blocked": "blocked",
                                        "#finalized": "finalizedAt"},
            "ExpressionAttributeValues": {
                ":reservation": attempt.reserved, ":remaining": limit - attempt.reserved}}}

    def reserve(self, attempt, *, now, month_limit, run_limit):
        """Reserve once before model calls; each Lambda invocation has a new token.

        Expired attempts retain their full reservation. Taking over a lease
        reserves a new attempt, so a late or ambiguously billed model call never
        becomes free. The supplied reservation must cover the caller's enforced
        model request/token limits at verified configured rates.
        """
        if (type(now) is not int or not 0 <= now <= 253402299599
                or attempt.lease_until != now + LEASE_SECONDS
                or datetime.fromtimestamp(now, timezone.utc).strftime("%Y-%m") != attempt.month):
            raise StateRejected("INVALID_LEASE")
        config = self.current_config(attempt.source_id, attempt.revision)
        row = {
            "PK": attempt.pk, "SK": attempt.sk, "attemptToken": attempt.token,
            "runId": attempt.run_id, "docHash": attempt.doc_hash,
            "sourceRevision": attempt.revision, "month": attempt.month,
            "reservedUsd": attempt.reserved, "chargedUsd": attempt.reserved,
            "leaseUntil": attempt.lease_until, "state": "RUNNING",
        }
        self._transaction([
            self._config_guard(attempt, config),
            {"ConditionCheck": {
                "TableName": self.table_name,
                "Key": {"PK": attempt.pk, "SK": "DOC#" + attempt.doc_hash},
                "ConditionExpression": "attribute_not_exists(#schema) OR #schema <> :one"
                                       " OR attribute_not_exists(#quality) OR #quality <> :published",
                "ExpressionAttributeNames": {"#schema": "schemaVersion", "#quality": "qualityStatus"},
                "ExpressionAttributeValues": {":one": 1, ":published": "PUBLISHED"}}},
            {"Put": {
                "TableName": self.table_name,
                "Item": {"PK": attempt.pk, "SK": "NEWSCLAIM#" + attempt.doc_hash,
                         "attemptToken": attempt.token, "leaseUntil": attempt.lease_until},
                "ConditionExpression": "attribute_not_exists(PK) OR leaseUntil < :now",
                "ExpressionAttributeValues": {":now": now}}},
            {"Put": {"TableName": self.table_name, "Item": row,
                     "ConditionExpression": "attribute_not_exists(PK)"}},
            self._budget_reservation(attempt, "NEWSBUDGET#" + attempt.month, month_limit),
            self._budget_reservation(attempt, "NEWSRUN#" + attempt.run_id, run_limit),
        ])

    def _settlement(self, attempt, sk, charged, unknown):
        values = {":delta": charged - attempt.reserved, ":reserved": attempt.reserved}
        names = {"#charge": "chargedUsd"}
        update = "ADD #charge :delta"
        if unknown:
            names["#unknown"] = "unknownAttempts"
            values[":one"] = 1
            update += ", #unknown :one"
        if charged > attempt.reserved:
            names["#blocked"] = "blocked"
            values[":yes"] = True
            update = "SET #blocked = :yes " + update
        return {"Update": {
            "TableName": self.table_name, "Key": {"PK": attempt.pk, "SK": sk},
            "UpdateExpression": update, "ConditionExpression": "#charge >= :reserved",
            "ExpressionAttributeNames": names, "ExpressionAttributeValues": values}}

    def finish(self, attempt, *, now, status, known_cost, usage_complete, usage,
               document=None, reason="", warnings=()):
        """Atomically settle usage, release the owned lease, and optionally publish.

        A retry returns the recorded receipt only for the same completion digest.
        Model errors with unknown usage keep at least the full reservation.
        """
        if (type(now) is not int or not attempt.lease_until - LEASE_SECONDS <= now <= 253402300799
                or status not in ("PUBLISHED", "REJECTED", "FAILED")
                or type(usage_complete) is not bool
                or not isinstance(reason, str) or not re.fullmatch(r"[A-Z0-9_]{0,64}", reason)
                or not isinstance(warnings, (list, tuple)) or len(warnings) > 2
                or any(value not in ("CORROBORATION_DEFERRED", "CORROBORATION_UNUSABLE",
                                     "CORROBORATION_FETCH_FAILED") for value in warnings)
                or not isinstance(usage, dict)
                or any(type(usage.get(key, 0)) is not int
                       or not 0 <= usage.get(key, 0) <= 10000000
                       for key in ("inputTokens", "outputTokens"))):
            raise StateRejected("INVALID_COMPLETION")
        known = _money(known_cost)
        charged = known if usage_complete else max(known, attempt.reserved)
        clean_usage = {key: usage.get(key, 0) for key in ("inputTokens", "outputTokens")}
        if (status == "PUBLISHED") != (document is not None):
            raise StateRejected("INVALID_COMPLETION")
        row = None
        if document is not None:
            if (not isinstance(document, dict) or type(document.get("schemaVersion")) is not int
                    or document["schemaVersion"] != 1):
                raise StateRejected("INVALID_COMPLETION")
            row = _native(document)
            required = {
                "PK": attempt.pk, "SK": "DOC#" + attempt.doc_hash,
                "schemaVersion": 1, "qualityStatus": "PUBLISHED",
                "sourceId": attempt.source_id, "docHash": attempt.doc_hash,
                "attemptToken": attempt.token, "sourceRevision": attempt.revision,
                "s3Key": attempt.markdown_key, "artifactKey": attempt.artifact_key,
            }
            if any(row.get(key) != value for key, value in required.items()):
                raise StateRejected("INVALID_COMPLETION")
            _match(row.get("markdownSha256"), _REVISION)
            _match(row.get("artifactSha256"), _REVISION)
        receipt = {
            "status": status, "reason": reason, "docHash": attempt.doc_hash,
            "attemptToken": attempt.token, "chargedUsd": charged,
            "usageComplete": usage_complete, **clean_usage,
            "docsAdded": int(status == "PUBLISHED"), "docsUpdated": 0,
        }
        if warnings:
            receipt["fetchWarnings"] = list(warnings)
        identity = {
            "runId": attempt.run_id, "docHash": attempt.doc_hash, "month": attempt.month,
            "attemptToken": attempt.token, "sourceRevision": attempt.revision,
            "reservedUsd": attempt.reserved, "leaseUntil": attempt.lease_until,
        }
        digest = hashlib.sha256(_encoded(
            {"identity": identity, "receipt": receipt, "document": row})).hexdigest()
        previous = self._get(attempt.pk, attempt.sk)
        if previous and any(previous.get(key) != value for key, value in identity.items()):
            raise StateRejected("STATE_CONFLICT")
        if previous and previous.get("state") != "RUNNING":
            if previous.get("completionDigest") != digest:
                raise StateRejected("STATE_CONFLICT")
            return previous["receipt"]
        if now >= attempt.lease_until:
            raise StateRejected("INVALID_COMPLETION")
        operations = [
            {"Update": {
                "TableName": self.table_name, "Key": {"PK": attempt.pk, "SK": attempt.sk},
                "UpdateExpression": "SET #state = :done, completionDigest = :digest,"
                                    " receipt = :receipt, chargedUsd = :charged, finishedAt = :now",
                "ConditionExpression": "#state = :running AND attemptToken = :token"
                                       " AND reservedUsd = :reserved AND sourceRevision = :revision"
                                       " AND #month = :month AND runId = :run AND docHash = :hash"
                                       " AND leaseUntil = :lease",
                "ExpressionAttributeNames": {"#state": "state", "#month": "month"},
                "ExpressionAttributeValues": {
                    ":done": status, ":running": "RUNNING", ":token": attempt.token,
                    ":reserved": attempt.reserved, ":revision": attempt.revision,
                    ":month": attempt.month, ":run": attempt.run_id, ":hash": attempt.doc_hash,
                    ":lease": attempt.lease_until,
                    ":digest": digest, ":receipt": receipt, ":charged": charged, ":now": now}}},
            {"Delete": {
                "TableName": self.table_name,
                "Key": {"PK": attempt.pk, "SK": "NEWSCLAIM#" + attempt.doc_hash},
                "ConditionExpression": "attemptToken = :token AND leaseUntil > :now",
                "ExpressionAttributeValues": {":token": attempt.token, ":now": now}}},
            self._settlement(attempt, "NEWSBUDGET#" + attempt.month, charged, not usage_complete),
            self._settlement(attempt, "NEWSRUN#" + attempt.run_id, charged, not usage_complete),
        ]
        if row is not None:
            config = self.current_config(attempt.source_id, attempt.revision)
            operations.extend([
                self._config_guard(attempt, config),
                {"Put": {
                    "TableName": self.table_name, "Item": row,
                    "ConditionExpression": "attribute_not_exists(#schema) OR #schema <> :one"
                                           " OR attribute_not_exists(#quality) OR #quality <> :published",
                    "ExpressionAttributeNames": {"#schema": "schemaVersion", "#quality": "qualityStatus"},
                    "ExpressionAttributeValues": {":one": 1, ":published": "PUBLISHED"}}},
            ])
        self._transaction(operations)
        return receipt
