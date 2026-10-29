"""Datasets from the normalized CUR (Task 6 output): cur_summary, cur_trend, cur_tag_coverage, cur_resources,
cur_tag_signals (new or fast-growing service-tag values, scripts/tag_signals.py), period.

main(norm_dir, out_data_dir, acct, summary=None) -> None; summary is normalize.run's result (format, dropped_rows).
CLI: python3 -I datasets.py <norm_dir> <out_data_dir> <acct> [summary.json]
cur_trend has day grain for service and the top DAY_TAGS tag keys only and at most MAX_TREND_ROWS rows; rows cut
by the cap are counted in cur_summary.dropped (truncated: true). Every row carries `entity`. Numbers are rounded to 6 decimals, JSON is UTF-8, rows are sorted deterministically.
"""
import calendar, json, os, shutil, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "scripts"))
import normalize  # noqa: E402
import period  # noqa: E402
import tag_signals as TS  # noqa: E402
import tagkeys as TK  # noqa: E402

TOP_KEYS = 20
TOP_TAGS = 10
TOP_RESOURCES = 500
DAYS = 92
DAY_TAGS = 3  # day grain: service and the top DAY_TAGS tag keys only (month grain: every dim)
MAX_TREND_ROWS = 200000  # cur_trend.json stays well under the web task's 48 MiB per-file cap (tests lower it)
UNTAGGED = "(태그 없음)"
OTHER = "기타"
DIMS = ("service", "usage_type", "region", "linked_account")


def _r(x):
    return round(float(x), 6)


def _lit(s):
    return normalize._lit(s)


def _write(out_dir, name, rows):
    with open(os.path.join(out_dir, name + ".json"), "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False, indent=1)


def _summary(con, acct, summary):
    lo, hi, rows, accts, cur = con.execute(
        "SELECT min(usage_date), max(usage_date), count(*), count(DISTINCT linked_account), "
        "min(NULLIF(%s, '')) FROM n" % normalize.trim_ws("currency")).fetchone()
    by_month = {m: _r(c) for m, c in con.execute("SELECT month, sum(cost) FROM n GROUP BY month ORDER BY month").fetchall()}
    return {"entity": acct, "format": (summary or {}).get("format"), "currency": cur or "",
            "first_day": lo.isoformat() if lo else None, "last_day": hi.isoformat() if hi else None,
            "months": sorted(by_month), "rows": rows, "dropped_rows": (summary or {}).get("dropped_rows"),
            "linked_accounts": accts, "cost_by_month": by_month}


def _trend_rows(con, acct, grain, dim, keyexpr, fold):
    """One (grain, dim): costs per (period, service, key), the keys past the top TOP_KEYS per service folded into 기타."""
    top = TOP_KEYS if fold else 10 ** 9
    sql = """
        WITH agg AS (SELECT period, service, %(key)s AS k, sum(cost) AS cost FROM b GROUP BY ALL),
             tot AS (SELECT service, k, sum(cost) AS t FROM agg GROUP BY ALL),
             rk AS (SELECT service, k, row_number() OVER (PARTITION BY service ORDER BY t DESC, k) AS r FROM tot)
        SELECT agg.period, agg.service, CASE WHEN rk.r <= %(top)d THEN agg.k ELSE %(other)s END AS k, sum(agg.cost)
        FROM agg JOIN rk ON agg.service = rk.service AND agg.k = rk.k
        GROUP BY ALL
    """ % {"key": keyexpr, "top": top, "other": _lit(OTHER)}
    return [{"entity": acct, "grain": grain, "period": p, "dim": dim, "key": k, "service": s, "cost": _r(c)}
            for p, s, k, c in con.execute(sql).fetchall()]


def _tag_keys(con):
    return [k for (k,) in con.execute(
        "SELECT k FROM (SELECT unnest(map_keys(tags)) AS k, cost FROM n) GROUP BY k ORDER BY sum(cost) DESC, k LIMIT %d"
        % TOP_TAGS).fetchall()]


def _trend(con, acct, tag_keys, last_day):
    out = []
    for grain, per, where in (("month", "month", "TRUE"),
                              ("day", "strftime(usage_date, '%Y-%m-%d')",
                               "usage_date > DATE %s - INTERVAL %d DAY" % (_lit(last_day), DAYS) if last_day else "FALSE")):
        con.execute("CREATE OR REPLACE TEMP VIEW b AS SELECT %s AS period, service, usage_type, region, linked_account, "
                    "tags, cost FROM n WHERE %s" % (per, where))
        for dim in (DIMS if grain == "month" else ("service",)):
            out += _trend_rows(con, acct, grain, dim, "service" if dim == "service" else dim, dim != "service")
        for tk in (tag_keys if grain == "month" else tag_keys[:DAY_TAGS]):
            out += _trend_rows(con, acct, grain, "tag:" + tk,
                               "COALESCE(NULLIF(element_at(tags, %s)[1], ''), %s)" % (_lit(tk), _lit(UNTAGGED)), True)
    out.sort(key=lambda r: (r["grain"], r["dim"], r["service"], r["period"], r["key"]))
    return out


def _cap(rows, cap):
    """(rows, dropped): whole (grain, dim) groups are dropped until at most cap rows remain, day grain first, the
    groups with the most rows first (a tie goes against tag dims before service). Month groups only if day is gone."""
    if len(rows) <= cap:
        return rows, 0
    sizes = {}
    for r in rows:
        sizes[(r["grain"], r["dim"])] = sizes.get((r["grain"], r["dim"]), 0) + 1
    order = sorted(sizes, key=lambda g: (g[0] != "day", -sizes[g], g[1] == "service", g[1]))
    gone, n = set(), len(rows)
    for g in order:
        if n <= cap:
            break
        gone.add(g)
        n -= sizes[g]
    kept = [r for r in rows if (r["grain"], r["dim"]) not in gone]
    return kept, len(rows) - len(kept)


def _coverage(con, acct, tag_keys):
    out = []
    totals = dict(con.execute("SELECT month, sum(cost) FROM n GROUP BY month").fetchall())
    for tk in tag_keys:
        for m, c in con.execute("SELECT month, sum(cost) FROM n WHERE map_contains(tags, %s) GROUP BY month"
                                % _lit(tk)).fetchall():
            tot = totals.get(m) or 0.0
            out.append({"entity": acct, "month": m, "tag_key": tk, "tagged_cost": _r(c), "total_cost": _r(tot),
                        "pct": _r(c / tot * 100) if tot else 0.0})
    out.sort(key=lambda r: (r["month"], r["tag_key"]))
    return out


def _resources(con, acct, last):
    if not last:
        return []
    prev = period.shift(last, -1)
    sql = """
        SELECT resource_id, arg_max(service, cost) AS service, arg_max(usage_type, cost) AS usage_type,
               arg_max(region, cost) AS region, arg_max(linked_account, cost) AS linked_account,
               arg_max(tags, usage_date) AS tags,
               sum(cost) FILTER (WHERE month = %(last)s) AS cost_last,
               COALESCE(sum(cost) FILTER (WHERE month = %(prev)s), 0) AS cost_prev, sum(cost) AS cost_total
        FROM n WHERE resource_id <> ''
        GROUP BY resource_id
        HAVING count(*) FILTER (WHERE month = %(last)s) > 0
        ORDER BY cost_last DESC, resource_id
        LIMIT %(n)d
    """ % {"last": _lit(last), "prev": _lit(prev), "n": TOP_RESOURCES}
    out = []
    for rid, svc, ut, reg, la, tags, cl, cp, ct in con.execute(sql).fetchall():
        tg = dict(tags) if tags else {}
        out.append({"entity": acct, "resource_id": rid, "service": svc, "usage_type": ut, "region": reg,
                    "linked_account": la, "tags": dict(sorted(tg.items())), "cost_last": _r(cl), "cost_prev": _r(cp),
                    "cost_total": _r(ct)})
    return out


def _tag_signals(con, acct, ranked_keys, months):
    """cur_tag_signals: new or fast-growing tag values (tag_signals.classify) over the signal keys of the top tag keys."""
    if len(months) < 2:
        return []
    cov = {}
    for m, days in con.execute("SELECT month, count(DISTINCT usage_date) FROM n GROUP BY month").fetchall():
        cov[m] = days / calendar.monthrange(int(m[:4]), int(m[5:7]))[1]
    last = months[-1]
    total = (con.execute("SELECT sum(cost) FROM n WHERE month = %s" % _lit(last)).fetchone()[0] or 0.0) / (cov.get(last) or 1)
    vals = {k: {str(v).strip().lower() for (v,) in con.execute(
        "SELECT DISTINCT element_at(tags, %s)[1] FROM n WHERE map_contains(tags, %s) LIMIT 50" % (_lit(k), _lit(k))).fetchall()}
        for k in ranked_keys}
    values = []
    for k in TK.signal_keys(ranked_keys, vals):
        v = "NULLIF(trim(element_at(tags, %s)[1]), '')" % _lit(k)
        by = {}
        for val, m, svc, c in con.execute("SELECT %s AS v, month, service, sum(cost) FROM n WHERE %s IS NOT NULL GROUP BY ALL"
                                          % (v, v)).fetchall():
            x = by.setdefault(val, {"tag_key": k, "tag_value": val, "by_month": {}, "services": {}})
            x["by_month"][m] = x["by_month"].get(m, 0.0) + c
            x["services"].setdefault(svc, {})[m] = c
        for dim, field in (("linked_account", "accounts"), ("region", "regions")):
            for val, d, c in con.execute("SELECT %s AS v, %s, sum(cost) AS c FROM n WHERE %s IS NOT NULL AND month = %s "
                                         "GROUP BY ALL ORDER BY v, c DESC, %s" % (v, dim, v, _lit(last), dim)).fetchall():
                if val in by:
                    by[val].setdefault(field, []).append(d)
        values += [by[val] for val in sorted(by)]
    return TS.classify(acct, months, cov, values, total)


def _write_period(out_dir, acct, months):
    path = os.path.join(out_dir, "period.json")
    rows = []
    if os.path.isfile(path):
        with open(path, encoding="utf-8") as f:
            rows = [r for r in json.load(f) if not (r.get("entity") == acct and r.get("source") == "cur")]
    rows.append(period.row(acct, months, "cur"))
    _write(out_dir, "period", rows)


def main(norm_dir, out_data_dir, acct, summary=None):
    os.makedirs(out_data_dir, exist_ok=True)
    con, tmp = normalize.open_norm(norm_dir)
    try:
        con.execute("CREATE VIEW n AS SELECT * FROM read_parquet(%s)"
                    % _lit(os.path.join(os.path.abspath(norm_dir), "part-*.parquet")))
        s = _summary(con, acct, summary)
        tags = _tag_keys(con)
        last_month = s["months"][-1] if s["months"] else None
        trend, dropped = _cap(_trend(con, acct, tags, s["last_day"]), MAX_TREND_ROWS)
        s["truncated"], s["dropped"] = dropped > 0, dropped
        _write(out_data_dir, "cur_summary", [s])
        _write(out_data_dir, "cur_trend", trend)
        _write(out_data_dir, "cur_tag_coverage", _coverage(con, acct, tags))
        _write(out_data_dir, "cur_resources", _resources(con, acct, last_month))
        _write(out_data_dir, "cur_tag_signals", _tag_signals(con, acct, tags, s["months"]))
        _write_period(out_data_dir, acct, s["months"])
    finally:
        con.close()
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    if len(sys.argv) not in (4, 5):
        sys.exit("usage: datasets.py <norm_dir> <out_data_dir> <acct> [summary.json]")
    sm = None
    if len(sys.argv) == 5:
        with open(sys.argv[4], encoding="utf-8") as fh:
            sm = json.load(fh)
    main(sys.argv[1], sys.argv[2], sys.argv[3], sm)
