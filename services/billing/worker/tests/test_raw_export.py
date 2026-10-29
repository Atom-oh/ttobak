"""raw_export.py: date columns and COH sheets without a tags column. Synthetic data only."""
import csv, os, sys, tempfile, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import raw_export  # noqa: E402

E = "acct-a"


def sheet(base, name, hdr, rows):
    os.makedirs(os.path.join(base, E), exist_ok=True)
    with open(os.path.join(base, E, name + ".csv"), "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(hdr)
        w.writerows(rows)


class Export(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.base = self.tmp.name
        self.ents = mock.patch.object(raw_export, "ENTITIES", [E])
        self.ents.start()

    def tearDown(self):
        self.ents.stop()
        self.tmp.cleanup()

    def test_only_date_columns_become_dates(self):
        hdr = ["", "Report Name", "", "", "", "timePeriod.start", "keys", "metrics.AmortizedCost.amount", "End Date", "endDateTime",
               "currentAverageHourlyOnDemandSpend", "", "", "Spend After Savings"]
        sheet(self.base, "Data_SP_Inventory", hdr,
              [["", E, "", "", "", "46000", "['sp-1']", "10", "46100", "46200", "45000", "", "", "30000"]])
        rows, *_ = raw_export.export_sheet(self.base, "Data_SP_Inventory")
        r = rows[0]
        self.assertEqual(r["Spend After Savings"], 30000)  # an amount, not 1982-02-18
        self.assertEqual(r["currentAverageHourlyOnDemandSpend"], 45000)
        self.assertEqual(r["End Date"], "2026-03-19")
        self.assertEqual(r["endDateTime"], "2026-06-27")

    def test_coh_sheet_without_tags_column(self):
        hdr = ["", "Report Name", "", "", "", "recommendationId", "accountId", "estimatedMonthlyCost"]
        sheet(self.base, "Data_COH_All", hdr, [["", E, "", "", "", "rec-1", "100000000001", "12.5"]])
        rows, by_ent, skipped, dropped = raw_export.export_sheet(self.base, "Data_COH_All")
        self.assertEqual([r["recommendationId"] for r in rows], ["rec-1"])
        self.assertFalse(any(k.startswith("tag:") for k in rows[0]))
        self.assertEqual(dropped, {E: []})


if __name__ == "__main__":
    unittest.main()
