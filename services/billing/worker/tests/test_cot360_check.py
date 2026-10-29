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
        os.makedirs(self.data)
        os.makedirs(os.path.join(self.ex, "acct-a"))
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
            rc = cot360_check.main(self.data, self.ex)
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
