"""Dataset scripts on edge-shaped inputs (empty summaries, missing sheets, NaN cells, mixed plan types, short tables,
mismatched periods). Synthetic data only."""
import csv, json, os, subprocess, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))

from fileutil import jdump, jload  # noqa: E402

E = "acct-a"


class HtmlKpis(unittest.TestCase):
    def page(self, d, html):
        os.makedirs(os.path.join(d, E), exist_ok=True)
        with open(os.path.join(d, E, "COT_%s_COT360.html" % E), "w", encoding="utf-8") as fh:
            fh.write(html)

    def test_strip_blocks_keeps_text_and_drops_scripts_and_styles(self):
        import build_data
        self.assertEqual(build_data.strip_blocks("a<SCRIPT x>b</script>c<style>d</STYLE>e<script>f"), "ace")

    def test_figures_are_read(self):
        import build_data
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            self.page(d, "<div>Past Month Spend</div> <b>$1,234</b> Amortized <script>Past Month Spend $9 Amortized"
                         "</script><p>Total Dedup Savings / mo</p> $56 COH")
            self.assertEqual(build_data.html_kpis(d, E), {"html_last_spend": 1234, "html_coh_dedup": 56})

    def test_strip_tags_matches_the_markup_rule(self):
        import build_data
        self.assertEqual(build_data.strip_tags("a<b>c</b>d<e"), "a c d<e")
        self.assertEqual(build_data.strip_tags("x < y and z > w"), "x   w")

    def test_oversized_page_has_no_figures(self):
        import build_data
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            self.page(d, "Past Month Spend $1,234 Amortized" + " " * 100)
            import htmltext
            old = htmltext.MAX_HTML
            self.addCleanup(setattr, htmltext, "MAX_HTML", old)
            htmltext.MAX_HTML = 50
            self.assertEqual(build_data.html_kpis(d, E), {"html_last_spend": None, "html_coh_dedup": None})

    def test_untrusted_page_is_read_in_linear_time(self):
        import time, build_data
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            for html in ("<script>" * 100000 + "x", "<script>x</script>" * 100000 + "<p>y",
                         "<style>x</style>" * 50000 + "<script>x</script>" * 50000, "<" * 400000 + "x",
                         "<a" * 200000 + ">", "Past Month Spend " * 100000 + "$1 x",
                         "Total Dedup Savings / mo " * 100000 + "$1 x"):
                self.page(d, html)
                t = time.monotonic()
                build_data.html_kpis(d, E)
                self.assertLess(time.monotonic() - t, 2.0, html[:30])


class BedrockTags(unittest.TestCase):
    def test_empty_resource_summary(self):
        import bedrock_tags
        recs = [{"currentResourceType": "NatGateway", "currentResourceSummary": "", "_cost": 5.0, "_tags": {"app": "a"}},
                {"currentResourceType": "NatGateway", "currentResourceSummary": "  ", "_cost": 3.0, "_tags": {}},
                {"currentResourceType": "Ec2Instance", "currentResourceSummary": "", "_cost": 2.0, "_tags": {"app": "a"}},
                {"currentResourceType": "Ec2Instance", "currentResourceSummary": "g5.xlarge", "_cost": 7.0, "_tags": {"app": "a"}}]
        with mock.patch.object(bedrock_tags.C, "recs", return_value=recs):
            out = bedrock_tags.tags(E)
        self.assertEqual(out["gpu_instances"], [["g5.xlarge", 7]])
        self.assertEqual(out["keys"]["app"]["values"][0]["gpu_cost"], 7)

    def test_one_row_per_resource(self):
        import bedrock_tags
        arn = "arn:aws:ec2:ap-northeast-2:111111111111:instance/i-1"
        rec = lambda rid, a, cost: {"recommendationId": rid, "resourceArn": a, "currentResourceType": "Ec2Instance",  # noqa: E731
                                    "currentResourceSummary": "g5.xlarge", "_cost": cost, "_tags": {"app": "a"}}
        recs = [rec("r1", arn, 100.0), rec("r2", arn, 120.0), rec("r3", "", 10.0), rec("r4", "", 10.0)]
        with mock.patch.object(bedrock_tags.C, "recs", return_value=recs):
            out = bedrock_tags.tags(E)
        self.assertEqual((out["resources"], out["tagged"], out["cost"]), (3, 3, 140), "highest cost per ARN; no-ARN rows stand alone")
        v = out["keys"]["app"]["values"][0]
        self.assertEqual((v["n"], v["cost"], v["by_type"], v["gpu_cost"]), (3, 140, {"EC2": 140}, 140))
        self.assertEqual(out["keys"]["app"]["n"], 3)
        self.assertEqual(out["gpu_instances"], [["g5.xlarge", 140]])


class SpPurchase(unittest.TestCase):
    def test_compute_sp_lever_ignores_other_plan_types(self):
        import build_data
        rec = lambda t, s, h, p: {"savingsPlansType": t, "termInYears": "THREE_YEARS", "paymentOption": "ALL_UPFRONT",  # noqa: E731
                                  "estimatedMonthlySavingsAmount": s, "hourlyCommitmentToPurchase": h,
                                  "estimatedSavingsPercentage": p}
        rows = [rec("COMPUTE_SP", "100", "1.5", "20"), rec("EC2_INSTANCE_SP", "200", "3", "40")]
        self.assertEqual(build_data.sp_best(rows, "THREE_YEARS", "ALL_UPFRONT"),
                         {"monthly_savings": 100, "hourly": 1.5, "savings_pct": 20.0})
        # per-account Compute SP rows: one purchase, summed; the rate is savings over the covered on-demand cost
        rows.append(rec("COMPUTE_SP", "300", "2.5", "30"))
        self.assertEqual(build_data.sp_best(rows, "THREE_YEARS", "ALL_UPFRONT"),
                         {"monthly_savings": 400, "hourly": 4.0, "savings_pct": round(400 / (500 + 1000) * 100, 1)})
        self.assertIsNone(build_data.sp_best(rows[1:2], "THREE_YEARS", "ALL_UPFRONT"))
        self.assertIsNone(build_data.sp_best(rows, "ONE_YEAR", "NO_UPFRONT"))


class Sessions(unittest.TestCase):
    def test_lambda_dt_reads_month_columns(self):
        import build_data, sessions
        months = [None, None, None, None, "2026-08", "2026-09"]
        j = {"headers": ["keys", "2026-08", "2026-09", "Rank"],
             "rows": [["APN2-DataTransfer-Out-Bytes", 3, 4, 1], ["APN2-Request", 9, 9, 2]]}
        with mock.patch.object(build_data, "M", months), mock.patch.object(sessions, "hj", return_value=j):
            self.assertEqual(sessions.lambda_dt(E), [0, 0, 0, 0, 3, 4])


if __name__ == "__main__":
    unittest.main()
