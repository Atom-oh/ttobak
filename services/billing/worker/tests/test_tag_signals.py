"""tag_signals.py: CUR classify() and the COT snapshot signals. Synthetic data only."""
import json, os, sys, tempfile, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import tag_signals as TS  # noqa: E402

MS = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
FULL = {m: 1.0 for m in MS}


def val(name, by_month, services=None):
    return {"tag_key": "app", "tag_value": name, "by_month": by_month,
            "services": services or {"AmazonEC2": dict(by_month)}, "accounts": [], "regions": []}


class Classify(unittest.TestCase):
    def test_partial_last_month_is_run_rate(self):
        # half the days of the last month at the same daily rate: not growth
        cov = dict(FULL, **{"2026-09": 0.5})
        v = val("steady", {**{m: 1000.0 for m in MS[:-1]}, "2026-09": 500.0})
        self.assertEqual(TS.classify("a", MS, cov, [v], 10000.0), [])

    def test_partial_first_month_does_not_hide_new(self):
        cov = dict(FULL, **{"2026-04": 0.1})
        v = val("late", {"2026-08": 900.0, "2026-09": 1000.0})
        out = TS.classify("a", MS, cov, [v], 10000.0)
        self.assertEqual(out[0]["kinds"], ["신규"])
        self.assertEqual(out[0]["first_month"], "2026-08")

    def test_floor_is_share_of_account(self):
        v = val("small-up", {**{m: 100.0 for m in MS[:-1]}, "2026-09": 300.0})
        self.assertEqual(TS.classify("a", MS, FULL, [v], 10 ** 6)[:1], [])  # floor $1,000: +$200 is noise
        self.assertEqual(TS.classify("a", MS, FULL, [v], 10000.0)[0]["kinds"], ["급증"])

    def test_surge_with_new_service_and_ordering(self):
        a = val("a", {**{m: 1000.0 for m in MS[:-1]}, "2026-09": 3000.0},
                {"AmazonEC2": {m: 1000.0 for m in MS}, "AmazonBedrock": {"2026-09": 2000.0}})
        b = val("b", {"2026-09": 5000.0})
        out = TS.classify("a", MS, FULL, [a, b], 20000.0)
        self.assertEqual([r["tag_value"] for r in out], ["b", "a"])  # largest delta first
        self.assertEqual(out[1]["kinds"], ["급증", "신규 서비스"])
        self.assertEqual(out[1]["growth_pct"], 200.0)
        self.assertEqual(out[1]["new_services"], ["AmazonBedrock"])
        self.assertEqual(out[1]["share_pct"], 15.0)

    def test_two_months_only(self):
        out = TS.classify("a", MS[-2:], FULL, [val("x", {"2026-09": 500.0})], 1000.0)
        self.assertEqual(out[0]["kinds"], ["신규"])


class Cot(unittest.TestCase):
    def test_new_and_growing_account(self):
        ts = [{"entity": "e", "service": "svc-new", "source_key": "app", "is_new": True, "est_monthly_cost": 10.0, "resources": 4,
               "created_recent_pct": 75.0, "first_created": "2026-07-01", "last_created": "2026-09-01", "regions": ["r1"]},
              {"entity": "e", "service": "svc-old", "source_key": "app", "is_new": False, "est_monthly_cost": 90.0, "resources": 9},
              {"entity": "e", "service": "svc-flat", "source_key": "app", "is_new": False, "est_monthly_cost": 50.0, "resources": 2},
              {"entity": "e", "service": "(태그 없음)", "source_key": "(untagged)", "is_new": True, "est_monthly_cost": 99.0, "resources": 9}]
        ar = [{"entity": "e", "account": "111", "tag_service": "svc-old", "est_monthly_cost": 90.0},
              {"entity": "e", "account": "222", "tag_service": "svc-flat", "est_monthly_cost": 50.0}]
        ac = [{"entity": "e", "account": "111", "name": "growth-acct", "kind": "급증", "m1": None, "m2": 10.0, "m6": 500.0, "pct_h2_h1": 300.0},
              {"entity": "e", "account": "222", "name": "flat-acct", "kind": "유지", "m1": 5.0, "m6": 5.0}]
        out = TS.cot_signals(ts, ar, ac)
        self.assertEqual([(r["tag_value"], r["kinds"]) for r in out], [("svc-new", ["신규"]), ("svc-old", ["증가 계정"])])
        self.assertEqual(out[1]["account_trend"], [{"account": "111", "name": "growth-acct", "kind": "급증", "first": 10.0,
                                                    "last": 500.0, "pct_h2_h1": 300.0}])
        self.assertTrue(all(r["source"] == "cot" for r in out))

    def test_main_writes_file_and_ensure_keeps_it(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "tag_signals.json")
            TS.ensure(d)
            with open(p, encoding="utf-8") as f:
                self.assertEqual(json.load(f), [])
            with open(p, "w", encoding="utf-8") as f:
                f.write("[1]")
            TS.ensure(d)  # already there: left alone
            with open(p, encoding="utf-8") as f:
                self.assertEqual(json.load(f), [1])


if __name__ == "__main__":
    unittest.main()
