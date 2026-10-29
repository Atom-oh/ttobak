"""Cross-check the computed datasets against the KPIs and the COT360 page totals; write checks.json.

Usage: cot360_check.py <data_dir> <extracted_dir>
Exit 0 when every check is ok, 1 otherwise (a failed check blocks publishing).
"""
import html, json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import entities  # noqa: E402
import period  # noqa: E402
import htmltext as HT  # noqa: E402


def _page_lines(path):
    """The page's text lines (linear and size-capped, see htmltext.py); an oversized page has none."""
    t = HT.page_text(path, "\n")
    return [] if t is None else [x.strip() for x in html.unescape(t).split("\n") if x.strip()]


def _after(lines, label, pick=lambda x: True):
    """The first line after label that pick accepts, or None when the label or such a line is missing."""
    try:
        i = lines.index(label)
    except ValueError:
        return None
    return next((x for x in lines[i + 1:] if pick(x)), None)


def _cost(r):
    """A raw row's cost: raw_export writes null for a nan/inf cell and omits an empty one; both count as 0."""
    v = r.get("cost")
    return float(v) if isinstance(v, (int, float)) and not isinstance(v, bool) else 0.0


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
    # every input entity (the run's, and any with raw costs or accounts) needs a KPI row, or it would get no check at all
    expected = set(entities.ENTITIES) | {r["entity"] for r in raw + acc if r.get("entity")}
    for e in sorted(expected - set(kp)):
        checks.append(_row(e, "kpis", "missing", "present", False))
    if not kp and not checks:  # nothing to reconcile is not a pass
        checks.append(_row("", "kpis", 0, "≥1 entity", False))
    for name, got_of in (("raw_service_costs", lambda e: sum(_cost(r) for r in raw if r.get("entity") == e and r.get("month") == last)),
                         ("accounts", lambda e: sum(r["last"] for r in acc if r["entity"] == e))):
        for e, want in kp.items():
            got = got_of(e)
            checks.append(_row(e, name, got, want, abs(got - want) <= max(1.0, want * 0.001)))
    for e in sorted(kp):
        lines = _page_lines(os.path.join(extracted_dir, e, f"COT_{e}_COT360.html"))
        def page(label):
            x = _after(lines, label, lambda v: v.startswith("$"))
            try:
                return float(x[1:].replace(",", "")) if x else None
            except ValueError:
                return None
        gp2 = sum(r["cost"] for r in st if r["entity"] == e and r["month"] == last and r["service"] == "EBS" and r["tier"].startswith("gp2"))
        for name, got, want in (
                ("modernization", sum(r["est_savings"] for r in md if r["entity"] == e) + 0.2 * gp2, page("Total Modernization Savings Est.")),
                ("elasticity", sum(r["savings_at_goal"] for r in el if r["entity"] == e and r["month"] == last), page("Potential Monthly Savings"))):
            checks.append(_row(e, name, got, want, want is not None and abs(got - want) <= 1.0))  # missing: mismatch
        sp = [r["end"] for r in cm if r["entity"] == e and r["kind"] == "SP" and r["days_left"] >= 0]
        got, want = (min(sp) if sp else "—"), _after(lines, "Next SP Expiry")
        checks.append(_row(e, "next SP expiry", got, want, got == want))
    with open(os.path.join(data_dir, "checks.json"), "w", encoding="utf-8") as f:
        json.dump(checks, f, ensure_ascii=False, indent=1)
    for c in checks:
        fmt = (lambda v: f"{v:>14,.2f}" if isinstance(v, float) else f"{'—' if v is None else v:>14}")
        print(f"  {c['name']:18} {c['entity']:15} {fmt(c['got'])} vs {fmt(c['want'])} {'OK' if c['ok'] else 'MISMATCH'}")
    return 0 if all(c["ok"] for c in checks) else 1


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: cot360_check.py <data_dir> <extracted_dir>")
    sys.exit(main(sys.argv[1], sys.argv[2]))
