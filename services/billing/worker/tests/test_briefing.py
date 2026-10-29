"""briefing.py: expiry title scope, Graviton/SP overlap line, stable action ids. Synthetic data only."""
import json, os, re, sys, tempfile, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import briefing  # noqa: E402

E = "acct-a"


from fileutil import jload, jdump  # noqa: E402

def commit(kind, days, hourly, net=None, count=None, detail="Any", typ="ComputeSavingsPlans"):
    return {"entity": E, "kind": kind, "type": typ, "account": "", "account_name": "acct-a", "detail": detail, "region": "",
            "hourly": hourly, "count": count, "term": "", "payment": "", "start": "2025-01-01", "end": "2026-12-01",
            "days_left": days, "utilization_pct": None, "monthly_net_savings": net, "monthly_commit": round(hourly * 730, 2)}


def datasets(commitments=None, sp=True, grav=True):
    um = [{"entity": E, "service": "EC2", "month": m, "cost": 50000, "instance_hours": 1, "vcpu_hours": 1, "cost_per_inst_hr": 1,
           "cost_per_vcpu_hr": 0.04, "coverage_pct": 60.0, "modern_pct": 5.0} for m in ("2026-04", "2026-09")]
    mix = [{"entity": E, "month": m, "total": 50000, "by_arch": {"graviton": 2500}, "by_purchase": {"OD": 20000}} for m in ("2026-04", "2026-09")]
    return {
        "kpis": [{"entity": E, "entity_ko": "한빛금융", "last_spend": 100000, "prev_spend": 99000, "mom_pct": 1.0, "top_service": briefing.EC2,
                  "top_service_last": 50000, "gpu_ec2_last": 0, "bedrock_last": 0, "bedrock_top_model": "", "coh_dedup": 0}],
        "services": [{"entity": E, "service": briefing.EC2, "m1": 50000, "m5": 49000, "m6": 50000}],
        "period": [{"entity": E, "source": "cot", "months": ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]}],
        "ec2_mix": mix, "ec2_type_growth": [], "accounts": [], "focus": [], "unit_metrics": um,
        "modernization": [{"entity": E, "kind": "Graviton", "instance_type": "c7i.large", "family": "c7i", "target": "c8g",
                           "path": "Immediately Migratable", "service": "EC2", "monthly_cost": 20000, "est_savings": 5000}] if grav else [],
        "elasticity": [], "commitments": commitments or [],
        "sp_purchase": [{"entity": E, "plan": "COMPUTE_SP", "term": "ONE_YEAR", "payment": "NO_UPFRONT", "hourly_commit": 10,
                         "monthly_savings": 3000, "upfront": 0, "od_hourly": 30}] if sp else [],
        "storage_tiers": [], "coh_actions": [], "coh_by_workload": [],
    }


def run_main(ds):
    with tempfile.TemporaryDirectory() as d:
        for n, rows in ds.items():
            jdump(rows, os.path.join(d, n + ".json"), "w", encoding="utf-8")
        briefing.main(d)
        return jload(os.path.join(d, "briefing.json"), encoding="utf-8")


def D_of(ds):
    return {n: briefing.by_entity(rows) for n, rows in ds.items()}


class ExpiryTitle(unittest.TestCase):
    def test_sp_and_ri_named_separately(self):
        ds = datasets([commit("SP", 30, 2.0, net=1500.0), commit("RI", 40, 1.0, count=4, detail="db.r6g.large Aurora", typ="RDS")])
        a = briefing.expiry_action(E, D_of(ds))
        self.assertIn("SP 미갱신 시 월 $1.5K 증가", a["title"])
        self.assertIn("RI 1건(4대, 월 약정액 $730)", a["title"])

    def test_ri_only_uses_commitment(self):
        ds = datasets([commit("RI", 40, 1.0, count=4, detail="db.r6g.large Aurora", typ="RDS")])
        a = briefing.expiry_action(E, D_of(ds))
        self.assertNotIn("SP 미갱신", a["title"])
        self.assertIn("월 약정액 $730", a["title"])


class Overlap(unittest.TestCase):
    def test_line_on_both_when_both_exist(self):
        rows = run_main(datasets())
        sp = next(r for r in rows if r["title"].startswith("Compute Savings Plans"))
        gr = next(r for r in rows if r["title"].startswith("Graviton"))
        for r in (sp, gr):
            self.assertTrue(any("합산하지 않습니다" in x for x in r["evidence"]), r["evidence"])

    def test_no_line_for_rds_only_graviton(self):
        ds = datasets()
        ds["modernization"][0]["service"] = "RDS"
        rows = run_main(ds)
        gr = next(r for r in rows if r["title"].startswith("Graviton"))
        self.assertFalse(any("합산하지 않습니다" in x for x in gr["evidence"]))

    def test_mixed_services_names_ec2_part_only(self):
        ds = datasets()
        ds["modernization"].append({**ds["modernization"][0], "service": "RDS", "family": "r6i", "est_savings": 7000})
        rows = run_main(ds)
        gr = next(r for r in rows if r["title"].startswith("Graviton"))
        line = next(x for x in gr["evidence"] if "합산하지 않습니다" in x)
        self.assertIn("EC2 부분(월 $5.0K)", line)

    def test_sp_amount_shown_below_100(self):
        ds = datasets([commit("SP", 30, 0.1, net=40.0), commit("RI", 40, 1.0, count=4, detail="db.r6g.large Aurora", typ="RDS")])
        a = briefing.expiry_action(E, D_of(ds))
        self.assertIn("SP 미갱신 시 월 $40 증가", a["title"])

    def test_no_line_when_only_one(self):
        rows = run_main(datasets(sp=False))
        gr = next(r for r in rows if r["title"].startswith("Graviton"))
        self.assertFalse(any("합산하지 않습니다" in x for x in gr["evidence"]))


class ZeroSpend(unittest.TestCase):
    def test_zero_latest_spend_keeps_the_run(self):
        ds = datasets()
        ds["kpis"][0].update(last_spend=0, prev_spend=1000, mom_pct=-100.0, top_service="Amazon CloudFront", top_service_last=0)
        with tempfile.TemporaryDirectory() as d:
            for n, rows in ds.items():
                jdump(rows, os.path.join(d, n + ".json"), "w", encoding="utf-8")
            briefing.main(d)
            brief, bench = (jload(os.path.join(d, n + ".json"), encoding="utf-8") for n in ("briefing", "benchmark"))
        self.assertTrue(brief)  # the other actions are still written
        self.assertFalse(any(r["kind"] == "가격" for r in brief))  # no concentration share without spend
        self.assertIsNone(bench[0]["top_share"])


class Ids(unittest.TestCase):
    def test_id_format_and_sensitivity(self):
        a = run_main(datasets())
        self.assertTrue(all(re.fullmatch(r"[0-9a-f]{12}", r["id"]) for r in a))
        b = run_main(datasets(commitments=[commit("SP", 30, 2.0, net=1500.0)]))
        ga = next(r for r in a if r["title"].startswith("Graviton"))
        gb = next(r for r in b if r["title"].startswith("Graviton"))
        self.assertEqual(ga["id"], gb["id"])  # unchanged action keeps its id while other actions change
        spa = next(r for r in a if r["title"].startswith("Compute"))
        spb = next(r for r in b if r["title"].startswith("Compute"))
        self.assertNotEqual(spa["id"], spb["id"])  # evidence gained the renewal line

    def test_id_ignores_rank(self):
        row = {"entity": E, "kind": "절감", "title": "t", "evidence": ["x"], "impact": 1}
        self.assertEqual(briefing.action_id({**row, "rank": 1}), briefing.action_id({**row, "rank": 9}))


if __name__ == "__main__":
    unittest.main()
