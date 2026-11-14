import contextlib
import io
import json
import os
import stat
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import export_labels as el  # noqa: E402

sealing = el.sealing
USER, ACC, HASH = "u1", "acc1", "ab" * 32


class FakeKMS:
    """Wraps data keys as key bytes + context; decrypt checks the context."""

    def generate_data_key(self, KeyId, KeySpec, EncryptionContext):
        key = os.urandom(32)
        blob = key + json.dumps(EncryptionContext, sort_keys=True).encode()
        return {"Plaintext": key, "CiphertextBlob": blob}

    def decrypt(self, CiphertextBlob, EncryptionContext, KeyId=None):
        key, ctx = CiphertextBlob[:32], CiphertextBlob[32:]
        if json.loads(ctx) != EncryptionContext:
            raise RuntimeError("context mismatch")
        return {"Plaintext": key}


class FakeDDB:
    def __init__(self, items, page=2):
        self.items, self.page = items, page

    def query(self, **kw):
        prefix = kw["ExpressionAttributeValues"][":p"]["S"]
        rows = [i for i in self.items if i["SK"]["S"].startswith(prefix)]
        start = kw.get("ExclusiveStartKey", {}).get("i", {}).get("N")
        s = int(start) if start else 0
        out = {"Items": rows[s:s + self.page]}
        if s + self.page < len(rows):
            out["LastEvaluatedKey"] = {"i": {"N": str(s + self.page)}}
        return out


def S(v):
    return {"S": v}


def seal(kms, payload, purpose, account=ACC, url_hash=HASH):
    return sealing.seal(kms, "key", json.dumps(payload, ensure_ascii=False).encode(),
                        {"purpose": purpose, "userId": USER, "accountId": account, "urlHash": url_hash})


def decision(kms, n, score=0.8, gate_input=True, purpose="ttobak-customer-update-decision"):
    h = f"{n:064x}"
    payload = {"reason": "r"}
    if isinstance(gate_input, dict):
        payload["gateInput"] = gate_input
    elif gate_input:
        payload["gateInput"] = {"question": "q",
                                "options": [{"id": "needed", "text": "a"},
                                            {"id": "not_needed", "text": "b"}],
                                "context": f"고객 {n}"}
    return {"SK": S(f"CUDEC#{h}#{ACC}"), "score": {"N": str(score)},
            "sealed": S(seal(kms, payload, purpose, url_hash=h))}


def draft(kms, n, status, reason=""):
    h = f"{n:064x}"
    return {"SK": S(f"CUDRAFT#{ACC}#{h}"), "status": S(status),
            "sealed": S(seal(kms, {"dismissReason": reason}, "ttobak-customer-update-draft", url_hash=h))}


class LabelTest(unittest.TestCase):
    def test_mapping(self):
        self.assertEqual(el.label_for("adopted", ""), "needed")
        self.assertEqual(el.label_for("dismissed", "not_relevant"), "not_needed")
        self.assertIsNone(el.label_for("dismissed", "already_known"))
        self.assertIsNone(el.label_for("dismissed", "not_now"))
        self.assertIsNone(el.label_for("draft", ""))
        self.assertIsNone(el.label_for(None, ""))


class ExportTest(unittest.TestCase):
    def setUp(self):
        self.kms = FakeKMS()

    def items(self):
        k = self.kms
        return [decision(k, 1), draft(k, 1, "adopted"),
                decision(k, 2), draft(k, 2, "dismissed", "not_relevant"),
                decision(k, 3), draft(k, 3, "dismissed", "not_now"),
                decision(k, 4, gate_input=False), draft(k, 4, "adopted"),
                decision(k, 5),  # no draft
                decision(k, 6, purpose="ttobak-other"),  # wrong binding
                decision(k, 7, gate_input={"question": "q", "context": "c",
                                           "options": [{"id": "needed", "text": "a"}]}),
                draft(k, 7, "adopted"),  # malformed: no not_needed option
                decision(k, 8, gate_input={"question": 1, "context": "c", "options": []}),
                draft(k, 8, "adopted")]  # malformed: question not a string

    def test_round_trip_and_counts(self):
        rows, counts = el.export(FakeDDB(self.items()), self.kms, "t", USER, "key")
        self.assertEqual([r["label"] for r in rows], ["needed", "not_needed"])
        self.assertEqual(rows[0]["context"], "고객 1")
        self.assertEqual(rows[0]["lunaScore"], 0.8)
        self.assertEqual(set(rows[0]), {"question", "options", "context", "label", "lunaScore"})
        self.assertEqual(counts, {"exported": 2, "skipped": 3, "unreadable": 1, "malformed": 2})

    def test_wrong_binding_is_unreadable(self):
        sealed = seal(self.kms, {"a": 1}, "ttobak-customer-update-decision")
        good = {"purpose": "ttobak-customer-update-decision", "userId": USER,
                "accountId": ACC, "urlHash": HASH}
        self.assertEqual(json.loads(el.open_envelope(self.kms, sealed, good, "key")), {"a": 1})
        with self.assertRaises(el.OpenError):
            el.open_envelope(self.kms, sealed, dict(good, userId="u2"), "key")

    def test_credential_failure_is_fatal(self):
        class Denied(Exception):
            response = {"Error": {"Code": "AccessDeniedException"}}

        class DeniedKMS(FakeKMS):
            def decrypt(self, **kw):
                raise Denied()

        with self.assertRaises(el.FatalError):
            el.export(FakeDDB(self.items()), DeniedKMS(), "t", USER, "key")

    def test_no_credentials_is_fatal(self):
        class NoCredentialsError(Exception):
            pass

        class NoCredsKMS(FakeKMS):
            def decrypt(self, **kw):
                raise NoCredentialsError()

        with self.assertRaises(el.FatalError):
            el.export(FakeDDB(self.items()), NoCredsKMS(), "t", USER, "key")


class MainTest(unittest.TestCase):
    def run_main(self, path, *extra, items=None, kms=None, key=True):
        kms = kms or FakeKMS()
        ddb = FakeDDB(items if items is not None else [decision(kms, 1), draft(kms, 1, "adopted")])
        out, err = io.StringIO(), io.StringIO()
        args = ["--user-id", USER, "--out", path] + (["--key-id", "key"] if key else []) + list(extra)
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = el.main(args, ddb=ddb, kms=kms)
        return code, out.getvalue(), err.getvalue()

    def test_key_id_required(self):
        saved = os.environ.pop("KMS_KEY_ID", None)
        try:
            with tempfile.TemporaryDirectory() as d:
                code, out, err = self.run_main(os.path.join(d, "l"), key=False)
        finally:
            if saved is not None:
                os.environ["KMS_KEY_ID"] = saved
        self.assertEqual(code, 2)
        self.assertIn("--key-id", err)

    def test_fatal_exit_and_no_file(self):
        class Denied(Exception):
            response = {"Error": {"Code": "AccessDeniedException"}}

        class DeniedKMS(FakeKMS):
            def decrypt(self, **kw):
                raise Denied()

        k = FakeKMS()
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "l")
            code, out, err = self.run_main(path, items=[decision(k, 1)], kms=DeniedKMS())
            self.assertEqual(code, 2)
            self.assertFalse(os.path.exists(path))
            self.assertEqual(len(err.strip().splitlines()), 1)

    def test_nothing_exported_but_unreadable_exits_nonzero(self):
        k = FakeKMS()
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "l")
            code, out, _ = self.run_main(path, items=[decision(k, 1, purpose="ttobak-other")])
            self.assertEqual(code, 1)
            self.assertIn("unreadable=1", out)

    def test_symlink_target_never_followed(self):
        with tempfile.TemporaryDirectory() as d:
            target = os.path.join(d, "target")
            with open(target, "w") as fh:
                fh.write("keep")
            link = os.path.join(d, "link")
            os.symlink(target, link)
            for extra in ((), ("--force",)):
                code, out, err = self.run_main(link, *extra)
                self.assertEqual(code, 1)
                self.assertNotIn("Traceback", err)
                self.assertEqual(len(err.strip().splitlines()), 1)
                with open(target) as fh:
                    self.assertEqual(fh.read(), "keep")

    def test_mode_overwrite_and_stdout(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "labels.jsonl")
            code, out, _ = self.run_main(path)
            self.assertEqual(code, 0)
            self.assertEqual(out.strip(), "exported=1 skipped=0 unreadable=0 malformed=0")
            self.assertNotIn("고객", out)
            self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600)
            with open(path, encoding="utf-8") as fh:
                self.assertEqual(json.loads(fh.readline())["context"], "고객 1")
            code, out, err = self.run_main(path)
            self.assertEqual(code, 1)
            self.assertEqual(out, "")
            self.assertIn("use --force", err)
            code, _, _ = self.run_main(path, "--force")
            self.assertEqual(code, 0)
            self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
