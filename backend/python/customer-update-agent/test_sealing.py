import base64
import json
import unittest
from pathlib import Path
from unittest import mock

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

import sealing

VECTORS = Path(__file__).resolve().parents[3] / "contracts" / "customer-updates" / "sealing-v1.json"
BINDING = {"purpose": "ttobak-customer-update-run", "runId": "0" * 32, "sk": "DRAFT#x"}


class FakeKMS:
    def __init__(self, data_key=b"k" * 32, wrapped=b"wrapped-key", error=None):
        self.data_key, self.wrapped, self.error = data_key, wrapped, error
        self.calls = []

    def generate_data_key(self, **kwargs):
        self.calls.append(kwargs)
        if self.error:
            raise self.error
        return {"Plaintext": self.data_key, "CiphertextBlob": self.wrapped}


def open_sealed(sealed, data_key, binding):
    """Test-only inverse of seal; the runtime role cannot decrypt."""
    raw = base64.b64decode(sealed, validate=True)
    assert raw[0] == 1
    n = int.from_bytes(raw[1:3], "big")
    nonce, body = raw[3 + n:3 + n + 12], raw[3 + n + 12:]
    return raw[3:3 + n], AESGCM(data_key).decrypt(nonce, body, sealing._aad(binding))


class VectorTest(unittest.TestCase):
    def test_vectors_reproduce(self):
        doc = json.loads(VECTORS.read_text(encoding="utf-8"))
        self.assertEqual(doc["version"], 1)
        self.assertGreaterEqual(len(doc["vectors"]), 2)
        for v in doc["vectors"]:
            with self.subTest(v["name"]):
                kms = FakeKMS(bytes.fromhex(v["dataKeyHex"]), bytes.fromhex(v["wrappedKeyHex"]))
                with mock.patch.object(sealing, "_random_nonce",
                                       return_value=bytes.fromhex(v["nonceHex"])):
                    got = sealing.seal(kms, "key", v["plaintextUtf8"].encode("utf-8"),
                                       v["binding"])
                self.assertEqual(got, v["sealedBase64"])
                self.assertEqual(kms.calls[0]["EncryptionContext"], v["binding"])

    def test_vectors_cover_go_escaping(self):
        doc = json.loads(VECTORS.read_text(encoding="utf-8"))
        text = json.dumps([v["binding"] for v in doc["vectors"]], ensure_ascii=False)
        for ch in ("<", ">", "&", "고객", " ", "\b"):
            self.assertIn(json.dumps(ch, ensure_ascii=False)[1:-1], text)


class AadTest(unittest.TestCase):
    def test_matches_go_json_marshal(self):
        binding = {"b": "<a&b>", "a": "한글 ", "c": "\b\f\n\r\t\x01\"\\\x7f"}
        self.assertEqual(
            sealing._aad(binding),
            '[["a","한글\\u2028"],["b","\\u003ca\\u0026b\\u003e"],'
            '["c","\\b\\f\\n\\r\\t\\u0001\\"\\\\\x7f"]]'.encode("utf-8"))

    def test_sorted_by_code_point(self):
        self.assertEqual(sealing._aad({"가": "1", "Z": "2", "a": "3"}),
                         '[["Z","2"],["a","3"],["가","1"]]'.encode("utf-8"))


class SealTest(unittest.TestCase):
    def test_round_trip_and_request(self):
        kms = FakeKMS()
        sealed = sealing.seal(kms, "alias/key", "초안 본문".encode("utf-8"), BINDING)
        self.assertEqual(kms.calls, [{"KeyId": "alias/key", "KeySpec": "AES_256",
                                      "EncryptionContext": BINDING}])
        wrapped, plain = open_sealed(sealed, b"k" * 32, BINDING)
        self.assertEqual((wrapped, plain.decode("utf-8")), (b"wrapped-key", "초안 본문"))
        self.assertNotIn("초안", base64.b64decode(sealed).decode("latin-1"))

    def test_fresh_nonce_per_seal(self):
        kms = FakeKMS()
        self.assertNotEqual(sealing.seal(kms, "k", b"x", BINDING), sealing.seal(kms, "k", b"x", BINDING))

    def test_rejections(self):
        cases = {
            "unavailable": (FakeKMS(), "", BINDING),
            "binding": (FakeKMS(), "k", {}),
            "binding_type": (FakeKMS(), "k", {"a": 1}),
            "data_key": (FakeKMS(data_key=b"short"), "k", BINDING),
            "wrapped_key": (FakeKMS(wrapped=b"x" * 65536), "k", BINDING),
            "wrapped_empty": (FakeKMS(wrapped=b""), "k", BINDING),
            "kms": (FakeKMS(error=RuntimeError("AccessDenied")), "k", BINDING),
        }
        expected = {"wrapped_empty": "wrapped_key", "binding_type": "binding"}
        for name, (kms, key_id, binding) in cases.items():
            with self.subTest(name):
                with self.assertRaises(sealing.SealingError) as ctx:
                    sealing.seal(kms, key_id, b"x", binding)
                self.assertEqual(ctx.exception.code, expected.get(name, name))

    def test_no_caller_supplied_nonce(self):
        with self.assertRaises(TypeError):
            sealing.seal(FakeKMS(), "k", b"x", BINDING, _nonce=bytes(12))

    def test_bad_nonce_length_rejected(self):
        with mock.patch.object(sealing, "_random_nonce", return_value=b"short"):
            with self.assertRaises(sealing.SealingError) as ctx:
                sealing.seal(FakeKMS(), "k", b"x", BINDING)
        self.assertEqual(ctx.exception.code, "nonce")

    def test_max_wrapped_key_length_is_accepted(self):
        sealed = sealing.seal(FakeKMS(wrapped=b"w" * 65535), "k", b"x", BINDING)
        self.assertEqual(open_sealed(sealed, b"k" * 32, BINDING)[0], b"w" * 65535)


if __name__ == "__main__":
    unittest.main()
