"""Dataset scripts on edge-shaped inputs (empty summaries, missing sheets, NaN cells, mixed plan types, short tables,
mismatched periods). Synthetic data only."""
import csv, json, os, subprocess, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))

from fileutil import jdump, jload  # noqa: E402

E = "acct-a"


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
