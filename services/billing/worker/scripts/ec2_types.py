"""EC2 instance-type trends from COT usage sheets (OD/SP/RI/Spot).

Usage: python3 -I ec2_types.py <converted-csv-dir> <out-dir>
<converted-csv-dir>/<Entity>/Data_*.csv as produced by xlsx2csv.py.
Writes ec2_types.json, ec2_type_growth.json, ec2_mix.json to <out-dir>.
"""
import ast, csv, datetime, json, math, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
from entities import ENTITIES  # noqa: E402
from period import H1, H2, LAST, MONTHS, PRESENT  # noqa: E402
SHEETS = {"OD": "Data_On_Demand_Usage.csv", "SP": "Data_Savings_Plan_Usage.csv",
          "RI": "Data_Reserved_Instance_Usage.csv", "Spot": "Data_Spot_Usage.csv"}
ACCEL = ("g", "p", "inf", "trn", "dl", "vt", "f")


def rows(path, entity):
    if not os.path.exists(path):
        return []
    data = list(csv.reader(open(path, encoding="utf-8")))
    try:
        hi = next(i for i, r in enumerate(data[:10]) if len(r) > 2 and r[1] == "Report Name")
    except StopIteration:
        return []
    hdr = [h.strip() for h in data[hi]]
    idx = {}
    for j, h in enumerate(hdr):
        if h and h not in idx:
            idx[h] = j
    out = []
    for r in data[hi + 1:]:
        if len(r) > 2 and r[1].strip() == entity:
            out.append({h: (r[j] if j < len(r) else "") for h, j in idx.items()})
    return out


def num(x):
    """A cell as a finite float; 0.0 for empty, unparseable, nan or inf cells (JSON has no NaN/Infinity)."""
    try:
        v = float(x)
    except (TypeError, ValueError):
        return 0.0
    return v if math.isfinite(v) else 0.0


def month(s):
    try:
        d = datetime.date(1899, 12, 30) + datetime.timedelta(days=int(float(s)))
    except ValueError:
        d = datetime.date.fromisoformat(s[:10])
    return d.strftime("%Y-%m")


def key(x):
    try:
        v = ast.literal_eval(x)
        return v[0] if isinstance(v, list) and v else str(v)
    except Exception:
        return x


def service_of(itype):
    """The usage sheets also carry RDS/ElastiCache/OpenSearch node types."""
    if itype.startswith("db."):
        return "RDS"
    if itype.startswith("cache."):
        return "ElastiCache"
    if itype.endswith(".search") or itype.startswith(("ultrawarm", "or1.", "or2.")):
        return "OpenSearch"
    return "EC2"


def base_type(itype):
    for p in ("db.", "cache."):
        if itype.startswith(p):
            itype = itype[len(p):]
    return itype[:-len(".search")] if itype.endswith(".search") else itype


def classify(itype):
    family, _, size = base_type(itype).partition(".")
    m = re.match(r"([a-z]+?)(\d+)([a-z\-]*)$", family)
    if not m:
        return family, family, 0, size, "other"
    cat, gen, attrs = m.group(1), int(m.group(2)), m.group(3)
    if cat == "mac":
        arch = "other"
    elif cat in ACCEL:
        arch = "gpu/accel"
    elif attrs.startswith("g") or (cat in ("im", "is") and attrs.startswith("g")):
        arch = "graviton"
    elif attrs.startswith("a"):
        arch = "amd"
    else:
        arch = "intel"
    return family, cat, gen, size, arch


def gen_bucket(g):
    return "<=5" if g <= 5 else str(min(g, 8))


def main(src, dst):
    types, growth, mix = [], [], []
    for e in ENTITIES:
        agg = {}  # (itype, purchase, month) -> [cost, hours]
        dropped = set()
        for purchase, fn in SHEETS.items():
            for r in rows(os.path.join(src, e, fn), e):
                m = month(r.get("timePeriod.start", ""))
                if m not in PRESENT:
                    dropped.add(m)
                    continue
                it = key(r.get("keys", ""))
                if not it or it in ("NoInstanceType", "No instance type"):
                    continue
                k = (it, purchase, m)
                a = agg.setdefault(k, [0.0, 0.0])
                a[0] += num(r.get("metrics.AmortizedCost.amount"))
                a[1] += num(r.get("metrics.UsageQuantity.amount"))
        if dropped:
            print(f"{e}: dropped months outside range: {sorted(dropped)}", file=sys.stderr)
        for (it, p, m), (c, h) in sorted(agg.items()):
            fam, cat, gen, size, arch = classify(it)
            types.append({"entity": e, "service": service_of(it), "instance_type": it, "family": fam, "category": cat, "generation": gen,
                          "size": size, "arch": arch, "purchase": p, "month": m,
                          "cost": round(c, 2), "hours": round(h, 2)})
        # growth per instance type
        # growth and mix cover EC2 only; other services stay in ec2_types.json with their service tag
        by_type = {}
        for (it, p, m), (c, _) in agg.items():
            if service_of(it) != "EC2":
                continue
            by_type.setdefault(it, {}).setdefault(m, {}).setdefault(p, 0.0)
            by_type[it][m][p] += c
        for it, mm in by_type.items():
            fam, cat, gen, size, arch = classify(it)
            mc = {m: sum(mm.get(m, {}).values()) for m in PRESENT}
            # a short period has no earlier-half month: no change to report (null), not a change from zero
            h1 = sum(mc[m] for m in H1) / len(H1) if H1 else None
            h2 = sum(mc[m] for m in H2) / len(H2) if H2 else 0.0
            last_p = mm.get(LAST, {})
            last = mc[LAST]
            row = {"entity": e, "instance_type": it, "family": fam, "arch": arch, "generation": gen}
            row.update({"m%d" % (i + 1): round(mc[m], 2) if m else None for i, m in enumerate(MONTHS)})
            row.update({
                "h1_avg": None if h1 is None else round(h1, 2), "h2_avg": round(h2, 2),
                "d_h2_h1": None if h1 is None else round(h2 - h1, 2),
                "pct_h2_h1": round((h2 - h1) / h1 * 100, 1) if h1 else None,
                "last": round(last, 2),
                # nothing in the first two months, spend in the later half
                "is_new": all(mc[m] <= 0 for m in PRESENT[:2]) and any(mc[m] > 0 for m in H2),
                "purchase_mix_last": {p: round(last_p.get(p, 0.0) / last, 4) if last > 0 else 0.0 for p in SHEETS},
            })
            growth.append(row)
        # monthly mix
        for m in PRESENT:
            cell = [(it, p, c) for (it, p, mm2), (c, _) in agg.items() if mm2 == m and service_of(it) == "EC2"]
            tot = sum(c for _, _, c in cell)
            ba, bp, bg = {}, {p: 0.0 for p in SHEETS}, {"<=5": 0.0, "6": 0.0, "7": 0.0, "8": 0.0}
            for it, p, c in cell:
                _, _, gen, _, arch = classify(it)
                ba[arch] = ba.get(arch, 0.0) + c
                bp[p] += c
                bg[gen_bucket(gen)] += c
            mix.append({"entity": e, "month": m, "total": round(tot, 2),
                        "by_arch": {k: round(v, 2) for k, v in sorted(ba.items())},
                        "by_purchase": {k: round(v, 2) for k, v in bp.items()},
                        "by_generation": {k: round(v, 2) for k, v in bg.items()},
                        "gpu_cost": round(ba.get("gpu/accel", 0.0), 2)})
    growth.sort(key=lambda r: (r["entity"], -(r["d_h2_h1"] or 0), r["instance_type"]))
    os.makedirs(dst, exist_ok=True)
    for name, data in (("ec2_types", types), ("ec2_type_growth", growth), ("ec2_mix", mix)):
        with open(os.path.join(dst, name + ".json"), "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, allow_nan=False)
        print(f"wrote {name}.json rows={len(data)}")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
