"""Insight sentences per entity x screen (ec2 / accounts / services), computed from the datasets.

Usage: python3 -I insights.py <dashboard_data_dir>
Reads ec2_type_growth.json, ec2_mix.json, accounts.json, tag_services.json (and the other datasets below,
period.json for each entity's months); writes insights.json.
Every number in a sentence comes from those files; nothing is typed in by hand.
"""
import json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import entities  # noqa: E402
import period as P  # noqa: E402

PERIODS = []  # period.json rows, set by main


def per(e):
    """An entity's months: first/prev/last, label(), the slot key of its first month, and the half-period spans."""
    p = P.for_entity(PERIODS, e)
    ms, lab = p["months"], p["labels"]
    h1, h2 = P.halves(ms)
    return {**p, "lab": lambda m: lab.get(m, m or ""), "fk": "m%d" % (P.N - len(ms) + 1) if ms else "m1",
            "h1": h1, "h2": h2, "h1s": P.span(h1, lab), "h2s": P.span(h2, lab), "alls": P.span(ms, lab),
            "since": lab.get(h2[0], "") if h2 else ""}
ARCH_KO = {"graviton": "Graviton", "intel": "Intel", "amd": "AMD", "gpu/accel": "GPU·가속기", "other": "기타"}
PUR_KO = {"OD": "온디맨드", "SP": "Savings Plans", "RI": "RI", "Spot": "Spot"}


def usd(v):
    v = float(v)
    sign, v = ("−" if v < 0 else ""), abs(v)
    if v >= 1e6:
        return f"{sign}${v / 1e6:,.2f}M"
    if v >= 1e3:
        return f"{sign}${v / 1e3:,.1f}K"
    return f"{sign}${v:,.0f}"


def share(part, total):
    return 100.0 * part / total if total else 0.0


def top_purchase(mix):
    k = max(mix, key=mix.get) if mix else None
    return f"{PUR_KO.get(k, k)} {mix[k] * 100:.0f}%" if k else "—"


def ec2(e, growth, mix):
    out = []
    g = [r for r in growth if r["entity"] == e]
    m = {r["month"]: r for r in mix if r["entity"] == e}
    p = per(e)
    if not g or p["first"] not in m or p["last"] not in m:
        return out
    L, F = p["lab"](p["last"]), p["lab"](p["first"])
    first, last = m[p["first"]], m[p["last"]]
    d_total = last["total"] - first["total"]
    # a short period has no earlier half: d_h2_h1 is null there (no change to report)
    up = sorted((r for r in g if (r["d_h2_h1"] or 0) > 0), key=lambda r: -r["d_h2_h1"])[:3] if p["h1"] else []
    if up:
        items = ", ".join(f"{r['instance_type']}(+{usd(r['d_h2_h1'])}/월, {top_purchase(r['purchase_mix_last'])})" for r in up)
        out.append(("growth", f"EC2 사용액은 {F} {usd(first['total'])} → {L} {usd(last['total'])}({'+' if d_total >= 0 else ''}{usd(d_total)}). "
                              f"최근 3개월 월평균이 가장 많이 늘어난 유형은 {items}."))
    down = sorted((r for r in g if (r["d_h2_h1"] or 0) < 0), key=lambda r: r["d_h2_h1"])[:2] if p["h1"] else []
    if down:
        out.append(("info", "가장 많이 줄어든 유형은 " + ", ".join(f"{r['instance_type']}({usd(r['d_h2_h1'])}/월)" for r in down)
                    + ". 같은 계열의 다른 크기·세대가 함께 늘었다면 교체(리사이징·세대 전환)로 볼 수 있습니다."))
    new = sorted((r for r in g if r["is_new"]), key=lambda r: -r["last"])
    if new:
        out.append(("new", f"{p['since']} 이후 새로 쓰기 시작한 유형 {len(new)}개, {L} {usd(sum(r['last'] for r in new))}. "
                           "큰 것부터 " + ", ".join(f"{r['instance_type']} {usd(r['last'])}" for r in new[:3]) + "."))
    ga, gs = share(first["by_arch"].get("graviton", 0), first["total"]), share(last["by_arch"].get("graviton", 0), last["total"])
    g8a, g8s = share(first["by_generation"].get("8", 0), first["total"]), share(last["by_generation"].get("8", 0), last["total"])
    out.append(("info", f"Graviton 비중 {ga:.0f}% → {gs:.0f}%, 8세대 비중 {g8a:.0f}% → {g8s:.0f}% ({F} → {L})."
                + (" Graviton 비중이 줄고 있어 전환 로드맵 세션 대상입니다." if gs < ga - 1 else "")))
    oda, ods = share(first["by_purchase"].get("OD", 0), first["total"]), share(last["by_purchase"].get("OD", 0), last["total"])
    out.append(("warn" if ods >= 40 else "info",
                f"온디맨드 비중 {oda:.0f}% → {ods:.0f}%."
                + (f" {L} 온디맨드가 40% 이상이라 Savings Plans 추가 약정 여지가 큽니다." if ods >= 40 else "")))
    gpu = [m[k]["gpu_cost"] for k in sorted(m)]
    if max(gpu) > 1000:
        out.append(("info", f"GPU·가속기 인스턴스 월 비용 {' → '.join(usd(v) for v in gpu)} ({p['alls']})."))
    return out


def accounts(e, accts, svcs):
    out = []
    a = sorted((r for r in accts if r["entity"] == e), key=lambda r: -r["last"])
    if not a:
        return out
    p = per(e)
    L, F, fk = p["lab"](p["last"]), p["lab"](p["first"]), p["fk"]
    top3 = a[:3]
    out.append(("info", f"연결 계정 {len(a)}개 중 상위 3개({', '.join(r['name'] for r in top3)})가 {L} 지출의 "
                        f"{sum(r['share_last'] for r in top3) * 100:.1f}%를 차지합니다."))
    # a null change (short period) sorts as none; ties keep the latest-month order of a
    hot = sorted((r for r in a if r["kind"] in ("신규", "급증")), key=lambda r: -(r["d_h2_h1"] or 0))
    for r in hot[:4]:
        svc = [s for s in r.get("services_by_tag") or [] if s != "—"]
        where = (f" 태그로 보면 {', '.join(svc)} 워크로드" if svc else
                 " COH 리소스는 있지만 서비스 이름 태그가 없음" if r.get("coh_resources") else
                 " COH 권고 리소스가 없어 태그·유형은 확인 불가")
        types = f", 주요 유형 {', '.join(r['top_instance_types'][:2])}" if r.get("top_instance_types") else ""
        regions = f", 리전 {', '.join(r['regions'][:2])}" if r.get("regions") else ""
        out.append(("new" if r["kind"] == "신규" else "growth",
                    f"{r['kind']} 계정 {r['name']}({r['account']}): {F} {usd(r[fk] or 0)} → {L} {usd(r['m6'] or 0)}.{where}{types}{regions}.".replace(".,", ",")))
    blind = [r for r in hot if not r.get("coh_resources")]
    if blind:
        out.append(("warn", f"늘어나는 계정 {len(hot)}개 중 {len(blind)}개는 COH 권고 리소스가 없어 무엇이 늘었는지 COT만으로는 알 수 없습니다. "
                            "고객에게 해당 계정의 서비스·팀을 직접 확인하세요."))
    return out


def services(e, svcs):
    out = []
    s = [r for r in svcs if r["entity"] == e and not r["service"].startswith("(")]
    if not s:
        return out
    s.sort(key=lambda r: -r["est_monthly_cost"])
    out.append(("info", "태그로 식별한 비용 상위 서비스: " + ", ".join(
        f"{r['service']} {usd(r['est_monthly_cost'])}/월({(r['top_instance_types'] or ['—'])[0]})" for r in s[:3])
        + ". COH 권고 리소스 기준의 월 추정액이라 전체 청구액이 아닙니다."))
    new = [r for r in s if r.get("is_new")]
    since = per(e)["since"]
    if new:
        out.append(("new", f"{since} 이후 생성된 리소스가 대부분인 신규 서비스 {len(new)}개: "
                    + ", ".join(f"{r['service']}({usd(r['est_monthly_cost'])}, 리소스 {r['created_recent_pct']:.0f}%가 {since} 이후 생성)" for r in new[:5]) + "."))
    return out


def pct1(v):
    """A percent with one decimal; None (no EC2 unit-metrics block) is unknown, not 0.0%."""
    return "—" if v is None else f"{v:.1f}%"


def pct_val(v):
    """kpis store shares either as a fraction (0.585) or a percentage (58.5); return percent."""
    v = float(v or 0)
    return v * 100 if v <= 1 else v


def svc_line(rows, names, fk="m1"):
    """'name $X → $Y' (first → last month) for services whose name contains any of names, biggest last month first."""
    hit = sorted((r for r in rows if any(n in r["service"] for n in names)), key=lambda r: -r["last"])
    return [f"{r['service'].replace('Amazon ', '').replace('AWS ', '')} {usd(r[fk] or 0)} → {usd(r['m6'] or 0)}" for r in hit], hit


def data_area(e, svcs, types):
    out = []
    rows = [r for r in svcs if r["entity"] == e]
    p = per(e)
    lines, hit = svc_line(rows, ("Relational Database", "Aurora", "ElastiCache", "OpenSearch", "DynamoDB", "DocumentDB", "Redshift", "MemoryDB"), p["fk"])
    if lines:
        out.append(("info", f"데이터 계층 월 비용({p['lab'](p['first'])} → {p['lab'](p['last'])}): " + ", ".join(lines[:4]) + "."))
    db = {}
    for r in types:
        if r["entity"] == e and r["service"] in ("RDS", "ElastiCache", "OpenSearch"):
            k = (r["service"], r["instance_type"]); db.setdefault(k, {}); db[k][r["month"]] = db[k].get(r["month"], 0) + r["cost"]
    def h(m, months): return sum(m.get(x, 0) for x in months) / len(months) if months else 0.0
    q2, q3 = p["h1"], p["h2"]
    up = sorted(((k, h(m, q3) - h(m, q2)) for k, m in db.items()), key=lambda x: -x[1])
    up = [x for x in up if x[1] > 300][:3] if q2 else []  # no earlier half observed: no change to report
    if up:
        out.append(("growth", "가장 많이 늘어난 DB·캐시 노드 유형(최근 3개월 월평균 증감): " + ", ".join(f"{t}({svc}) +{usd(d)}/월" for (svc, t), d in up) + "."))
    return out


def network_area(e, svcs, dts):
    out = []
    rows = [r for r in svcs if r["entity"] == e]
    p = per(e)
    lines, _ = svc_line(rows, ("CloudFront", "Virtual Private Cloud", "Elastic Load Balancing", "Global Accelerator", "Direct Connect", "Route 53"), p["fk"])
    if lines:
        out.append(("info", f"네트워크·CDN 월 비용({p['lab'](p['first'])} → {p['lab'](p['last'])}): " + ", ".join(lines[:4]) + "."))
    ko = {"Internet-Out": "인터넷 아웃", "Inter-AZ": "AZ 간", "Inter-Region": "리전 간"}
    for c in ("Internet-Out", "Inter-AZ", "Inter-Region"):
        a = next((r for r in dts if r["entity"] == e and r["category"] == c and r["month"] == p["first"]), None)
        z = next((r for r in dts if r["entity"] == e and r["category"] == c and r["month"] == p["last"]), None)
        if not a or not z or max(a["cost"], z["cost"]) < 2000:
            continue
        unit = lambda r: r["cost"] / r["gb"] if r.get("gb") else 0
        gbchg = 100.0 * (z["gb"] - a["gb"]) / a["gb"] if a.get("gb") else 0
        sev = "growth" if z["cost"] > a["cost"] * 1.3 else "info"
        # "rate unchanged, volume grew" only when the computed GB rate held (within 10%) and the volume rose
        flat = unit(a) > 0 and abs(unit(z) / unit(a) - 1) <= 0.1 and gbchg > 0
        why_az = " 단가는 그대로라 전송량 증가가 원인입니다." if flat else " 전송량과 GB당 단가 변화를 함께 확인합니다."
        hint = {"Inter-AZ": why_az + " EKS 토폴로지 인지 라우팅, AZ 고정 배치, 캐시·DB 리더 AZ 정렬을 검토할 세션 대상입니다.",
                "Internet-Out": " 전송량 대비 단가를 함께 보고 CloudFront 정액 요금제·DX 정액제 세션 근거로 씁니다.",
                "Inter-Region": " 리전 간 복제·백업 경로를 확인하고 DX 정액제·리전 배치 세션 근거로 씁니다."}[c]
        out.append((sev, f"{ko[c]} 전송 {usd(a['cost'])} → {usd(z['cost'])}, 전송량 {gbchg:+.0f}%, GB당 ${unit(a):.4f} → ${unit(z):.4f}."
                    + (hint if sev == "growth" else "")))
    return out


def genai_area(e, kp, mix, svcs):
    out = []
    k = next((r for r in kp if r["entity"] == e), None)
    p = per(e)
    L, F = p["lab"](p["last"]), p["lab"](p["first"])
    if k and k.get("bedrock_6m"):
        out.append(("growth" if k["bedrock_last"] > 2 * max(k["bedrock_first"], 1) else "info",
                    f"Bedrock 월 사용액 {F} {usd(k['bedrock_first'])} → {L} {usd(k['bedrock_last'])}, {len(p['months'])}개월 {usd(k['bedrock_6m'])}. "
                    f"{L} 최다 모델 {k.get('bedrock_top_model') or '—'}({pct_val(k.get('bedrock_top_share')):.1f}%), 사용 모델 {k.get('bedrock_models_last') or 0}개."))
    m = {r["month"]: r for r in mix if r["entity"] == e}
    if m:
        g = [m[x]["gpu_cost"] for x in sorted(m)]
        if max(g) > 1000:
            out.append(("info", f"GPU·가속기 EC2 {F} {usd(g[0])} → {L} {usd(g[-1])}. 자체 GPU 추론과 Bedrock 관리형 모델 사이의 비용 비교가 세션 주제가 됩니다."))
    sm = [r for r in svcs if r["entity"] == e and "SageMaker" in r["service"]]
    for r in sm:
        out.append(("info", f"SageMaker {usd(r[p['fk']] or 0)} → {usd(r['m6'] or 0)}."))
    return out


def finops_area(e, kp, ops, sp):
    out = []
    k = next((r for r in kp if r["entity"] == e), None)
    p = per(e)
    if k:
        until = P.ko_month(P.shift(p["last"], 6)) if p["last"] else "—"
        out.append(("warn" if (k.get("sp_expiring_pct") or 0) >= 50 else "info",
                    f"Savings Plans 약정 시간당 ${k.get('sp_hourly') or 0:,.2f} 중 {k.get('sp_expiring_pct') or 0:.1f}%가 {until}까지 만료. "
                    f"EC2 약정 커버리지 {p['lab'](p['first'])} {pct1(k.get('ec2_coverage_first'))} → {p['lab'](p['last'])} {pct1(k.get('ec2_coverage_last'))}."))
    top = sorted((r for r in ops if r["entity"] == e), key=lambda r: -r["monthly_savings"])[:3]
    if top:
        out.append(("info", "월 절감 여지 상위: " + ", ".join(f"{r['lever']} {usd(r['monthly_savings'])}" for r in top) + "."))
    exp = sorted((r for r in sp if r["entity"] == e), key=lambda r: -r["hourly"])[:2]
    if exp:
        out.append(("warn", "만료가 몰린 달: " + ", ".join(f"{r['month']} ${r['hourly']:,.2f}/h" for r in exp) + ". 갱신 전 커버리지·세대 전환 계획을 같이 잡아야 합니다."))
    return out


def security_area(e, svcs):
    out = []
    rows = [r for r in svcs if r["entity"] == e]
    names = ("WAF", "Shield", "GuardDuty", "Inspector", "Security Hub", "Macie", "Key Management", "Network Firewall", "Firewall Manager", "Detective", "CloudTrail")
    p = per(e)
    fk, F, L = p["fk"], p["lab"](p["first"]), p["lab"](p["last"])
    lines, hit = svc_line(rows, names, fk)
    if lines:
        out.append(("info", f"보안 서비스 월 비용({F} → {L}): " + ", ".join(lines[:5]) + "."))
    v = lambda r, k: r[k] or 0  # noqa: E731
    new = [r for r in hit if v(r, fk) < 1 and v(r, "m6") > 0]
    if new:
        out.append(("new", f"{F}에 없다가 새로 쓰기 시작한 보안 서비스: " + ", ".join(r["service"] for r in new) + "."))
    fast = [r for r in hit if v(r, fk) >= 1 and v(r, "m6") > 2 * v(r, fk) and v(r, "m6") > 1000]
    if fast:
        out.append(("growth", "2배 이상 늘어난 보안 서비스: " + ", ".join(f"{r['service']} {usd(v(r, fk))} → {usd(v(r, 'm6'))}" for r in fast) + ". 공격 트래픽 증가나 신규 서비스 오픈 여부를 확인하세요."))
    return out


def main(d):
    load = lambda n: json.load(open(os.path.join(d, n), encoding="utf-8"))
    growth, mix, accts, svcs = load("ec2_type_growth.json"), load("ec2_mix.json"), load("accounts.json"), load("tag_services.json")
    services_cost, types, dts, kp = load("services.json"), load("ec2_types.json"), load("data_transfer.json"), load("kpis.json")
    ops, sp = load("opportunities.json"), load("sp_expiry.json")
    PERIODS[:] = load("period.json") if os.path.exists(os.path.join(d, "period.json")) else []
    rows = []
    for e in entities.present(kp):
        for area, items in (("ec2", ec2(e, growth, mix)), ("accounts", accounts(e, accts, svcs)), ("services", services(e, svcs)),
                            ("data", data_area(e, services_cost, types)), ("network", network_area(e, services_cost, dts)),
                            ("genai", genai_area(e, kp, mix, services_cost)), ("finops", finops_area(e, kp, ops, sp)),
                            ("security", security_area(e, services_cost))):
            for i, (sev, text) in enumerate(items):
                rows.append({"entity": e, "area": area, "order": i, "severity": sev, "text": text})
    json.dump(rows, open(os.path.join(d, "insights.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print("insights", len(rows))


if __name__ == "__main__":
    main(sys.argv[1])
