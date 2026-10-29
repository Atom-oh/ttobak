"""trend.py: the monthly pivot over a COT sheet. Synthetic data only."""
import os, sys, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import trend  # noqa: E402

MONTHS = ["2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10"]


class Pivot(unittest.TestCase):
    def test_rows_outside_the_period_are_left_out(self):
        # seven months on the sheet; the period is the latest six
        rows = [{"keys": "['AmazonEC2']", "timePeriod.start": m + "-01", "metrics.AmortizedCost.amount": str(10 * (i + 1))}
                for i, m in enumerate(["2026-04"] + MONTHS)]
        with mock.patch.object(trend, "MONTHS", MONTHS), mock.patch.object(trend.L, "load", return_value=rows):
            p = trend.pivot("acct-a", "Data_Service_Costs")
        self.assertEqual(p["AmazonEC2"], {m: 10.0 * (i + 2) for i, m in enumerate(MONTHS)})


if __name__ == "__main__":
    unittest.main()
