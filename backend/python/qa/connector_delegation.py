"""Pinned, local verification of the KMS RSA-PSS delegation wire contract."""
import base64
import hashlib
import hmac
import json
import re
import time
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa
from connector_contract import (
    ConnectorError, MAX_ARGUMENT_BYTES, MAX_LIFETIME_SECONDS, MAX_PAYLOAD_BYTES,
    MAX_SAFE_INTEGER, MAX_WIRE_BYTES, PROTOCOL, Principal, SIGNING_ALGORITHM,
    effective_capabilities, required_capabilities, valid_capabilities,
)

IDENTIFIER = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
KEY_ARN = re.compile(r"^arn:(aws|aws-us-gov|aws-cn):kms:[a-z0-9-]{1,32}:[0-9]{12}:key/[0-9a-f-]{36}$")
FIELDS = ("version", "protocol", "destination", "sub", "clientId", "capabilities",
          "approvalGeneration", "operation", "argumentsSHA256", "requestId",
          "issuedAt", "expiresAt", "upstreamExpiresAt")


def _utf8(raw):
    text = None
    try:
        text = raw.decode("utf-8", errors="strict")
    except (UnicodeDecodeError, AttributeError):
        pass
    if text is None:
        raise ConnectorError("INVALID_DELEGATION")
    return text


def _canonical_object(raw, fields):
    text = _utf8(raw)
    canonical = None
    try:
        value = json.loads(text)
        if not isinstance(value, dict):
            raise ValueError()
        canonical = {key: value[key] for key in fields}
        if json.dumps(canonical, ensure_ascii=False, separators=(",", ":")) != text:
            raise ValueError()
    except (ValueError, TypeError, KeyError, RecursionError):
        canonical = None
    if canonical is None:
        raise ConnectorError("INVALID_DELEGATION")
    return canonical


def _decode(value, limit):
    if (type(value) is not str or len(value) > (limit * 4 + 2) // 3
            or re.fullmatch(r"[A-Za-z0-9_-]*", value) is None):
        raise ConnectorError("INVALID_DELEGATION")
    raw = None
    try:
        raw = base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
    except ValueError:
        pass
    if raw is None or len(raw) > limit or base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii") != value:
        raise ConnectorError("INVALID_DELEGATION")
    return raw


def _payload(raw):
    p = _canonical_object(raw, FIELDS)
    numeric = ("version", "approvalGeneration", "issuedAt", "expiresAt", "upstreamExpiresAt")
    if (any(type(p[k]) is not int or not 0 <= p[k] <= MAX_SAFE_INTEGER for k in numeric)
            or p["version"] != 1 or p["protocol"] != PROTOCOL
            or any(type(p[k]) is not str or IDENTIFIER.fullmatch(p[k]) is None for k in ("sub", "clientId", "requestId"))
            or not valid_capabilities(p["capabilities"])
            or type(p["argumentsSHA256"]) is not str or re.fullmatch(r"[0-9a-f]{64}", p["argumentsSHA256"]) is None
            or p["issuedAt"] <= 0 or p["expiresAt"] <= p["issuedAt"]
            or p["upstreamExpiresAt"] < p["expiresAt"] or p["expiresAt"] - p["issuedAt"] > MAX_LIFETIME_SECONDS
            or type(p["destination"]) is not str or type(p["operation"]) is not str):
        raise ConnectorError("INVALID_DELEGATION")
    return p


def kms_signing_input(key_id, raw_payload):
    """Pass Message unchanged to KMS Sign; DIGEST must not be hashed twice."""
    if type(key_id) is not str or KEY_ARN.fullmatch(key_id) is None or len(raw_payload) > MAX_PAYLOAD_BYTES:
        raise ConnectorError("INVALID_DELEGATION")
    _payload(raw_payload)
    return {"KeyId": key_id, "Message": hashlib.sha256(raw_payload).digest(),
            "MessageType": "DIGEST", "SigningAlgorithm": SIGNING_ALGORITHM}


class DelegationVerifier:
    def __init__(self, key_id, spki, destination, resolve, clock=None):
        key = None
        try:
            if (type(key_id) is not str or KEY_ARN.fullmatch(key_id) is None or len(spki) > 2048
                    or destination not in ("ttobak-api-bridge", "ttobak-qa-bridge") or not callable(resolve)
                    or (clock is not None and not callable(clock))):
                raise ValueError()
            key = serialization.load_der_public_key(spki)
            if (not isinstance(key, rsa.RSAPublicKey) or key.key_size not in (2048, 3072, 4096)
                    or key.public_numbers().e != 65537):
                raise ValueError()
        except (ValueError, TypeError):
            key = None
        if key is None:
            raise ConnectorError("INVALID_CONNECTOR_CONFIG")
        self._key_id, self._key, self._destination = key_id, key, destination
        self._resolve = resolve
        self._clock = clock if clock is not None else lambda: int(time.time())

    def verify(self, wire, operation, request_id, arguments):
        now = self._clock()
        if (not isinstance(wire, bytes) or not 0 < len(wire) <= MAX_WIRE_BYTES
                or not isinstance(arguments, bytes) or len(arguments) > MAX_ARGUMENT_BYTES
                or type(request_id) is not str or IDENTIFIER.fullmatch(request_id) is None
                or type(now) is not int or not 0 < now <= MAX_SAFE_INTEGER):
            raise ConnectorError("INVALID_DELEGATION")
        _utf8(arguments)
        e = _canonical_object(wire, ("keyId", "algorithm", "payload", "signature"))
        if any(type(value) is not str for value in e.values()):
            raise ConnectorError("INVALID_DELEGATION")
        if e["keyId"] != self._key_id:
            raise ConnectorError("UNTRUSTED_KEY")
        if e["algorithm"] != SIGNING_ALGORITHM:
            raise ConnectorError("INVALID_DELEGATION")
        raw = _decode(e["payload"], MAX_PAYLOAD_BYTES)
        signature = None
        try:
            signature = _decode(e["signature"], 512)
        except ConnectorError:
            pass
        if signature is None or len(signature) != self._key.key_size // 8:
            raise ConnectorError("INVALID_SIGNATURE")
        valid = False
        try:
            self._key.verify(signature, raw, padding.PSS(mgf=padding.MGF1(hashes.SHA256()), salt_length=32), hashes.SHA256())
            valid = True
        except (InvalidSignature, ValueError):
            pass
        if not valid:
            raise ConnectorError("INVALID_SIGNATURE")
        p = _payload(raw)
        if (p["destination"] != self._destination or p["operation"] != operation or p["requestId"] != request_id
                or not hmac.compare_digest(p["argumentsSHA256"], hashlib.sha256(arguments).hexdigest())):
            raise ConnectorError("REQUEST_MISMATCH")
        if p["expiresAt"] <= now or p["issuedAt"] > now + 5:
            raise ConnectorError("EXPIRED_DELEGATION")
        required = required_capabilities(operation, self._destination)
        grants = None
        try:
            grants = self._resolve(p["sub"], p["clientId"])
        except Exception:
            pass
        if grants is None:
            raise ConnectorError("POLICY_UNAVAILABLE")
        # A network lookup must never publish a principal after its signed expiry.
        now = self._clock()
        if type(now) is not int or not 0 < now <= MAX_SAFE_INTEGER or p["expiresAt"] <= now or p["issuedAt"] > now + 5:
            raise ConnectorError("EXPIRED_DELEGATION")
        caps = effective_capabilities(p, grants, now)
        if not set(required) <= caps:
            raise ConnectorError("CAPABILITY_DENIED")
        return Principal(p["sub"], p["clientId"], caps, p["approvalGeneration"],
                         p["operation"], p["requestId"], p["expiresAt"])
