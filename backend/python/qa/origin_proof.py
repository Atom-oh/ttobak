"""Bounded HTTP origin proof verification; no request or provider text logging."""
import hmac
import json
import logging
import re
import threading
import time

GENERATION = re.compile(r"^[0-9a-f]{32}$")
MAX_OVERLAP_SECONDS = 24 * 60 * 60


def valid_proof(value):
    return isinstance(value, str) and 16 <= len(value) <= 128 and all(
        33 <= ord(character) <= 126 and character != "," for character in value)


def parse_proofs(raw):
    if not isinstance(raw, str):
        return None
    try:
        if len(raw.encode("utf-8")) > 2048:
            return None
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        return None
    if not isinstance(value, dict) or type(value.get("schemaVersion")) is not int or value["schemaVersion"] != 1:
        return None
    if not valid_proof(value.get("current")) or not isinstance(value.get("generation"), str) or not GENERATION.fullmatch(value["generation"]):
        return None
    start, expires = value.get("rotationStartedAt"), value.get("previousExpiresAt")
    if type(start) is not int or type(expires) is not int or not 0 <= start <= 9007199254740991 or expires > 9007199254740991:
        return None
    if value.get("phase") in ("seeded", "active"):
        return value if value.get("previous") == "" and expires == 0 else None
    if value.get("phase") != "rotating" or not valid_proof(value.get("previous")):
        return None
    if value["previous"] == value["current"] or start <= 0 or not 0 < expires - start <= MAX_OVERLAP_SECONDS:
        return None
    return value


def origin_header(event, strict=True):
    headers = event.get("headers") or {}
    if not isinstance(headers, dict):
        return None
    found = [value for name, value in headers.items()
             if isinstance(name, str) and name.lower() == "x-origin-verify"]
    # Payload 2.0 joins duplicates with commas. Case duplicates in synthetic or
    # alternate adapters are rejected too; never normalize a malformed value.
    if len(found) != 1 or not isinstance(found[0], str) or (strict and not valid_proof(found[0])):
        return None
    multi = event.get("multiValueHeaders") or {}
    if not isinstance(multi, dict):
        return None
    values = [value for name, value in multi.items()
              if isinstance(name, str) and name.lower() == "x-origin-verify"]
    if len(values) > 1 or (values and values[0] != [found[0]]):
        return None
    return found[0]


class OriginVerifier:
    def __init__(self, arn, client, *, local_development=False, now=time.time, version_id=""):
        self.arn, self.client, self.local = arn, client, local_development
        self.now = now
        self.version_id = version_id
        self.cached, self.expires = None, 0
        self.lock = threading.Lock()

    def secret(self):
        if self.client is None or not re.fullmatch(r"[A-Za-z0-9-]{32,64}", self.version_id):
            logging.getLogger(__name__).warning("http_origin_rejected category=configuration_invalid")
            return None
        if self.cached is not None and self.now() < self.expires:
            return self.cached
        # The SDK client has one-second connect/read limits and no retries;
        # waiting for an in-flight lookup is bounded independently.
        if not self.lock.acquire(timeout=1):
            logging.getLogger(__name__).warning("http_origin_rejected category=secret_unavailable")
            return None
        try:
            if self.cached is not None and self.now() < self.expires:
                return self.cached
            try:
                arguments = {"SecretId": self.arn, "VersionId": self.version_id}
                result = self.client.get_secret_value(**arguments)
            except Exception:
                logging.getLogger(__name__).warning("http_origin_rejected category=secret_unavailable")
                return None
            value = parse_proofs(result.get("SecretString") if isinstance(result, dict) else None)
            if value is None:
                logging.getLogger(__name__).warning("http_origin_rejected category=secret_invalid")
                return None
            self.cached, self.expires = value, self.now() + 60
            return value
        finally:
            self.lock.release()

    def verify(self, event):
        if not self.arn and self.local:
            return True
        actual = origin_header(event, strict=bool(self.arn))
        if actual is None:
            return False
        if not self.arn:
            logging.getLogger(__name__).warning("http_origin_rejected category=configuration_invalid")
            return False
        value = self.secret()
        if value is None:
            return False
        current = hmac.compare_digest(actual, value["current"])
        previous = hmac.compare_digest(actual, value["previous"])
        now = self.now()
        return current or (previous and value["phase"] == "rotating" and
                           value["rotationStartedAt"] <= now < value["previousExpiresAt"])
