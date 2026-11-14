"""DynamoDB rows of the runtime: the shared URL cache and one run's result rows.

UrlCache: PK CUURL#{urlHash}, SK META, also written by the Go API. Attribute
names follow backend/internal/model.UpdateURL. Writes are attribute-level
UpdateItem so the Go-written url and entityType survive. The row holds public
page summaries only, so it is not sealed.

RunResults: rows under PK CURUN#{runId} (contracts/customer-updates/run-v1.json
resultRows) that the Go worker validates and materializes. Customer and draft
text is sealed; plaintext attributes are keys, codes and counts only.
"""

import json
import time
from datetime import datetime, timezone
from decimal import Decimal

import sealing

READY_TTL_SECONDS = 30 * 86400
RETRY_TTL_SECONDS = 86400
_READABLE = frozenset({"ready", "insufficient", "failed"})
_RETRY_STATUSES = frozenset({"insufficient", "failed"})


def _key(url_hash):
    return {"PK": "CUURL#" + url_hash, "SK": "META"}


def _rfc3339(epoch):
    return datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _is_condition_failed(error):
    response = getattr(error, "response", None)
    return isinstance(response, dict) and \
        response.get("Error", {}).get("Code") == "ConditionalCheckFailedException"


class UrlCache:
    def __init__(self, table, clock=time.time):
        self._table = table
        self._clock = clock

    def _now(self):
        return int(self._clock())

    def get(self, url_hash):
        """Strongly consistent read; pending, expired or unknown rows read as absent."""
        item = self._table.get_item(Key=_key(url_hash), ConsistentRead=True).get("Item")
        if not item or item.get("status") not in _READABLE:
            return None
        expires = item.get("pendingShareExpiresAt")
        if isinstance(expires, bool) or not isinstance(expires, (int, Decimal)) \
                or expires <= self._now():
            return None
        return dict(item)

    def _update(self, url_hash, url, fields, condition=None, condition_values=None):
        names = {"#entityType": "entityType"}
        values = {":entityType": "UPDATE_URL"}
        sets = ["#entityType = if_not_exists(#entityType, :entityType)"]
        if url:
            names["#url"], values[":url"] = "url", url
            sets.append("#url = if_not_exists(#url, :url)")
        for attr, placeholder, value in fields:
            names["#" + attr] = attr
            values[placeholder] = value
            sets.append(f"#{attr} = {placeholder}")
        request = {"Key": _key(url_hash), "UpdateExpression": "SET " + ", ".join(sets),
                   "ExpressionAttributeNames": names, "ExpressionAttributeValues": values}
        if condition:
            request["ConditionExpression"] = condition
            values.update(condition_values)
        self._table.update_item(**request)

    def put_ready(self, url_hash, content):
        now = self._now()
        self._update(url_hash, content.url, [
            ("urlHash", ":urlHash", url_hash),
            ("status", ":status", "ready"),
            ("title", ":title", content.title),
            ("summary", ":summary", content.summary),
            ("services", ":services", list(content.services)),
            ("topics", ":topics", list(content.topics)),
            ("fetchedAt", ":fetchedAt", _rfc3339(now)),
            ("pendingShareExpiresAt", ":exp", now + READY_TTL_SECONDS),
        ])

    def put_status(self, url_hash, status, *, url=None):
        """Record a retryable bad read; never downgrade a live ready row."""
        if status not in _RETRY_STATUSES:
            raise ValueError("status")
        now = self._now()
        try:
            self._update(url_hash, url, [
                ("urlHash", ":urlHash", url_hash),
                ("status", ":status", status),
                ("fetchedAt", ":fetchedAt", _rfc3339(now)),
                ("pendingShareExpiresAt", ":exp", now + RETRY_TTL_SECONDS),
            ], condition="#status <> :ready OR #pendingShareExpiresAt <= :now"
                         " OR attribute_not_exists(#status)",
                condition_values={":ready": "ready", ":now": now})
        except Exception as error:
            if not _is_condition_failed(error):
                raise


RUN_TTL_SECONDS = 2 * 86400
SEAL_PURPOSE = "ttobak-customer-update-run"
ITEM_STATES = frozenset({"decided", "deferred", "skipped"})
ITEM_REASONS = frozenset({"", "insufficient", "failed", "budget", "gate_error"})
OUTCOMES = frozenset({"", "drafted", "discarded", "deferred"})


class RunExists(Exception):
    """The runId was already claimed: a runId is single-use."""


def _compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


class RunResults:
    """Writers for one run. A missing KMS key id fails here, before any row is
    written. claim() first (conditional), then plain PutItem rows, DONE last."""

    def __init__(self, table, run_id, kms_client, key_id, clock=time.time):
        if not key_id:
            raise sealing.SealingError("unavailable")
        self._table, self._run_id = table, run_id
        self._kms, self._key_id = kms_client, key_id
        self._clock = clock
        self._claimed = False

    def claim(self):
        """Create CURUN#{runId}/CLAIM only if that row does not exist yet (every
        run claims before any other row); raises RunExists otherwise.
        Idempotent on the same writer."""
        if self._claimed:
            return
        now = int(self._clock())
        try:
            self._table.put_item(Item={
                "PK": "CURUN#" + self._run_id, "SK": "CLAIM", "entityType": "UPDATE_RUN_CLAIM",
                "claimedAt": _rfc3339(now), "pendingShareExpiresAt": now + RUN_TTL_SECONDS,
            }, ConditionExpression="attribute_not_exists(PK)")
        except Exception as error:
            if _is_condition_failed(error):
                raise RunExists() from None
            raise
        self._claimed = True

    def _put(self, sk, entity_type, fields, sealed_payload=None):
        item = {"PK": "CURUN#" + self._run_id, "SK": sk, "entityType": entity_type,
                "pendingShareExpiresAt": int(self._clock()) + RUN_TTL_SECONDS}
        item.update(fields)
        if sealed_payload is not None:
            binding = {"purpose": SEAL_PURPOSE, "runId": self._run_id, "sk": sk}
            item["sealed"] = sealing.seal(self._kms, self._key_id, _compact(sealed_payload),
                                          binding)
        self._table.put_item(Item=item)

    def put_item_state(self, url_hash, state, reason):
        if state not in ITEM_STATES or reason not in ITEM_REASONS:
            raise ValueError("item_state")
        self._put("ITEM#" + url_hash, "UPDATE_RUN_ITEM", {"state": state, "reason": reason})

    def put_decision(self, url_hash, account_id, decision, backend, outcome, discard_reason):
        """`decision` is a gate.PairDecision; reason and gate input are sealed."""
        if outcome not in OUTCOMES:
            raise ValueError("outcome")
        payload = {"reason": decision.reason}
        if decision.choice == "needed":
            payload["gateInput"] = decision.gate_input
        self._put(f"DEC#{url_hash}#{account_id}", "UPDATE_RUN_DECISION", {
            "choice": decision.choice,
            "score": Decimal(str(round(float(decision.score), 4))),
            "backend": backend,
            "outcome": outcome,
            "discardReason": discard_reason,
        }, payload)

    def put_draft(self, account_id, url_hash, draft):
        """`draft` is a writer.Draft. Text is sealed raw (unescaped), so a
        re-check of the row with checks.check_draft matches the run's check; a
        consumer that posts through the Slack API escapes with checks.escape_slack."""
        self._put(f"DRAFT#{account_id}#{url_hash}", "UPDATE_RUN_DRAFT", {}, {
            "sourceUrl": draft.source_url,
            "sourceTitle": draft.source_title,
            "why": draft.why,
            "body": draft.body,
            "links": list(draft.links),
            "opportunityIds": list(draft.opportunity_ids),
            "fetchedUrls": list(draft.fetched_urls),
        })

    def put_done(self, stats):
        self._put("DONE", "UPDATE_RUN_DONE",
                  {"finishedAt": _rfc3339(int(self._clock())), "stats": dict(stats)})
