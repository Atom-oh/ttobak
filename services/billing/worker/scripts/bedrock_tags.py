"""Add Bedrock per-model usage and tag -> service/resource-type attribution to the dashboard dataset.
Usage: python -I bedrock_tags.py <dashboard_json>"""
import collections, json, os, re, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import cotload as L
import trend as T
import cohtags as C
import tagkeys as TK
from build_data import hj, fl, r0, GPU_RE, month_vals

M = T.MONTHS
BEDROCK_RE = re.compile(r"^(.*?)\s*\(Amazon Bedrock Edition\)$")
# Bedrock-native service lines that sit outside Marketplace
NATIVE = ["Amazon Bedrock", "Amazon Bedrock AgentCore", "Amazon Q", "Amazon SageMaker"]
RTYPE = {"Ec2Instance": "EC2", "EbsVolume": "EBS", "RdsDbInstance": "RDS", "NatGateway": "NAT Gateway",
         "LambdaFunction": "Lambda", "EcsService": "ECS", "AuroraDbClusterStorage": "Aurora", "Ec2AutoScalingGroup": "EC2 ASG",
         "ComputeSavingsPlans": "Savings Plans", "RdsReservedInstances": "RDS RI", "DynamoDbReservedCapacity": "DynamoDB",
         "WorkSpaces": "WorkSpaces"}


def family(model):
    m = re.match(r"(?i)claude\s+(opus|sonnet|haiku)", model)
    if m:
        return "Claude " + m.group(1).title()
    return model.split()[0]


def bedrock(e):
    j = hj(e, "Marketplace_Costs") or {"headers": [], "rows": []}
    models = []
    for r in j["rows"]:
        name = str(r[0])
        m = BEDROCK_RE.match(name)
        if not m:
            continue
        v = r0(month_vals(j, r))  # 6 slots, a short period's leading slots 0
        if sum(v) < 1:
            continue
        models.append({"model": m.group(1), "family": family(m.group(1)), "vendor": m.group(1).split()[0],
                       "months": v, "total": sum(v), "last": v[-1]})
    models.sort(key=lambda x: -x["total"])
    svc = T.table(T.pivot(e, "Data_Service_Costs"))
    native = [{"service": s, "months": r0(svc[s]["months"]), "total": round(sum(svc[s]["months"]))}
              for s in NATIVE if s in svc and sum(svc[s]["months"]) >= 1]
    fam = collections.defaultdict(lambda: [0] * 6)
    for x in models:
        fam[x["family"]] = [a + b for a, b in zip(fam[x["family"]], x["months"])]
    total = [sum(x["months"][i] for x in models) for i in range(6)]
    last_models = sorted([x for x in models if x["last"] > 0], key=lambda x: -x["last"])
    return {"models": models, "native": native, "by_family": dict(fam), "marketplace_total": total,
            "six_month_total": sum(total), "active_models_last": len(last_models),
            "top_model_last": last_models[0]["model"] if last_models else None,
            "top_model_last_share": round(last_models[0]["last"] / total[-1] * 100, 1) if last_models and total[-1] else None}


def ec2_type(r):
    """The instance type of an EC2 instance recommendation (first word of its summary); "" for any other resource or
    an empty summary."""
    w = (r["currentResourceSummary"] or "").split()
    return w[0] if w and r["currentResourceType"] == "Ec2Instance" else ""


def tags(e):
    rs = C.recs(e)
    if not rs:
        return None
    cost_rs = [r for r in rs if r["currentResourceType"] not in ("ComputeSavingsPlans", "RdsReservedInstances", "DynamoDbReservedCapacity")]
    tot_cost = sum(r["_cost"] for r in cost_rs)
    tagged = [r for r in cost_rs if r["_tags"]]
    out = {"resources": len(cost_rs), "tagged": len(tagged), "cost": round(tot_cost),
           "tagged_cost": round(sum(r["_cost"] for r in tagged)), "keys": {}}
    kc = collections.Counter(k for r in cost_rs for k in r["_tags"])
    out["key_counts"] = kc.most_common(20)
    for key in TK.tag_keys(cost_rs):
        rr = [r for r in cost_rs if key in r["_tags"]]
        if not rr:
            continue
        g = collections.defaultdict(lambda: collections.defaultdict(float))
        cnt = collections.Counter()
        gpu = collections.defaultdict(float)
        for r in rr:
            v = r["_tags"][key][:48]
            g[v][RTYPE.get(r["currentResourceType"], r["currentResourceType"])] += r["_cost"]
            cnt[v] += 1
            if GPU_RE.match(ec2_type(r)):
                gpu[v] += r["_cost"]
        rows = [{"value": v, "n": cnt[v], "cost": round(sum(t.values())), "by_type": {k: round(x) for k, x in sorted(t.items(), key=lambda y: -y[1])},
                 "gpu_cost": round(gpu.get(v, 0))} for v, t in g.items()]
        rows.sort(key=lambda x: -x["cost"])
        out["keys"][key] = {"n": len(rr), "cost": round(sum(r["_cost"] for r in rr)), "values": rows[:15], "distinct": len(rows)}
    gpu_all = collections.Counter()
    for r in cost_rs:
        itype = ec2_type(r)
        if GPU_RE.match(itype):
            gpu_all[itype] += r["_cost"]
    out["gpu_instances"] = [[k, round(v)] for k, v in gpu_all.most_common(10)]
    return out


def main(path):
    d = json.load(open(path))
    for e, x in d["entities"].items():
        x["bedrock"] = bedrock(e)
        x["tags"] = tags(e)
        b = x["bedrock"]
        print(e, "bedrock 6m", b["six_month_total"], "last", b["marketplace_total"][-1], "models", len(b["models"]),
              "top", b["top_model_last"], b["top_model_last_share"], "| tags", None if not x["tags"] else
              (x["tags"]["resources"], x["tags"]["tagged"], list(x["tags"]["keys"])))
    json.dump(d, open(path, "w"), ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main(*sys.argv[1:])
