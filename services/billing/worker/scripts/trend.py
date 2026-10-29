"""Monthly pivot + growth / new-item detection over COT long-format sheets.
Series are 6 slots (period.MONTHS, oldest first); a slot with no month (short period) holds 0."""
import collections
import cotload as L
import period as P

MONTHS = P.MONTHS
FI = P.FI  # slot of the first present month


def pivot(entity, sheet, metric="metrics.AmortizedCost.amount", keyfn=None):
    p = collections.defaultdict(lambda: dict.fromkeys(MONTHS, 0.0))
    for r in L.load(entity, sheet):
        k = L.key(r["keys"])
        if keyfn:
            k = keyfn(k)
        p[k][L.month(r)] += L.num(r[metric])
    return p


def _avg(v, months):
    xs = [v[i] for i, m in enumerate(MONTHS) if m in months]
    return sum(xs) / len(xs) if xs else 0.0


def stats(series):
    v = [series[m] if m else 0.0 for m in MONTHS]
    h1, h2 = _avg(v, P.H1), _avg(v, P.H2)
    return {
        "months": [round(x, 2) for x in v],
        "last": round(v[-1], 2),
        "d_last_first": round(v[-1] - v[FI], 2),
        "pct_last_first": round((v[-1] / v[FI] - 1) * 100, 1) if v[FI] > 0 else None,
        "h1_avg": round(h1, 2),
        "h2_avg": round(h2, 2),
        "d_h2_h1": round(h2 - h1, 2),
        "pct_h2_h1": round((h2 / h1 - 1) * 100, 1) if h1 > 0 else None,
        "first_month": next((m for m, x in zip(MONTHS, v) if m and x > 1), None),
    }


def table(p):
    return {k: stats(s) for k, s in p.items()}


def new_items(t, min_last=500.0, base_max=50.0):
    """Items that were ~absent in the first two months but material in the later half."""
    out = []
    for k, s in t.items():
        v = s["months"]
        if max(v[FI:FI + 2]) <= base_max and max(v[3:]) >= min_last:
            out.append((k, s))
    return sorted(out, key=lambda x: -max(x[1]["months"][3:]))


def gone_items(t, min_base=1000.0, end_max=50.0):
    out = []
    for k, s in t.items():
        v = s["months"]
        if max(v[FI:FI + 2]) >= min_base and max(v[4:]) <= end_max:
            out.append((k, s))
    return sorted(out, key=lambda x: -x[1]["months"][FI])
