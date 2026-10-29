"""COT360 computed metrics (the report's own JSON blocks) -> dashboard datasets, so every number the
COT360 page shows is also on the dashboard, plus a COH-by-workload view the report does not have.

Usage: python3 -I cot360_metrics.py <out dir with <Entity>/html/json_*.json and <Entity>/Data_COH_All.csv> <dashboard data dir> <as_of YYYY-MM-DD>

Writes unit_metrics, modernization, elasticity, commitments, sp_purchase, storage_tiers, coh_actions,
coh_by_workload (.json). as_of is the COT run date; expiry windows count from it.
"""
import ast, collections, csv, datetime, glob, json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
from entities import ENTITIES  # noqa: E402
import period as P  # noqa: E402
from tag_services import service_of, precedence_for  # noqa: E402

UNIT = {"um_ec2": "EC2", "um_rds": "RDS", "um_elasticache": "ElastiCache", "um_opensearch": "OpenSearch"}
# COT360 writes months as "September 2026": map every month of the period's years (and the year around them)
_YEARS = sorted({int(m[:4]) + d for m in P.PRESENT for d in (-1, 0, 1)}) or [datetime.date.today().year]
MONTH_NAME = {datetime.date(y, m, 1).strftime("%B %Y"): f"{y}-{m:02d}" for y in _YEARS for m in range(1, 13)}
WEEKEND_DAYS = 8
LOOKBACK_MONTHS = 6  # SP inventory savings/OD equivalent cover the 6-month report window
ACTION_KO = {"MigrateToGraviton": "Graviton 전환", "PurchaseSavingsPlans": "Savings Plans 구매", "PurchaseReservedInstances": "RI 구매",
             "Rightsize": "라이트사이징", "Delete": "유휴 리소스 삭제", "Stop": "유휴 리소스 중지", "Upgrade": "세대 업그레이드", "ScaleIn": "스케일 인"}


def block(base, e, prefix):
    """The json_<prefix>[_<n>].json block of an entity's COT360 page, or None when the page has none."""
    for fn in sorted(glob.glob(os.path.join(base, e, "html", f"json_{prefix}*.json"))):
        if re.fullmatch(rf"json_{re.escape(prefix)}(_\d+)?\.json", os.path.basename(fn)):
            j = json.load(open(fn, encoding="utf-8"))
            return j["headers"], j["rows"]
    return None


def rows_of(base, e, prefix):
    b = block(base, e, prefix)
    return [dict(zip(b[0], r)) for r in b[1]] if b else []


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return 0.0


def unit_metrics(base, e):
    out = []
    for prefix, svc in UNIT.items():
        for r in rows_of(base, e, prefix):
            out.append({"entity": e, "service": svc, "month": r["Month"], "cost": round(num(r["Cost ($)"]), 2),
                        "instance_hours": num(r["Instance Hours"]), "vcpu_hours": num(r["vCPU Hours"]),
                        "cost_per_inst_hr": num(r["Cost/Instance-Hr"]), "cost_per_vcpu_hr": num(r["Cost/vCPU-Hr"]),
                        "coverage_pct": num(r["Coverage %"]), "modern_pct": num(r["Graviton/Latest %"])})
    return out


def modernization(base, e):
    out = []
    for r in rows_of(base, e, "shared_Graviton_Migration_Opportunities_Full"):
        out.append({"entity": e, "kind": "Graviton", "instance_type": r["Instance Type"], "family": r["Current Family"],
                    "target": r["Graviton Equivalent"], "path": r["Migration Path"], "service": r["Service"],
                    "monthly_cost": round(num(r["Monthly Cost"]), 2),
                    "est_savings": round(num(r["Est. Savings (25%, excl. committed)"]), 2)})
    for r in rows_of(base, e, "shared_Previous_Generation_Instances_Full"):
        out.append({"entity": e, "kind": "구세대", "instance_type": r["Instance Type"], "family": r["Current Family"],
                    "target": r["Latest Instance Family"] or "—", "path": "Upgrade", "service": r["Service"],
                    "monthly_cost": round(num(r["Monthly Cost"]), 2), "est_savings": round(num(r["Est. Savings (15%)"]), 2)})
    return out


def elasticity(base, e):
    out = []
    for prefix, svc in (("elast_ec2", "EC2"), ("elast_rds", "RDS")):
        for r in rows_of(base, e, prefix):
            out.append({"entity": e, "service": svc, "month": MONTH_NAME.get(r["Month"], r["Month"]),
                        "weekday_avg": round(num(r["Weekday Avg"]), 2), "weekend_avg": round(num(r["Weekend Avg"]), 2),
                        # COT360's Savings column is per weekend day; the report's monthly figure is × 8 weekend days
                        "elasticity_pct": num(r["Elasticity %"]), "savings_at_goal": round(num(r["Savings"]) * WEEKEND_DAYS, 2)})
    return out


def commitments(base, e, as_of):
    out = []
    for r in rows_of(base, e, "shared_SP_Inventory_Raw"):
        end = str(r["End Date"])[:10]
        out.append({"entity": e, "kind": "SP", "type": r["SP Type"], "account": str(r["Account ID"]), "account_name": r["Account Name"],
                    "detail": r["Instance Family"] or "Any", "region": r["Region"], "hourly": num(r["Hourly Commitment"]),
                    "count": None, "term": r["Term"], "payment": r["Payment"], "start": str(r["Start Date"])[:10], "end": end,
                    "days_left": (datetime.date.fromisoformat(end) - as_of).days, "utilization_pct": num(r["Utilization %"]),
                    # what the account would pay on top each month if this plan lapsed unrenewed
                    "monthly_net_savings": round(num(r["Net Savings"]) / LOOKBACK_MONTHS, 2),
                    "monthly_commit": round(num(r["Hourly Commitment"]) * 730, 2)})
    for r in rows_of(base, e, "shared_RI_Inventory_Raw"):
        end = str(r["End Date"])[:10]
        rate = num(r["Effective Hourly Rate"])
        out.append({"entity": e, "kind": "RI", "type": r["Service"], "account": "", "account_name": r["Account Name"],
                    "detail": f'{r["Instance Type"]} {r["Platform"]}'.strip(), "region": r["Region"], "hourly": rate,
                    "count": int(num(r["# Instances"])), "term": "", "payment": r["Payment"], "start": str(r["Start Date"])[:10], "end": end,
                    "days_left": (datetime.date.fromisoformat(end) - as_of).days, "utilization_pct": None,
                    "monthly_net_savings": None, "monthly_commit": round(rate * 730, 2)})
    return out


def sp_purchase(base, e):
    """One row per (plan type, term, payment): AWS's purchase recommendation summed over accounts."""
    agg = collections.defaultdict(lambda: {"hourly_commit": 0.0, "monthly_savings": 0.0, "upfront": 0.0, "od_hourly": 0.0})
    for r in rows_of(base, e, "shared_SP_Purchase_Recommendations"):
        a = agg[(r["savingsPlansType"], r["termInYears"], r["paymentOption"])]
        a["hourly_commit"] += num(r["hourlyCommitmentToPurchase"])
        a["monthly_savings"] += num(r["estimatedMonthlySavingsAmount"])
        a["upfront"] += num(r["upfrontCost"])
        # EC2 Instance SP rows are per family, each with its own OD baseline; Compute SP has one row per option
        a["od_hourly"] += num(r["currentAverageHourlyOnDemandSpend"])
    return [{"entity": e, "plan": k[0], "term": k[1], "payment": k[2], **{f: round(v, 2) for f, v in a.items()}}
            for k, a in sorted(agg.items())]


def storage_tiers(base, e):
    out = []
    for prefix, svc in (("shared_ebs_storage_by_tier", "EBS"), ("shared_efs_storage_by_tier", "EFS")):
        for r in rows_of(base, e, prefix):
            for tier, v in r.items():
                if tier != "Month":
                    out.append({"entity": e, "service": svc, "month": r["Month"], "tier": tier, "cost": round(num(v), 2)})
    return out


def coh_actions(base, e):
    return [{"entity": e, "action": r["group"], "action_ko": ACTION_KO.get(r["group"], r["group"]),
             "est_savings": round(num(r["estimatedMonthlySavings"]), 2), "recs": int(num(r["recommendationCount"]))}
            for r in rows_of(base, e, "shared_Cost_Optimization_Hub_Summary_by_Action")]


def coh_by_workload(base, e):
    """COH resource recommendations (not deduplicated) by tag-derived workload and action."""
    fn = os.path.join(base, e, "Data_COH_All.csv")
    if not os.path.exists(fn):
        return []
    rows = list(csv.reader(open(fn, encoding="utf-8")))
    hi = next((i for i, r in enumerate(rows[:10]) if len(r) > 2 and r[1] == "Report Name"), None)
    if hi is None:
        return []
    ix = {}
    for j, h in enumerate(rows[hi]):
        ix.setdefault(h.strip(), j)
    agg = collections.defaultdict(lambda: [0, 0.0, 0.0, collections.Counter()])
    prec = precedence_for(base, e)
    for r in rows[hi + 1:]:
        if len(r) <= ix["resourceArn"] or r[1].strip() != e or not r[ix["recommendationId"]].strip():
            continue
        r = r + [""] * (len(rows[hi]) - len(r))  # a short row's missing trailing cells (e.g. no tags) are empty
        try:
            tags = {t["key"]: t["value"] for t in ast.literal_eval(r[ix["tags"]] or "[]")}
        except (ValueError, SyntaxError):
            tags = {}
        svc, _ = service_of({"type": r[ix["currentResourceType"]], "tags": tags}, prec)
        a = agg[(svc, r[ix["actionType"]])]
        a[0] += 1
        a[1] += num(r[ix["estimatedMonthlySavings"]])
        a[2] += num(r[ix["estimatedMonthlyCost"]])
        if r[ix["currentResourceSummary"]] and r[ix["currentResourceType"]] in ("Ec2Instance", "Ec2AutoScalingGroup"):
            a[3][f'{r[ix["currentResourceSummary"]]}→{r[ix["recommendedResourceSummary"]]}'] += 1
    return [{"entity": e, "workload": s, "action": act, "action_ko": ACTION_KO.get(act, act), "recs": n,
             "est_savings": round(sv, 2), "est_cost": round(c, 2), "top_changes": [f"{k}×{v}" for k, v in moves.most_common(3)]}
            for (s, act), (n, sv, c, moves) in sorted(agg.items(), key=lambda kv: -kv[1][1])]


def main(base, out_dir, as_of):
    as_of = datetime.date.fromisoformat(as_of)
    sets = {n: [] for n in ("unit_metrics", "modernization", "elasticity", "commitments", "sp_purchase",
                            "storage_tiers", "coh_actions", "coh_by_workload")}
    for e in ENTITIES:
        sets["unit_metrics"] += unit_metrics(base, e)
        sets["modernization"] += modernization(base, e)
        sets["elasticity"] += elasticity(base, e)
        sets["commitments"] += commitments(base, e, as_of)
        sets["sp_purchase"] += sp_purchase(base, e)
        sets["storage_tiers"] += storage_tiers(base, e)
        sets["coh_actions"] += coh_actions(base, e)
        sets["coh_by_workload"] += coh_by_workload(base, e)
    for n, rows in sets.items():
        with open(os.path.join(out_dir, n + ".json"), "w", encoding="utf-8") as f:
            json.dump(rows, f, ensure_ascii=False)
    print({n: len(r) for n, r in sets.items()})


if __name__ == "__main__":
    main(*sys.argv[1:4])
