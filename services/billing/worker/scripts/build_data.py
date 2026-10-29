"""Build the consolidated dashboard/report dataset for every entity of the run.
Usage: python -I build_data.py <extracted_root> <out_json>"""
import collections, json, os, re, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cotload as L
import trend as T
import cohtags as C
import period as P
from entities import ENTITIES

M = T.MONTHS
AI_RE = re.compile(r"(?i)claude|bedrock|gpt|openai|llama|mistral|cohere|anthropic")
GPU_RE = re.compile(r"^(g\d|p\d|inf\d|trn\d|dl\d|gr6)")


def fl(x):
    try:
        return float(x)
    except (TypeError, ValueError):
        return 0.0


def hj(e, name):
    fn = os.path.join(L.OUT, e, "html", f"json_shared_{name}.json")
    if not os.path.exists(fn):
        return None
    return json.load(open(fn))


def hj_prefix(e, prefix):
    d = os.path.join(L.OUT, e, "html")
    for fn in sorted(os.listdir(d)):
        if fn.startswith(prefix):
            return json.load(open(os.path.join(d, fn)))
    return None


def month_vals(j, r):
    """One HTML table row as 6 slot values: each slot reads the column headed by its month (0 when absent). A short
    period's table has fewer month columns, followed by Rank etc., so positions cannot be used."""
    hdr = (j or {}).get("headers") or []
    return [fl(r[hdr.index(m)]) if m and m in hdr and hdr.index(m) < len(r) else 0.0 for m in M]


def by_month(rows, field, col="Month"):
    """Per-month rows (unit metrics: one row per month) as 6 slot values; 0 for a slot without a row."""
    got = {str(r.get(col)): fl(r.get(field)) for r in rows}
    return [got.get(m, 0.0) if m else 0.0 for m in M]


def half_avg(tot, half):
    """Average over the present months of one half (period.H1/H2); None when the half has no month."""
    xs = [tot[i] for i, m in enumerate(M) if m and m in half]
    return round(sum(xs) / len(xs)) if xs else None


def rows_of(j):
    return [dict(zip(j["headers"], r)) for r in j["rows"]] if j else []


def r0(v):
    return [round(x) for x in v]


def html_kpis(root, e):
    h = open(os.path.join(root, e, f"COT_{e}_COT360.html"), encoding="utf-8").read()
    t = re.sub(r"<script.*?</script>|<style.*?</style>", "", h, flags=re.S)
    t = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", t))
    spend = re.search(r"Past Month Spend.*?\$([\d,]+) Amortized", t)
    coh = re.search(r"Total Dedup Savings / mo.*?(\$[\d,]+|—) COH", t)
    return {"html_last_spend": int(spend.group(1).replace(",", "")) if spend else None,
            "html_coh_dedup": int(coh.group(1)[1:].replace(",", "")) if coh and coh.group(1) != "—" else None}


def family(k):
    return ".".join(k.split(".")[:2]) if k.startswith(("db.", "cache.")) else k.split(".")[0]


def entity(root, e):
    out = {"name": e}
    out.update(html_kpis(root, e))
    svc = T.table(T.pivot(e, "Data_Service_Costs"))
    tot = [sum(s["months"][i] for s in svc.values()) for i in range(6)]
    assert round(tot[-1]) == out["html_last_spend"], (e, tot[-1], out["html_last_spend"])
    out["monthly_total"] = r0(tot)
    out["h1_avg"], out["h2_avg"] = half_avg(tot, P.H1), half_avg(tot, P.H2)
    out["six_month_total"] = round(sum(tot))
    keep = lambda k, s: {"name": k, "months": r0(s["months"]), "last": round(s["last"]),
                         "d_h2_h1": round(s["d_h2_h1"]), "pct_h2_h1": s["pct_h2_h1"]}
    out["top_services"] = [keep(k, s) for k, s in sorted(svc.items(), key=lambda x: -x[1]["last"])[:12]]
    out["growth_services"] = [keep(k, s) for k, s in sorted(svc.items(), key=lambda x: -x[1]["d_h2_h1"])[:10] if s["d_h2_h1"] > 0]
    out["decline_services"] = [keep(k, s) for k, s in sorted(svc.items(), key=lambda x: x[1]["d_h2_h1"])[:6] if s["d_h2_h1"] < 0]
    out["new_services"] = [keep(k, s) for k, s in T.new_items(svc, 20, 5)]

    # linked accounts
    la = T.table(T.pivot(e, "Data_Linked_Account_Costs"))
    out["accounts_active"] = [sum(1 for s in la.values() if s["months"][i] > 1) for i in range(6)]
    out["new_accounts"] = [keep(k, s) for k, s in T.new_items(la, 200, 5)]
    out["growth_accounts"] = [keep(k, s) for k, s in sorted(la.items(), key=lambda x: -x[1]["d_h2_h1"])[:6] if s["d_h2_h1"] > 0]

    # marketplace: GenAI vs SaaS
    ai, saas, ai_models, saas_items = [0.0] * 6, [0.0] * 6, [], []
    mj = hj(e, "Marketplace_Costs") or {"headers": [], "rows": []}
    for r in mj["rows"]:
        v = month_vals(mj, r)
        tgt = ai if AI_RE.search(str(r[0])) else saas
        for i in range(6):
            tgt[i] += v[i]
        (ai_models if tgt is ai else saas_items).append({"name": str(r[0]), "months": r0(v), "total": round(sum(v))})
    bedrock = svc.get("Amazon Bedrock", {"months": [0] * 6})["months"]
    sagemaker = svc.get("Amazon SageMaker", {"months": [0] * 6})["months"]
    out["genai"] = {"marketplace_ai": r0(ai), "bedrock_service": r0(bedrock), "sagemaker": r0(sagemaker),
                    "total": r0([ai[i] + bedrock[i] + sagemaker[i] for i in range(6)]),
                    "models": sorted(ai_models, key=lambda x: -x["months"][5])[:8]}
    out["marketplace_saas"] = {"months": r0(saas), "items": sorted(saas_items, key=lambda x: -x["total"])[:5]}

    # instance families (all purchase options)
    tc = collections.defaultdict(lambda: dict.fromkeys(M, 0.0))
    for sh in ["Data_On_Demand_Usage", "Data_Savings_Plan_Usage", "Data_Reserved_Instance_Usage", "Data_Spot_Usage"]:
        try:
            rs = L.load(e, sh)
        except StopIteration:
            continue
        for r in rs:
            k = L.key(r["keys"])
            if isinstance(k, str) and L.month(r) in P.PRESENT:
                tc[family(k)][L.month(r)] += L.num(r["metrics.AmortizedCost.amount"])
    ft = T.table(tc)
    out["new_families"] = [keep(k, s) for k, s in T.new_items(ft, 200, 20)]
    out["growth_families"] = [keep(k, s) for k, s in sorted(ft.items(), key=lambda x: -x[1]["d_h2_h1"])[:8] if s["d_h2_h1"] > 0]
    gpu = [0.0] * 6
    for k, s in ft.items():
        if GPU_RE.match(k):
            gpu = [gpu[i] + s["months"][i] for i in range(6)]
    out["gpu_ec2"] = r0(gpu)
    out["gpu_families"] = [keep(k, s) for k, s in sorted(ft.items(), key=lambda x: -x[1]["last"]) if GPU_RE.match(k)]

    # unit metrics (coverage, graviton/latest, cost per vCPU-hr)
    um = {}
    for svcname in ["ec2", "rds", "elasticache", "opensearch"]:
        j = hj_prefix(e, f"json_um_{svcname}_")
        if j:
            rr = rows_of(j)
            um[svcname] = {"coverage": by_month(rr, "Coverage %"), "graviton_latest": by_month(rr, "Graviton/Latest %"),
                           "cost": r0(by_month(rr, "Cost ($)")), "vcpu_hours": r0(by_month(rr, "vCPU Hours"))}
    out["unit"] = um
    po = rows_of(hj_prefix(e, "json_po_"))
    out["purchase_option_last"] = [{"service": r["Service"], "option": r["Purchase Option"], "spend": round(fl(r["Monthly Spend"])), "pct": r["% of Service Total"]} for r in po]

    # data transfer categories
    def dtcat(k):
        if "CloudFront" in k: return "CloudFront"
        if "xAZ" in k or "Regional-Bytes" in k: return "Inter-AZ"
        if re.search(r"-[A-Z]{3,4}\d-AWS-(Out|In)-Bytes", k): return "Inter-Region"
        if "DataTransfer-Out-Bytes" in k: return "Internet-Out"
        return "Other"
    dt = T.table(T.pivot(e, "Data_Data_Transfer", keyfn=dtcat))
    dq = T.table(T.pivot(e, "Data_Data_Transfer", metric="metrics.UsageQuantity.amount", keyfn=dtcat))
    out["dt"] = {k: {"cost": r0(dt[k]["months"]), "gb": r0(dq[k]["months"]) if k in dq else None} for k in ["Internet-Out", "Inter-AZ", "Inter-Region"] if k in dt}
    reg = lambda k: (re.match(r"^([A-Z]{2,4}\d)-", k) or [None, "GLOBAL"])[1]
    st = T.table(T.pivot(e, "Data_Storage", keyfn=reg))
    out["storage_by_region"] = {k: r0(s["months"]) for k, s in sorted(st.items(), key=lambda x: -x[1]["last"])[:6]}

    # commitments
    sp = [r for r in rows_of(hj(e, "SP_Inventory_Raw")) if str(r["Status"]).lower() == "active"]
    exp = collections.defaultdict(float)
    for r in sp:
        exp[str(r["End Date"])[:7]] += fl(r["Hourly Commitment"])
    hc = sum(fl(r["Hourly Commitment"]) for r in sp)
    # plans ending within 6 months after the period's last month
    soon = sum(v for k, v in exp.items() if k <= P.shift(P.LAST, 6))
    out["sp"] = {"active": len(sp), "hourly": round(hc, 2), "expiry": {k: round(v, 2) for k, v in sorted(exp.items())},
                 "expiring_6m": round(soon, 2), "expiring_pct": round(soon / hc * 100, 1) if hc else 0,
                 "low_util": [{"family": r["Instance Family"], "region": r["Region"], "hourly": fl(r["Hourly Commitment"]),
                               "util": round(fl(r["Utilization %"]), 1), "end": str(r["End Date"])[:10]}
                              for r in sp if r["Utilization %"] not in ("", None, "None") and fl(r["Utilization %"]) < 95]}

    # savings opportunities
    opp = {"coh_dedup": out["html_coh_dedup"]}
    sprec = rows_of(hj(e, "SP_Purchase_Recommendations"))
    def best(term, pay):
        c = [r for r in sprec if r.get("termInYears") == term and r.get("paymentOption") == pay]
        b = max(c, key=lambda r: fl(r["estimatedMonthlySavingsAmount"]), default=None)
        return {"monthly_savings": round(fl(b["estimatedMonthlySavingsAmount"])), "hourly": round(fl(b["hourlyCommitmentToPurchase"]), 2),
                "savings_pct": round(fl(b["estimatedSavingsPercentage"]), 1)} if b else None
    opp["sp_3yr_all_upfront"] = best("THREE_YEARS", "ALL_UPFRONT")
    opp["sp_1yr_no_upfront"] = best("ONE_YEAR", "NO_UPFRONT")
    ri = collections.defaultdict(float)
    for r in rows_of(hj(e, "RI_Purchase_Recommendations")):
        if r.get("termInYears") == "ONE_YEAR" and r.get("paymentOption") == "NO_UPFRONT":
            ri[r["service"]] += fl(r["estimatedMonthlySavingsAmount"])
    opp["ri_1yr_no_upfront"] = {k: round(v) for k, v in ri.items()}
    for key, name in [("graviton", "Graviton_Migration_Opportunities_Full"), ("prevgen", "Previous_Generation_Instances_Full")]:
        rr = rows_of(hj(e, name))
        sk = next((h for h in (hj(e, name) or {"headers": []})["headers"] if h.startswith("Est. Savings")), None)
        opp[key] = {"savings": round(sum(fl(r[sk]) for r in rr)) if sk else 0, "cost": round(sum(fl(r["Monthly Cost"]) for r in rr)),
                    "top": [{"type": r["Instance Type"], "to": r.get("Graviton Equivalent") or r.get("Latest Instance Family"), "savings": round(fl(r[sk]))}
                            for r in sorted(rr, key=lambda r: -fl(r[sk]))[:5]] if sk else []}
    el = rows_of(hj_prefix(e, "json_elast_ec2_"))
    opp["weekend_elasticity_ec2_last"] = {"elasticity_pct": fl(el[-1]["Elasticity %"]), "daily_savings": round(fl(el[-1]["Savings"]))} if el else None
    out["opportunities"] = opp

    # charge-type view (credits / discounts) from amortized costs
    uc = rows_of(hj_prefix(e, "json_uc_"))
    out["charge_types"] = {r["keys"]: r0([fl(r.get(m)) if m else 0.0 for m in M]) for r in uc}

    # COH tags
    coh = C.analyze(e, [a["name"] for a in out["new_accounts"]] + [a["name"] for a in out["growth_accounts"]])
    if coh:
        out["coh_tags"] = {k: coh[k] for k in ["n", "untagged", "by_action", "created_by_month", "recent_n", "recent_cost", "recent", "accounts", "team_key", "company_key"]}
        out["coh_tags"]["dims"] = {k: v[:12] for k, v in coh["dims"].items() if v}
    return out


def main(root, out_json):
    data = {"months": M, "entities": {e: entity(root, e) for e in ENTITIES}}
    json.dump(data, open(out_json, "w"), ensure_ascii=False, indent=1)
    for e, d in data["entities"].items():
        print(e, "last", d["monthly_total"][-1], "html", d["html_last_spend"], "coh", d["html_coh_dedup"],
              "genai last", d["genai"]["total"][-1], "new acct", len(d["new_accounts"]))


if __name__ == "__main__":
    main(*sys.argv[1:])
