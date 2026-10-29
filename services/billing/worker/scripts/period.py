"""Detect the COT billing period (last 6 months present in Data_Service_Costs) and month labels."""
import os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import cotload  # noqa: E402

N = 6


def detect(entity):
    """The last N months; [] when the sheet has no 'Report Name' header or an unreadable timePeriod.start (an
    invalid upload: run_account.sh exits 3 on an empty period). A missing sheet still raises."""
    try:
        ms = sorted({cotload.month(r) for r in cotload.load(entity, "Data_Service_Costs")})
    except (StopIteration, ValueError):
        return []
    return ms[-N:]


def slots(months):
    ms = list(months)[-N:]
    return [None] * (N - len(ms)) + ms


def labels(months):
    same_year = len({m[:4] for m in months}) <= 1
    out = {}
    for m in months:
        y, mo = int(m[:4]), int(m[5:7])
        out[m] = f"{mo}월" if same_year else f"{y % 100}년 {mo}월"
    return out


def shift(month, n):
    t = int(month[:4]) * 12 + int(month[5:7]) - 1 + n
    return f"{t // 12:04d}-{t % 12 + 1:02d}"


def ko_month(m):
    """'YYYY-MM' -> 'YYYY년 M월' (dates outside the period, e.g. commitment expiry)."""
    return f"{int(m[:4])}년 {int(m[5:7])}월"


def span(months, lab):
    """'1~3월' for same-year labels, '25년 11월~26년 1월' otherwise."""
    ms = [m for m in months if m]
    if not ms:
        return ""
    a, b = lab.get(ms[0], ms[0]), lab.get(ms[-1], ms[-1])
    if a == b:
        return a
    if a.endswith("월") and b.endswith("월") and " " not in a and " " not in b:
        return a[:-1] + "~" + b
    return a + "~" + b


def halves(months):
    """(earlier, later) halves of the 6 slots, present months only: the h1/h2 averages compare these."""
    s = slots(sorted(set(months)))
    return [m for m in s[:N // 2] if m], [m for m in s[N // 2:] if m]


def info(months):
    ms = sorted(set(months))[-N:]
    return {"months": ms, "first": ms[0] if ms else None, "prev": ms[-2] if len(ms) > 1 else None,
            "last": ms[-1] if ms else None, "labels": labels(ms)}


def row(entity, months, source="cot"):
    """One period.json row (the Period contract)."""
    return {"entity": entity, "source": source, **info(months)}


def for_entity(rows, entity, data_months=()):
    """An entity's period from period.json rows; else COT_MONTHS; else the months its data carries."""
    r = next((x for x in rows or [] if x.get("entity") == entity and x.get("source", "cot") == "cot"), None)
    if r:
        return info(r.get("months") or [])
    return info(PRESENT or [m for m in data_months if m])


PRESENT = sorted({m.strip() for m in os.environ.get("COT_MONTHS", "").split(",") if m.strip()})[-N:]
MONTHS = slots(PRESENT)
FIRST = PRESENT[0] if PRESENT else None
LAST = PRESENT[-1] if PRESENT else None
PREV = PRESENT[-2] if len(PRESENT) > 1 else None
LABEL = labels(PRESENT)
FI = MONTHS.index(FIRST) if FIRST else 0  # slot of the first present month in 6-long arrays
H1, H2 = halves(PRESENT)

if __name__ == "__main__":
    print(",".join(detect(sys.argv[1])))
