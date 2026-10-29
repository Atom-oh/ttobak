"""Dataset scripts on edge-shaped inputs (empty summaries, missing sheets, NaN cells, mixed plan types, short tables,
mismatched periods). Synthetic data only."""
import csv, os, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))

from fileutil import jdump, jload  # noqa: E402

E = "acct-a"
COH_HDR = ["", "Report Name", "recommendationId", "accountId", "actionType", "currentResourceType", "region",
           "currentResourceSummary", "recommendedResourceSummary", "estimatedMonthlyCost", "estimatedMonthlySavings",
           "resourceArn", "tags"]


def coh_csv(path, rows):
    """rows: dicts over COH_HDR names (missing cells empty); a row given as a list is written as is."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(COH_HDR)
        for r in rows:
            w.writerow(r if isinstance(r, list) else [r.get(h, E if h == "Report Name" else "") for h in COH_HDR])


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


class Accounts(unittest.TestCase):
    def test_missing_linked_account_sheet(self):
        import accounts
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            self.assertEqual(accounts.linked_accounts(d, E), ({}, {}))
            with mock.patch.object(accounts, "ENTITIES", [E]):
                accounts.main(d, d)
            self.assertEqual(jload(os.path.join(d, "accounts.json")), [])


class TagServices(unittest.TestCase):
    def test_nan_cost_and_short_row(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [
                {"recommendationId": "r1", "currentResourceType": "Ec2Instance", "estimatedMonthlyCost": "nan", "tags": "nan"},
                {"recommendationId": "r2", "currentResourceType": "EbsVolume", "estimatedMonthlyCost": "4.5"},
                ["", E, "r3", "111111111111"]])  # a short trailing row
            rs = tag_services.resources(d, E)
            self.assertEqual([r["cost"] for r in rs], [0.0, 4.5, 0.0])
            with mock.patch.object(tag_services, "ENTITIES", [E]):
                tag_services.main(d, d)  # the reconciliation holds (no NaN)
            with open(os.path.join(d, "tag_services.json"), encoding="utf-8") as f:
                self.assertNotIn("NaN", f.read())

    def test_new_workload_needs_most_resources_recent(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            tg = lambda created=None: repr([{"key": "app", "value": "svc"}]  # noqa: E731
                                           + ([{"key": "x/created-at", "value": created}] if created else []))
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [
                {"recommendationId": "r%d" % i, "currentResourceType": "Ec2Instance", "estimatedMonthlyCost": "1",
                 "tags": tg("2026-09-01" if i == 0 else None)} for i in range(3)])
            with mock.patch.object(tag_services, "ENTITIES", [E]), mock.patch.object(tag_services, "NEW_SINCE", "2026-07"):
                tag_services.main(d, d)
            s = jload(os.path.join(d, "tag_services.json"), encoding="utf-8")[0]
            self.assertEqual(s["service"], "svc")
            self.assertEqual(s["created_recent_pct"], 33.3, "share of all the workload's resources")
            self.assertFalse(s["is_new"])


class Sessions(unittest.TestCase):
    def test_lambda_dt_reads_month_columns(self):
        import build_data, sessions
        months = [None, None, None, None, "2026-08", "2026-09"]
        j = {"headers": ["keys", "2026-08", "2026-09", "Rank"],
             "rows": [["APN2-DataTransfer-Out-Bytes", 3, 4, 1], ["APN2-Request", 9, 9, 2]]}
        with mock.patch.object(build_data, "M", months), mock.patch.object(sessions, "hj", return_value=j):
            self.assertEqual(sessions.lambda_dt(E), [0, 0, 0, 0, 3, 4])


class MergeDashboard(unittest.TestCase):
    def test_different_periods_refused(self):
        import merge_datasets
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as t:
            ins = []
            for e, m in (("acct-a", "2026-03"), ("acct-b", "2026-04")):
                p = os.path.join(t, e + ".json")
                jdump({"months": [m], "entities": {e: {"k": 1}}}, p, "w")
                ins.append(p)
            out = os.path.join(t, "all.json")
            with self.assertRaises(SystemExit) as cm:
                merge_datasets.merge_dashboard(out, ins)
            self.assertNotEqual(cm.exception.code, 0)
            self.assertFalse(os.path.exists(out))


if __name__ == "__main__":
    unittest.main()
