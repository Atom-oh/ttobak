"""Idempotent run start, atomic history finalization and abandoned-run recovery.

Runs and attempts are private accounting state. Finalization never re-adds token
cost and never changes CONFIG status, owner, subscribers or enabled state.
"""

from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal
import re

from news_store import (
    ACCOUNTING_RETENTION_SECONDS, MAX_RUN_ATTEMPTS, StateRejected, _ID, _REVISION, _match, _money,
)

ACCEPT_SECONDS = 1800
RETENTION_SECONDS = ACCOUNTING_RETENTION_SECONDS


def _integer(value, maximum=10000000):
    if (isinstance(value, bool) or not isinstance(value, (int, Decimal))
            or value != int(value) or not 0 <= value <= maximum):
        raise StateRejected("INVALID_RUN_STATE")
    return int(value)


def _aggregate_charge(value):
    # Each settled attempt can report an overrun up to the per-attempt cap.
    # A valid run total can therefore exceed the reservation-input money cap.
    if isinstance(value, bool) or not isinstance(value, (int, Decimal)):
        raise StateRejected("INVALID_RUN_STATE")
    amount = Decimal(value)
    if (not amount.is_finite() or not 0 <= amount <= MAX_RUN_ATTEMPTS * 100000
            or amount.as_tuple().exponent < -12):
        raise StateRejected("INVALID_RUN_STATE")
    return amount


@dataclass(frozen=True)
class Run:
    source_id: str
    run_id: str
    revision: str

    def __post_init__(self):
        _match(self.source_id, _ID)
        _match(self.run_id, _ID)
        _match(self.revision, _REVISION)
        if self.source_id.startswith("__"):
            raise StateRejected("INVALID_IDENTITY")

    @property
    def pk(self):
        return "CRAWLER#" + self.source_id

    @property
    def sk(self):
        return "NEWSRUN#" + self.run_id


class NewsRuns:
    def __init__(self, store):
        self.store = store

    def begin(self, run, now):
        now = _integer(now, 253402299599)
        existing = self.store._get(run.pk, run.sk)
        if existing:
            if existing.get("sourceRevision") != run.revision:
                raise StateRejected("SOURCE_CHANGED")
            if existing.get("runState") not in ("OPEN", "FINALIZED"):
                raise StateRejected("INVALID_RUN_STATE")
            return existing
        config = self.store.current_config(run.source_id, run.revision)
        row = {"PK": run.pk, "SK": run.sk, "runId": run.run_id, "sourceId": run.source_id,
               "sourceRevision": run.revision, "runState": "OPEN", "startedAt": now,
               "acceptUntil": now + ACCEPT_SECONDS, "attemptCount": 0, "finishedCount": 0,
               "chargedUsd": Decimal(0), "unknownAttempts": 0,
               "pendingShareExpiresAt": now + RETENTION_SECONDS}
        try:
            self.store._transaction([
                self.store._config_guard(run, config),
                {"Put": {"TableName": self.store.table_name, "Item": row,
                         "ConditionExpression": "attribute_not_exists(PK)"}}])
        except StateRejected as error:
            if error.reason != "STATE_CONFLICT":
                raise
            # A concurrent identical start may win; never reset its budget or clock.
            existing = self.store._get(run.pk, run.sk)
            if (not existing or existing.get("sourceRevision") != run.revision
                    or existing.get("runState") not in ("OPEN", "FINALIZED")):
                raise
            return existing
        return row

    def _attempts(self, run):
        result, cursor = [], None
        for _ in range(MAX_RUN_ATTEMPTS + 1):
            arguments = {
                "TableName": self.store.table_name,
                "KeyConditionExpression": "PK = :pk AND begins_with(SK, :prefix)",
                "ExpressionAttributeValues": {":pk": run.pk, ":prefix": run.sk + "#ATTEMPT#"},
                "ConsistentRead": True, "Limit": MAX_RUN_ATTEMPTS + 1 - len(result)}
            if cursor:
                arguments["ExclusiveStartKey"] = cursor
            response = self.store.client.query(**arguments)
            values = response.get("Items")
            if not isinstance(values, list):
                raise StateRejected("STATE_UNAVAILABLE")
            result.extend(values)
            if len(result) > MAX_RUN_ATTEMPTS:
                raise StateRejected("INVALID_RUN_STATE")
            following = response.get("LastEvaluatedKey")
            if not following:
                return result
            if following == cursor:
                raise StateRejected("STATE_UNAVAILABLE")
            cursor = following
        raise StateRejected("STATE_UNAVAILABLE")

    def finish(self, run, now, *, discovery=None, outcomes=(), recovery=False):
        now = _integer(now, 253402299599)
        row = self.store._get(run.pk, run.sk)
        if not row or row.get("sourceRevision") != run.revision:
            raise StateRejected("INVALID_RUN_STATE")
        if row.get("runState") == "FINALIZED":
            return row["receipt"]
        if row.get("runState") != "OPEN":
            raise StateRejected("INVALID_RUN_STATE")
        started = _integer(row.get("startedAt"), now)
        count = _integer(row.get("attemptCount"), MAX_RUN_ATTEMPTS)
        finished = _integer(row.get("finishedCount"), count)
        charged = _aggregate_charge(row.get("chargedUsd"))
        unknown = _integer(row.get("unknownAttempts"), finished)
        attempts = self._attempts(run)
        if len(attempts) != count:
            raise StateRejected("STATE_CONFLICT")
        expired, completed, published, usage = [], [], 0, {"inputTokens": 0, "outputTokens": 0}
        warnings, errors = [], []
        for attempt in attempts:
            if (attempt.get("PK") != run.pk
                    or not isinstance(attempt.get("SK"), str)
                    or not attempt["SK"].startswith(run.sk + "#ATTEMPT#")
                    or attempt.get("sourceRevision") != run.revision):
                raise StateRejected("INVALID_RUN_STATE")
            state = attempt.get("state")
            if state == "RUNNING":
                if _integer(attempt.get("leaseUntil"), 253402300799) > now:
                    raise StateRejected("RUN_BUSY")
                expired.append(attempt)
                continue
            if state not in ("PUBLISHED", "REJECTED", "FAILED"):
                raise StateRejected("INVALID_RUN_STATE")
            receipt = attempt.get("receipt", {})
            if receipt.get("status") != state:
                raise StateRejected("INVALID_RUN_STATE")
            completed.append(attempt)
            published += int(state == "PUBLISHED")
            for key in usage:
                usage[key] += _integer(receipt.get(key))
            reason = receipt.get("reason", "")
            if reason:
                if not isinstance(reason, str) or not re.fullmatch(r"[A-Z0-9_]{1,64}", reason):
                    raise StateRejected("INVALID_RUN_STATE")
                errors.append(reason)
            for warning in receipt.get("fetchWarnings", []):
                if warning not in ("CORROBORATION_DEFERRED", "CORROBORATION_UNUSABLE",
                                   "CORROBORATION_FETCH_FAILED"):
                    raise StateRejected("INVALID_RUN_STATE")
                warnings.append(warning)
        if len(completed) != finished:
            raise StateRejected("STATE_CONFLICT")
        if sum((_money(item.get("chargedUsd")) for item in attempts), Decimal(0)) != charged:
            raise StateRejected("INVALID_RUN_STATE")
        if expired:
            errors.append("EXPIRED_ATTEMPTS")
        if type(recovery) is not bool or not isinstance(outcomes, (list, tuple)) or len(outcomes) > 20:
            raise StateRejected("INVALID_WORKER_RESULTS")
        if recovery:
            errors.append("RECOVERED_WITHOUT_OUTCOMES")
        outcome_counts = {}
        for outcome in outcomes:
            if (not isinstance(outcome, dict) or outcome.get("sourceId") != run.source_id
                    or outcome.get("runId") != run.run_id
                    or outcome.get("status") not in (
                        "PUBLISHED", "REJECTED", "FAILED", "FETCH_REJECTED", "DUPLICATE", "DEFERRED")):
                raise StateRejected("INVALID_WORKER_RESULTS")
            status = outcome["status"]
            outcome_counts[status] = outcome_counts.get(status, 0) + 1
            if status in ("FETCH_REJECTED", "DEFERRED", "FAILED"):
                reason = outcome.get("reason")
                if not isinstance(reason, str) or not re.fullmatch(r"[A-Z0-9_]{1,64}", reason):
                    raise StateRejected("INVALID_WORKER_RESULTS")
                errors.append(reason)
        diagnostics = {}
        if discovery is not None:
            if (not isinstance(discovery, dict) or discovery.get("sourceId") != run.source_id
                    or discovery.get("runId") != run.run_id
                    or discovery.get("sourceRevision") != run.revision):
                raise StateRejected("INVALID_DISCOVERY_RESULT")
            for key in ("queryCount", "invalidRejected", "staleRejected",
                        "duplicateRejected", "budgetRejected"):
                diagnostics[key] = _integer(discovery.get(key, 0), 10000)
            supplied = discovery.get("errors", [])
            if not isinstance(supplied, list) or len(supplied) > 24:
                raise StateRejected("INVALID_DISCOVERY_RESULT")
            for error in supplied:
                code = error.get("code") if isinstance(error, dict) else None
                if code not in ("QUERY_TOO_LONG", "SEARCH_FAILED"):
                    raise StateRejected("INVALID_DISCOVERY_RESULT")
                errors.append(code)
        timestamp = datetime.fromtimestamp(started, timezone.utc).isoformat()
        receipt = {
            "sourceId": run.source_id, "runId": run.run_id, "sourceRevision": run.revision,
            "status": "PARTIAL" if errors or warnings else "COMPLETED",
            "docsAdded": published, "docsUpdated": 0, "attemptCount": count,
            "expiredAttempts": len(expired), "unknownAttempts": unknown + len(expired),
            "chargedUsd": charged, "usageComplete": not (unknown or expired), **usage,
            "errors": sorted(set(errors))[:20], "fetchWarnings": sorted(set(warnings)),
            "duration": (now - started) * 1000, "timestamp": timestamp,
            "workerOutcomes": outcome_counts, **diagnostics}
        operations = []
        # Each expired row is frozen in the same transaction as the run. Its full
        # pre-reserved charge is retained; later settlement cannot resurrect it.
        for attempt in expired:
            operations.append({"Update": {
                "TableName": self.store.table_name,
                "Key": {"PK": run.pk, "SK": attempt["SK"]},
                "UpdateExpression": "SET #state = :expired, finishedAt = :now",
                "ConditionExpression": "#state = :running AND leaseUntil <= :now",
                "ExpressionAttributeNames": {"#state": "state"},
                "ExpressionAttributeValues": {":expired": "EXPIRED", ":running": "RUNNING",
                                              ":now": now}}})
        expired_months = {}
        for attempt in expired:
            month = attempt.get("month")
            if not isinstance(month, str) or not re.fullmatch(r"\d{4}-(?:0[1-9]|1[0-2])", month):
                raise StateRejected("INVALID_RUN_STATE")
            group = expired_months.setdefault(month, {"count": 0, "reserved": Decimal(0)})
            group["count"] += 1
            group["reserved"] += _money(attempt.get("reservedUsd"))
        if len(expired_months) > 2:
            raise StateRejected("INVALID_RUN_STATE")
        for month, values in expired_months.items():
            operations.append({"Update": {
                "TableName": self.store.table_name,
                "Key": {"PK": run.pk, "SK": "NEWSBUDGET#" + month},
                "UpdateExpression": "ADD unknownAttempts :count",
                "ConditionExpression": "chargedUsd >= :reserved",
                "ExpressionAttributeValues": {":count": values["count"],
                                              ":reserved": values["reserved"]}}})
        operations.extend([
            {"Update": {
                "TableName": self.store.table_name, "Key": {"PK": run.pk, "SK": run.sk},
                "UpdateExpression": "SET #state = :done, finalizedAt = :now, receipt = :receipt",
                "ConditionExpression": "#state = :open AND sourceRevision = :revision"
                                       " AND attemptCount = :count AND finishedCount = :finished"
                                       " AND chargedUsd = :charged AND unknownAttempts = :unknown",
                "ExpressionAttributeNames": {"#state": "runState"},
                "ExpressionAttributeValues": {
                    ":done": "FINALIZED", ":now": now, ":receipt": receipt, ":open": "OPEN",
                    ":revision": run.revision, ":count": count, ":finished": finished,
                    ":charged": charged, ":unknown": unknown}}},
            {"Put": {"TableName": self.store.table_name,
                     "Item": {"PK": run.pk, "SK": f"HISTORY#{timestamp}#NEWS#{run.run_id}",
                              "schemaVersion": 1, "type": "news", **receipt,
                              "pendingShareExpiresAt": now + RETENTION_SECONDS},
                     "ConditionExpression": "attribute_not_exists(PK)"}}])
        self.store._transaction(operations)
        return receipt
