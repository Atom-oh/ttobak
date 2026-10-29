"""Account, tag-service and COT360 metric scripts on edge-shaped inputs (short COH rows, NaN cells, dashboard merge).
Synthetic data only."""
import collections, csv, json, os, subprocess, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))

from fileutil import jdump, jload  # noqa: E402
from test_script_edges import E  # noqa: E402 (shared account id)
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


class Accounts(unittest.TestCase):
    def test_missing_linked_account_sheet(self):
        import accounts
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            self.assertEqual(accounts.linked_accounts(d, E), ({}, {}))
            with mock.patch.object(accounts, "ENTITIES", [E]):
                accounts.main(d, d)
            self.assertEqual(jload(os.path.join(d, "accounts.json")), [])


    def test_linked_account_short_rows(self):
        import accounts
        hdr = ["", "Report Name", "timePeriod.start", "keys", "metrics.AmortizedCost.amount", "Linked Account", "Name"]
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_Linked_Account_Costs.csv"), "w", newline="", encoding="utf-8") as f:
                w = csv.writer(f)
                w.writerow(hdr)
                w.writerow(["", E, "2026-09-01", "['111111111111']", "5", "['111111111111']", "dev"])
                w.writerow(["", E, "2026-09-01", "['222222222222']"])  # cut after keys: no cost cell
                w.writerow(["", E, "2026-09-01", "['111111111111']", "2", "['111111111111']"])  # no name cell
            with mock.patch.object(accounts, "PRESENT", ["2026-09"]):
                cost, names = accounts.linked_accounts(d, E)
        self.assertEqual({k: dict(v) for k, v in cost.items()},
                         {"111111111111": {"2026-09": 7.0}, "222222222222": {"2026-09": 0.0}})
        self.assertEqual(names, {"111111111111": "dev"})


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

    def test_placeholder_creation_tags_do_not_make_a_workload_new(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [
                {"recommendationId": "r%d" % i, "currentResourceType": "Ec2Instance", "actionType": "Rightsize",
                 "resourceArn": "arn:p%d" % i, "estimatedMonthlyCost": "100",
                 "tags": repr([{"key": "app", "value": "svc"}, {"key": "CreatedAt", "value": v}])}
                for i, v in enumerate(["unknown", "N/A", "TBD", "none"])])
            with mock.patch.object(tag_services, "ENTITIES", [E]), mock.patch.object(tag_services, "NEW_SINCE", "2026-06"):
                tag_services.main(d, d)
            svc = next(s for s in jload(os.path.join(d, "tag_services.json"), encoding="utf-8") if s["service"] == "svc")
            self.assertFalse(svc["is_new"])
            self.assertIsNone(svc["created_recent_pct"])

    ODD_TAGS = ["{'key': 'app', 'value': 'a'}", "7", "None", "[1, 'x', None]", "[{'value': 'v'}]", "[{'key': 'app'}]",
                "[{'key': 3, 'value': 'v'}]", "[{'key': 'app', 'value': 5}]", "[{'key': 'app'", "{'a': [1, 2]",
                "[" * 5000 + "]" * 5000]

    def odd_coh(self, d):
        rows = [{"recommendationId": "r%d" % i, "currentResourceType": "Ec2Instance", "actionType": "Rightsize",
                 "resourceArn": "arn:x%d" % i, "estimatedMonthlyCost": "1", "tags": t} for i, t in enumerate(self.ODD_TAGS)]
        rows.append({"recommendationId": "ok", "currentResourceType": "Ec2Instance", "actionType": "Rightsize",
                     "resourceArn": "arn:ok", "estimatedMonthlyCost": "2",
                     "tags": repr([{"key": "app", "value": "svc"}, {"key": "bad"}, "junk"])})
        coh_csv(os.path.join(d, E, "Data_COH_All.csv"), rows)

    def test_odd_tag_cells_are_untagged(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            self.odd_coh(d)
            rs = tag_services.resources(d, E)
            self.assertEqual([r["tags"] for r in rs], [{}] * len(self.ODD_TAGS) + [{"app": "svc"}],
                             "a non-list cell is untagged; an odd item is skipped")
            with mock.patch.object(tag_services, "ENTITIES", [E]):
                tag_services.main(d, d)
            got = {s["service"]: s["resources"] for s in jload(os.path.join(d, "tag_services.json"), encoding="utf-8")}
            self.assertEqual(got, {"svc": 1, "(태그 없음)": len(self.ODD_TAGS)})

    def test_odd_tag_cells_in_coh_by_workload(self):
        import cot360_metrics
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            self.odd_coh(d)
            got = {r["workload"]: r["recs"] for r in cot360_metrics.coh_by_workload(d, E)}
            self.assertEqual(got, {"svc": 1, "(태그 없음)": len(self.ODD_TAGS)})

    def test_odd_tag_cells_in_account_resources(self):
        import accounts
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            self.odd_coh(d)
            got = collections.Counter(r["tag_service"] for r in accounts.coh_resources(d, E))
            self.assertEqual(got, {"svc": 1, "—": len(self.ODD_TAGS)}, "the tag_services parser: no crash, odd items skipped")

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

    def test_one_row_per_resource(self):
        import accounts, tag_services
        tg = repr([{"key": "app", "value": "svc"}, {"key": "x/created-at", "value": "2026-09-01"}])
        rec = lambda rid, arn, cost, action="Rightsize": {  # noqa: E731
            "recommendationId": rid, "accountId": "111111111111", "actionType": action, "currentResourceType": "Ec2Instance",
            "currentResourceSummary": "m5.large", "resourceArn": arn, "estimatedMonthlyCost": cost, "tags": tg}
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [
                rec("r1", "arn:a", "10"), rec("r2", "arn:a", "30", "MigrateToGraviton"), rec("r3", "arn:a", "20", "Stop"),
                rec("r4", "arn:b", "5"), rec("r5", "", "1"), rec("r6", "", "2")])  # no ARN: each recommendation counts
            self.assertEqual(sorted(r["cost"] for r in tag_services.resources(d, E)), [1.0, 2.0, 5.0, 30.0])
            with mock.patch.object(tag_services, "ENTITIES", [E]), mock.patch.object(tag_services, "NEW_SINCE", "2026-07"):
                tag_services.main(d, d)
            s = jload(os.path.join(d, "tag_services.json"), encoding="utf-8")[0]
            self.assertEqual((s["service"], s["resources"], s["est_monthly_cost"], s["created_recent_pct"], s["is_new"],
                              s["top_instance_types"]), ("svc", 4, 38.0, 100.0, True, ["m5.large×4"]))
            # the same resources and cost as account_resources for that service
            mine = [r for r in accounts.coh_resources(d, E) if r["tag_service"] == "svc"]
            self.assertEqual((len(mine), sum(r["cost"] for r in mine)), (4, 38.0))
            keys = jload(os.path.join(d, "tag_keys.json"), encoding="utf-8")
            self.assertEqual({k["tag_key"]: k["resources"] for k in keys}, {"app": 4, "x/created-at": 4})

    def test_no_resource_arn_column(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_COH_All.csv"), "w", newline="", encoding="utf-8") as f:
                csv.writer(f).writerows([["", "Report Name", "recommendationId", "estimatedMonthlyCost"],
                                         ["", E, "r1", "3"], ["", E, "r2", "4"]])
            self.assertEqual([r["cost"] for r in tag_services.resources(d, E)], [3.0, 4.0])

    def test_account_names_short_row(self):
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_Linked_Account_Costs.csv"), "w", newline="", encoding="utf-8") as f:
                csv.writer(f).writerows([["", "Report Name", "keys", "Linked Account", "Name"],
                                         ["", E, "x", "111111111111", "acct one"],
                                         ["", E, "x", "222222222222"]])  # trailing empty Name cell dropped
            self.assertEqual(tag_services.account_names(d, E), {"111111111111": "acct one"})


# xlsx2csv pads a row only to its last present cell: an untagged resource's row ends before the tags column
SHORT_COH = ["", E, "r1", "111111111111", "Rightsize", "Ec2Instance", "", "m5.large", "m5.small", "10", "2", "arn:r1"]


class ShortCohRows(unittest.TestCase):
    def test_coh_resources_short_row(self):
        import accounts
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [SHORT_COH])
            rs = accounts.coh_resources(d, E)
        self.assertEqual([(r["account"], r["instance_type"], r["region"], r["cost"]) for r in rs],
                         [("111111111111", "m5.large", "—", 10.0)])

    def test_coh_by_workload_short_row(self):
        import cot360_metrics
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [SHORT_COH])
            rs = cot360_metrics.coh_by_workload(d, E)
        self.assertEqual([(r["recs"], r["est_savings"], r["est_cost"], r["top_changes"]) for r in rs],
                         [(1, 2.0, 10.0, ["m5.large→m5.small×1"])])

    def produce(self, hdr, rows):
        import accounts, cot360_metrics
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_COH_All.csv"), "w", newline="", encoding="utf-8") as f:
                csv.writer(f).writerows([hdr] + rows)
            return accounts.coh_resources(d, E), cot360_metrics.coh_by_workload(d, E)

    def test_export_without_a_tags_column(self):
        # an older or trimmed export has no tags column (raw_export and tag_services accept it too)
        res, wl = self.produce(COH_HDR[:-1], [SHORT_COH])
        self.assertEqual([(r["account"], r["tag_service"], r["cost"]) for r in res], [("111111111111", "—", 10.0)])
        self.assertEqual([(r["recs"], r["est_cost"]) for r in wl], [(1, 10.0)])

    def test_row_cut_before_the_resource_arn(self):
        # an ARN-less recommendation without tags ends after its savings cell: it still counts
        res, wl = self.produce(COH_HDR, [SHORT_COH[:-1]])
        self.assertEqual([(r["account"], r["cost"]) for r in res], [("111111111111", 10.0)])
        self.assertEqual([(r["recs"], r["est_savings"], r["est_cost"]) for r in wl], [(1, 2.0, 10.0)])


def strict_json(path):
    """Parse as the BFF's JSON.parse would: NaN/Infinity tokens are an error."""
    def bad(tok):
        raise ValueError("non-finite JSON token " + tok)
    with open(path, encoding="utf-8") as f:
        return json.load(f, parse_constant=bad)


def run_script(name, *args, months="2026-08,2026-09"):
    env = {k: v for k, v in os.environ.items() if not k.startswith("COT_")}
    env.update(COT_MONTHS=months, COT_ENTITIES=E)
    r = subprocess.run([sys.executable, "-I", os.path.join(HERE, "..", "scripts", name), *args], env=env,
                       capture_output=True, text=True, timeout=120)
    assert r.returncode == 0, (name, r.stderr[-2000:])


class NonFiniteCells(unittest.TestCase):
    """COT cells can read nan/inf: a dataset never carries NaN/Infinity (invalid JSON for the BFF)."""
    USAGE_HDR = ["", "Report Name", "timePeriod.start", "keys", "metrics.AmortizedCost.amount",
                 "metrics.UsageQuantity.amount", "x", "y"]

    def test_cell_parsers_are_finite(self):
        import accounts, build_data, cot360_metrics, cotload, ec2_types
        for f in (accounts.num, build_data.fl, cot360_metrics.num, cotload.num, ec2_types.num):
            self.assertEqual([f(v) for v in ("nan", "inf", "-inf", "NaN", "2.5", "", None)], [0.0] * 4 + [2.5, 0.0, 0.0])

    def test_ec2_types(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_On_Demand_Usage.csv"), "w", newline="", encoding="utf-8") as f:
                w = csv.writer(f)
                w.writerow(self.USAGE_HDR)
                w.writerow(["", E, "2026-08-01", "['m5.large']", "nan", "inf", "", ""])
                w.writerow(["", E, "2026-09-01", "['m5.large']", "10", "nan", "", ""])
                w.writerow(["", E, "2026-09-01", "['m5.large']", "inf", "5", "", ""])
            run_script("ec2_types.py", d, d)
            types = strict_json(os.path.join(d, "ec2_types.json"))
            self.assertEqual([(r["month"], r["cost"], r["hours"]) for r in types], [("2026-08", 0.0, 0.0), ("2026-09", 10.0, 5.0)])
            self.assertEqual(strict_json(os.path.join(d, "ec2_type_growth.json"))[0]["last"], 10.0)
            self.assertEqual([r["total"] for r in strict_json(os.path.join(d, "ec2_mix.json"))], [0.0, 10.0])

    def test_accounts(self):
        hdr = ["", "Report Name", "timePeriod.start", "keys", "metrics.AmortizedCost.amount", "Linked Account", "Name"]
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E))
            with open(os.path.join(d, E, "Data_Linked_Account_Costs.csv"), "w", newline="", encoding="utf-8") as f:
                w = csv.writer(f)
                w.writerow(hdr)
                w.writerow(["", E, "2026-08-01", "['111111111111']", "inf", "['111111111111']", "dev"])
                w.writerow(["", E, "2026-09-01", "['111111111111']", "7", "['111111111111']", "dev"])
            coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [
                {"recommendationId": "r1", "accountId": "111111111111", "actionType": "Rightsize",
                 "currentResourceType": "Ec2Instance", "resourceArn": "arn:a", "estimatedMonthlyCost": "nan"}])
            run_script("accounts.py", d, d)
            a = strict_json(os.path.join(d, "accounts.json"))
            self.assertEqual([(r["m5"], r["m6"], r["coh_est_monthly"]) for r in a], [(0.0, 7.0, 0.0)])
            self.assertEqual(strict_json(os.path.join(d, "account_resources.json"))[0]["est_monthly_cost"], 0.0)

    def test_cot360_unit_metrics(self):
        import cot360_metrics
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            os.makedirs(os.path.join(d, E, "html"))
            jdump({"headers": ["Month", "Cost ($)", "Instance Hours", "vCPU Hours", "Cost/Instance-Hr", "Cost/vCPU-Hr",
                               "Coverage %", "Graviton/Latest %"],
                   "rows": [["2026-09", "nan", "inf", 4, float("nan"), 1, "-inf", 5]]},
                  os.path.join(d, E, "html", "json_um_ec2_1.json"), "w")
            rows = cot360_metrics.unit_metrics(d, E)
        self.assertEqual([(r["cost"], r["instance_hours"], r["cost_per_inst_hr"], r["coverage_pct"]) for r in rows],
                         [(0.0, 0.0, 0.0, 0.0)])
        json.dumps(rows, allow_nan=False)


class ShortPeriodGrowth(unittest.TestCase):
    """Without an earlier-half month there is no baseline: growth fields are null, never a change from $0."""
    LA_HDR = ["", "Report Name", "timePeriod.start", "keys", "metrics.AmortizedCost.amount", "Linked Account", "Name"]

    def produce(self, months):
        d = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        self.addCleanup(__import__("shutil").rmtree, d)
        os.makedirs(os.path.join(d, E))
        with open(os.path.join(d, E, "Data_On_Demand_Usage.csv"), "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow(NonFiniteCells.USAGE_HDR)
            for m in months:
                w.writerow(["", E, m + "-01", "['m5.large']", "5000", "100", "", ""])
        with open(os.path.join(d, E, "Data_Linked_Account_Costs.csv"), "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow(self.LA_HDR)
            for m in months:
                w.writerow(["", E, m + "-01", "['111111111111']", "5000", "['111111111111']", "dev"])
        coh_csv(os.path.join(d, E, "Data_COH_All.csv"), [])
        run_script("ec2_types.py", d, d, months=",".join(months))
        run_script("accounts.py", d, d, months=",".join(months))
        return strict_json(os.path.join(d, "ec2_type_growth.json"))[0], strict_json(os.path.join(d, "accounts.json"))[0]

    def test_three_months_have_no_change(self):
        g, a = self.produce(["2026-07", "2026-08", "2026-09"])
        for r in (g, a):
            self.assertEqual((r["h1_avg"], r["d_h2_h1"], r["pct_h2_h1"], r["h2_avg"]), (None, None, None, 5000.0))
        self.assertNotEqual(a["kind"], "급증")

    def test_six_months_flat_is_zero_change(self):
        g, a = self.produce(["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"])
        for r in (g, a):
            self.assertEqual((r["h1_avg"], r["d_h2_h1"], r["pct_h2_h1"]), (5000.0, 0.0, 0.0))
        self.assertEqual(a["kind"], "유지")


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
