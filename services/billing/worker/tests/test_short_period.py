"""Short COT periods (fewer than 6 months): first-month fields use the first present month, half averages divide by
the months present, and HTML tables with fewer month columns land in the right slots. build_data -> bedrock_tags ->
export_datasets end to end on synthetic CSVs. Synthetic data only."""
import csv, json, os, subprocess, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPTS = os.path.join(HERE, "..", "scripts")
E = "acct-a"
HDR = ["", "Report Name", "timePeriod.start", "keys", "metrics.AmortizedCost.amount", "metrics.UsageQuantity.amount", "x", "y"]
EMPTY_SHEETS = ("Data_Data_Transfer", "Data_Storage", "Data_On_Demand_Usage", "Data_Savings_Plan_Usage",
                "Data_Reserved_Instance_Usage", "Data_Spot_Usage", "Data_COH_All")


from fileutil import jload, jdump  # noqa: E402

def sheet(path, rows):
    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(HDR)
        for month, key, cost in rows:
            w.writerow(["", E, month + "-01", "['%s']" % key, str(cost), "1", "", ""])


def fixture(d, months, svc, mkt, cov):
    """svc: {service: [cost per month]}; mkt: [(name, [cost per month])]; cov: EC2 coverage % per month."""
    csvd, root = os.path.join(d, "csv"), os.path.join(d, "extracted")
    html = os.path.join(csvd, E, "html")
    os.makedirs(html)
    os.makedirs(os.path.join(root, E))
    sheet(os.path.join(csvd, E, "Data_Service_Costs.csv"), [(m, s, v[i]) for s, v in svc.items() for i, m in enumerate(months)])
    sheet(os.path.join(csvd, E, "Data_Linked_Account_Costs.csv"), [(m, "111111111111", 50) for m in months])
    for n in EMPTY_SHEETS:
        sheet(os.path.join(csvd, E, n + ".csv"), [])
    jdump({"headers": ["keys"] + months + ["Rank", "MoM Trend"],
               "rows": [[name] + v + [i + 1, "up"] for i, (name, v) in enumerate(mkt)]}, os.path.join(html, "json_shared_Marketplace_Costs.json"), "w")
    if cov is not None:  # None: the COT360 page has no EC2 unit-metrics block
        jdump({"headers": ["Month", "Cost ($)", "Instance Hours", "vCPU Hours", "Cost/Instance-Hr", "Cost/vCPU-Hr", "Coverage %",
                               "Graviton/Latest %"],
                   "rows": [[m, 100, 10, 20, 1, 1, cov[i], 5] for i, m in enumerate(months)]}, os.path.join(html, "json_um_ec2_1.json"), "w")
    last = round(sum(v[-1] for v in svc.values()))
    with open(os.path.join(root, E, "COT_%s_COT360.html" % E), "w", encoding="utf-8") as f:
        f.write("<html><body><div>Past Month Spend</div><div>$%s Amortized</div></body></html>" % format(last, ","))
    return csvd, root


def run(d, months, csvd, root, as_of=None):
    env = {k: v for k, v in os.environ.items() if not k.startswith("COT_") and k != "AS_OF"}
    env.update(COT_MONTHS=",".join(months), COT_CSV_DIR=csvd, COT_ENTITIES=E)
    if as_of is not None:
        env["AS_OF"] = as_of
    dj, out = os.path.join(d, "dashboard_data.json"), os.path.join(d, "out")
    for args in (["build_data.py", root, dj], ["bedrock_tags.py", dj], ["export_datasets.py", dj, out]):
        r = subprocess.run([sys.executable, "-I", os.path.join(SCRIPTS, args[0])] + args[1:], env=env,
                           capture_output=True, text=True, timeout=120)
        assert r.returncode == 0, (args[0], r.stderr[-2000:])
    load = lambda n: jload(os.path.join(out, n + ".json"), encoding="utf-8")  # noqa: E731
    return jload(dj, encoding="utf-8")["entities"][E], load


class ShortPeriod(unittest.TestCase):
    def setUp(self):
        self._d = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.d = self._d.name

    def tearDown(self):
        self._d.cleanup()

    def test_three_months(self):
        months = ["2026-07", "2026-08", "2026-09"]
        svc = {"Amazon EC2": [100, 110, 120], "Amazon S3": [5, 5, 5]}
        mkt = [("Claude Sonnet 4 (Amazon Bedrock Edition)", [40, 50, 120]), ("Some SaaS", [7, 0, 0])]
        csvd, root = fixture(self.d, months, svc, mkt, [40.0, 50.0, 60.0])
        x, load = run(self.d, months, csvd, root, as_of="2026-10-19")
        k = load("kpis")[0]
        self.assertEqual(load("period")[0]["as_of"], "2026-10-19", "M1: the run date is published")
        self.assertEqual(load("period")[0]["months"], months)
        self.assertEqual(x["monthly_total"], [0, 0, 0, 105, 115, 125])
        self.assertEqual(x["bedrock"]["marketplace_total"], [0, 0, 0, 40, 50, 120], "HTML month columns in the right slots")
        self.assertEqual(x["genai"]["marketplace_ai"], [0, 0, 0, 40, 50, 120])
        self.assertEqual(x["marketplace_saas"]["months"], [0, 0, 0, 7, 0, 0])
        self.assertEqual(k["bedrock_first"], 40)
        self.assertEqual(k["bedrock_last"], 120)
        self.assertEqual(k["ec2_coverage_first"], 40.0)
        self.assertEqual(k["ec2_coverage_last"], 60.0)
        self.assertEqual(k["accounts_first"], 1)
        self.assertIsNone(k["h1_avg"], "no month in the earlier half")
        self.assertEqual(k["h2_avg"], 115)
        self.assertIsNone(k["h2_vs_h1_pct"])
        f = {r["area"]: r for r in load("findings")}
        self.assertIn("$40 →", f["Bedrock"]["text"])
        self.assertEqual(f["Bedrock"]["severity"], "new", "120 > 2 x the first present month (40)")
        self.assertNotIn("None", f["지출 추세"]["text"])
        self.assertIn("40.0%", f["커버리지"]["text"])
        sm = load("spend_monthly")
        self.assertEqual([r["month"] for r in sm], months)

    def test_four_months_half_average_divides_by_present_months(self):
        months = ["2026-06", "2026-07", "2026-08", "2026-09"]
        svc = {"Amazon EC2": [90, 100, 110, 120]}
        mkt = [("Claude Haiku 4 (Amazon Bedrock Edition)", [10, 20, 30, 15])]
        csvd, root = fixture(self.d, months, svc, mkt, [10.0, 20.0, 30.0, 40.0])
        x, load = run(self.d, months, csvd, root, as_of="not-a-date")
        k = load("kpis")[0]
        self.assertNotIn("as_of", load("period")[0], "an invalid AS_OF is not published")
        self.assertEqual(k["h1_avg"], 90, "one present month in the earlier half")
        self.assertEqual(k["h2_avg"], 110)
        self.assertEqual(k["h2_vs_h1_pct"], round((110 / 90 - 1) * 100, 1))
        self.assertEqual(k["bedrock_first"], 10)
        self.assertEqual(k["ec2_coverage_first"], 10.0)
        self.assertEqual(x["unit"]["ec2"]["coverage"], [0.0, 0.0, 10.0, 20.0, 30.0, 40.0])
        self.assertEqual(x["bedrock"]["models"][0]["months"], [0, 0, 10, 20, 30, 15])

    def test_no_ec2_unit_metrics(self):
        months = ["2026-07", "2026-08", "2026-09"]
        csvd, root = fixture(self.d, months, {"Amazon S3": [5, 6, 7]}, [], None)
        x, load = run(self.d, months, csvd, root)
        self.assertNotIn("ec2", x["unit"])
        k = load("kpis")[0]
        self.assertIsNone(k["ec2_coverage_last"])
        self.assertIsNone(k["ec2_coverage_first"])
        self.assertNotIn("커버리지", {r["area"] for r in load("findings")})

    def test_zero_spend_earlier_half(self):
        months = ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]
        csvd, root = fixture(self.d, months, {"Amazon EC2": [0, 0, 0, 100, 100, 100]}, [], [0.0] * 6)
        x, load = run(self.d, months, csvd, root)
        k = load("kpis")[0]
        self.assertEqual(k["h1_avg"], 0)
        self.assertIsNone(k["h2_vs_h1_pct"])
        text = {r["area"]: r for r in load("findings")}["지출 추세"]["text"]
        self.assertNotIn("None", text)
        self.assertNotIn("%)", text)

    def test_bedrock_kpis_match_the_chart_series(self):
        # native Bedrock service lines count in the chart series, so the KPIs must count them too (SageMaker is not Bedrock)
        months = ["2026-07", "2026-08", "2026-09"]
        svc = {"Amazon EC2": [100, 100, 100], "Amazon Bedrock": [5, 6, 30], "Amazon SageMaker": [9, 9, 9]}
        mkt = [("Claude Sonnet 4 (Amazon Bedrock Edition)", [40, 50, 120])]
        csvd, root = fixture(self.d, months, svc, mkt, [40.0, 50.0, 60.0])
        _, load = run(self.d, months, csvd, root)
        k = load("kpis")[0]
        chart = [r["bedrock"] for r in load("spend_monthly")]
        self.assertEqual(chart, [45, 56, 150])
        self.assertEqual((k["bedrock_first"], k["bedrock_last"], k["bedrock_6m"]), (chart[0], chart[-1], sum(chart)))

    def test_native_only_bedrock_kpi_not_zero(self):
        months = ["2026-08", "2026-09"]
        csvd, root = fixture(self.d, months, {"Amazon EC2": [100, 100], "Amazon Bedrock": [7, 8]}, [], [40.0, 50.0])
        _, load = run(self.d, months, csvd, root)
        k = load("kpis")[0]
        self.assertEqual((k["bedrock_first"], k["bedrock_last"], k["bedrock_6m"]), (7, 8, 15))


class NonFiniteCells(unittest.TestCase):
    def test_nan_and_inf_cost_cells_count_as_zero(self):
        months = ["2026-08", "2026-09"]
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            csvd, root = fixture(d, months, {"Amazon EC2": [100, 120]}, [], [40.0, 50.0])
            with open(os.path.join(csvd, E, "Data_Service_Costs.csv"), "a", newline="") as f:
                csv.writer(f).writerows([["", E, "2026-08-01", "['Odd']", "nan", "1", "", ""],
                                         ["", E, "2026-09-01", "['Odd']", "inf", "1", "", ""]])
            sheet(os.path.join(csvd, E, "Data_Spot_Usage.csv"), [("2026-09", "m5.large", "nan"), ("2026-09", "c5.large", "-inf")])
            x, load = run(d, months, csvd, root)
            with open(os.path.join(d, "dashboard_data.json"), encoding="utf-8") as f:
                json.load(f, parse_constant=lambda tok: self.fail("non-finite JSON token " + tok))
            self.assertEqual(x["monthly_total"][-2:], [100, 120])
            self.assertTrue(load("kpis"))


class OptionalSheets(unittest.TestCase):
    """A sheet other than Data_Service_Costs that is missing or headerless is empty, recorded, never an abort."""
    months = ["2026-08", "2026-09"]

    def test_missing_and_headerless_optional_sheets(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            csvd, root = fixture(d, self.months, {"Amazon EC2": [100, 120]}, [], [40.0, 50.0])
            for n in ("Data_Linked_Account_Costs", "Data_Storage", "Data_Spot_Usage", "Data_COH_All"):
                os.remove(os.path.join(csvd, E, n + ".csv"))
            for n in ("Data_Data_Transfer", "Data_On_Demand_Usage"):  # no 'Report Name' header row
                with open(os.path.join(csvd, E, n + ".csv"), "w", newline="") as f:
                    csv.writer(f).writerow(["a", "b", "c"])
            x, load = run(d, self.months, csvd, root)
            self.assertEqual(sorted(x["missing_sheets"]), ["Data_Data_Transfer", "Data_Linked_Account_Costs",
                                                           "Data_On_Demand_Usage", "Data_Spot_Usage", "Data_Storage"])
            self.assertEqual(x["monthly_total"][-2:], [100, 120])
            self.assertEqual(x["accounts_active"], [0] * 6)
            self.assertEqual((x["dt"], x["storage_by_region"]), ({}, {}))
            self.assertNotIn("coh_tags", x)
            self.assertTrue(load("kpis"), "downstream datasets are still written")

    def test_present_sheets_record_nothing(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            csvd, root = fixture(d, self.months, {"Amazon EC2": [100, 120]}, [], [40.0, 50.0])
            x, _ = run(d, self.months, csvd, root)
            self.assertEqual(x["missing_sheets"], [])


class SpendReconcile(unittest.TestCase):
    """The last month's service total against the COT360 page figure: reported, never an abort of the whole build."""
    months = ["2026-08", "2026-09"]

    def setUp(self):
        self._d = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.d = self._d.name

    def tearDown(self):
        self._d.cleanup()

    def html(self, root, body):
        with open(os.path.join(root, E, "COT_%s_COT360.html" % E), "w", encoding="utf-8") as f:
            f.write("<html><body>%s</body></html>" % body)

    def test_cent_rounding_drift_over_many_services_reconciles(self):
        # 200 lines of $0.004 each: per-service stats round each to $0.00, the true total is $100.80 -> page $101
        svc = {"Amazon EC2": [100, 100]}
        svc.update({"Svc %03d" % i: [0.004, 0.004] for i in range(200)})
        csvd, root = fixture(self.d, self.months, svc, [], [40.0, 50.0])
        x, _ = run(self.d, self.months, csvd, root)
        self.assertEqual(x["spend_check"], {"ok": True, "computed_last": 100.8, "html_last_spend": 101})
        self.assertEqual(x["monthly_total"][-1], 101)

    def test_missing_page_figure_is_reported(self):
        csvd, root = fixture(self.d, self.months, {"Amazon EC2": [100, 120]}, [], [40.0, 50.0])
        self.html(root, "<div>no spend figure</div>")
        x, load = run(self.d, self.months, csvd, root)
        self.assertEqual(x["spend_check"], {"ok": False, "computed_last": 120.0, "html_last_spend": None})
        self.assertEqual(x["monthly_total"][-2:], [100, 120])
        self.assertTrue(load("kpis"), "downstream datasets are still written")

    def test_real_mismatch_stays_visible(self):
        csvd, root = fixture(self.d, self.months, {"Amazon EC2": [100, 120]}, [], [40.0, 50.0])
        self.html(root, "<div>Past Month Spend</div><div>$150 Amortized</div>")
        x, _ = run(self.d, self.months, csvd, root)
        self.assertEqual(x["spend_check"], {"ok": False, "computed_last": 120.0, "html_last_spend": 150})


if __name__ == "__main__":
    unittest.main()
