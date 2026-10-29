"""Cross-check the computed datasets against the KPIs and the COT360 page totals; write checks.json.

Usage: cot360_check.py <data_dir> <extracted_dir>
Exit 0 when every check is ok, 1 otherwise (a failed check blocks publishing).
"""
import html, json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import period  # noqa: E402


def _page_lines(path):
    s = re.sub(r"<script.*?</script>", "", open(path, encoding="utf-8").read(), flags=re.S)
    return [x.strip() for x in html.unescape(re.sub(r"<[^>]+>", "\n", s)).split("\n") if x.strip()]


def _row(entity, name, got, want, ok):
    return {"entity": entity, "name": name, "got": got, "want": want, "ok": bool(ok)}


def main(data_dir, extracted_dir):
    last = period.LAST
    if not last:
        print("no billing period (COT_MONTHS unset)", file=sys.stderr)
        return 1
    load = lambda n: json.load(open(os.path.join(data_dir, n + ".json"), encoding="utf-8"))
    kpis, raw, acc = load("kpis"), load("raw_service_costs"), load("accounts")
    md, el, st, cm = load("modernization"), load("elasticity"), load("storage_tiers"), load("commitments")
    checks = []
    kp = {r["entity"]: r["last_spend"] for r in kpis}
    for name, got_of in (("raw_service_costs", lambda e: sum(r["cost"] for r in raw if r["entity"] == e and r["month"] == last)),
                         ("accounts", lambda e: sum(r["last"] for r in acc if r["entity"] == e))):
        for e, want in kp.items():
            got = got_of(e)
            checks.append(_row(e, name, got, want, abs(got - want) <= max(1.0, want * 0.001)))
    for e in sorted(kp):
        lines = _page_lines(os.path.join(extracted_dir, e, f"COT_{e}_COT360.html"))
        page = lambda label: float(next(x for x in lines[lines.index(label) + 1:] if x.startswith("$"))[1:].replace(",", ""))
        gp2 = sum(r["cost"] for r in st if r["entity"] == e and r["month"] == last and r["service"] == "EBS" and r["tier"].startswith("gp2"))
        for name, got, want in (
                ("modernization", sum(r["est_savings"] for r in md if r["entity"] == e) + 0.2 * gp2, page("Total Modernization Savings Est.")),
                ("elasticity", sum(r["savings_at_goal"] for r in el if r["entity"] == e and r["month"] == last), page("Potential Monthly Savings"))):
            checks.append(_row(e, name, got, want, abs(got - want) <= 1.0))
        sp = [r["end"] for r in cm if r["entity"] == e and r["kind"] == "SP" and r["days_left"] >= 0]
        got, want = (min(sp) if sp else "—"), lines[lines.index("Next SP Expiry") + 1]
        checks.append(_row(e, "next SP expiry", got, want, got == want))
    with open(os.path.join(data_dir, "checks.json"), "w", encoding="utf-8") as f:
        json.dump(checks, f, ensure_ascii=False, indent=1)
    for c in checks:
        fmt = (lambda v: f"{v:>14,.2f}") if isinstance(c["got"], float) else (lambda v: f"{v:>14}")
        print(f"  {c['name']:18} {c['entity']:15} {fmt(c['got'])} vs {fmt(c['want'])} {'OK' if c['ok'] else 'MISMATCH'}")
    return 0 if all(c["ok"] for c in checks) else 1


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: cot360_check.py <data_dir> <extracted_dir>")
    sys.exit(main(sys.argv[1], sys.argv[2]))
