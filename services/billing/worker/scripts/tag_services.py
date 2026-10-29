"""COH recommendation resources -> tag-key inventory and tag-derived service (workload) names.

Usage: python3 -I tag_services.py <out dir with <Entity>/Data_*.csv> <dashboard data dir>

COH covers only resources that have a savings recommendation, and estimatedMonthlyCost is a
monthly estimate snapshot (no trend). Commitment purchase recommendations (RI/SP) carry no tags
and are reported as their own bucket so per-entity totals still reconcile.
"""
import collections, csv, json, math, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # -I drops the script dir
import tagkeys as TK  # noqa: E402
from entities import ENTITIES  # noqa: E402
import period as P  # noqa: E402

# The workload keys are detected per entity (tagkeys.precedence: the most widely used tag keys of its COH
# resources, most used first); a resource is named by the first of them it carries.
COMMITMENT = {"Ec2ReservedInstances", "RdsReservedInstances", "ElastiCacheReservedInstances", "OpenSearchReservedInstances",
              "RedshiftReservedInstances", "DynamoDbReservedCapacity", "ComputeSavingsPlans", "Ec2InstanceSavingsPlans",
              "SageMakerSavingsPlans", "MemoryDbReservedInstances"}
# a service is new when most of its resources were created in the period's last 3 months
NEW_SINCE = P.PRESENT[-3] if len(P.PRESENT) >= 3 else P.FIRST


def sheet(base, entity, name):
    fn = os.path.join(base, entity, name + ".csv")
    if not os.path.exists(fn):
        return None, []
    rows = list(csv.reader(open(fn, encoding="utf-8")))
    hi = next((i for i, r in enumerate(rows[:10]) if len(r) > 2 and r[1] == "Report Name"), None)
    if hi is None:
        return None, []
    return rows[hi], [r for r in rows[hi + 1:] if len(r) > 2 and r[1].strip() == entity]


def first_index(hdr):
    ix = {}
    for j, h in enumerate(hdr):
        h = h.strip()
        if h and h not in ix:
            ix[h] = j
    return ix


def account_names(base, entity):
    hdr, rows = sheet(base, entity, "Data_Linked_Account_Costs")
    if not hdr:
        return {}
    h = [x.strip() for x in hdr]
    la, nm = h.index("Linked Account"), h.index("Name")
    out = {}
    for r in rows:
        acct = re.findall(r"\d{12}", r[la]) if la < len(r) else []
        if acct and nm < len(r) and r[nm].strip():
            out[acct[0]] = r[nm].strip()
    return out


# one trailing "-" segment that norm_name strips (an ID, a counter, an ASG or node-group suffix)
_SUFFIX = re.compile(r"[0-9a-f]{6,}|\d+|asg|node(group)?s?", re.I)


def norm_name(v):
    """Strip trailing ID/ASG/node-group segments and a leading "eks-". Segment by segment rather than one
    repeated regex, whose overlapping alternatives backtracked exponentially on an untrusted Name tag."""
    parts = v.strip().split("-")
    n = len(parts)
    while n > 1 and _SUFFIX.fullmatch(parts[n - 1]):
        n -= 1
    return re.sub(r"^eks-", "", "-".join(parts[:n]))


def resources(base, entity):
    """One row per resource (a resource can carry several recommendations; keep its highest cost, as accounts.py
    does). A recommendation without a resourceArn stands for itself."""
    hdr, rows = sheet(base, entity, "Data_COH_All")
    if not hdr:
        return None
    ix = first_index(hdr)
    best = {}
    for r in rows:
        cell = lambda n: r[ix[n]] if n in ix and ix[n] < len(r) else ""  # noqa: E731  a short row's missing cells are empty
        rid = cell("recommendationId").strip()
        if not rid:
            continue
        tags = TK.parse_cell(cell("tags"))
        rec = {"account": cell("accountId"), "type": cell("currentResourceType"), "region": cell("region"),
               "summary": cell("currentResourceSummary"), "cost": num(cell("estimatedMonthlyCost")), "tags": tags}
        arn = cell("resourceArn").strip() or "rec:" + rid
        if arn not in best or rec["cost"] > best[arn]["cost"]:
            best[arn] = rec
    return list(best.values())


def num(x):
    """A cost cell as a finite float; 0.0 for empty, None, nan or unparseable cells (NaN would break the
    reconciliation and the JSON)."""
    try:
        v = float(x)
    except (TypeError, ValueError):
        return 0.0
    return v if math.isfinite(v) else 0.0


def precedence_for(base, entity):
    return TK.precedence(resources(base, entity) or [])


def service_of(res, precedence):
    if res["type"] in COMMITMENT:
        return "(약정 구매 권고)", "(commitment)"
    for k in precedence:
        v = (res["tags"].get(k) or "").strip()
        if v:
            return (norm_name(v) if k == "Name" else v), k
    return "(태그 없음)", "(untagged)"


def kind(t):
    return {"Ec2Instance": "EC2", "Ec2AutoScalingGroup": "EC2 ASG", "EbsVolume": "EBS", "RdsDbInstance": "RDS",
            "AuroraDbClusterStorage": "Aurora", "LambdaFunction": "Lambda", "EcsService": "ECS", "NatGateway": "NAT"}.get(t, t)


def main(base, out_dir):
    keys_out, svc_out, checks = [], [], {}
    for e in ENTITIES:
        rs = resources(base, e)
        if rs is None:
            checks[e] = "no COH data (sheet has no header)"
            continue
        names = account_names(base, e)
        prec = TK.precedence(rs)
        total = sum(r["cost"] for r in rs)
        tagged = [r for r in rs if r["tags"]]
        used = set()
        # one row per service name even when several keys carry it (e.g. aws:eks:cluster-name on
        # nodes and KubernetesCluster on their volumes); source_key is the dominant key
        groups, via = collections.defaultdict(list), collections.defaultdict(collections.Counter)
        for r in rs:
            s, k = service_of(r, prec)
            groups[s].append(r)
            via[s][k] += 1
            used.add(k)
        groups = {(s, via[s].most_common(1)[0][0]): g for s, g in groups.items()}
        inv = collections.defaultdict(lambda: [0, set(), 0.0])
        for r in tagged:
            for k, v in r["tags"].items():
                inv[k][0] += 1
                inv[k][1].add(v)
                inv[k][2] += r["cost"]
        for k, (n, vals, c) in sorted(inv.items(), key=lambda x: -x[1][0]):
            keys_out.append({"entity": e, "tag_key": k, "resources": n, "distinct_values": len(vals),
                             "est_monthly_cost": round(c, 2), "coverage_pct": round(100 * n / len(rs), 1),
                             "used_for_service": k in used})
        ck = TK.created_key(TK.rank(rs))
        for (s, k), g in sorted(groups.items(), key=lambda x: -sum(r["cost"] for r in x[1])):
            created = sorted(d for d in (TK.created_date(r["tags"].get(ck)) for r in g if ck) if d)
            recent = sum(1 for c in created if NEW_SINCE and c[:7] >= NEW_SINCE)
            # share of all the workload's resources (an undated resource is not counted as recent), so a few dated
            # recent resources cannot mark a large workload new
            pct = round(100 * recent / len(g), 1) if created else None
            insts = collections.Counter(r["summary"] for r in g if r["type"] in ("Ec2Instance", "Ec2AutoScalingGroup") and r["summary"])
            accts = collections.Counter(r["account"] for r in g)
            svc_out.append({
                "entity": e, "service": s, "source_key": k, "resources": len(g),
                "est_monthly_cost": round(sum(r["cost"] for r in g), 2),
                "accounts": [f"{a} {names.get(a, '')}".strip() for a, _ in accts.most_common()],
                "top_instance_types": [f"{t}×{n}" for t, n in insts.most_common(5)],
                "resource_kinds": dict(collections.Counter(kind(r["type"]) for r in g).most_common()),
                "regions": [x for x, _ in collections.Counter(r["region"] for r in g).most_common()],
                "created_recent_pct": pct,
                "is_new": bool(pct is not None and pct >= 60 and len(g) >= 3),
                "first_created": created[0] if created else None,
                "last_created": created[-1] if created else None,
            })
        mine = [x for x in svc_out if x["entity"] == e]
        svc_sum = sum(x["est_monthly_cost"] for x in mine)
        named = [x for x in mine if not x["source_key"].startswith("(")]
        assert abs(svc_sum - total) < 0.05 * len(mine) + 0.01, (e, svc_sum, total)
        checks[e] = {"resources": len(rs), "coh_total_cost": round(total, 2), "service_sum": round(svc_sum, 2),
                     "named_resources": sum(x["resources"] for x in named),
                     "named_cost": round(sum(x["est_monthly_cost"] for x in named), 2),
                     "services": len(named), "new_services": sum(1 for x in named if x["is_new"])}
    os.makedirs(out_dir, exist_ok=True)
    for fn, data in (("tag_keys.json", keys_out), ("tag_services.json", svc_out)):
        with open(os.path.join(out_dir, fn), "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, allow_nan=False)
    print(json.dumps(checks, ensure_ascii=False, indent=1))
    print("rows: tag_keys", len(keys_out), "tag_services", len(svc_out))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
