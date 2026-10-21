"""Service-IAM QA consumer with exact signed arguments and live grant checks."""
import json
import re
import threading

from connector_contract import ConnectorError
from connector_delegation import DelegationVerifier, _decode

PROTOCOL = "ttobak.connector.invoke.v1"
MAX_RESULT_BYTES = 14_000


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError()
        result[key] = value
    return result


def _request(event):
    if (not isinstance(event, dict)
            or set(event) != {"protocol", "operation", "requestId", "delegation", "arguments"}
            or event.get("protocol") != PROTOCOL or event.get("operation") != "qa.readonlyAsk"):
        raise ConnectorError("CAPABILITY_DENIED")
    try:
        args = _decode(event["arguments"], 32_000)
        wire = _decode(event["delegation"], 8192)
        body = json.loads(args.decode("utf-8", errors="strict"), object_pairs_hook=_unique_object)
        if (not isinstance(body, dict) or set(body) != {"method", "path", "body"}
                or body["method"] != "POST" or body["path"] != "/api/qa/readonly-ask"):
            raise ValueError()
        request = body["body"]
        if (not isinstance(request, dict) or set(request) - {"question", "meetingId"}
                or not isinstance(request.get("question"), str) or not request["question"].strip()
                or len(request["question"].encode("utf-8", errors="strict")) > 4096
                or request.get("meetingId") is not None and (
                    not isinstance(request["meetingId"], str)
                    or re.fullmatch(r"[A-Za-z0-9_-]{1,128}", request["meetingId"]) is None)):
            raise ValueError()
    except (ValueError, TypeError, UnicodeError, RecursionError, ConnectorError):
        raise ConnectorError("REQUEST_MISMATCH") from None
    return args, wire, request


class PrivateQAConsumer:
    """Only a successfully validated public key is cached; grants are never cached."""

    def __init__(self, key_id, kms_client, resolve, clock=None):
        self._key_id, self._kms, self._resolve, self._clock = key_id, kms_client, resolve, clock
        self._lock, self._verifier = threading.Lock(), None

    def _key_verifier(self):
        if not self._key_id or not self._lock.acquire(timeout=3):
            raise ConnectorError("POLICY_UNAVAILABLE")
        try:
            if self._verifier is None:
                key = self._kms.get_public_key(KeyId=self._key_id)
                if key.get("KeyId") != self._key_id or key.get("KeyUsage") != "SIGN_VERIFY":
                    raise ValueError()
                self._verifier = DelegationVerifier(
                    self._key_id, key["PublicKey"], "ttobak-qa-bridge", self._resolve, self._clock)
            return self._verifier
        except Exception:
            raise ConnectorError("POLICY_UNAVAILABLE") from None
        finally:
            self._lock.release()

    def invoke(self, event, execute):
        args, wire, request = _request(event)
        verifier = self._key_verifier()
        principal = verifier.verify(wire, event["operation"], event["requestId"], args)
        result = execute(request, principal.user_id)
        verifier.verify(wire, event["operation"], event["requestId"], args)
        try:
            if not isinstance(result, dict):
                raise ValueError()
            encoded = json.dumps(result, ensure_ascii=False, allow_nan=False).encode("utf-8", errors="strict")
        except (ValueError, TypeError, UnicodeError, RecursionError):
            raise ConnectorError("INVALID_CONNECTOR_RESULT") from None
        if len(encoded) > MAX_RESULT_BYTES:
            raise ConnectorError("RESULT_TOO_LARGE")
        return result
