"""Reconcile the normalized CUR (Task 6 output) with itself and with the COT months it overlaps; write checks.json.

main(norm_dir, out_data_dir, acct, cot_spend_path=None) -> 0 when every row is ok, else 1.
CLI: python3 -I check.py <norm_dir> <out_data_dir> <acct> [cot_spend_path]
checks.json rows: {entity, name, got, want, ok}; want is null when the row has no numeric target.
"""
import json, os, shutil, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import normalize  # noqa: E402

TOL = 0.01  # CUR vs COT month: 1% of the COT figure
SUM_TOL = 0.01  # dollars: float summation order


def _r(x):
    return round(float(x), 6)


def _row(entity, name, got, want, ok):
    return {"entity": entity, "name": name, "got": got, "want": want, "ok": bool(ok)}


def _cot_months(path, acct):
    if not path or not os.path.isfile(path):
        return {}
    with open(path, encoding="utf-8") as f:
        rows = json.load(f)
    out = {}
    for r in rows:
        if r.get("entity") == acct and r.get("month"):
            out[r["month"]] = out.get(r["month"], 0.0) + float(r.get("spend") or 0)
    return out


def main(norm_dir, out_data_dir, acct, cot_spend_path=None):
    con, tmp = normalize.open_norm(norm_dir)
    try:
        src = "read_parquet(%s)" % normalize._lit(os.path.join(os.path.abspath(norm_dir), "part-*.parquet"))
        t = normalize.trim_ws("currency")
        n_cur = con.execute("SELECT count(DISTINCT %s) FROM %s WHERE NULLIF(%s, '') IS NOT NULL" % (t, src, t)).fetchone()[0]
        by_month = {m: float(c) for m, c in con.execute(
            "SELECT month, sum(cost) FROM %s GROUP BY month ORDER BY month" % src).fetchall()}
        total = float(con.execute("SELECT COALESCE(sum(cost), 0) FROM %s" % src).fetchone()[0])
    finally:
        con.close()
        shutil.rmtree(tmp, ignore_errors=True)
    months_sum = sum(by_month.values())
    checks = [
        _row(acct, "single_currency", n_cur, 1, n_cur <= 1),
        _row(acct, "period_detected", len(by_month), None, len(by_month) > 0),
        _row(acct, "sum_matches_months", _r(total), _r(months_sum), abs(total - months_sum) <= SUM_TOL),
    ]
    cot = _cot_months(cot_spend_path, acct)
    for m in sorted(set(by_month) & set(cot)):
        got, want = by_month[m], cot[m]
        checks.append(_row(acct, "cot_overlap:" + m, _r(got), _r(want), abs(got - want) <= TOL * abs(want)))
    os.makedirs(out_data_dir, exist_ok=True)
    with open(os.path.join(out_data_dir, "checks.json"), "w", encoding="utf-8") as f:
        json.dump(checks, f, ensure_ascii=False, indent=1)
    for c in checks:
        print("  %-24s %s" % (c["name"], "OK" if c["ok"] else "MISMATCH"))
    return 0 if all(c["ok"] for c in checks) else 1


if __name__ == "__main__":
    if len(sys.argv) not in (4, 5):
        sys.exit("usage: check.py <norm_dir> <out_data_dir> <acct> [cot_spend_path]")
    sys.exit(main(*sys.argv[1:]))
