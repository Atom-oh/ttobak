"""Linked-account view: per-account 6-month trend + COH resource profile.

Usage: python3 -I accounts.py <csv_out_dir> <dashboard_data_dir>
  csv_out_dir/<Entity>/Data_Linked_Account_Costs.csv, Data_COH_All.csv (from xlsx2csv.py)
Writes accounts.json and account_resources.json into dashboard_data_dir.
COH costs are a monthly estimate snapshot of resources with recommendations, not a trend.
"""
import ast, collections, csv, datetime, json, os, re, sys

# tag key precedence for a "service name" (shared with the tag analysis)
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
from entities import ENTITIES  # noqa: E402
from period import H1, H2, LAST, MONTHS, PRESENT  # noqa: E402
from tag_services import norm_name, precedence_for  # noqa: E402  one naming rule for accounts and services
MK = ["m%d" % (i + 1) for i in range(len(MONTHS))]  # m1..m6, m6 = latest; a slot with no month is null
SIZED = {"Ec2Instance", "Ec2AutoScalingGroup"}  # currentResourceSummary is the instance type
# commitment purchases (RI/SP) are not running resources
COMMITMENT_ACTIONS = {"PurchaseReservedInstances", "PurchaseSavingsPlans"}


def sheet(path, entity):
    """(header, rows) of the raw block whose column 1 is 'Report Name'; None if absent."""
    if not os.path.exists(path):
        return None
    rows = list(csv.reader(open(path, encoding="utf-8")))
    try:
        hi = next(i for i, r in enumerate(rows[:10]) if len(r) > 2 and r[1] == "Report Name")
    except StopIteration:
        return None
    return [h.strip() for h in rows[hi]], rows[hi + 1:], entity


def first_col(hdr, name, start=0):
    return next(i for i, h in enumerate(hdr) if i >= start and h == name)


def num(x):
    try:
        return float(x)
    except (TypeError, ValueError):
        return 0.0


def month(s):
    try:
        d = datetime.date(1899, 12, 30) + datetime.timedelta(days=int(float(s)))
    except ValueError:
        d = datetime.date.fromisoformat(s[:10])
    return d.strftime("%Y-%m")


def unwrap(k):
    """"['022071340449']" or the doubly wrapped "['['0220...']']" -> '022071340449' (string, zeros kept)."""
    m = re.search(r"\d{12}", k or "")
    return m.group(0) if m else str(k).strip()


def tag_service(tags_raw, precedence):
    try:
        tags = {t["key"]: t["value"] for t in ast.literal_eval(tags_raw or "[]")}
    except Exception:
        tags = {}
    for k in precedence:
        v = (tags.get(k) or "").strip()
        if v:
            return norm_name(v) if k == "Name" else v
    return "—"


def linked_accounts(base, e):
    s = sheet(os.path.join(base, e, "Data_Linked_Account_Costs.csv"), e)
    if s is None:  # no sheet or no header block: no linked accounts, as coh_resources
        return {}, {}
    hdr, rows, _ = s
    ik, ic, it = first_col(hdr, "keys"), first_col(hdr, "metrics.AmortizedCost.amount"), first_col(hdr, "timePeriod.start")
    il, iname = first_col(hdr, "Linked Account"), first_col(hdr, "Name")
    cost = collections.defaultdict(lambda: collections.defaultdict(float))
    names = {}
    for r in rows:
        if len(r) <= it or r[1].strip() != e:
            continue
        r = r + [""] * (len(hdr) - len(r))  # a short row's missing trailing cells (keys, cost, name) are empty
        if r[ik].strip():
            m = month(r[it])
            if m in PRESENT:
                cost[unwrap(r[ik])][m] += num(r[ic])
        if len(r) > iname and r[il].strip() and r[iname].strip():
            names[unwrap(r[il])] = r[iname].strip()
    return cost, names


def coh_resources(base, e):
    """One row per resource (a resource can carry several recommendations; keep its highest cost)."""
    s = sheet(os.path.join(base, e, "Data_COH_All.csv"), e)
    if s is None:
        return []
    hdr, rows, _ = s
    col = {n: first_col(hdr, n) for n in ["accountId", "actionType", "currentResourceType", "estimatedMonthlyCost",
                                          "region", "tags", "currentResourceSummary", "resourceArn"]}
    prec = precedence_for(base, e)
    best = {}
    for r in rows:
        if len(r) <= col["resourceArn"] or r[1].strip() != e or r[col["actionType"]].strip() in COMMITMENT_ACTIONS:
            continue
        r = r + [""] * (len(hdr) - len(r))  # a short row's missing trailing cells (e.g. no tags) are empty
        arn = r[col["resourceArn"]].strip() or r[col["accountId"]] + "/" + str(len(best))
        kind = r[col["currentResourceType"]].strip()
        rec = {
            "account": unwrap(r[col["accountId"]]),
            "resource_kind": kind,
            "instance_type": r[col["currentResourceSummary"]].strip() if kind in SIZED else "—",
            "region": r[col["region"]].strip() or "—",
            "tag_service": tag_service(r[col["tags"]], prec),
            "cost": num(r[col["estimatedMonthlyCost"]]),
        }
        if arn not in best or rec["cost"] > best[arn]["cost"]:
            best[arn] = rec
    return list(best.values())


def _sum(row, keys):
    return sum(row[k] or 0.0 for k in keys)


def _avg(row, months):
    ks = [k for k, m in zip(MK, MONTHS) if m in months]
    return round(sum(row[k] for k in ks) / len(ks), 2) if ks else 0.0


def kind_of(row):
    """신규: nothing in the first two months, spend in the last three (the slots, oldest first)."""
    first = [k for k, m in zip(MK, MONTHS) if m][:2]
    early = _sum(row, first)
    late = _sum(row, MK[3:])
    if early <= 0 and late > 0:
        return "신규"
    if row["pct_h2_h1"] is not None and row["pct_h2_h1"] >= 50 and row["d_h2_h1"] >= 1000:
        return "급증"
    if row["pct_h2_h1"] is not None and row["pct_h2_h1"] <= -30:
        return "감소"
    return "유지"


def main(base, outdir):
    accounts, resources = [], []
    for e in ENTITIES:
        cost, names = linked_accounts(base, e)
        res = coh_resources(base, e)
        by_acct = collections.defaultdict(list)
        for r in res:
            by_acct[r["account"]].append(r)
        total_last = sum(c.get(LAST, 0) for c in cost.values())
        for acct, c in cost.items():
            row = {"entity": e, "account": acct, "name": names.get(acct, "")}
            for m, k in zip(MONTHS, MK):
                row[k] = round(c.get(m, 0.0), 2) if m else None
            row["last"] = row[MK[-1]] or 0.0
            row["h1_avg"] = _avg(row, H1)
            row["h2_avg"] = _avg(row, H2)
            row["d_h2_h1"] = round(row["h2_avg"] - row["h1_avg"], 2)
            # no meaningful % on a near-zero baseline (new accounts)
            row["pct_h2_h1"] = round(100 * row["d_h2_h1"] / row["h1_avg"], 1) if row["h1_avg"] >= 100 else None
            row["kind"] = kind_of(row)
            row["share_last"] = round(row["last"] / total_last, 4) if total_last else 0
            rs = by_acct.get(acct, [])
            row["coh_resources"] = len(rs)
            row["coh_est_monthly"] = round(sum(r["cost"] for r in rs), 2)
            types = collections.defaultdict(lambda: [0, 0.0])
            for r in rs:
                if r["instance_type"] != "—":
                    types[r["instance_type"]][0] += 1
                    types[r["instance_type"]][1] += r["cost"]
            row["top_instance_types"] = [f"{t}×{n}" for t, (n, _) in sorted(types.items(), key=lambda kv: -kv[1][1])[:3]]
            row["regions"] = sorted({r["region"] for r in rs if r["region"] != "—"})
            svc = collections.Counter()
            for r in rs:
                if r["tag_service"] != "—":
                    svc[r["tag_service"]] += r["cost"]
            row["services_by_tag"] = [s for s, _ in svc.most_common(3)] or ["—"]
            accounts.append(row)
        groups = collections.defaultdict(lambda: [0, 0.0])
        for r in res:
            k = (e, r["account"], r["resource_kind"], r["instance_type"], r["region"], r["tag_service"])
            groups[k][0] += 1
            groups[k][1] += r["cost"]
        for (en, a, rk, it, rg, ts), (n, c) in groups.items():
            resources.append({"entity": en, "account": a, "resource_kind": rk, "instance_type": it,
                              "region": rg, "tag_service": ts, "count": n, "est_monthly_cost": round(c, 2)})
    accounts.sort(key=lambda r: (r["entity"], -r["last"]))
    resources.sort(key=lambda r: (r["entity"], r["account"], -r["est_monthly_cost"]))
    json.dump(accounts, open(os.path.join(outdir, "accounts.json"), "w", encoding="utf-8"), ensure_ascii=False)
    json.dump(resources, open(os.path.join(outdir, "account_resources.json"), "w", encoding="utf-8"), ensure_ascii=False)
    print("accounts", len(accounts), "account_resources", len(resources))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
