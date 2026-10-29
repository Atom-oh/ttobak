"""Account-team briefing: ranked actions (what / how much / who / next step) and a cross-entity benchmark.

Usage: python3 -I briefing.py <dashboard data dir>
Reads datasets written earlier in the pipeline (kpis, services, ec2_mix, ec2_type_growth, accounts, focus,
unit_metrics, modernization, elasticity, commitments, sp_purchase, storage_tiers, coh_actions,
coh_by_workload, period) and writes briefing.json and benchmark.json.
Every number is copied or summed from those files; nothing here is estimated by hand.
"""
import collections, hashlib, json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import entities  # noqa: E402
import period as P  # noqa: E402

EC2 = "Amazon Elastic Compute Cloud - Compute"
EXPIRY_DAYS = 120
SHORT = {EC2: "EC2", "EC2 - Other": "EC2 기타(EBS·NAT 등)", "Amazon Relational Database Service": "RDS",
         "Amazon Simple Storage Service": "S3", "Amazon Virtual Private Cloud": "VPC", "Amazon Elastic Load Balancing": "ELB"}


def short(s):
    return SHORT.get(s, s.replace("Amazon ", "").replace("AWS ", ""))


def usd(v):
    v = float(v or 0)
    s = "−" if v < 0 else ""
    a = abs(v)
    if a >= 1e6:
        return f"{s}${a / 1e6:.2f}M"
    if a >= 1e3:
        return f"{s}${a / 1e3:.1f}K"
    return f"{s}${a:,.0f}"


def pct(v, digits=0):
    return f"{v:.{digits}f}%"


def load(d, n):
    p = os.path.join(d, n + ".json")
    return json.load(open(p, encoding="utf-8")) if os.path.exists(p) else []


def by_entity(rows):
    out = collections.defaultdict(list)
    for r in rows:
        out[r["entity"]].append(r)
    return out


def per(e, D):
    """An entity's months (period.json; else COT_MONTHS; else the months in its data) with label() and the first slot key."""
    rows = D.get("period", {}).get(e, [])
    seen = [r["month"] for n in ("unit_metrics", "ec2_mix") for r in D.get(n, {}).get(e, []) if r.get("month")]
    p = P.for_entity(rows, e, seen)
    lab = p["labels"]
    return {**p, "lab": lambda m: lab.get(m, m or ""), "fk": "m%d" % (P.N - len(p["months"]) + 1) if p["months"] else "m1"}


def act(kind, owner, title, impact, evidence, next_step, tab, impact_label):
    return {"kind": kind, "owner": owner, "title": title, "impact": round(impact, 2), "impact_label": impact_label,
            "evidence": [x for x in evidence if x], "next_step": next_step, "tab": tab}


def sp_purchase_action(e, D):
    recs = [r for r in D["sp_purchase"][e] if r["plan"] == "COMPUTE_SP"]
    base = next((r for r in recs if r["term"] == "ONE_YEAR" and r["payment"] == "NO_UPFRONT"), None)
    if not base or base["monthly_savings"] < 1000:
        return None
    top = max(recs, key=lambda r: r["monthly_savings"])
    um = {r["month"]: r for r in D["unit_metrics"][e] if r["service"] == "EC2"}
    mix = {r["month"]: r for r in D["ec2_mix"][e]}
    p = per(e, D)
    L, F = p["lab"](p["last"]), p["lab"](p["first"])
    last = mix.get(p["last"])
    od_share = last["by_purchase"].get("OD", 0) / last["total"] * 100 if last and last["total"] else None
    soon = [r for r in D["commitments"][e] if r["kind"] == "SP" and 0 <= r["days_left"] <= EXPIRY_DAYS]
    renew = (f"이 권고는 현재 약정을 전제로 합니다. {EXPIRY_DAYS}일 안에 끝나는 SP {len(soon)}건(시간당 ${sum(r['hourly'] for r in soon):,.2f})을 "
             "갱신하지 않으면 온디맨드가 그만큼 더 늘어나므로 갱신분과 합쳐 계획해야 합니다.") if soon else ""
    return act("절감", "TAM·AM", f"Compute Savings Plans 추가 약정: 월 {usd(base['monthly_savings'])} 절감 (1년·선결제 없음)",
               base["monthly_savings"],
               [f"AWS 권고 약정 시간당 ${base['hourly_commit']:,.2f}. 현재 온디맨드 평균 시간당 ${base['od_hourly']:,.2f}.",
                f"{L} EC2 비용 중 온디맨드 {pct(od_share)}, EC2 약정 커버리지 {pct(um[p['last']]['coverage_pct'], 1)} ({F} {pct(um[p['first']]['coverage_pct'], 1)})."
                if od_share is not None and p["last"] in um and p["first"] in um else "",
                f"3년·{ {'ALL_UPFRONT': '전액 선결제', 'PARTIAL_UPFRONT': '부분 선결제', 'NO_UPFRONT': '선결제 없음'}[top['payment']] }이면 월 {usd(top['monthly_savings'])}까지 (선결제 {usd(top['upfront'])})."
                if top is not base else "", renew],
               "약정 기간·선결제 여력을 고객 재무팀과 확인하고, 만료 예정 약정과 묶어 한 번에 구매 계획을 잡습니다.", "savings", "월 절감")


def expiry_action(e, D):
    soon = [r for r in D["commitments"][e] if 0 <= r["days_left"] <= EXPIRY_DAYS]
    if not soon:
        return None
    sp = [r for r in soon if r["kind"] == "SP"]
    ri = [r for r in soon if r["kind"] == "RI"]
    at_risk = sum(r["monthly_net_savings"] or 0 for r in sp)
    commit = sum(r["monthly_commit"] for r in soon)
    first = min(soon, key=lambda r: r["days_left"])
    months = collections.Counter(r["end"][:7] for r in soon)
    # SP and RI are stated separately: RI rows carry no net-savings figure, so the SP amount is not the whole exposure
    parts = [f"SP 미갱신 시 월 {usd(at_risk)} 증가"] if sp else []
    if ri:
        parts.append(f"RI {len(ri)}건({sum(r['count'] or 0 for r in ri)}대, 월 약정액 {usd(sum(r['monthly_commit'] for r in ri))})")
    title = f"{EXPIRY_DAYS}일 안에 약정 {len(soon)}건 만료: " + (" · ".join(parts) or f"월 약정액 {usd(commit)}")
    # family-locked plans ending on a family that is also a top Graviton candidate: renewal decides the migration
    locked = collections.defaultdict(float)
    for r in sp:
        if r["type"] == "EC2InstanceSavingsPlans" and r["detail"] != "Any":
            locked[r["detail"]] += r["hourly"]
    grav = collections.defaultdict(lambda: [0.0, ""])
    for r in D["modernization"][e]:
        if r["kind"] == "Graviton":
            grav[r["family"]][0] += r["monthly_cost"]; grav[r["family"]][1] = r["target"]
    overlap = sorted((f for f in locked if f in grav), key=lambda f: -grav[f][0])
    return act("위험", "TAM·AM", title, max(at_risk, commit if at_risk < 100 else 0),
               [", ".join(x for x in (f"Savings Plans {len(sp)}건(시간당 ${sum(r['hourly'] for r in sp):,.2f})" if sp else "",
                                      f"RI {len(ri)}건(인스턴스 {sum(r['count'] or 0 for r in ri)}대)" if ri else "") if x) + ".",
                f"가장 빠른 만료 {first['end']} ({first['days_left']}일 후): {first['type']} {first['detail']} · {first['account_name']}.",
                "만료가 몰린 달: " + ", ".join(f"{m} {n}건" for m, n in sorted(months.items())) + ".",
                ("만료되는 EC2 Instance SP가 묶인 계열 " + ", ".join(f"{f}(시간당 ${locked[f]:,.2f}, 이 계열 월 비용 {usd(grav[f][0])}→{grav[f][1]} 전환 대상)" for f in overlap[:3])
                 + "은 Graviton 전환 상위 계열입니다. 같은 계열로 재약정하면 전환이 1~3년 늦어집니다.") if overlap else "",
                "SP 미갱신 시 증가액은 지난 6개월 순절감액의 월평균입니다. RI는 순절감 데이터가 없어 월 약정액만 표시합니다."],
               "만료 30일 전까지 갱신 범위를 확정합니다. Graviton 전환 대상이 있으면 Instance SP·Standard RI 대신 Compute SP로 갱신해 전환 여지를 남깁니다.",
               "savings", "월 영향")


def locked_note(e, D):
    """Graviton-eligible spend held by family-locked commitments, and when the first of those locks ends."""
    wait = [r for r in D["modernization"][e] if r["kind"] == "Graviton" and r["path"] == "Requires Commitment Wait"]
    cost = sum(r["monthly_cost"] for r in wait)
    if cost < 1000:
        return ""
    fams = {r["family"] for r in wait}
    # SP detail is the family (c7i); RI detail is "<type> <platform>" (db.r6i.4xlarge Aurora -> db.r6i)
    fam_of = lambda r: r["detail"] if r["kind"] == "SP" else r["detail"].split(" ")[0].rsplit(".", 1)[0]
    ends = sorted(r["end"] for r in D["commitments"][e] if r["days_left"] >= 0 and fam_of(r) in fams)
    when = f" 해당 계열 약정의 첫 만료는 {ends[0]}." if ends else ""
    return (f"별도로 월 {usd(cost)}({len(wait)}개 유형)는 Standard RI·EC2 Instance SP에 묶여 지금 전환하면 약정이 남습니다. "
            f"만료 시점에 맞춰 전환을 계획하세요.{when}")


def graviton_action(e, D):
    rows = [r for r in D["modernization"][e] if r["kind"] == "Graviton" and r["path"] == "Immediately Migratable"]
    total = sum(r["est_savings"] for r in rows)
    if total < 1000:
        return None
    fam = collections.defaultdict(lambda: [0.0, 0.0, ""])
    for r in rows:
        f = fam[(r["family"], r["service"])]
        f[0] += r["monthly_cost"]; f[1] += r["est_savings"]; f[2] = r["target"]
    top = sorted(fam.items(), key=lambda kv: -kv[1][1])[:3]
    wl = [r for r in D["coh_by_workload"][e] if r["action"] == "MigrateToGraviton" and not r["workload"].startswith("(")][:3]
    mix = {r["month"]: r for r in D["ec2_mix"][e]}
    p = per(e, D)
    g = lambda m: mix[m]["by_arch"].get("graviton", 0) / mix[m]["total"] * 100 if m in mix and mix[m]["total"] else 0
    return act("절감", "SA", f"Graviton 전환: 즉시 전환 가능한 대상만으로 월 {usd(total)} 절감 추정", total,
               ["상위 계열: " + ", ".join(f"{k[0]}→{v[2]} ({k[1]}, 월 비용 {usd(v[0])}, 절감 {usd(v[1])})" for k, v in top) + ".",
                f"EC2 비용 중 Graviton 비중 {p['lab'](p['first'])} {pct(g(p['first']))} → {p['lab'](p['last'])} {pct(g(p['last']))}.",
                ("COH가 전환을 권고한 워크로드(태그): " + ", ".join(f"{w['workload']} {usd(w['est_savings'])}/월({w['recs']}건)" for w in wl) + ".") if wl else "",
                locked_note(e, D),
                "COT 기준 25% 절감 가정이며 약정(Standard RI·EC2 Instance SP)에 묶인 인스턴스는 제외했습니다."],
               "절감액 상위 워크로드 1~2개로 Graviton PoC(빌드 파이프라인 멀티 아키텍처, 성능 비교)를 제안합니다.", "savings", "월 절감")


def mom_driver_action(e, D):
    k = next(r for r in D["kpis"][e])
    sv = D["services"][e]
    dv = lambda r: (r["m6"] or 0) - (r["m5"] or 0)  # noqa: E731  last − previous month
    d = sorted(((r["service"], dv(r)) for r in sv), key=lambda x: -abs(x[1]))
    total = k["last_spend"] - k["prev_spend"]
    p = per(e, D)
    if abs(total) < 0.02 * k["prev_spend"] or not p["prev"]:  # no previous month: nothing to compare
        return None
    same = [x for x in d if (x[1] > 0) == (total > 0)][:3]
    L = p["lab"](p["last"])
    ev = [f"{p['lab'](p['prev'])} {usd(k['prev_spend'])} → {L} {usd(k['last_spend'])} ({'+' if total > 0 else ''}{usd(total)}"
          + (f", {k['mom_pct']:+.1f}%" if k["mom_pct"] is not None else "") + ").",  # null: the previous month was zero
          "변화가 큰 서비스: " + ", ".join(f"{short(s)} {'+' if v > 0 else ''}{usd(v)}" for s, v in same) + "."]
    if same and same[0][0] == EC2:
        ty = sorted(D["ec2_type_growth"][e], key=lambda r: -dv(r) * (1 if total > 0 else -1))[:3]
        ev.append("EC2 안에서는 " + ", ".join(f"{r['instance_type']} {'+' if dv(r) > 0 else ''}{usd(dv(r))}" for r in ty) + ".")
    share = same[0][1] / total * 100 if same and total else 0
    word = "증가" if total > 0 else "감소"
    lead = (f"{short(same[0][0])}가 {pct(share)}" if share <= 100 else f"가장 큰 요인은 {short(same[0][0])} ({usd(same[0][1])})") if same else ""
    return act("변화", "SA·AM", f"{L} 지출 {word} {usd(abs(total))}" + (f": {lead}" if lead else ""),
               abs(total), ev, f"고객에게 {L} 변화의 원인(트래픽, 신규 출시, 배치 작업)을 확인하고 일시적인지 추세인지 구분합니다.", "entity", "월 변화")


def coverage_drop_actions(e, D):
    out = []
    by = collections.defaultdict(dict)
    for r in D["unit_metrics"][e]:
        by[r["service"]][r["month"]] = r
    p = per(e, D)
    f0, f1, F, L = p["first"], p["last"], p["lab"](p["first"]), p["lab"](p["last"])
    for svc, m in by.items():
        if f0 not in m or f1 not in m or svc == "EC2":
            continue
        a, b = m[f0]["coverage_pct"], m[f1]["coverage_pct"]
        uncovered = m[f1]["cost"] * (1 - b / 100)
        if a - b >= 5 and uncovered >= 1000:
            out.append(act("위험", "TAM", f"{svc} 약정 커버리지 {pct(a)} → {pct(b)}: 약정 없이 쓰는 비용 월 {usd(uncovered)}", uncovered,
                           [f"{L} {svc} 비용 {usd(m[f1]['cost'])}, 인스턴스 시간 {m[f1]['instance_hours']:,.0f} ({F} {m[f0]['instance_hours']:,.0f}).",
                            f"시간당 단가 ${m[f1]['cost_per_inst_hr']:.4f} ({F} ${m[f0]['cost_per_inst_hr']:.4f}). 노드가 늘었는데 RI를 추가하지 않았다는 뜻입니다.",
                            f"약정 없이 쓰는 비용 = {L} 비용 × (1 − 커버리지)로 계산한 추정치입니다."],
                           f"늘어난 {svc} 노드 유형을 확인하고 RI(또는 Database Savings Plans) 추가 구매를 권합니다.", "unit", "월 노출"))
    return out


def elasticity_action(e, D):
    last = [r for r in D["elasticity"][e] if r["month"] == per(e, D)["last"]]
    sav = sum(r["savings_at_goal"] for r in last)
    if sav < 1000:
        return None
    ev = [f"{r['service']} 평일 하루 {usd(r['weekday_avg'])} vs 주말 {usd(r['weekend_avg'])} → 탄력성 {pct(r['elasticity_pct'], 1)} (목표 20%)." for r in last]
    return act("절감", "SA", f"주말 스케일다운: 탄력성 20%를 맞추면 월 {usd(sav)} 절감", sav, ev + ["온디맨드 비용 기준이며 COT360의 주말 탄력성 계산(목표 20%, 월 8일)과 같습니다."],
               "개발·스테이징 계정부터 Instance Scheduler나 Karpenter 통합(consolidation)으로 주말 축소를 제안합니다.", "unit", "월 절감")


def efficiency_action(e, D):
    acts = {r["action"]: r for r in D["coh_actions"][e]}
    keys = ["Delete", "Rightsize", "Stop", "Upgrade"]
    total = sum(acts[k]["est_savings"] for k in keys if k in acts)
    if total < 1000:
        return None
    wl = [r for r in D["coh_by_workload"][e] if r["action"] in keys and not r["workload"].startswith("(")][:3]
    return act("절감", "TAM", f"정리·라이트사이징 (COH): 월 {usd(total)}, 대부분 작업량이 작음", total,
               ["액션별: " + ", ".join(f"{acts[k]['action_ko']} {usd(acts[k]['est_savings'])}({acts[k]['recs']}건)" for k in keys if k in acts and acts[k]["est_savings"] > 0) + ".",
                ("워크로드별 상위: " + ", ".join(f"{w['workload']} {w['action_ko']} {usd(w['est_savings'])}" for w in wl) + ".") if wl else "",
                "COH 중복 제거 요약값입니다."],
               "유휴 EBS·RDS 삭제 목록을 팀별로 나눠 전달하고, 다음 정기 리뷰에서 처리 결과를 확인합니다.", "savings", "월 절감")


def growth_actions(e, D):
    out = []
    p = per(e, D)
    v = lambda r, k: r[k] or 0  # noqa: E731  (a slot outside a short period is null)
    fk = p["fk"]
    acc = sorted([r for r in D["accounts"][e] if r["kind"] in ("신규", "급증")], key=lambda r: -(v(r, "m6") - v(r, fk)))[:3]
    if acc:
        gain = sum(v(r, "m6") - v(r, fk) for r in acc)
        out.append(act("성장", "SA·AM", f"빠르게 크는 계정 {len(acc)}개: {p['lab'](p['first'])} 대비 월 +{usd(gain)}", gain,
                       [f"{r['name'] or r['account']}({r['kind']}): {usd(v(r, fk))} → {usd(v(r, 'm6'))}"
                        + (f", 태그 워크로드 {', '.join(t for t in r['services_by_tag'][:3] if t != '—')}" if any(t != '—' for t in r['services_by_tag']) else ", COH 리소스 태그 없음(고객 확인 필요)")
                        + (f", 리전 {', '.join(r['regions'][:2])}" if r['regions'] else "") + "." for r in acc],
                       "새 워크로드의 아키텍처 리뷰(Well-Architected)를 제안하고 초기부터 Graviton·Savings Plans 적용을 설계에 넣습니다.", "accounts", "월 증가"))
    for f in D["focus"][e]:
        if f["confidence"] == "높음":
            out.append(act("성장", "SA", "고객 집중 영역: " + f["focus"], 0, [f["evidence"]],
                           "관련 AWS 신규 기능·사례를 시장 동향 탭에서 골라 다음 미팅 주제로 씁니다.", "market", ""))
    return out


def concentration_action(e, D):
    k = D["kpis"][e][0]
    if (k["last_spend"] or 0) <= 0:  # no latest spend: the share is undefined, not an action
        return None
    share = k["top_service_last"] / k["last_spend"] * 100
    if share < 25 or k["top_service"] == EC2:
        return None
    s = next((r for r in D["services"][e] if r["service"] == k["top_service"]), None)
    p = per(e, D)
    L = p["lab"](p["last"])
    trend = f"{p['lab'](p['first'])} {usd(s[p['fk']] or 0)} → {L} {usd(s['m6'] or 0)}" if s else ""
    return act("가격", "AM", f"{short(k['top_service'])}가 {L} 지출의 {pct(share)} ({usd(k['top_service_last'])})", k["top_service_last"],
               [trend + ".", "단일 서비스 비중이 큰 경우 사용량 기반 가격 협상(Private Pricing)과 아키텍처 최적화가 가장 큰 레버입니다."],
               "트래픽 전망과 함께 CloudFront 사설 요금(PPA)·보안 번들·정액 요금제 적용 가능성을 검토합니다." if "CloudFront" in k["top_service"]
               else "사용량 전망을 받아 가격 협상 범위를 정합니다.", "entity", f"{L} 지출")


def genai_action(e, D):
    k = D["kpis"][e][0]
    p = per(e, D)
    L = p["lab"](p["last"])
    if k["gpu_ec2_last"] < 5000 or k["gpu_ec2_last"] < 10 * max(k["bedrock_last"], 1):
        return None
    return act("성장", "SA", f"GPU EC2 월 {usd(k['gpu_ec2_last'])} vs Bedrock {usd(k['bedrock_last'])}: 자체 추론 비용 비교 기회", k["gpu_ec2_last"],
               [f"GPU·가속기 인스턴스 {L} {usd(k['gpu_ec2_last'])}, Bedrock {L} {usd(k['bedrock_last'])} (주요 모델 {k['bedrock_top_model'] or '—'}).",
                "자체 GPU 추론과 관리형 모델(Bedrock, 배치 추론, 프로비저닝 처리량)을 같은 워크로드로 비교하면 세션 주제가 됩니다."],
               "GPU 워크로드 목록(학습/추론, 모델 크기)을 받아 Bedrock·Inferentia·SageMaker 비교안을 준비합니다.", "bedrock", "GPU 월 비용")


def storage_action(e, D):
    p = per(e, D)
    last = [r for r in D["storage_tiers"][e] if r["month"] == p["last"]]
    gp2 = sum(r["cost"] for r in last if r["service"] == "EBS" and r["tier"].startswith("gp2"))
    if gp2 * 0.2 < 300:
        return None
    return act("절감", "TAM", f"gp2 → gp3 전환: 월 {usd(gp2 * 0.2)} 절감", gp2 * 0.2,
               [f"{p['lab'](p['last'])} gp2 볼륨 비용 {usd(gp2)}. gp3는 기본 20% 저렴하고 3,000 IOPS가 포함됩니다."],
               "무중단 볼륨 수정(ModifyVolume)으로 전환 가능함을 안내합니다.", "unit", "월 절감")


def overlap_line(e, D, sp):
    """Compute SP and the EC2 part of Graviton savings price the same on-demand EC2 usage, so they must not be summed.
    Compute SP does not cover RDS/ElastiCache/OpenSearch, so only EC2 Graviton rows overlap."""
    ec2 = sum(r["est_savings"] for r in D["modernization"][e]
              if r["kind"] == "Graviton" and r["path"] == "Immediately Migratable" and r["service"] == "EC2")
    if ec2 < 1:
        return ""
    return (f"Compute SP 구매 절감(월 {usd(sp['impact'])})과 Graviton 전환 절감 중 EC2 부분(월 {usd(ec2)})은 같은 온디맨드 EC2 사용량을 대상으로 한 추정이라 "
            "합산하지 않습니다. 하나를 먼저 실행하면 다른 쪽의 절감 기준이 줄어듭니다.")


def action_id(row):
    body = {k: v for k, v in row.items() if k not in ("rank", "id")}
    return hashlib.sha1(json.dumps(body, ensure_ascii=False, sort_keys=True).encode("utf-8")).hexdigest()[:12]


def benchmark(e, D):
    k = D["kpis"][e][0]
    um = {(r["service"], r["month"]): r for r in D["unit_metrics"][e]}
    mix = {r["month"]: r for r in D["ec2_mix"][e]}
    p = per(e, D)
    f0, f1 = p["first"], p["last"]
    last = mix.get(f1, {"total": 0, "by_arch": {}, "by_purchase": {}})
    el = {r["service"]: r for r in D["elasticity"][e] if r["month"] == f1}
    soon = [r for r in D["commitments"][e] if 0 <= r["days_left"] <= EXPIRY_DAYS]
    sp1 = next((r for r in D["sp_purchase"][e] if r["plan"] == "COMPUTE_SP" and r["term"] == "ONE_YEAR" and r["payment"] == "NO_UPFRONT"), None)
    grav = sum(r["est_savings"] for r in D["modernization"][e] if r["kind"] == "Graviton")
    g = lambda s, m, f: um[(s, m)][f] if (s, m) in um else None
    tot = last["total"] or 0
    return {"entity": e, "last_spend": k["last_spend"], "mom_pct": k["mom_pct"],
            "ec2_last": g("EC2", f1, "cost"),
            "ec2_vcpu_hr": g("EC2", f1, "cost_per_vcpu_hr"), "ec2_vcpu_hr_first": g("EC2", f0, "cost_per_vcpu_hr"),
            "ec2_coverage": g("EC2", f1, "coverage_pct"), "rds_coverage": g("RDS", f1, "coverage_pct"),
            "rds_coverage_first": g("RDS", f0, "coverage_pct"),
            "graviton_share": round(last["by_arch"].get("graviton", 0) / tot * 100, 1) if tot else None,
            "od_share": round(last["by_purchase"].get("OD", 0) / tot * 100, 1) if tot else None,
            "elasticity_ec2": el["EC2"]["elasticity_pct"] if "EC2" in el else None,
            "coh_dedup": k["coh_dedup"], "sp_buy_1y": sp1["monthly_savings"] if sp1 else 0,
            "graviton_savings": round(grav, 2),
            "expiring_n": len(soon), "expiring_at_risk": round(sum(r["monthly_net_savings"] or 0 for r in soon), 2),
            "next_expiry": min((r["end"] for r in soon), default=None),
            "top_service": k["top_service"], "top_share": round(k["top_service_last"] / k["last_spend"] * 100, 1) if (k["last_spend"] or 0) > 0 else None,
            "bedrock_last": k["bedrock_last"], "gpu_ec2_last": k["gpu_ec2_last"]}


def main(d):
    names = ["kpis", "services", "ec2_mix", "ec2_type_growth", "accounts", "focus", "unit_metrics", "modernization", "elasticity",
             "commitments", "sp_purchase", "storage_tiers", "coh_actions", "coh_by_workload", "period"]
    D = {n: by_entity(load(d, n)) for n in names}
    ents = entities.present(load(d, "kpis"))
    brief, bench = [], []
    for e in ents:
        if not D["kpis"][e]:
            continue
        sp, gr = sp_purchase_action(e, D), graviton_action(e, D)
        line = overlap_line(e, D, sp) if sp and gr else ""
        if line:
            sp["evidence"].append(line); gr["evidence"].append(line)
        items = [sp, expiry_action(e, D), gr, mom_driver_action(e, D),
                 elasticity_action(e, D), efficiency_action(e, D), concentration_action(e, D), genai_action(e, D), storage_action(e, D)]
        items += coverage_drop_actions(e, D) + growth_actions(e, D)
        items = [x for x in items if x]
        # money first (savings, risk, price), then change/growth signals; focus notes (impact 0) last
        order = {"절감": 0, "위험": 0, "가격": 1, "변화": 2, "성장": 2}
        items.sort(key=lambda x: (x["impact"] == 0, order[x["kind"]], -x["impact"]))
        for i, x in enumerate(items):
            row = {"entity": e, "rank": i + 1, **x}
            brief.append({**row, "id": action_id(row)})
        bench.append(benchmark(e, D))
    for n, rows in (("briefing", brief), ("benchmark", bench)):
        with open(os.path.join(d, n + ".json"), "w", encoding="utf-8") as f:
            json.dump(rows, f, ensure_ascii=False)
    for e in ents:
        print(e)
        for x in [b for b in brief if b["entity"] == e]:
            print(f"  {x['rank']:>2} [{x['kind']}/{x['owner']}] {x['title']}")


if __name__ == "__main__":
    main(sys.argv[1])
