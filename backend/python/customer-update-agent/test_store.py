import base64
import json
import unittest
from decimal import Decimal
from unittest import mock

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

import checks
import sealing
import store
from gate import PairDecision
from sealing import SealingError
from writer import Draft

RUN_ID = "0123456789abcdef0123456789abcdef"
HASH = "b" * 64
NOW = 1_800_000_000
DATA_KEY = b"d" * 32
BODY = "안녕하세요 <!here> A&B 미팅 어떠세요?"
WHY = "고객이 <관심> & 확장 중"
GATE_INPUT = {"question": "q", "options": [{"id": "needed", "text": "t"}],
              "context": "고객 컨텍스트"}


class ConditionFailed(Exception):
    response = {"Error": {"Code": "ConditionalCheckFailedException"}}


class FakeTable:
    """Records puts; honours attribute_not_exists(PK) against earlier puts."""

    def __init__(self, error=None):
        self.puts, self.error = [], error

    def put_item(self, **kwargs):
        if self.error:
            raise self.error
        key = (kwargs["Item"]["PK"], kwargs["Item"]["SK"])
        if kwargs.get("ConditionExpression") == "attribute_not_exists(PK)" and any(
                (p["Item"]["PK"], p["Item"]["SK"]) == key for p in self.puts):
            raise ConditionFailed()
        self.puts.append(kwargs)


class FakeKMS:
    def __init__(self, error=None):
        self.calls, self.error = [], error

    def generate_data_key(self, **kwargs):
        self.calls.append(kwargs)
        if self.error:
            raise self.error
        return {"Plaintext": DATA_KEY, "CiphertextBlob": b"wrapped"}


def open_row(item):
    """Test-only inverse of sealing.seal for a written row; checks the SK binding."""
    binding = {"purpose": "ttobak-customer-update-run", "runId": RUN_ID, "sk": item["SK"]}
    raw = base64.b64decode(item["sealed"], validate=True)
    n = int.from_bytes(raw[1:3], "big")
    nonce, body = raw[3 + n:3 + n + 12], raw[3 + n + 12:]
    plain = AESGCM(DATA_KEY).decrypt(nonce, body, sealing._aad(binding))
    return json.loads(plain.decode("utf-8"))


def results(table=None, kms=None, key_id="key-1"):
    return store.RunResults(table if table is not None else FakeTable(), RUN_ID,
                            kms or FakeKMS(), key_id, clock=lambda: NOW)


class RunResultsTest(unittest.TestCase):
    def setUp(self):
        self.table, self.kms = FakeTable(), FakeKMS()
        self.res = results(self.table, self.kms)

    def only(self):
        self.assertEqual(len(self.table.puts), 1)
        return self.table.puts[0]["Item"]

    def test_missing_key_raises_unavailable_before_any_write(self):
        for key in (None, ""):
            table = FakeTable()
            with self.assertRaises(SealingError) as ctx:
                results(table, key_id=key)
            self.assertEqual(ctx.exception.code, "unavailable")
            self.assertEqual(table.puts, [])

    def test_item_state_row(self):
        self.res.put_item_state(HASH, "skipped", "insufficient")
        self.assertEqual(self.only(), {
            "PK": "CURUN#" + RUN_ID, "SK": "ITEM#" + HASH, "entityType": "UPDATE_RUN_ITEM",
            "state": "skipped", "reason": "insufficient",
            "pendingShareExpiresAt": NOW + 2 * 86400})

    def test_item_state_rejects_unknown_codes(self):
        with self.assertRaises(ValueError):
            self.res.put_item_state(HASH, "done", "")
        with self.assertRaises(ValueError):
            self.res.put_item_state(HASH, "decided", "some prose")

    def test_decision_needed_row_seals_reason_and_gate_input(self):
        dec = PairDecision("needed", 0.876543, "고객이 관심을 가질 이유", GATE_INPUT)
        self.res.put_decision(HASH, "acc1", dec, "luna-v1", "discarded", "contains_figure")
        item = self.only()
        sealed = item.pop("sealed")
        self.assertEqual(item, {
            "PK": "CURUN#" + RUN_ID, "SK": f"DEC#{HASH}#acc1",
            "entityType": "UPDATE_RUN_DECISION", "choice": "needed",
            "score": Decimal("0.8765"), "backend": "luna-v1", "outcome": "discarded",
            "discardReason": "contains_figure", "pendingShareExpiresAt": NOW + 2 * 86400})
        item["sealed"] = sealed
        self.assertEqual(open_row(item),
                         {"reason": "고객이 관심을 가질 이유", "gateInput": GATE_INPUT})
        self.assertEqual(self.kms.calls[0]["EncryptionContext"], {
            "purpose": "ttobak-customer-update-run", "runId": RUN_ID, "sk": f"DEC#{HASH}#acc1"})

    def test_decision_not_needed_omits_gate_input(self):
        self.res.put_decision(HASH, "acc1", PairDecision("not_needed", 0.2, "무관", GATE_INPUT),
                              "luna-v1", "", "")
        item = self.only()
        self.assertEqual(open_row(item), {"reason": "무관"})
        self.assertEqual(item["outcome"], "")
        self.assertEqual(item["score"], Decimal("0.2"))

    def test_decision_rejects_unknown_outcome(self):
        with self.assertRaises(ValueError):
            self.res.put_decision(HASH, "acc1", PairDecision("needed", 0.9, "", {}),
                                  "luna-v1", "sent", "")

    def test_draft_row_plaintext_keys_only_and_text_sealed_raw(self):
        draft = Draft(WHY, BODY, ("https://a.example/x",), "https://a.example/x", "제목 <b>",
                      ("p1", "p2"), ("https://a.example/x", "https://b.example/y"))
        self.res.put_draft("acc1", HASH, draft)
        item = self.only()
        self.assertEqual(set(item), {"PK", "SK", "entityType", "pendingShareExpiresAt", "sealed"})
        self.assertEqual(item["SK"], f"DRAFT#acc1#{HASH}")
        self.assertEqual(item["entityType"], "UPDATE_RUN_DRAFT")
        self.assertEqual(open_row(item), {
            "sourceUrl": "https://a.example/x", "sourceTitle": "제목 <b>",
            "why": WHY,
            "body": BODY,
            "links": ["https://a.example/x"], "opportunityIds": ["p1", "p2"],
            "fetchedUrls": ["https://a.example/x", "https://b.example/y"]})
        text = json.dumps(item, ensure_ascii=False, default=str)
        for leaked in ("안녕하세요", "관심", "제목"):
            self.assertNotIn(leaked, text)

    def test_sealed_draft_rechecks_clean_from_the_row(self):
        link = "https://example.com/a?b=1&c=2"
        body = f"새 기능 <확장> 소식입니다 & 자료는 {link} 입니다. 짧은 미팅 어떠세요?"
        draft = Draft("이유 <a> & b", body, (link,), link, "제목", (), (link,))
        self.assertEqual(checks.check_draft(body, [link], {link}, [], []), [])
        self.res.put_draft("acc1", HASH, draft)
        row = open_row(self.only())
        for ch in "&<>":
            self.assertIn(ch, row["body"])
        self.assertNotIn("&amp;", row["body"] + row["why"])
        self.assertEqual(checks.check_draft(row["body"], row["links"], set(row["fetchedUrls"]),
                                            [], []), [])

    def test_claim_row_is_conditional_and_single_use(self):
        self.res.claim()
        self.res.claim()  # idempotent on the same writer
        put = self.table.puts[0]
        self.assertEqual(len(self.table.puts), 1)
        self.assertEqual(put["ConditionExpression"], "attribute_not_exists(PK)")
        self.assertEqual(put["Item"], {
            "PK": "CURUN#" + RUN_ID, "SK": "CLAIM", "entityType": "UPDATE_RUN_CLAIM",
            "claimedAt": "2027-01-15T08:00:00Z", "pendingShareExpiresAt": NOW + 2 * 86400})
        with self.assertRaises(store.RunExists):
            results(self.table, self.kms).claim()
        self.assertEqual(self.kms.calls, [])

    def test_claim_store_error_propagates(self):
        with self.assertRaises(RuntimeError):
            results(FakeTable(error=RuntimeError("down"))).claim()

    def test_sealed_json_is_compact_utf8(self):
        captured = []
        original = sealing.seal

        def capture(kms, key, plaintext, binding):
            captured.append(plaintext)
            return original(kms, key, plaintext, binding)

        res = results(self.table, self.kms)
        with mock.patch.object(store.sealing, "seal", side_effect=capture):
            res.put_decision(HASH, "a", PairDecision("not_needed", 0.1, "한글", {}), "luna-v1",
                             "", "")
        self.assertEqual(captured, ['{"reason":"한글"}'.encode("utf-8")])

    def test_done_row(self):
        self.res.put_done({"items": 2, "drafted": 1})
        self.assertEqual(self.only(), {
            "PK": "CURUN#" + RUN_ID, "SK": "DONE", "entityType": "UPDATE_RUN_DONE",
            "finishedAt": "2027-01-15T08:00:00Z", "stats": {"items": 2, "drafted": 1},
            "pendingShareExpiresAt": NOW + 2 * 86400})

    def test_kms_failure_propagates_and_writes_nothing(self):
        table = FakeTable()
        res = results(table, FakeKMS(error=RuntimeError("down")))
        with self.assertRaises(SealingError):
            res.put_decision(HASH, "a", PairDecision("needed", 0.9, "r", {}), "luna-v1", "", "")
        self.assertEqual(table.puts, [])


if __name__ == "__main__":
    unittest.main()
