"""agents.py FSI web-search egress guard (safe_query), the CUR currency in the note fingerprint, and the ai status.
Synthetic data only."""
import os, sys, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, ds  # noqa: E402


class SafeQuery(unittest.TestCase):
    def test_generic_query_passes(self):
        self.assertTrue(agents.safe_query("bank payments case study", set()))

    def test_short_private_name_split_or_respelled(self):
        # "qpay" is a private tag value: split into vocabulary words it must still be refused
        for q in ("AWS Q Pay bank case study", "q-pay bank case study", "Q.Pay bank case study", "qpay bank case study"):
            self.assertFalse(agents.safe_query(q, {"qpay"}), q)

    def test_short_name_does_not_block_longer_words(self):
        # a tag "pay" is a whole word only: "payments" and "pay" glued to a neighbour stay allowed
        self.assertTrue(agents.safe_query("bank payments case study", {"pay"}))
        self.assertFalse(agents.safe_query("bank pay case study", {"pay"}))

    def test_private_terms_reach_the_guard(self):
        D = ds()
        D["accounts"] = [{"entity": A, "account": "111111111111", "name": "QPay"}]
        priv = agents.private_terms(D)
        self.assertIn("qpay", priv)
        self.assertFalse(agents.safe_query("AWS Q Pay bank case study", priv))

    def test_shape_rules(self):
        for q in ("x" * 121, "bank 2026 case study", "bank case study <b>", None, "cloud migration case study"):
            self.assertFalse(agents.safe_query(q, set()), q)


class CurrencyFingerprint(unittest.TestCase):
    def test_non_usd_relabel_changes_the_fingerprint(self):
        # the business digest carries CUR tag signals but not the currency: a relabel alone must still make it stale
        D = ds()
        none = agents.digest_fp(D, A, ["business"])
        D["cur_summary"] = [{"entity": A, "currency": "USD"}]
        usd = agents.digest_fp(D, A, ["business"])
        self.assertEqual(usd, none)
        D["cur_summary"] = [{"entity": A, "currency": "KRW"}]
        self.assertNotEqual(agents.digest_fp(D, A, ["business"]), usd)


class AiStatus(unittest.TestCase):
    def test_no_call_made_is_no_analysis(self):
        def invoke(*a, **k):
            raise AssertionError("no model call expected")
        D = ds()
        D["cur_summary"] = [{"entity": A, "currency": "KRW"}]
        D["kpis"] = []
        out = agents.generate(D, invoke, {}, areas=["cur"], synth=False, extras=False)
        self.assertEqual(out["ai"], "none")


if __name__ == "__main__":
    unittest.main()
