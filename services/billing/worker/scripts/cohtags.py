"""COH recommendation resources -> tag inventory, creation-date cohorts, new-account attribution."""
import collections, json, os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cotload as L
import period as P
import tagkeys as TK


def recs(entity):
    try:
        rs = L.load(entity, "Data_COH_All")
    except (StopIteration, FileNotFoundError):  # no COH sheet or no header: no recommendations
        return []
    out = []
    for r in rs:
        if not r.get("recommendationId"):
            continue
        # a cell that is not a list of string {key, value} dicts is untagged (an odd item is skipped), never a crash
        r["_tags"] = TK.parse_cell(r.get("tags", ""))
        r["_cost"] = L.num(r.get("estimatedMonthlyCost"))
        r["_sav"] = L.num(r.get("estimatedMonthlySavings"))
        out.append(r)
    return out


def analyze(entity, new_accounts=()):
    rs = recs(entity)
    if not rs:
        return None
    res = {"n": len(rs), "untagged": sum(1 for r in rs if not r["_tags"]),
           "savings": round(sum(r["_sav"] for r in rs), 2)}
    res["by_action"] = sorted(
        ([a, t, n, round(s)] for (a, t), (n, s) in _group(rs, lambda r: (r["actionType"], r["currentResourceType"])).items()),
        key=lambda x: -x[3])
    ranked = TK.rank(rs)
    # the entity's most used tag keys; the team key always gets a breakdown so account profiles can name a team
    dims = TK.dims(rs)
    res["team_key"] = TK.team_key(ranked)
    res["company_key"] = TK.role_key(rs, "company")
    if res["team_key"] and res["team_key"] not in dims:
        dims.append(res["team_key"])
    recent_dims = dims
    created_key = TK.created_key(ranked)
    res["dims"] = {}
    for d in dims:
        g = _group([r for r in rs if d in r["_tags"]], lambda r: r["_tags"][d])
        res["dims"][d] = sorted(([k, n, round(c)] for k, (n, c) in _group_cost(rs, d).items()), key=lambda x: -x[2])[:20]
    # creation cohorts from the detected created-at style tag; "recent" = the last 4 months of the period
    # a placeholder (unknown, N/A, ...) is undated: it opens no month bucket and is never recent
    created = [r for r in rs if TK.created_date(r["_tags"].get(created_key))]
    res["created_by_month"] = dict(sorted(collections.Counter(TK.created_date(r["_tags"][created_key])[:7]
                                                               for r in created).items()))
    since = P.PRESENT[-4] if len(P.PRESENT) >= 4 else P.FIRST
    recent = [r for r in created if since and TK.created_date(r["_tags"][created_key])[:7] >= since]
    res["recent_n"] = len(recent)
    res["recent_cost"] = round(sum(r["_cost"] for r in recent))
    res["recent"] = {}
    for d in recent_dims:
        res["recent"][d] = sorted(([k, n, round(c)] for k, (n, c) in _group_cost(recent, d).items()), key=lambda x: -x[1])[:15]
    res["recent_types"] = collections.Counter(
        (r["currentResourceSummary"] or "")[:20] for r in recent if r["currentResourceType"] == "Ec2Instance").most_common(15)
    # per account tag profile (for new linked accounts)
    res["accounts"] = {}
    for acct in new_accounts:
        ar = [r for r in rs if r["accountId"] == acct]
        if not ar:
            continue
        prof = {d: collections.Counter(r["_tags"][d] for r in ar if d in r["_tags"]).most_common(5) for d in dims}
        res["accounts"][acct] = {"n": len(ar), "cost": round(sum(r["_cost"] for r in ar)),
                                 "types": collections.Counter(r["currentResourceType"] for r in ar).most_common(5),
                                 "regions": collections.Counter(r["region"] for r in ar).most_common(3),
                                 "tags": {k: v for k, v in prof.items() if v}}
    res["acct_resources"] = collections.Counter(r["accountId"] for r in rs).most_common(60)
    return res


def _group(rs, kf):
    g = collections.defaultdict(lambda: [0, 0.0])
    for r in rs:
        k = kf(r)
        g[k][0] += 1
        g[k][1] += r["_sav"]
    return g


def _group_cost(rs, d):
    g = collections.defaultdict(lambda: [0, 0.0])
    for r in rs:
        if d in r["_tags"]:
            g[r["_tags"][d][:60]][0] += 1
            g[r["_tags"][d][:60]][1] += r["_cost"]
    return g


if __name__ == "__main__":
    e = sys.argv[1]
    print(json.dumps(analyze(e, sys.argv[2:]), ensure_ascii=False, indent=1, default=str))
