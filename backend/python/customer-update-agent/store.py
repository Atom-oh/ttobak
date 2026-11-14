"""DynamoDB rows of the runtime: the shared URL cache.

UrlCache: PK CUURL#{urlHash}, SK META, also written by the Go API. Attribute
names follow backend/internal/model.UpdateURL. Writes are attribute-level
UpdateItem so the Go-written url and entityType survive. The row holds public
page summaries only, so it is not sealed.
"""

import time
from datetime import datetime, timezone
from decimal import Decimal

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
