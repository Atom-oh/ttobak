"""KMS envelope sealing, byte-compatible with backend/internal/sealing Envelope.Seal.

Format (base64 standard): 0x01 | uint16 BE len(wrapped key) | wrapped key |
12-byte nonce | AES-256-GCM ciphertext+tag. The GCM additional data is Go's
json.Marshal of the binding as [key, value] pairs sorted by key, and the KMS
encryption context is the binding itself. The runtime role cannot decrypt, so
there is no open here. Contract vectors: contracts/customer-updates/sealing-v1.json.
"""

import base64
import json
import os

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

VERSION = 1
NONCE_BYTES = 12
_GO_HTML_ESCAPES = (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"),
                    (" ", "\\u2028"), (" ", "\\u2029"))


class SealingError(Exception):
    """code: unavailable (no key configured: fail the run), binding, data_key,
    wrapped_key, kms, plaintext, nonce."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


def _aad(binding):
    """Exact bytes of Go's json.Marshal([][2]string{...sorted by key}).

    Python's compact ensure_ascii=False output matches Go for quotes,
    backslashes, \\b \\f \\n \\r \\t and other control characters (lowercase
    \\u00XX); Go additionally escapes <, >, &, U+2028 and U+2029. Code-point
    order equals Go's UTF-8 byte order for sort.Strings."""
    if not isinstance(binding, dict) or not binding or not all(
            isinstance(k, str) and isinstance(v, str) for k, v in binding.items()):
        raise SealingError("binding")
    text = json.dumps([[k, binding[k]] for k in sorted(binding)], ensure_ascii=False,
                      separators=(",", ":"))
    for raw, escaped in _GO_HTML_ESCAPES:
        text = text.replace(raw, escaped)
    try:
        return text.encode("utf-8")
    except UnicodeEncodeError:
        raise SealingError("binding") from None


def _random_nonce():
    return os.urandom(NONCE_BYTES)


def seal(kms_client, key_id, plaintext, binding):
    if not key_id:
        raise SealingError("unavailable")
    if not isinstance(plaintext, (bytes, bytearray)):
        raise SealingError("plaintext")
    additional = _aad(binding)
    try:
        key = kms_client.generate_data_key(KeyId=key_id, KeySpec="AES_256",
                                           EncryptionContext=dict(binding))
    except Exception:
        raise SealingError("kms") from None
    data_key, wrapped = key.get("Plaintext"), key.get("CiphertextBlob")
    if not isinstance(data_key, (bytes, bytearray)) or len(data_key) != 32:
        raise SealingError("data_key")
    if not isinstance(wrapped, (bytes, bytearray)) or not 0 < len(wrapped) <= 0xFFFF:
        raise SealingError("wrapped_key")
    nonce = _random_nonce()
    if len(nonce) != NONCE_BYTES:
        raise SealingError("nonce")
    aead = AESGCM(data_key)
    del key, data_key
    body = aead.encrypt(nonce, bytes(plaintext), additional)
    del aead
    out = bytes([VERSION]) + len(wrapped).to_bytes(2, "big") + bytes(wrapped) + nonce + body
    return base64.b64encode(out).decode("ascii")
