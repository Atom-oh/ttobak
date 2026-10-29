"""Loader for COT 'Data *' sheet CSVs: locate the raw-data header row and return dict rows."""
import ast, collections, csv, datetime, math, os

# CSVs from xlsx2csv.py; the worker points this at its work dir
OUT = os.environ.get("COT_CSV_DIR") or os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "out")


def load(entity, sheet):
    fn = os.path.join(OUT, entity, sheet + ".csv")
    with open(fn, newline="", encoding="utf-8") as f:  # xlsx2csv writes UTF-8 whatever the locale
        rows = list(csv.reader(f))
    hi = next(i for i, r in enumerate(rows[:10]) if len(r) > 2 and r[1] == "Report Name")
    hdr = rows[hi]
    # raw block ends at the first blank header cell after the populated run
    end = len(hdr)
    seen = False
    for j in range(1, len(hdr)):
        h = hdr[j].strip()
        if h:
            seen = True
        elif seen and j > 6 and not any(hdr[k].strip() for k in range(j, min(j + 2, len(hdr)))):
            end = j
            break
    cols = [h.strip() or f"_c{j}" for j, h in enumerate(hdr[:end])]
    out = []
    for r in rows[hi + 1:]:
        if len(r) < 2 or r[1].strip() != entity:
            continue
        out.append({c: (r[j] if j < len(r) else "") for j, c in enumerate(cols)})
    return out


def num(x):
    """A cell as a finite float; 0.0 for empty, unparseable, nan or inf cells (JSON has no NaN/Infinity)."""
    try:
        v = float(x)
    except (TypeError, ValueError):
        return 0.0
    return v if math.isfinite(v) else 0.0


def key(x):
    try:
        v = ast.literal_eval(x)
        return v[0] if isinstance(v, list) and len(v) == 1 else tuple(v) if isinstance(v, list) else v
    except Exception:
        return x


def month(r):
    """Month label YYYY-MM from timePeriod.start (Excel serial or ISO); ValueError when it is neither."""
    s = r.get("timePeriod.start", "")
    try:
        d = datetime.date(1899, 12, 30) + datetime.timedelta(days=int(float(s)))
    except (ValueError, OverflowError):  # OverflowError: inf, or a serial past the date range
        d = datetime.date.fromisoformat(s[:10])
    return d.strftime("%Y-%m")
