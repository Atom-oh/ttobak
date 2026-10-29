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



def mo(*costs):
    """{month: cost} over MS; None leaves the month without rows."""
    return {m: c for m, c in zip(MS, costs) if c is not None}


EC2, BR = "AmazonEC2", "AmazonBedrock"
NAN = float("nan")
NOCOV = dict(FULL, **{m: 0.0 for m in MS[:3]})  # Apr-Jun not covered


class SignalRules(unittest.TestCase):
    """One row per rule of the spec in tag_signals.py (numbers are run rates; floor 50 unless total says otherwise).
    Row: (name, months, coverage, values, total_last, expected {tag_value: {field: value}}, expected order or None)."""
    ROWS = [
        # rule 3: a covered month without rows is 0, the same as an explicit zero row (round 5b)
        ("absent covered months count as 0", MS, FULL, [val("x", mo(100, 100, 300, None, None, 300))], 10000.0,
         {"x": {"kinds": ["급증"], "base": 100.0, "run_rate_last": 300.0, "growth_pct": 200.0, "delta": 200.0}}, None),
        ("explicit zero rows give the same", MS, FULL, [val("x", mo(100, 100, 300, 0, 0, 300))], 10000.0,
         {"x": {"kinds": ["급증"], "base": 100.0, "growth_pct": 200.0}}, None),
        ("cost_by_month lists covered empty months as 0", MS, FULL, [val("x", mo(100, 100, 300, None, None, 300))], 10000.0,
         {"x": {"cost_by_month": [{"month": m, "cost": c} for m, c in zip(MS, (100.0, 100.0, 300.0, 0.0, 0.0, 300.0))]}},
         None),
        ("service baseline counts absent covered months as 0", MS, FULL,
         [val("x", mo(1000, 1000, 1000, 1300, 1000, 1600),
              {EC2: mo(*[1000.0] * 6), BR: mo(None, None, None, 300, None, 600)})], 10000.0,
         {"x": {"kinds": ["신규 서비스"], "delta": 500.0, "new_services": [BR], "growth_pct": 45.45}}, None),
        # rule 6: an earlier large negative month (credit, true-up) is prior activity, not absence
        ("an earlier large credit is not absence", MS, FULL, [val("x", mo(-100000, None, None, None, None, 1000))],
         10000.0, {}, None),
        ("an earlier large service credit is not a new service", MS, FULL,
         [val("x", mo(1000, 1000, 1000, 1000, 1000, 1300),
              {EC2: mo(*[1000.0] * 6), BR: mo(-50000, None, None, None, None, 300)})], 10000.0, {}, None),
        # rule 2: a month without coverage is removed, never read as 0 or as absence
        ("uncovered early months are not absence", MS, NOCOV, [val("x", mo(None, None, None, 1000, 1000, 1000))],
         10000.0, {}, None),
        ("uncovered first of two months is not absence", MS[-2:], {MS[-2]: 0.0, MS[-1]: 1.0},
         [val("x", {MS[-1]: 1000.0})], 10000.0, {}, None),
        ("a missing coverage key is uncovered too", MS, {m: 1.0 for m in MS if m != "2026-07"},
         [val("x", mo(100, 100, 100, 5000, 100, 300))], 10000.0,
         {"x": {"kinds": ["급증"], "base": 100.0, "cost_by_month": [{"month": m, "cost": c} for m, c in
                                                                    zip(MS, (100.0, 100.0, 100.0, 5000.0, 100.0, 300.0))]}},
         None),
        ("NaN coverage is uncovered", MS, dict(NOCOV, **{m: NAN for m in MS[:3]}),
         [val("x", mo(None, None, None, 1000, 1000, 1000))], 10000.0, {}, None),
        ("uncovered latest month: no signal", MS, dict(FULL, **{MS[-1]: 0.0}), [val("x", mo(*[None] * 5, 5000))],
         10000.0, {}, None),
        ("new relative to the covered months", MS, NOCOV, [val("x", mo(None, None, None, 0, 0, 1000))], 10000.0,
         {"x": {"kinds": ["신규"], "first_month": "2026-09", "growth_pct": None}}, None),
        # rule 1/4: rates are cost / coverage, partial edges are run rates, not growth
        ("partial last month at the same rate is flat", MS, dict(FULL, **{MS[-1]: 0.5}),
         [val("x", mo(1000, 1000, 1000, 1000, 1000, 500))], 10000.0, {}, None),
        ("partial last month growth is scaled", MS, dict(FULL, **{MS[-1]: 0.5}),
         [val("x", mo(1000, 1000, 1000, 1000, 1000, 1000))], 10000.0,
         {"x": {"kinds": ["급증"], "run_rate_last": 2000.0, "base": 1000.0, "cost_by_month": [
             {"month": m, "cost": 1000.0} for m in MS]}}, None),
        ("partial first month in the baseline is scaled", MS[-3:], {MS[-3]: 0.25, MS[-2]: 1.0, MS[-1]: 1.0},
         [val("x", {MS[-3]: 250.0, MS[-2]: 1000.0, MS[-1]: 1000.0})], 10000.0, {}, None),
        ("flat spend is no signal", MS, FULL, [val("x", mo(*[1000.0] * 6))], 10000.0, {}, None),
        # rules 5 and 7: floors on rates; the surge threshold is inclusive
        ("floor applies after scaling", MS, dict(FULL, **{MS[-1]: 0.25}), [val("x", mo(10, 10, 10, 10, 10, 20))],
         10000.0, {"x": {"kinds": ["급증"], "run_rate_last": 80.0, "base": 10.0}}, None),
        ("floor is a share of the account", MS, FULL, [val("x", mo(100, 100, 100, 100, 100, 300))], 10 ** 6, {}, None),
        ("surge at exactly 1 + SURGE", MS, FULL, [val("x", mo(*[1000.0] * 5, 1500))], 10000.0,
         {"x": {"kinds": ["급증"], "growth_pct": 50.0}}, None),
        ("just under 1 + SURGE", MS, FULL, [val("x", mo(*[1000.0] * 5, 1499))], 10000.0, {}, None),
        ("surge under the absolute delta floor", MS, FULL, [val("x", mo(*[60.0] * 5, 100))], 10000.0, {}, None),
        ("a covered zero month lowers the base like a zero row", MS, FULL, [val("x", mo(900, 900, 900, 900, None, 900))],
         10000.0, {"x": {"kinds": ["급증"], "base": 600.0}}, None),
        # rule 6: new
        ("new within NEW_EPS of now", MS, FULL, [val("x", mo(10, None, None, None, None, 1000))], 10000.0,
         {"x": {"kinds": ["신규"], "first_month": "2026-09"}}, None),
        ("back after a gap is neither new nor surge", MS, FULL, [val("x", mo(20, None, None, None, None, 1000))],
         10000.0, {}, None),
        ("new needs now over the floor", MS, FULL, [val("x", mo(*[None] * 5, 49))], 10000.0, {}, None),
        ("new in the last two months", MS[-2:], FULL, [val("x", {MS[-1]: 500.0})], 1000.0,
         {"x": {"kinds": ["신규"], "base": 0.0, "share_pct": 50.0}}, None),
        ("a value only in the first month is no signal", MS, FULL, [val("x", mo(5000))], 10000.0, {}, None),
        # rule 9: non-finite data, empty and degenerate totals, ordering
        ("NaN cost: no signal for the value", MS, FULL,
         [val("x", mo(1000, 1000, NAN, 1000, 1000, 1600), {EC2: mo(1000, 1000, NAN, 1000, 1000, 1000),
                                                          BR: mo(*[None] * 5, 600)})], 10000.0, {}, None),
        ("NaN total: no signals", MS, FULL, [val("x", mo(*[None] * 5, 1000))], NAN, {}, None),
        ("non-positive total: no share", MS, FULL, [val("x", mo(*[None] * 5, 1000))], -5.0,
         {"x": {"kinds": ["신규"], "share_pct": None}}, None),
        ("zero total: floor is MIN_ABS", MS, FULL, [val("x", mo(*[None] * 5, 50))], 0.0,
         {"x": {"kinds": ["신규"], "share_pct": None}}, None),
        ("ties order by key then value", MS, FULL, [val(n, mo(*[None] * 5, 500)) for n in ("b", "a", "c")], 10000.0,
         {"a": {"delta": 500.0}}, ["a", "b", "c"]),
        ("larger delta first", MS, FULL, [val("small", mo(*[None] * 5, 100)), val("big", mo(*[None] * 5, 900))],
         10000.0, {}, ["big", "small"]),
    ]

    def test_rows(self):
        for name, months, cov, values, total, want, order in self.ROWS:
            with self.subTest(name):
                out = TS.classify("a", months, cov, values, total)
                got = {r["tag_value"]: r for r in out}
                self.assertEqual(set(got), set(want) if order is None else set(order))
                for v, fields in want.items():
                    for k, x in fields.items():
                        self.assertEqual(got[v][k], x, k)
                if order is not None:
                    self.assertEqual([r["tag_value"] for r in out], order)
                self.assertNotIn("NaN", json.dumps(out))

    def test_degenerate_inputs(self):
        self.assertEqual(TS.classify("a", MS[:1], FULL, [val("x", {MS[0]: 1000.0})], 1000.0), [])
        self.assertEqual(TS.classify("a", MS, FULL, [], 1000.0), [])
        self.assertEqual(TS.classify("a", MS, {}, [val("x", mo(*[None] * 5, 1000))], 1000.0), [])

    def test_services_and_new_services_order(self):
        v = val("x", mo(*[None] * 5, 900), {"S3": mo(*[None] * 5, 300), BR: mo(*[None] * 5, 300),
                                            EC2: mo(*[None] * 5, 300)})
        out = TS.classify("a", MS, FULL, [v], 10000.0)[0]
        self.assertEqual([s["service"] for s in out["services"]], [BR, EC2, "S3"])
        v["by_month"] = mo(*[1000.0] * 6)  # flat: 600 of Z moved to two new services
        v["services"] = {"Z": mo(*[1000.0] * 5, 400), "S3": mo(*[None] * 5, 300), BR: mo(*[None] * 5, 300)}
        out = TS.classify("a", MS, FULL, [v], 10000.0)[0]
        self.assertEqual((out["kinds"], out["new_services"], out["delta"]), (["신규 서비스"], [BR, "S3"], 600.0))


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
