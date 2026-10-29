"""Flatten dashboard_data.json into row-array JSON datasets for the dashboard.
Usage: python -I export_datasets.py <dashboard_json> <outdir>"""
import json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
from entities import NAMES  # noqa: E402
import period as P  # noqa: E402

FI = P.FI  # slot of the first present month in 6-long arrays
# the COT run date (worker / run_account.sh AS_OF), published in period.json for the commitment windows' label
AS_OF = os.environ.get("AS_OF", "") if re.fullmatch(r"\d{4}-\d{2}-\d{2}", os.environ.get("AS_OF", "")) else None
lab = lambda m: P.LABEL.get(m, m or "")  # noqa: E731


def mfields(vals, M):
    """m1..m6 (m6 = latest); a slot with no month is null."""
    return {"m%d" % (i + 1): (vals[i] if m else None) for i, m in enumerate(M)}


def usd(x):
    return f"${x:,.0f}"


def pct(a, b):
    return round((a / b - 1) * 100, 1) if b else None


def main(src, outdir):
    os.makedirs(outdir, exist_ok=True)
    d = json.load(open(src))
    M = d["months"]
    ds = {k: [] for k in ["spend_monthly", "kpis", "services", "bedrock_models", "tag_service",
                          "new_accounts", "opportunities", "sp_expiry", "data_transfer", "findings", "period"]}
    for e, x in d["entities"].items():
        b = x["bedrock"]
        ds["period"].append(dict(P.row(e, [m for m in M if m]), **({"as_of": AS_OF} if AS_OF else {})))
        for i, m in enumerate(M):
            if not m:
                continue
            ds["spend_monthly"].append({"entity": e, "month": m, "spend": x["monthly_total"][i],
                                        "bedrock": b["marketplace_total"][i] + sum(n["months"][i] for n in b["native"] if n["service"] != "Amazon SageMaker"),
                                        "gpu_ec2": x["gpu_ec2"][i], "saas_marketplace": x["marketplace_saas"]["months"][i]})
        o = x["opportunities"]
        levers = []
        if o["coh_dedup"]:
            levers.append(("COH 권고 합계(중복 제거)", o["coh_dedup"], "Cost Optimization Hub 추정 월 절감, 중복 제거 기준"))
        if o["sp_3yr_all_upfront"]:
            levers.append(("Compute SP 추가 구매 (3년 전선납)", o["sp_3yr_all_upfront"]["monthly_savings"],
                           f"시간당 ${o['sp_3yr_all_upfront']['hourly']:,} 약정, 절감률 {o['sp_3yr_all_upfront']['savings_pct']}%"))
        if o["sp_1yr_no_upfront"]:
            levers.append(("Compute SP 추가 구매 (1년 무선납)", o["sp_1yr_no_upfront"]["monthly_savings"],
                           f"시간당 ${o['sp_1yr_no_upfront']['hourly']:,} 약정, 절감률 {o['sp_1yr_no_upfront']['savings_pct']}%"))
        for svc, v in o["ri_1yr_no_upfront"].items():
            if svc.startswith("Amazon Relational"):
                levers.append(("RDS RI 구매 (1년 무선납)", v, "RDS 커버리지 하락분 재약정"))
        levers.append(("Graviton 전환", o["graviton"]["savings"], f"대상 월 비용 {usd(o['graviton']['cost'])}, 25% 절감 가정, 약정분 제외"))
        levers.append(("구세대 인스턴스 현대화", o["prevgen"]["savings"], f"대상 월 비용 {usd(o['prevgen']['cost'])}, 15% 절감 가정"))
        if o["weekend_elasticity_ec2_last"]:
            levers.append(("EC2 주말 스케일다운", o["weekend_elasticity_ec2_last"]["daily_savings"] * 8,
                           f"{lab(P.LAST)} 주말 탄력성 {o['weekend_elasticity_ec2_last']['elasticity_pct']}%, 이상치 대비 일 절감 × 월 8일"))
        for lv, s, basis in levers:
            ds["opportunities"].append({"entity": e, "lever": lv, "monthly_savings": s, "basis": basis})
        best = max(levers, key=lambda t: t[1])
        top = x["top_services"][0]
        ec2cov = x["unit"]["ec2"]["coverage"]
        ds["kpis"].append({
            "entity": e, "entity_ko": NAMES[e], "last_spend": x["monthly_total"][-1], "prev_spend": x["monthly_total"][-2],
            "mom_pct": pct(x["monthly_total"][-1], x["monthly_total"][-2]), "h1_avg": x["h1_avg"], "h2_avg": x["h2_avg"],
            "h2_vs_h1_pct": pct(x["h2_avg"], x["h1_avg"]), "six_month": x["six_month_total"],
            "accounts_first": x["accounts_active"][FI], "accounts_last": x["accounts_active"][-1],
            "bedrock_6m": b["six_month_total"], "bedrock_last": b["marketplace_total"][-1], "bedrock_first": b["marketplace_total"][FI],
            "bedrock_models_last": b["active_models_last"], "bedrock_top_model": b["top_model_last"], "bedrock_top_share": b["top_model_last_share"],
            "gpu_ec2_last": x["gpu_ec2"][-1], "coh_dedup": o["coh_dedup"], "sp_hourly": x["sp"]["hourly"],
            "sp_expiring_pct": x["sp"]["expiring_pct"], "ec2_coverage_last": ec2cov[-1], "ec2_coverage_first": ec2cov[FI],
            "top_service": top["name"], "top_service_last": top["last"], "best_lever": best[0], "best_lever_savings": best[1]})
        seen = {}
        for sig, lst in [("top", x["top_services"]), ("growth", x["growth_services"]), ("decline", x["decline_services"]), ("new", x["new_services"])]:
            for s in lst:
                r = seen.setdefault(s["name"], {"entity": e, "service": s["name"], **mfields(s["months"], M),
                                                "last": s["last"], "d_h2_h1": s["d_h2_h1"], "pct_h2_h1": s["pct_h2_h1"], "signals": []})
                r["signals"].append(sig)
        for r in seen.values():
            r["signals"] = ",".join(r["signals"])
            ds["services"].append(r)
        for mdl in b["models"]:
            for i, m in enumerate(M):
                if not m:
                    continue
                ds["bedrock_models"].append({"entity": e, "model": mdl["model"], "family": mdl["family"], "vendor": mdl["vendor"],
                                             "billing": "Marketplace (Bedrock Edition)", "month": m, "cost": mdl["months"][i]})
        for n in b["native"]:
            for i, m in enumerate(M):
                if not m:
                    continue
                ds["bedrock_models"].append({"entity": e, "model": n["service"], "family": "AWS 서비스 직접 과금", "vendor": "AWS",
                                             "billing": "AWS 서비스", "month": m, "cost": n["months"][i]})
        t = x.get("tags")
        if t:
            for key, kv in t["keys"].items():
                for v in kv["values"]:
                    for rt, c in v["by_type"].items():
                        ds["tag_service"].append({"entity": e, "tag_key": key, "tag_value": v["value"], "resource_type": rt,
                                                  "cost": c, "resources": v["n"]})
        ct = x.get("coh_tags", {}).get("accounts", {})
        team_key = x.get("coh_tags", {}).get("team_key")
        company_key = x.get("coh_tags", {}).get("company_key")
        for a in x["new_accounts"] + [g for g in x["growth_accounts"][:4] if g["name"] not in {n["name"] for n in x["new_accounts"]}]:
            p = ct.get(a["name"], {})
            tg = p.get("tags", {})
            clusters = ", ".join(v for v, _ in (tg.get("aws:eks:cluster-name") or [])[:3])
            ds["new_accounts"].append({"entity": e, "account": a["name"], "kind": "신규" if a in x["new_accounts"] else "증가",
                                       **mfields(a["months"], M), "last": a["last"],
                                       "d_h2_h1": a["d_h2_h1"],
                                       "regions": ", ".join(r for r, _ in p.get("regions", [])), "eks_clusters": clusters,
                                       "map_tag": ", ".join(v for v, _ in (tg.get("map-migrated") or [])[:2]),
                                       "team": ", ".join(v for v, _ in (tg.get(team_key) or tg.get(company_key) or [])[:3])})
        for k, v in x["sp"]["expiry"].items():
            if k <= P.shift(P.LAST, 15):  # through the year after next
                ds["sp_expiry"].append({"entity": e, "month": k, "hourly": v})
        for cat, v in x["dt"].items():
            for i, m in enumerate(M):
                if not m:
                    continue
                ds["data_transfer"].append({"entity": e, "category": cat, "month": m, "cost": v["cost"][i], "gb": v["gb"][i] if v["gb"] else None})
        ds["findings"].extend(findings(e, x, M))
    for k, rows in ds.items():
        json.dump(rows, open(os.path.join(outdir, k + ".json"), "w"), ensure_ascii=False)
        print(k, len(rows), os.path.getsize(os.path.join(outdir, k + ".json")))


def findings(e, x, M):
    """Data-driven narrative lines per entity; every number is computed from x."""
    F = []
    add = lambda area, text, sev="info": F.append({"entity": e, "area": area, "severity": sev, "text": text, "order": len(F)})
    tot, b, o, sp = x["monthly_total"], x["bedrock"], x["opportunities"], x["sp"]
    h1, h2, last, n = P.span(P.H1, P.LABEL), P.span(P.H2, P.LABEL), lab(P.LAST), len(P.PRESENT)
    if x["h1_avg"] is None:  # short period: no month in the earlier half
        add("지출 추세", f"{last} 지출 {usd(tot[-1])}, 최근 {len(P.H2)}개월({h2}) 평균 {usd(x['h2_avg'])}. 앞 기간 데이터가 없어 비교하지 않습니다.")
    else:
        h1n, h2n = len(P.H1), len(P.H2)
        add("지출 추세", f"{last} 지출 {usd(tot[-1])}, 앞 {h1n}개월({h1}) 평균 {usd(x['h1_avg'])} 대비 최근 {h2n}개월({h2}) 평균 {usd(x['h2_avg'])} ({pct(x['h2_avg'], x['h1_avg']):+.1f}%).")
    g = x["growth_services"][:3]
    if g:
        add("증가 워크로드", "최근 3개월 평균 기준 증가 상위: " + ", ".join(f"{s['name']} {s['d_h2_h1']:+,}$/월" for s in g) + ".")
    dcl = x["decline_services"][:2]
    if dcl:
        add("감소 워크로드", "감소 상위: " + ", ".join(f"{s['name']} {s['d_h2_h1']:+,}$/월" for s in dcl) + ".")
    ns = [s for s in x["new_services"] if max(s["months"][3:]) >= 50]
    if ns:
        add("신규 서비스", f"{P.span(P.PRESENT[:2], P.LABEL)} 거의 0에서 새로 발생: " + ", ".join(f"{s['name']} ({h2} 최대 {usd(max(s['months'][3:]))}, {last} {usd(s['last'])})" for s in ns[:4]) + ".", "new")
    if x["new_families"]:
        add("신규 인스턴스", "새로 쓰기 시작한 인스턴스 패밀리: " + ", ".join(f"{f['name']} ({usd(f['last'])})" for f in x["new_families"][:6]) + f" ({last}).", "new")
    if x["new_accounts"]:
        acc = x["accounts_active"]
        add("신규 계정", f"활성 연결 계정 {acc[FI]} → {acc[-1]}개. {lab(P.H2[0] if P.H2 else P.LAST)} 이후 새로 비용이 발생한 계정 {len(x['new_accounts'])}개, {last} 합계 {usd(sum(a['last'] for a in x['new_accounts']))}.", "new")
    bt = b["marketplace_total"]
    if b["six_month_total"] > 0:
        add("Bedrock", f"Bedrock(Marketplace Claude 등) {n}개월 {usd(b['six_month_total'])}, {lab(P.FIRST)} {usd(bt[FI])} → {last} {usd(bt[-1])}. {last} 사용 모델 {b['active_models_last']}종, 1위 {b['top_model_last']} ({b['top_model_last_share']}%).",
            "new" if bt[-1] > bt[FI] * 2 else "info")
    if x["gpu_ec2"][-1] > 1000:
        add("GPU", f"GPU EC2(g/p 계열) {last} {usd(x['gpu_ec2'][-1])} ({lab(P.FIRST)} {usd(x['gpu_ec2'][FI])}).")
    if sp["expiring_pct"] >= 30:
        add("약정 만료", f"활성 SP 시간당 ${sp['hourly']:,} 중 {sp['expiring_pct']}%(${sp['expiring_6m']:,}/h)가 {P.ko_month(P.shift(P.LAST, 6))}까지 만료.", "critical")
    cov = x["unit"]["ec2"]["coverage"]
    if cov[-1] < 70:
        add("커버리지", f"EC2 약정 커버리지 {last} {cov[-1]}% ({lab(P.FIRST)} {cov[FI]}%). 온디맨드 노출이 큼.", "warn")
    if "rds" in x["unit"] and x["unit"]["rds"]["coverage"][FI] - x["unit"]["rds"]["coverage"][-1] > 15:
        r = x["unit"]["rds"]["coverage"]
        add("커버리지", f"RDS RI 커버리지 {r[FI]}% → {r[-1]}%. 만료된 RI 재구매가 필요.", "warn")
    saas = [i for i in x["marketplace_saas"]["items"] if i["total"] >= 1000]
    if saas and saas[0]["total"] > 20000:
        add("Marketplace SaaS", "Marketplace SaaS 일시 결제: " + ", ".join(f"{i['name']} {usd(i['total'])}" for i in saas[:3]) + " (서비스 KPI에서 제외됨).")
    ch = x["charge_types"].get("Credit")
    if ch and sum(ch) < -50000:
        add("크레딧", f"{n}개월 크레딧 {usd(-sum(ch))} 적용(MAP 등). 서비스 비용 KPI에서는 제외.")
    return F


if __name__ == "__main__":
    main(*sys.argv[1:])
