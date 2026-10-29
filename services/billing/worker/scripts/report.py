"""The account-team report, generated from the datasets so it always matches the dashboard.

Usage: python3 report.py <dashboard data dir> <out.md> <dashboard url, may be empty>
Reads kpis, benchmark, briefing, the tag signals, the CUR datasets, agent_notes, sessions, market and the agent digest
sources. No network. Order: summary, business signals (service-tag items first), sessions, cost actions, the rest.
Agent text is shown only when its note is fresh (the entity passes agents.fresh_entities: source_fp matches the
current briefing and digest_fp the current digests; action_id matches an action), passes verify() again against that entity's own inputs, and is escaped as literal text. Generated
sessions and What's New rows are shown only while the entity's meta note is fresh; a What's New link is kept only
for an aws.amazon.com URL (title, date and URL are copied from the feed by code, never written by the model).
"""
import html, json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agents  # noqa: E402
from briefing import usd  # noqa: E402
import entities  # noqa: E402

LIMITS = [
    "COT 시트는 한 기준(서비스·계정·인스턴스 유형)으로만 나뉘어 있어 계정 × 유형 같은 교차 추이는 없습니다. 계정별 유형·태그는 COH 권고 리소스의 월 추정액입니다.",
    "태그는 COH 권고 리소스에만 있고, COH가 꺼진 법인은 태그가 없습니다.",
    "Bedrock은 modelId·호출 단위가 아니라 Marketplace 상품·사용 유형 단위입니다.",
    "절감 추정끼리는 같은 사용량을 대상으로 하는 경우가 있어 합산하지 않습니다(예: Compute SP 구매와 EC2 Graviton 전환).",
    "AI 해석은 계산된 데이터셋만 읽고, 문장 속 숫자·날짜·이름이 그 데이터에 있는 문장만 싣습니다. 원인·계획에 대한 해석은 고객 확인이 필요한 가설입니다.",
]
MD_SPECIAL = re.compile(r"([\\`*_\[\]|~#!<>])")  # brackets escaped + URLs removed, so "(" needs no escape
URL = re.compile(r"(?:[a-z][a-z0-9+.-]*://|www\.)\S+", re.I)
EMAIL = re.compile(r"[\w.+-]+@[\w-]+(?:\.[\w-]+)+")
AWS_URL = re.compile(r"https://aws\.amazon\.com/[A-Za-z0-9._~/-]*")
# peer-case links (web search): https, and no character that could end the Markdown link or start markup
WEB_URL = agents.FSI_URL
SIGNAL_LIMIT = ("태그 신호: CUR은 태그 값별 월 비용 추이(일부 날짜만 있는 달은 일평균으로 환산)로, COT는 추이가 없어 COH 권고 리소스의 "
                "생성일과 그 리소스가 있는 연결 계정의 추이로 추정합니다. COT 금액은 권고 리소스의 추정치라 서비스 전체 비용이 아닙니다.")
CUR_LIMIT = "CUR은 상각(Amortized) 비용으로 정규화한 값입니다. 태그 커버리지는 태그 키가 있는 비용의 비중이고, 리소스는 최근 월 비용 상위만 싣습니다."
NO_AI = "AI 해석 없음"


def esc(text):
    """Agent text as literal Markdown: no HTML, links (incl. GFM autolinks), images, tables or code spans."""
    plain = EMAIL.sub("(이메일 제거)", URL.sub("(링크 제거)", " ".join(str(text).split())))
    return MD_SPECIAL.sub(r"\\\1", html.escape(plain, quote=False))


def unescape(text):
    return html.unescape(re.sub(r"\\(.)", r"\1", text))


entity_index = agents.entity_index


def fresh_notes(D, e):
    fp = agents.source_fp(D, e)
    ids = {r["id"] for r in D.get("briefing", []) if r.get("entity") == e}
    ix = entity_index(D, e)
    aix = {a: agents.area_index(D, e, a) for a in agents.AREA_KEYS}
    ok = lambda t: isinstance(t, str) and agents.verify(t, ix)
    # findings were generated from one area's digest; re-check them against that same digest
    fok = lambda t, a: isinstance(t, str) and a in aix and agents.verify(t, aix[a])
    out = {"meta": None, "fresh": False, "headline": None, "actions": {}, "links": [], "findings": []}
    mine = [n for n in D.get("agent_notes", []) if n.get("entity") == e]
    # the same check as agents.fresh_entities (--prune): briefing ids AND the digests of the areas the meta note ran
    fresh = e in agents.fresh_entities(D, mine)
    for n in mine:
        if n["kind"] == "meta":
            out["meta"] = n
            out["fresh"] = fresh
            continue
        if not fresh or n.get("source_fp") != fp:
            continue
        if n["kind"] == "headline" and ok(n.get("text")):
            out["headline"] = n["text"]
        elif n["kind"] == "action" and n.get("action_id") in ids:
            step = n.get("next_step") if ok(n.get("next_step")) else None
            qs = [q for q in n.get("questions") or [] if ok(q)]
            if step or qs:
                out["actions"][n["action_id"]] = {"next_step": step, "questions": qs}
        elif n["kind"] == "link" and ok(n.get("title")) and ok(n.get("text")):
            out["links"].append(n)
        elif n["kind"] == "finding" and fok(n.get("title"), n.get("area")):
            a = n["area"]
            out["findings"].append({**n, "detail": n.get("detail") if fok(n.get("detail"), a) else None,
                                    "so_what": n.get("so_what") if fok(n.get("so_what"), a) else None})
    return out


def ko(D, e):
    return next((r.get("entity_ko", e) for r in D.get("kpis", []) if r["entity"] == e), e)


def last_label(D, ents):
    """The latest month's label when every entity ends on the same month, else a generic heading."""
    ps = [agents.P.for_entity(D.get("period", []), e) for e in ents]
    lasts = {p["last"] for p in ps}
    return ps[0]["labels"].get(ps[0]["last"], "최근 월") if len(lasts) == 1 and ps and ps[0]["last"] else "최근 월"


def summary(D, ents, no):
    out = [f"## {no}. 요약", "", f"| 법인 | {last_label(D, ents)} 지출 | 전월 대비 | 가장 큰 절감 항목 (월) | 가장 큰 위험 항목 (월) | 실행 항목 |", "|---|---:|---:|---|---|---:|"]
    for e in ents:
        k = next(r for r in D["kpis"] if r["entity"] == e)
        b = [r for r in D["briefing"] if r["entity"] == e]
        top = lambda kind: max((r for r in b if r["kind"] == kind), key=lambda r: r["impact"], default=None)
        cell = lambda r: f"{esc(r['title'].split(':')[0])} {usd(r['impact'])}" if r else "—"
        out.append(f"| {esc(ko(D, e))} | {usd(k['last_spend'])} | {'—' if k.get('mom_pct') is None else format(k['mom_pct'], '+.1f') + '%'} | {cell(top('절감'))} | {cell(top('위험'))} | {len(b)} |")
    return out


def entity_section(D, e):
    notes = fresh_notes(D, e)
    out = [f"### {esc(ko(D, e))}", ""]
    m = notes["meta"]
    if notes["headline"]:
        label = f"검증 통과 {m['checked'] - m['dropped']}/{m['checked']}" if m else "검증 통과"
        out += [f"**AI 요약** (AI 해석, {label}): {esc(notes['headline'])}", ""]
    for r in sorted((r for r in D["briefing"] if r["entity"] == e), key=lambda r: r["rank"]):
        lab = r.get("impact_label") or "영향"
        imp = f" — {lab} {usd(r['impact'])}" if r["impact"] else ""
        # COT-derived names (accounts, tags) land in titles and evidence: escape them like agent text
        out.append(f"{r['rank']}. **[{esc(r['kind'])} · {esc(r['owner'])}] {esc(r['title'])}**{imp}")
        out += [f"   - 근거: {esc(x)}" for x in r["evidence"]]
        n = notes["actions"].get(r["id"])
        if n and n["next_step"]:  # a verified agent step replaces the rule-based one
            out.append(f"   - AI 해석(숫자 검증) 다음 행동: {esc(n['next_step'])}")
        else:
            out.append(f"   - 다음 행동: {esc(r['next_step'])}")
        if n and n["questions"]:
            out.append("   - AI 해석(숫자 검증) 고객 질문: " + " / ".join(esc(q) for q in n["questions"]))
    if notes["links"]:
        out += ["", "**영역 간 연결 (AI 해석)**"]
        out += [f"- {esc(l['title'])}: {esc(l['text'])}" for l in notes["links"]]
    fins = [f for f in notes["findings"] if f["area"] not in ("cur", "business")]  # those sit in their own sections
    if fins:
        out += ["", "**영역별 발견 (AI 해석)**"]
        for f in fins:
            body = " ".join(esc(x) for x in (f["detail"], f["so_what"]) if x)
            out.append(f"- [{f['area']}·{esc(f.get('confidence', ''))}] {esc(f['title'])}" + (f": {body}" if body else ""))
    if m and m.get("errors"):
        out += ["", f"_AI 해석 일부 실패: {len(m['errors'])}건 (해당 영역은 규칙 기반 내용만 표시)_"]
    return out + [""]


BENCH_COLS = [("ec2_vcpu_hr", "EC2 $/vCPU시간", lambda v: f"${v:.4f}"), ("ec2_coverage", "EC2 약정 커버리지", lambda v: f"{v:.1f}%"),
              ("rds_coverage", "RDS 약정 커버리지", lambda v: f"{v:.1f}%"), ("od_share", "EC2 온디맨드 비중", lambda v: f"{v:.1f}%"),
              ("graviton_share", "Graviton 비중", lambda v: f"{v:.1f}%"), ("elasticity_ec2", "EC2 주말 탄력성", lambda v: f"{v:.1f}%"),
              ("sp_buy_1y", "SP 구매 권고 절감(월)", usd), ("expiring_at_risk", "120일 내 SP 만료 영향(월)", usd)]


def bench(D, ents, no):
    rows = {r["entity"]: r for r in D.get("benchmark", [])}
    out = [f"## {no}. 법인 비교", "", "| 지표 | " + " | ".join(esc(ko(D, e)) for e in ents) + " |", "|---|" + "---:|" * len(ents)]
    for k, name, f in BENCH_COLS:
        vals = [rows.get(e, {}).get(k) for e in ents]
        if all(v is None for v in vals):
            continue
        out.append(f"| {name} | " + " | ".join("—" if v is None else f(v) for v in vals) + " |")
    return out


def _signed_usd(v, fmt=usd):
    return ("+" if v >= 0 else "-") + fmt(abs(v))


def cur_money(D, e):
    """Amount format for an entity's CUR rows: $ for USD; another currency keeps its code, never a $ sign."""
    code = agents.unsupported_currency(D, e)
    return usd if code is None else (lambda v: usd(v).replace("$", "", 1) + " " + code)


def signals_section(D, ents, no):
    """Service-tag items that are new or growing (code-computed) and the business agent's verified reading of them."""
    out = [f"## {no}. 사업 신호 (서비스 태그 항목)", "",
           "고객이 새로 시작하거나 키우는 워크로드를 서비스 태그 항목 단위로 먼저 봅니다. 표는 코드가 계산했고, 가설은 AI 해석입니다.", ""]
    shown = False
    for e in ents:
        dg = agents.digest(D, e, "business")
        fins = [f for f in fresh_notes(D, e)["findings"] if f["area"] == "business"]
        if not (dg["cur_tag_signals"] or dg["tag_signals"] or fins):
            continue
        shown = True
        out += [f"### {esc(ko(D, e))}", ""]
        if dg["cur_tag_signals"]:
            money = cur_money(D, e)
            out += ["**CUR 태그 항목 (월별 추이)**", "", "| 태그 키 | 항목 | 구분 | 최근 월 | 이전 평균 대비 | 증가율 | 새 서비스 / 상위 서비스 |",
                    "|---|---|---|---:|---:|---:|---|"]
            for r in dg["cur_tag_signals"]:
                svcs = r.get("new_services") or [x["service"] for x in r.get("services") or []][:3]
                g = "신규" if r.get("growth_pct") is None else f"{r['growth_pct']:+.0f}%"
                # the whole value's change, as growth_pct: delta is only the new services' part on a new-service row
                out.append(f"| {esc(r['tag_key'])} | {esc(r['tag_value'])} | {esc(', '.join(r['kinds']))} | {money(r['run_rate_last'])} | "
                           f"{_signed_usd(round(r['run_rate_last'] - r['base'], 2), money)} | {g} | {esc(', '.join(svcs))} |")
            out.append("")
        if dg["tag_signals"]:
            out += ["**COT 태그 항목 (COH 리소스 스냅샷)**", "", "| 태그 키 | 항목 | 구분 | 월 추정 | 리소스 | 최근 생성 비중 | 증가 계정 |",
                    "|---|---|---|---:|---:|---:|---|"]
            for r in dg["tag_signals"]:
                pct = "—" if r.get("created_recent_pct") is None else f"{r['created_recent_pct']:.0f}%"
                accts = ", ".join(f"{a['name'] or a['account']}({a['kind']})" for a in r.get("account_trend") or []) or "—"
                out.append(f"| {esc(r['tag_key'])} | {esc(r['tag_value'])} | {esc(', '.join(r['kinds']))} | {usd(r['est_monthly_cost'])} | "
                           f"{r['resources']} | {pct} | {esc(accts)} |")
            out.append("")
        if fins:
            out += ["**사업 가설 (AI 해석)**"]
            for f in fins:
                tag = f" {esc(f['tag'])} —" if f.get("tag") else ""
                out.append(f"- [{esc(f.get('help') or '—')}·{esc(f.get('confidence', ''))}]{tag} {esc(f['title'])}")
                out += [f"   - {lab}: {esc(f[k])}" for k, lab in (("detail", "근거"), ("so_what", "도울 일")) if f.get(k)]
                qs = [q for q in f.get("questions") or [] if isinstance(q, str) and agents.verify(q, agents.area_index(D, e, "business"))]
                if qs:
                    out.append("   - 확인 질문: " + " / ".join(esc(q) for q in qs))
        out.append("")
    return out if shown else out + ["태그 신호 없음 (새로 생기거나 빠르게 느는 서비스 태그 항목이 없거나, 태그 데이터가 없습니다)", ""]


def sessions_section(D, ents, no):
    out = [f"## {no}. SA 세션 (AI 생성)", ""]
    for e in ents:
        if not fresh_notes(D, e)["fresh"]:
            continue
        ix = entity_index(D, e)
        ok = lambda t: isinstance(t, str) and agents.verify(t, ix)
        rows = sorted((r for r in D.get("sessions", []) if r.get("entity") == e and r.get("origin") == "generated" and ok(r.get("title"))),
                      key=lambda r: r.get("rank") or 0)
        if not rows:
            continue
        out += [f"### {esc(ko(D, e))}", ""]
        for i, r in enumerate(rows, 1):
            kind = f"[{esc(r['type'])}] " if r.get("type") in agents.HELP_TYPES else ""
            out.append(f"{i}. {kind}**{esc(r['title'])}**" + (f" — 대상: {esc(r['audience'])}" if ok(r.get("audience")) else ""))
            if ok(r.get("signal")) and r["signal"]:
                out.append(f"   - 돕는 사업 신호: {esc(r['signal'])}")
            if ok(r.get("why")):
                out.append(f"   - 왜 지금: {esc(r['why'])}")
            out += [f"   - 안건: {esc(a)}" for a in r.get("agenda") or [] if ok(a)]
        out.append("")
    return out if len(out) > 2 else out + [NO_AI, ""]


def market_section(D, ents, no):
    out = [f"## {no}. 시장 동향 (What's New)", ""]
    shown = False
    for e in ents:
        if not fresh_notes(D, e)["fresh"]:
            continue
        lines = []
        for r in D.get("market", []):
            if r.get("entity") != e or r.get("origin") != "generated" or r.get("kind") in ("note", "peer_case"):
                continue
            if not all(isinstance(r.get(k), str) for k in ("title", "date", "url", "why")):
                continue
            if not agents.verify(r["why"], agents.market_index(D, e, r)):
                continue
            title = f"[{esc(r['title'])}]({r['url']})" if AWS_URL.fullmatch(r["url"]) else esc(r["title"])
            lines.append(f"- {esc(r['date'])} {title}: {esc(r['why'])}")
        if lines:
            shown = True
            out += [f"### {esc(ko(D, e))}", ""] + lines + [""]
    return out if shown else out + [NO_AI, ""]


def fsi_section(D, ents, no):
    """Generated FSI peer cases (web search), re-verified against the published row; else the entity's note."""
    out = [f"## {no}. FSI 동종업계 사례 (웹 검색)", "",
           "검색어는 고객·계정·태그 이름이 빠진 일반 용어이고, 제목·링크·날짜는 검색 결과에서 코드가 옮깁니다. 외부 사이트이므로 열어서 확인하세요.", ""]
    for e in ents:
        mine = [r for r in D.get("market", []) if isinstance(r, dict) and r.get("entity") == e and r.get("origin") == "generated"]
        lines = []
        if fresh_notes(D, e)["fresh"]:
            for r in mine:
                if r.get("kind") != "peer_case" or not all(isinstance(r.get(k), str) for k in ("title", "date", "url", "org", "signal", "why")):
                    continue
                if not agents.verify(r["why"], agents.case_index(D, e, r)):
                    continue
                link = WEB_URL.fullmatch(r["url"])
                title = f"[{esc(r['title'])}]({r['url']})" if link else esc(r["title"])
                # show where an external link goes; the host is validated, so it is escaped without esc()'s URL removal
                host = " · " + MD_SPECIAL.sub(r"\\\1", html.escape(r["url"].split("/")[2], quote=False)) if link else ""
                lines.append(f"- {esc(r['org'])} — {title}{host}" + (f" ({esc(r['date'])})" if r["date"] else "") + f": {esc(r['why'])}")
                if r["signal"]:
                    lines.append(f"   - 돕는 사업 신호: {esc(r['signal'])}")
        if not lines and fresh_notes(D, e)["fresh"]:  # a stale entity shows nothing, as in market_section
            note = next((r["text"] for r in mine if r.get("kind") == "note" and isinstance(r.get("text"), str)), None)
            lines = [esc(note)] if note else []
        if lines:
            out += [f"### {esc(ko(D, e))}", ""] + lines + [""]
    return out if len(out) > 4 else out + [NO_AI, ""]


def cur_section(D, ents, no):
    out = [f"## {no}. CUR 분석", ""]
    for e in ents:
        dg = agents.digest(D, e, "cur")
        if not dg["cur_summary"]:
            continue
        s, money = dg["cur_summary"][0], cur_money(D, e)
        out += [f"### {esc(ko(D, e))}", "",
                f"- 기간 {esc(s.get('first_day') or '—')} ~ {esc(s.get('last_day') or '—')}, 형식 {esc(s.get('format') or '—')}, "
                f"통화 {esc(s.get('currency') or '—')}, 연결 계정 {s.get('linked_accounts', 0)}개, 행 {s.get('rows', 0):,}, "
                f"읽지 못해 제외한 행 {s.get('dropped_rows') or 0:,}", "", "| 월 | 비용 |", "|---|---:|"]
        out += [f"| {esc(m['month'])} | {money(m['cost'])} |" for m in s["cost_by_month"]]
        last = dg["months"][-1] if dg["months"] else None
        # ranked from the entity's full monthly rows: the digest keeps the services largest over all months, so a
        # service new and largest in the latest month would be missing from it
        top = sorted((r for r in D.get("cur_trend", []) if r.get("entity") == e and r.get("grain") == "month"
                      and r["dim"] == "service" and r["period"] == last), key=lambda r: (-r["cost"], r["service"]))[:5]
        if top:
            out += ["", "**상위 서비스 (최근 월)**: " + ", ".join(f"{esc(r['service'])} {money(r['cost'])}" for r in top)]
        if dg["cur_tag_coverage"]:
            out += ["", "| 태그 키 | 태그 있는 비용 | 비중 |", "|---|---:|---:|"]
            out += [f"| {esc(r['tag_key'])} | {money(r['tagged_cost'])} | {r['pct']:.1f}% |" for r in dg["cur_tag_coverage"]]
        if dg["cur_resources"]:
            out += ["", "| 비용 상위 리소스 | 서비스 | 최근 월 | 전월 |", "|---|---|---:|---:|"]
            out += [f"| {esc(r['resource_id'])} | {esc(r.get('service', ''))} | {money(r.get('cost_last'))} | {money(r.get('cost_prev'))} |"
                    for r in dg["cur_resources"][:5]]
        fins = [f for f in fresh_notes(D, e)["findings"] if f["area"] == "cur"]
        if fins:
            out += ["", "**CUR 발견 (AI 해석)**"]
            for f in fins:
                body = " ".join(esc(x) for x in (f["detail"], f["so_what"]) if x)
                out.append(f"- [{esc(f.get('confidence', ''))}] {esc(f['title'])}" + (f": {body}" if body else ""))
        out.append("")
    return out


def render(D, url):
    ents = entities.present(D.get("kpis", []))
    cur_ents = entities.present(D.get("cur_summary", []))
    out = ["# 빌링 인사이트 — 계정팀(SA·TAM·AM)용", "",  # no customer or entity names in the title
           f"- 자동 생성: `scripts/report.py`가 대시보드와 같은 데이터셋에서 만듭니다."
           + (f" 대시보드: {url} (TTOBAK 로그인)" if url else "")]
    if ents:
        out.append("- 원천: 법인별 COT.zip(Template·COH·COT360.html). 기준: Amortized 비용, EDP·PRC 할인·크레딧·환불·세금·Support 제외(COT360 KPI 기준).")
    if cur_ents:
        out.append("- 원천: 계정별 CUR 내보내기(정규화한 Amortized 비용).")
    out += ["- 숫자는 코드가 계산합니다. \"AI 해석\" 표시는 Bedrock 에이전트가 쓴 문장이고, 숫자·날짜·이름이 데이터셋과 맞는 문장만 실립니다.", ""]
    no = iter(range(1, 100))
    sig_ents = ents + [e for e in cur_ents if e not in ents]
    if ents:
        out += summary(D, ents, next(no)) + [""]
    # what the customer is building comes first; cost actions follow
    out += signals_section(D, sig_ents, next(no))
    if ents:
        out += sessions_section(D, ents, next(no)) + [f"## {next(no)}. 법인별 실행 항목 (비용)", ""]
        for e in ents:
            out += entity_section(D, e)
        out += market_section(D, ents, next(no))
        out += fsi_section(D, ents, next(no))
        out += bench(D, ents, next(no)) + [""]
    if cur_ents:
        out += cur_section(D, cur_ents, next(no))
    limits = (LIMITS if ents else LIMITS[-1:]) + [SIGNAL_LIMIT] + ([CUR_LIMIT] if cur_ents else [])
    out += [f"## {next(no)}. 데이터 한계", ""] + [f"- {x}" for x in limits]
    out += ["", f"## {next(no)}. 다시 만들기", "",
            "Billing 업로드 페이지에서 다시 실행(AI 재실행)하면 데이터셋, AI 해석, 이 보고서를 다시 만듭니다. "
            "`AGENTS=off`이면 모델을 부르지 않고, 입력이 바뀐 AI 해석은 지웁니다.", ""]
    return "\n".join(out)


def main(argv):
    if len(argv) != 3:
        print(__doc__)
        return 2
    d, out, url = argv
    D = agents.load(d)
    for n in ("agent_notes", "sessions", "market"):
        p = os.path.join(d, n + ".json")
        D[n] = json.load(open(p, encoding="utf-8")) if os.path.exists(p) else []
    with open(out, "w", encoding="utf-8") as f:
        f.write(render(D, url))
    print("wrote", out)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
