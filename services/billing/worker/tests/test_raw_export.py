"""raw_export.py: date columns and COH sheets without a tags column. Synthetic data only."""
import csv, json, os, sys, tempfile, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import raw_export  # noqa: E402

E = "acct-a"


def sheet(base, name, hdr, rows, entity=E):
    os.makedirs(os.path.join(base, entity), exist_ok=True)
    with open(os.path.join(base, entity, name + ".csv"), "w", newline="", encoding="utf-8") as f:
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

    def test_non_finite_cells_export_as_strict_json_null(self):
        # COT exports carry literal "nan" cells; bare NaN/Infinity in the JSON breaks a strict parser
        hdr = ["", "Report Name", "", "", "", "timePeriod.start", "keys", "metrics.AmortizedCost.amount",
               "metrics.UsageQuantity.amount", "ratio"]
        sheet(self.base, "Data_Service_Costs", hdr,
              [["", E, "", "", "", "46000", "['svc-a']", "nan", "inf", "-Infinity"],
               ["", E, "", "", "", "46000", "['svc-b']", "2", "3", "0.5"]])
        out = os.path.join(self.base, "out")
        os.makedirs(out)
        with mock.patch("builtins.print"):
            raw_export.main(self.base, out)

        def strict(c):
            raise ValueError("non-finite constant " + c)
        with open(os.path.join(out, "raw_service_costs.json"), encoding="utf-8") as f:
            rows = json.loads(f.read(), parse_constant=strict)
        self.assertEqual([(r["cost"], r["usage"], r["ratio"]) for r in rows], [(None, None, None), (2, 3, 0.5)])
        with open(os.path.join(out, "raw_catalog.json"), encoding="utf-8") as f:
            cols = {c["name"]: c["type"] for c in json.loads(f.read(), parse_constant=strict)[0]["columns"]}
        self.assertEqual((cols["cost"], cols["usage"], cols["ratio"]), ("num", "num", "num"))


    def test_sheets_are_discovered_across_all_entities(self):
        # the first entity lacks a COH sheet (COH off) and the third has no directory at all
        e2 = "acct-b"
        hdr = ["", "Report Name", "", "", "", "timePeriod.start", "keys", "metrics.AmortizedCost.amount"]
        sheet(self.base, "Data_Service_Costs", hdr, [["", E, "", "", "", "46000", "['svc-a']", "2"]])
        coh = ["", "Report Name", "", "", "", "recommendationId", "accountId", "estimatedMonthlyCost"]
        sheet(self.base, "Data_COH_All", coh, [["", e2, "", "", "", "rec-1", "100000000001", "12.5"]], entity=e2)
        out = os.path.join(self.base, "out")
        os.makedirs(out)
        with mock.patch.object(raw_export, "ENTITIES", [E, e2, "acct-c"]), mock.patch("builtins.print"):
            raw_export.main(self.base, out)
        with open(os.path.join(out, "raw_catalog.json"), encoding="utf-8") as f:
            cat = {c["id"]: c["rows_by_entity"] for c in json.load(f)}
        self.assertEqual(cat, {"raw_coh_all": {e2: 1}, "raw_service_costs": {E: 1}})

    def test_no_entity_directory_fails_closed(self):
        with self.assertRaises(SystemExit):
            raw_export.main(self.base, self.base)


if __name__ == "__main__":
    unittest.main()
