"""Read-client ceiling at the API Gateway JWT-authorized HTTP boundary.

The claims below must be the trusted authorizer context, never decoded caller
headers. SigV4 worker and WebSocket invocations keep their existing boundary;
the WS authorizer separately refuses this dedicated read client.
"""
import re
import json
import time
import base64
import binascii
from urllib.parse import urlsplit

from connector_contract import ConnectorError, Grants, valid_capabilities

MAX_REQUEST_BYTES = 32_000
MAX_RESPONSE_BYTES = 14_000


class ReadOnlyQAIngress:
    def __init__(self, client_id="", audience="", web_clients=()):
        self.enabled = bool(client_id or audience)
        self.client_id, self.audience = client_id, audience
        self.web_clients = frozenset(web_clients)
        parsed = urlsplit(audience)
        if self.enabled and (
            not re.fullmatch(r"[A-Za-z0-9]{1,128}", client_id)
            or parsed.scheme != "https" or not parsed.hostname or parsed.username
            or parsed.password or parsed.path != "/api/mcp"
            or parsed.query or parsed.fragment or parsed.geturl() != audience
            or not self.web_clients or any(
                not re.fullmatch(r"[A-Za-z0-9]{1,128}", client) or client == client_id
                for client in self.web_clients
            )
        ):
            raise ValueError("INVALID_CONNECTOR_CONFIG")

    def authorize_http(self, event, now=None):
        if not self.enabled:
            return None
        claims = event.get("requestContext", {}).get("authorizer", {}).get("jwt", {}).get("claims")
        if not isinstance(claims, dict):
            raise ConnectorError("INVALID_CONNECTOR_IDENTITY")
        user_id = claims.get("sub")
        if not isinstance(user_id, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", user_id):
            raise ConnectorError("INVALID_CONNECTOR_IDENTITY")
        # Ordinary web identities still use the same verified authorizer. A
        # dedicated client's ID token is recognized by aud, not client_id.
        dedicated = claims.get("client_id") == self.client_id or claims.get("aud") == self.client_id
        if not dedicated:
            use, client, audience = claims.get("token_use"), claims.get("client_id"), claims.get("aud")
            if not isinstance(client, (str, type(None))) or not isinstance(audience, (str, type(None))):
                raise ConnectorError("INVALID_CONNECTOR_IDENTITY")
            scopes = claims.get("scope", "")
            if not isinstance(scopes, str) or any(
                scope.startswith((self.audience + "/", "ttobak.connector/")) for scope in scopes.split()
            ):
                raise ConnectorError("CAPABILITY_DENIED")
            if (use == "id" and audience in self.web_clients or
                use == "access" and client in self.web_clients and audience in (None, "", self.audience)):
                return user_id
            raise ConnectorError("CAPABILITY_DENIED")
        if claims.get("token_use") != "access" or claims.get("aud") != self.audience:
            raise ConnectorError("CAPABILITY_DENIED")
        scopes = claims.get("scope", "")
        if not isinstance(scopes, str):
            raise ConnectorError("CAPABILITY_DENIED")
        scopes = scopes.split()
        read_scope = self.audience + "/read"
        if read_scope not in scopes or any(
            scope not in ("openid", "email", "profile", read_scope) for scope in scopes
        ):
            raise ConnectorError("CAPABILITY_DENIED")
        expiry = claims.get("exp")
        if type(expiry) is int:
            pass
        elif isinstance(expiry, str) and re.fullmatch(r"[0-9]{1,16}", expiry):
            expiry = int(expiry)
        else:
            raise ConnectorError("INVALID_CONNECTOR_IDENTITY")
        if expiry <= (time.time() if now is None else now):
            raise ConnectorError("INVALID_CONNECTOR_IDENTITY")
        method = event.get("requestContext", {}).get("http", {}).get("method")
        # Ask, jobs and stream can persist sessions/start research. Their
        # read-only executors must be designed before this list can expand.
        if method != "POST" or event.get("rawPath") != "/api/qa/detect-questions":
            raise ConnectorError("CAPABILITY_DENIED")
        raw = event.get("body", "{}")
        try:
            if not isinstance(raw, str):
                raise ValueError()
            if event.get("isBase64Encoded"):
                if len(raw) > 4 * ((MAX_REQUEST_BYTES + 2) // 3):
                    raise ValueError()
                raw_bytes = base64.b64decode(raw, validate=True)
                raw_bytes.decode("utf-8", errors="strict")
            else:
                raw_bytes = raw.encode("utf-8", errors="strict")
            if len(raw_bytes) > MAX_REQUEST_BYTES:
                raise ValueError()
        except (ValueError, UnicodeError, binascii.Error):
            raise ConnectorError("INVALID_CONNECTOR_REQUEST") from None
        return user_id

    def authorize_current(self, event, resolve):
        user_id = self.authorize_http(event)
        if not self.is_connector(event):
            return user_id
        claims = event["requestContext"]["authorizer"]["jwt"]["claims"]
        if resolve is None:
            raise ConnectorError("INVALID_GRANT")
        try:
            grants = resolve(user_id, self.client_id)
        except Exception:
            raise ConnectorError("INVALID_GRANT") from None
        if (not isinstance(grants, Grants) or grants.client_enabled is not True
                or grants.user_id != user_id or grants.client_id != self.client_id
                or not valid_capabilities(grants.client_maximum)):
            raise ConnectorError("INVALID_GRANT")
        # A slow current-grant lookup must not carry an expired upstream token.
        self.authorize_http(event)
        return user_id

    def detection(self, event, executor, resolve):
        self.authorize_current(event, resolve)
        def unique_object(pairs):
            result = {}
            for key, value in pairs:
                if key in result:
                    raise ValueError("duplicate field")
                result[key] = value
            return result
        try:
            raw = event.get("body", "{}")
            if event.get("isBase64Encoded"):
                raw = base64.b64decode(raw, validate=True).decode("utf-8", errors="strict")
            body = json.loads(raw, object_pairs_hook=unique_object)
            validate_detection_body(body)
        except (ValueError, UnicodeError, binascii.Error, RecursionError):
            raise ConnectorError("INVALID_CONNECTOR_REQUEST") from None
        result = executor(body)
        self.authorize_current(event, resolve)
        if not isinstance(result, dict) or not isinstance(result.get("body"), str):
            raise ConnectorError("INVALID_CONNECTOR_RESULT")
        try:
            result_bytes = result["body"].encode("utf-8", errors="strict")
        except UnicodeError:
            raise ConnectorError("INVALID_CONNECTOR_RESULT") from None
        if len(result_bytes) > MAX_RESPONSE_BYTES:
            raise ConnectorError("RESULT_TOO_LARGE")
        return result

    def is_connector(self, event):
        claims = event.get("requestContext", {}).get("authorizer", {}).get("jwt", {}).get("claims", {})
        return self.enabled and isinstance(claims, dict) and (
            claims.get("client_id") == self.client_id or claims.get("aud") == self.client_id
        )


def validate_detection_body(body):
    if not isinstance(body, dict) or set(body) - {"transcript", "summary", "previousQuestions"}:
        raise ConnectorError("INVALID_CONNECTOR_REQUEST")
    transcript, summary = body.get("transcript"), body.get("summary", "")
    previous = body.get("previousQuestions", [])
    if (not isinstance(transcript, str) or not transcript.strip() or not isinstance(summary, str)
            or not isinstance(previous, list) or len(previous) > 20
            or any(not isinstance(question, str) for question in previous)):
        raise ConnectorError("INVALID_CONNECTOR_REQUEST")
    try:
        for value in [transcript, summary, *previous]:
            value.encode("utf-8", errors="strict")
    except UnicodeError:
        raise ConnectorError("INVALID_CONNECTOR_REQUEST") from None
