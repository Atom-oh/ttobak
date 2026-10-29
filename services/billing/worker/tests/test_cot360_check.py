"""cot360_check.py: dataset totals vs COT360 page totals, written to checks.json. Synthetic data only."""
import json, os, sys, tempfile, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import period  # noqa: E402
import cot360_check  # noqa: E402
import entities  # noqa: E402

MONTH = "2031-02"


from fileutil import wr, jload, jdump  # noqa: E402

def page(savings):
    return ("<html><body><script>var x=1;</script><div>Total Modernization Savings Est.</div><b>$130.00</b>"
            "<div>Potential Monthly Savings</div><b>$%s</b><div>Next SP Expiry</div><b>2031-08-01</b></body></html>" % savings)


class Cot360Check(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.data = os.path.join(self.tmp.name, "data")
        self.ex = os.path.join(self.tmp.name, "extracted")
        self.csv = os.path.join(self.tmp.name, "csv")
        os.makedirs(self.data)
        os.makedirs(os.path.join(self.ex, "acct-a"))
        os.makedirs(os.path.join(self.csv, "acct-a"))
        self.accounts_sheet = os.path.join(self.csv, "acct-a", "Data_Linked_Account_Costs.csv")
        wr(",Report Name,timePeriod.start,keys\n", self.accounts_sheet, "w", encoding="utf-8")
        tables = {
            "kpis": [{"entity": "acct-a", "last_spend": 1000.0}],
            "raw_service_costs": [{"entity": "acct-a", "month": MONTH, "cost": 600.0},
                                  {"entity": "acct-a", "month": MONTH, "cost": 400.0},
                                  {"entity": "acct-a", "month": "2031-01", "cost": 9.0}],
            "accounts": [{"entity": "acct-a", "last": 700.0}, {"entity": "acct-a", "last": 300.0}],
            "modernization": [{"entity": "acct-a", "est_savings": 100.0}],
            "elasticity": [{"entity": "acct-a", "month": MONTH, "savings_at_goal": 50.0},
                           {"entity": "acct-a", "month": "2031-01", "savings_at_goal": 7.0}],
            "storage_tiers": [{"entity": "acct-a", "month": MONTH, "service": "EBS", "tier": "gp2", "cost": 150.0},
                              {"entity": "acct-a", "month": MONTH, "service": "EBS", "tier": "gp3", "cost": 99.0}],
            "commitments": [{"entity": "acct-a", "kind": "SP", "end": "2031-08-01", "days_left": 100},
                            {"entity": "acct-a", "kind": "SP", "end": "2032-01-01", "days_left": 300},
                            {"entity": "acct-a", "kind": "SP", "end": "2030-01-01", "days_left": -5}],
        }
        for n, rows in tables.items():
            jdump(rows, os.path.join(self.data, n + ".json"), "w", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def run_check(self, savings, run_entities=()):
        wr(page(savings), os.path.join(self.ex, "acct-a", "COT_acct-a_COT360.html"), "w", encoding="utf-8")
        with mock.patch.object(period, "LAST", MONTH), mock.patch.object(entities, "ENTITIES", list(run_entities)):
            rc = cot360_check.main(self.data, self.ex, self.csv)
        return rc, jload(os.path.join(self.data, "checks.json"), encoding="utf-8")

    def test_match_returns_0(self):
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 0)
        self.assertEqual({r["name"] for r in rows},
                         {"raw_service_costs", "accounts", "modernization", "elasticity", "next SP expiry"})
        self.assertTrue(all(r["ok"] for r in rows))
        self.assertTrue(all(set(r) == {"entity", "name", "got", "want", "ok"} for r in rows))

    def test_mismatch_returns_1(self):
        rc, rows = self.run_check("55.00")
        self.assertEqual(rc, 1)
        self.assertFalse(next(r for r in rows if r["name"] == "elasticity")["ok"])
        self.assertTrue(all(r["ok"] for r in rows if r["name"] != "elasticity"))

    def test_page_is_read_in_linear_time(self):
        import time
        p = os.path.join(self.ex, "acct-a", "COT_acct-a_COT360.html")
        for html in ("<script>" * 100000 + "x", "<script>x</script>" * 100000, "<" * 400000 + "x"):
            wr(html, p, "w", encoding="utf-8")
            t = time.monotonic()
            cot360_check._page_lines(p)
            self.assertLess(time.monotonic() - t, 2.0, html[:30])

    def test_missing_label_is_a_mismatch_not_a_crash(self):
        wr(page("50.00").replace("Potential Monthly Savings", "Something else"),
           os.path.join(self.ex, "acct-a", "COT_acct-a_COT360.html"), "w", encoding="utf-8")
        with mock.patch.object(period, "LAST", MONTH), mock.patch.object(entities, "ENTITIES", []):
            rc = cot360_check.main(self.data, self.ex, self.csv)
        rows = jload(os.path.join(self.data, "checks.json"), encoding="utf-8")
        el = next(r for r in rows if r["name"] == "elasticity")
        self.assertEqual((rc, el["ok"], el["want"]), (1, False, None))

    def test_missing_figure_does_not_borrow_the_next_kpi(self):
        # modernization's own figure is missing: the next dollar line belongs to elasticity, and equals the
        # computed modernization total (130), so borrowing it would pass
        html = page("130.00").replace("<b>$130.00</b><div>Potential", "<div>Potential")
        wr(html, os.path.join(self.ex, "acct-a", "COT_acct-a_COT360.html"), "w", encoding="utf-8")
        with mock.patch.object(period, "LAST", MONTH), mock.patch.object(entities, "ENTITIES", []):
            rc = cot360_check.main(self.data, self.ex, self.csv)
        rows = {r["name"]: r for r in jload(os.path.join(self.data, "checks.json"), encoding="utf-8")}
        self.assertEqual((rc, rows["modernization"]["want"], rows["modernization"]["ok"]), (1, None, False))
        self.assertEqual(rows["elasticity"]["want"], 130.0)

    def test_missing_linked_account_sheet_is_not_a_mismatch(self):
        # the sheet is optional (build_data.optional_sheet): accounts.json is then empty for the entity
        os.remove(self.accounts_sheet)
        jdump([], os.path.join(self.data, "accounts.json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 0)
        acc = next(r for r in rows if r["name"] == "accounts")
        self.assertEqual((acc["got"], acc["ok"]), ("n/a", True))
        # with the sheet present, no account rows is a real shortfall
        wr(",Report Name,timePeriod.start,keys\n", self.accounts_sheet, "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 1)
        self.assertEqual([(r["name"], r["got"]) for r in rows if not r["ok"]], [("accounts", 0)])

    def test_oversized_page_is_a_mismatch(self):
        import htmltext
        self.addCleanup(setattr, htmltext, "MAX_HTML", htmltext.MAX_HTML)
        htmltext.MAX_HTML = 10
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 1)
        self.assertFalse(any(r["ok"] for r in rows if r["name"] in ("modernization", "elasticity", "next SP expiry")))

    def test_null_or_missing_raw_cost_counts_as_zero(self):
        # raw_export writes null for a nan/inf cost cell and omits an empty cost or month cell
        raw = jload(os.path.join(self.data, "raw_service_costs.json"), encoding="utf-8")
        extra = [{"entity": "acct-a", "month": MONTH, "cost": None}, {"entity": "acct-a", "month": MONTH},
                 {"entity": "acct-a", "cost": 5.0}]
        jdump(raw + extra, os.path.join(self.data, "raw_service_costs.json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 0)
        self.assertEqual(next(r for r in rows if r["name"] == "raw_service_costs")["got"], 1000.0)
        # a real shortfall stays a visible mismatch
        jdump([{"entity": "acct-a", "month": MONTH, "cost": None}, {"entity": "acct-a", "month": MONTH, "cost": 600.0}],
              os.path.join(self.data, "raw_service_costs.json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 1)
        bad = [r for r in rows if not r["ok"]]
        self.assertEqual([(r["name"], r["got"]) for r in bad], [("raw_service_costs", 600.0)])

    def test_entity_without_kpis_fails(self):
        # an input entity (raw costs, or a run entity) with no KPI row must not pass with no checks
        jdump([], os.path.join(self.data, "kpis.json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 1)
        self.assertEqual([(r["entity"], r["name"], r["ok"]) for r in rows], [("acct-a", "kpis", False)])
        jdump([{"entity": "acct-a", "last_spend": 1000.0}], os.path.join(self.data, "kpis.json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00", run_entities=["acct-a", "acct-b"])
        self.assertEqual(rc, 1)
        self.assertEqual([(r["entity"], r["ok"]) for r in rows if r["name"] == "kpis"], [("acct-b", False)])
        self.assertTrue(all(r["ok"] for r in rows if r["name"] != "kpis"))

    def test_no_entities_at_all_fails(self):
        for n in ("kpis", "raw_service_costs", "accounts"):
            jdump([], os.path.join(self.data, n + ".json"), "w", encoding="utf-8")
        rc, rows = self.run_check("50.00")
        self.assertEqual(rc, 1)
        self.assertEqual([(r["name"], r["ok"]) for r in rows], [("kpis", False)])


if __name__ == "__main__":
    unittest.main()
