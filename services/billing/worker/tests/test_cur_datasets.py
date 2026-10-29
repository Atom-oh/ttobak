"""CUR datasets (datasets.py) from normalized CUR, incl. trend bounds. Synthetic fixtures only."""
import contextlib, io, json, os, sys, tempfile, unittest, zipfile
from cur_fixtures import *  # noqa: E402,F401,F403 (shared CUR fixtures and the Tmp base, moved from test_cur.py)


class Datasets(Tmp):
    def setUp(self):
        super().setUp()
        self.norm = os.path.join(self.d, "work", "cur_norm")
        self.data = os.path.join(self.d, "data")

    def build(self, rows, summary=None):
        import datasets
        write_norm(self.norm, rows)
        datasets.main(self.norm, self.data, "acct-a", summary)
        out = {}
        for n in ("cur_summary", "cur_trend", "cur_tag_coverage", "cur_resources", "period"):
            with open(os.path.join(self.data, n + ".json"), encoding="utf-8") as fh:
                out[n] = json.load(fh)
        return out

    def test_trend_ec2_by_tag(self):
        rows = [nrow("2026-08-03", cost=10.0, tags={"user:Team": "a"}), nrow("2026-08-04", cost=5.0, tags={"user:Team": "b"}),
                nrow("2026-08-05", cost=2.5), nrow("2026-07-05", cost=1.5, tags={"user:Team": "a"}),
                nrow("2026-08-05", cost=7.0, service="AmazonS3", usage_type="TimedStorage")]
        o = self.build(rows)
        t = [r for r in o["cur_trend"] if r["dim"] == "tag:user:Team" and r["service"] == "AmazonEC2" and r["grain"] == "month"]
        self.assertEqual({r["key"] for r in t}, {"a", "b", "(태그 없음)"})
        self.assertAlmostEqual(sum(r["cost"] for r in t), 19.0)
        self.assertTrue(all(r["entity"] == "acct-a" for r in t))
        svc = [r for r in o["cur_trend"] if r["dim"] == "service" and r["grain"] == "month" and r["period"] == "2026-08"]
        self.assertEqual({(r["key"], r["service"]) for r in svc}, {("AmazonEC2", "AmazonEC2"), ("AmazonS3", "AmazonS3")})
        keys = [(r["grain"], r["dim"], r["service"], r["period"], r["key"]) for r in o["cur_trend"]]
        self.assertEqual(keys, sorted(keys))
        self.assertEqual(o["period"], [{"entity": "acct-a", "source": "cur", "months": ["2026-07", "2026-08"],
                                        "first": "2026-07", "prev": "2026-07", "last": "2026-08",
                                        "labels": {"2026-07": "7월", "2026-08": "8월"}}])

    def test_top20_folds_rest(self):
        rows = [nrow("2026-08-03", cost=float(i + 1), usage_type="U%02d" % i) for i in range(25)]
        o = self.build(rows)
        t = [r for r in o["cur_trend"] if r["dim"] == "usage_type" and r["grain"] == "month"]
        self.assertEqual(len(t), 21)
        self.assertEqual(sum(1 for r in t if r["key"] == "기타"), 1)
        self.assertAlmostEqual(sum(r["cost"] for r in t), sum(range(1, 26)))
        self.assertAlmostEqual(next(r["cost"] for r in t if r["key"] == "기타"), sum(range(1, 6)))  # the 5 smallest

    def test_day_grain_last_92_days_only(self):
        rows = [nrow("2026-08-31", cost=1.0), nrow("2026-06-01", cost=2.0), nrow("2026-05-31", cost=4.0)]
        o = self.build(rows)
        days = sorted({r["period"] for r in o["cur_trend"] if r["grain"] == "day"})
        self.assertEqual(days, ["2026-06-01", "2026-08-31"])  # 92 days ending 08-31 start 06-01
        months = {r["period"] for r in o["cur_trend"] if r["grain"] == "month"}
        self.assertEqual(months, {"2026-05", "2026-06", "2026-08"})

    def test_no_tags(self):
        o = self.build([nrow("2026-08-03", cost=3.0), nrow("2026-07-03", cost=1.0)])
        self.assertFalse([r for r in o["cur_trend"] if r["dim"].startswith("tag:")])
        self.assertEqual(o["cur_tag_coverage"], [])
        self.assertEqual(o["cur_summary"][0]["rows"], 2)

    def test_tag_signals_new_surge_and_new_service(self):
        import datasets
        ms = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
        rows = []
        for i, m in enumerate(ms):
            day = m + "-01"  # one line item per month, dated like a monthly-granularity export
            rows.append(nrow(day, cost=1000.0, tags={"user:Service": "pay-core", "env": "prod"}, linked_account="333333333333"))
            rows.append(nrow(day, cost=1200.0 if m == ms[-1] else 400.0, tags={"user:Service": "ledger"}))
            if i >= 4:
                rows.append(nrow(day, cost=800.0, tags={"user:Service": "ai-chat"}, region="us-east-1"))
                rows.append(nrow(day, cost=300.0, service="AmazonBedrock", usage_type="tokens", tags={"user:Service": "pay-core"}))
            if i == 5:
                rows.append(nrow(day, cost=1.0, tags={"user:Service": "tiny"}))
        self.build(rows)
        with open(os.path.join(self.data, "cur_tag_signals.json"), encoding="utf-8") as fh:
            sig = {r["tag_value"]: r for r in json.load(fh)}
        self.assertEqual(set(sig), {"pay-core", "ai-chat", "ledger"})  # tiny is under the floor; env is not a signal key
        self.assertEqual(sig["ai-chat"]["kinds"], ["신규"])
        self.assertEqual(sig["ai-chat"]["first_month"], "2026-08")
        self.assertEqual(sig["ai-chat"]["regions"], ["us-east-1"])
        self.assertIsNone(sig["ai-chat"]["growth_pct"])
        self.assertEqual(sig["ledger"]["kinds"], ["급증"])
        self.assertEqual(sig["pay-core"]["kinds"], ["신규 서비스"])
        self.assertEqual(sig["pay-core"]["new_services"], ["AmazonBedrock"])
        self.assertEqual(sig["pay-core"]["accounts"][0], "333333333333")
        self.assertTrue(all(r["entity"] == "acct-a" and r["source"] == "cur" and r["tag_key"] == "user:Service" for r in sig.values()))
        self.assertEqual([c["month"] for c in sig["ledger"]["cost_by_month"]], ms)
        self.assertEqual(datasets.TS.classify("acct-a", ms[:1], {}, [], 0), [])

    def signals(self, rows):
        self.build(rows)
        with open(os.path.join(self.data, "cur_tag_signals.json"), encoding="utf-8") as fh:
            return {r["tag_value"]: r for r in json.load(fh)}

    def test_monthly_granularity_is_not_inflated(self):
        # a monthly export dates each line item on the 1st: one usage_date per month must not read as 1/30 coverage
        ms = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
        rows = []
        for m in ms:
            last = m == ms[-1]
            rows.append(nrow(m + "-01", cost=1000.0, tags={"user:Service": "pay-core"}))
            rows.append(nrow(m + "-01", cost=1200.0 if last else 400.0, tags={"user:Service": "ledger"}))
            rows.append(nrow(m + "-01", cost=40.0 if last else 10.0, tags={"user:Service": "small"}))
        sig = self.signals(rows)
        self.assertAlmostEqual(sig["ledger"]["run_rate_last"], 1200.0)
        self.assertAlmostEqual(sig["ledger"]["base"], 400.0)
        self.assertNotIn("small", sig)  # $40/month stays under the $50 floor; 30x inflation would have made it a surge

    def test_daily_partial_edge_months(self):
        # daily rows from 2026-08-21 to 2026-10-10: the first and last months are partial, September is full
        import datetime
        rows = []
        d = datetime.date(2026, 8, 21)
        while d <= datetime.date(2026, 10, 10):
            rows.append(nrow(d.isoformat(), cost=10.0, tags={"user:Service": "steady"}))
            rows.append(nrow(d.isoformat(), cost=40.0 if d.month == 10 else 10.0, tags={"user:Service": "jump"}))
            d += datetime.timedelta(days=1)
        sig = self.signals(rows)
        self.assertAlmostEqual(sig["jump"]["run_rate_last"], 40.0 * 31)  # 10 days of $40 scaled to the whole month
        self.assertNotIn("steady", sig)  # constant daily spend is not growth after edge scaling

    def test_tag_coverage_and_top10_keys(self):
        rows = [nrow("2026-08-03", cost=4.0, tags={"k%02d" % i: "v"}) for i in range(12)]
        rows.append(nrow("2026-08-04", cost=6.0, tags={"k00": "v", "k01": "w"}))
        o = self.build(rows)
        dims = {r["dim"] for r in o["cur_trend"] if r["dim"].startswith("tag:")}
        self.assertEqual(len(dims), 10)
        self.assertIn("tag:k00", dims)
        self.assertNotIn("tag:k11", dims)
        cov = {r["tag_key"]: r for r in o["cur_tag_coverage"]}
        self.assertEqual(set(cov), {d[4:] for d in dims})
        self.assertAlmostEqual(cov["k00"]["tagged_cost"], 10.0)
        self.assertAlmostEqual(cov["k00"]["total_cost"], 54.0)
        self.assertAlmostEqual(cov["k00"]["pct"], 10.0 / 54.0 * 100, places=5)
        self.assertEqual(cov["k00"]["month"], "2026-08")

    def test_tag_coverage_has_a_row_for_an_untagged_month(self):
        o = self.build([nrow("2026-09-03", cost=100.0, tags={"team": "a"}), nrow("2026-10-03", cost=100.0)])
        cov = [(r["month"], r["tag_key"], r["tagged_cost"], r["total_cost"], r["pct"]) for r in o["cur_tag_coverage"]]
        self.assertEqual(cov, [("2026-09", "team", 100.0, 100.0, 100.0), ("2026-10", "team", 0.0, 100.0, 0.0)])

    def test_summary_fields(self):
        rows = [nrow("2026-07-05", cost=2.0), nrow("2026-08-03", cost=3.0, linked_account="333333333333")]
        o = self.build(rows, {"format": "cur2-parquet", "dropped_rows": 3})
        s = o["cur_summary"][0]
        self.assertEqual(s, {"entity": "acct-a", "format": "cur2-parquet", "currency": "USD", "first_day": "2026-07-05",
                             "last_day": "2026-08-03", "months": ["2026-07", "2026-08"], "rows": 2, "dropped_rows": 3,
                             "linked_accounts": 2, "cost_by_month": {"2026-07": 2.0, "2026-08": 3.0},
                             "truncated": False, "dropped": 0})
        self.assertIsNone(self.build(rows)["cur_summary"][0]["format"])

    def test_resources_top500_sorted(self):
        rows = []
        for i in range(502):
            rows.append(nrow("2026-08-03", cost=float(i + 1), resource_id="r%03d" % i, tags={"t": "x"} if i == 501 else {}))
        rows.append(nrow("2026-07-03", cost=9.0, resource_id="r501"))
        rows.append(nrow("2026-08-03", cost=999.0, resource_id=""))
        o = self.build(rows)
        r = o["cur_resources"]
        self.assertEqual(len(r), 500)
        self.assertEqual(r[0]["resource_id"], "r501")
        self.assertEqual((r[0]["cost_last"], r[0]["cost_prev"], r[0]["cost_total"]), (502.0, 9.0, 511.0))
        self.assertEqual(r[0]["tags"], {"t": "x"})
        self.assertEqual(r[0]["service"], "AmazonEC2")
        self.assertEqual([x["cost_last"] for x in r], sorted((x["cost_last"] for x in r), reverse=True))
        self.assertNotIn("r000", {x["resource_id"] for x in r})
        self.assertNotIn("", {x["resource_id"] for x in r})


# ---------- final fix wave ----------
class TrendBounds(Datasets):
    def test_day_grain_only_service_and_top3_tags(self):
        rows = []
        for i in range(5):  # tag k0 has the most cost, k4 the least
            rows.append(nrow("2026-08-03", cost=float(100 - i * 10), usage_type="U%d" % i, tags={"k%d" % i: "v"}))
        o = self.build(rows)
        day = {r["dim"] for r in o["cur_trend"] if r["grain"] == "day"}
        month = {r["dim"] for r in o["cur_trend"] if r["grain"] == "month"}
        self.assertEqual(day, {"service", "tag:k0", "tag:k1", "tag:k2"})
        self.assertEqual(month, {"service", "usage_type", "region", "linked_account"} | {"tag:k%d" % i for i in range(5)})
        self.assertEqual((o["cur_summary"][0]["truncated"], o["cur_summary"][0]["dropped"]), (False, 0))

    def test_high_cardinality_capped(self):
        import datasets
        old = datasets.MAX_TREND_ROWS
        self.addCleanup(setattr, datasets, "MAX_TREND_ROWS", old)
        self.assertEqual(old, 200000)
        datasets.MAX_TREND_ROWS = 170
        rows = []
        # 30 days x 3 services (each with its own usage type) and a tag with a new value every day
        for d in range(1, 31):
            for svc in ("AmazonEC2", "AmazonS3", "AmazonRDS"):
                rows.append(nrow("2026-08-%02d" % d, cost=1.0 + d, service=svc, usage_type=svc + "-U",
                                 tags={"team": "t%02d" % d}))
        # month: service, usage_type, region, linked_account one row per service; tag:team 20 values + 기타 per service
        full_month = 3 + 3 + 3 + 3 + 21 * 3
        o = self.build(rows)
        t = o["cur_trend"]
        s = o["cur_summary"][0]
        self.assertLessEqual(len(t), 170)
        self.assertTrue(s["truncated"])
        # before the cap: day service 90 + day tag:team 90 + month 75 = 255; the tag's day rows go first (a tie with
        # service goes against the tag), which leaves 165
        self.assertEqual(s["dropped"], 90)
        self.assertEqual(len(t), 90 + full_month)
        self.assertEqual(len([r for r in t if r["grain"] == "month"]), full_month, "month grain kept")
        self.assertEqual({r["dim"] for r in t if r["grain"] == "day"}, {"service"})
        self.assertAlmostEqual(sum(r["cost"] for r in t if r["grain"] == "day"), sum(3 * (1.0 + d) for d in range(1, 31)))
        keys = [(r["grain"], r["dim"], r["service"], r["period"], r["key"]) for r in t]
        self.assertEqual(keys, sorted(keys))

    def test_byte_budget_drops_groups_until_the_file_fits(self):
        import datasets
        self.assertEqual(datasets.MAX_TREND_BYTES, 40 << 20)
        self.addCleanup(setattr, datasets, "MAX_TREND_BYTES", datasets.MAX_TREND_BYTES)
        datasets.MAX_TREND_BYTES = 80000
        # few rows (far under the row cap), but every tag value is ~3 KB: day tag:team 30 x 3 KB, month 20 x 3 KB
        rows = [nrow("2026-08-%02d" % d, cost=1.0 + d, tags={"team": "t%02d-" % d + "x" * 3000}) for d in range(1, 31)]
        o = self.build(rows)
        t, s = o["cur_trend"], o["cur_summary"][0]
        size = os.path.getsize(os.path.join(self.data, "cur_trend.json"))
        self.assertLessEqual(size, 80000)
        self.assertEqual(size, 2 + sum(datasets._row_bytes(r) for r in t), "measured the way the file is written")
        self.assertLess(len(t) + s["dropped"], datasets.MAX_TREND_ROWS)
        self.assertEqual((s["truncated"], s["dropped"]), (True, 30))
        self.assertEqual({r["dim"] for r in t if r["grain"] == "day"}, {"service"})
        self.assertIn("tag:team", {r["dim"] for r in t if r["grain"] == "month"})

    def test_byte_budget_can_drop_month_groups_too(self):
        import datasets
        rows = [{"grain": g, "dim": dim, "service": "S", "period": p, "key": "k" * 500, "cost": 1.0}
                for g, p in (("day", "2026-08-01"), ("month", "2026-08")) for dim in ("service", "tag:a")]
        kept, dropped = datasets._cap(rows, 100, 2 + datasets._row_bytes(rows[2]))
        self.assertEqual(([(r["grain"], r["dim"]) for r in kept], dropped), ([("month", "service")], 3))
        self.assertEqual(datasets._cap(rows, 100, 1), ([], 4))
        self.assertIs(datasets._cap(rows, 100)[0], rows, "small input unchanged")


if __name__ == "__main__":
    unittest.main()
