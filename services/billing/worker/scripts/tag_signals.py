"""Tag signals: service-tag values (workloads) that are new or growing fast, the first input of the business agent.

Usage: python3 -I tag_signals.py <dashboard data dir>   (COT: reads tag_services.json, account_resources.json,
       accounts.json there and writes tag_signals.json)

CUR (cur/datasets.py) calls classify() with real monthly costs per tag value and writes cur_tag_signals.json.
COT has no tag trend: a COT signal is a tag-derived service whose COH resources were mostly created in the last
months (is_new), or whose resources sit in a linked account that is new or growing fast. COT costs are COH
estimates of resources with a recommendation, not the service's whole spend.

CUR signal rules (classify; coverage comes from datasets._tag_signals). When unsure, a rule errs towards no signal:
it never inflates the current run rate and never reads flat spend as growth.
1. Coverage of a month is the share of its days the export holds, in (0, 1]. Only rows of the month's own bill
   count (a late line item of a later bill dated in the month does not cover it), and granularity is decided per
   month, so carried-over daily bills and a monthly latest bill mix. A monthly month (most rows dated on the 1st)
   is a whole month. A daily month is whole, except the first covered month starts on its first usage day and the
   last covered month ends on its last usage day (the export's partial edges). Interior months are whole.
2. A month without coverage (missing, 0 or not a finite number) is removed from the history before anything else:
   it is neither a 0 nor evidence of absence. If the latest month is not covered there is no signal.
3. In a covered month a value (or a value's service) without rows costs 0.
4. Rate of a covered month = cost / coverage (a monthly run rate). now = run_rate_last = the latest month's rate.
   base = mean rate over the up to RECENT covered months before the latest (0 if there are none).
   delta = now - base; growth_pct = (now / base - 1) * 100 when base > 0 and the value is not 신규, else None.
   share_pct = now / total_last * 100 when total_last > 0, else None.
5. floor = max(MIN_ABS, MIN_SHARE * total_last), compared with rates (after coverage scaling), never raw costs.
6. 신규 (new): now >= floor and every early covered month has rate <= NEW_EPS * now, where early = the covered months
   before the last RECENT covered ones (with RECENT or fewer covered months: the first covered month). At least one
   early month is needed, so a single covered month is never new.
7. 급증 (surge), only if not 신규: now >= floor, base > 0, now >= base * (1 + SURGE) and now - base >= floor. A value
   coming back after covered zero months (base 0 but not new) is neither.
8. 신규 서비스 (new service), only if not 신규: a service of the value is new by rule 6 on its own series (same floor).
   The row's delta is then the sum of the new services' deltas unless the value is also 급증.
9. A value with a non-finite cost in any month (value or service series) has no signal; a non-finite total_last
   gives no signals at all. Output is ordered by delta desc, then tag key and value; services by cost_last desc,
   then name; new services by delta desc, then name. cost_by_month lists the raw costs of covered months (0 when
   the value has no rows) and of uncovered months the value has rows in.
"""
import json, math, os, sys

RECENT = 3         # months that count as "recent" for a new tag value or service
NEW_EPS = 0.01     # before the recent months, at most this share of the latest run rate counts as absent
SURGE = 0.5        # latest run rate at least base * (1 + SURGE)
MIN_ABS = 50.0     # USD per month: smaller signals are noise
MIN_SHARE = 0.001  # ... or this share of the account's latest monthly run rate, whichever is larger
TOP = 30
TOP_SERVICES = 5
NEW, UP, NEW_SVC, NEW_ACCT = "신규", "급증", "신규 서비스", "증가 계정"


def _r(x):
    return round(float(x), 2)


def _finite(x):
    return isinstance(x, (int, float)) and math.isfinite(x)


def _absent_before(rate, months, last_rate):
    """Rule 6: no meaningful cost in the early covered months (months: the covered months, rate: their rates)."""
    early = months[:-RECENT] if len(months) > RECENT else months[:1] if len(months) > 1 else []
    return bool(early) and all(rate[m] <= NEW_EPS * last_rate for m in early)


def classify(entity, months, coverage, values, total_last):
    """Signals from monthly costs (rules in the module docstring). months: sorted YYYY-MM; coverage: {month: share
    of the month's days with data}; values: one dict per (tag_key, tag_value) with "tag_key", "tag_value", "by_month"
    {month: cost}, "services" {service: {month: cost}}, "accounts" and "regions" (names by latest cost).
    total_last: the account's latest monthly run rate."""
    if len(months) < 2 or not _finite(total_last):
        return []
    covered = [m for m in months if _finite(coverage.get(m)) and coverage[m] > 0]  # rule 2
    if not covered or covered[-1] != months[-1]:
        return []
    last = covered[-1]
    prior = covered[-1 - RECENT:-1] if len(covered) > RECENT else covered[:-1]
    floor = max(MIN_ABS, MIN_SHARE * total_last)

    def rate(by_month):
        return {m: by_month.get(m, 0.0) / coverage[m] for m in covered}  # rule 3: a covered month without rows is 0

    def base_of(rt):
        return sum(rt[m] for m in prior) / len(prior) if prior else 0.0

    out = []
    for v in values:
        if not all(_finite(c) for bm in [v["by_month"], *v["services"].values()] for c in bm.values()):
            continue  # rule 9
        rt = rate(v["by_month"])
        now, base = rt[last], base_of(rt)
        kinds = []
        if now >= floor and _absent_before(rt, covered, now):
            kinds.append(NEW)
        elif now >= floor and base > 0 and now >= base * (1 + SURGE) and now - base >= floor:
            kinds.append(UP)
        svcs = []
        for s, bm in v["services"].items():
            sr = rate(bm)
            s_now = sr[last]
            svcs.append({"service": s, "cost_last": _r(bm.get(last, 0.0)), "delta": _r(s_now - base_of(sr)),
                         "is_new": bool(s_now >= floor and _absent_before(sr, covered, s_now))})
        new_svcs = sorted((x for x in svcs if x["is_new"]), key=lambda x: (-x["delta"], x["service"]))
        if NEW not in kinds and new_svcs:
            kinds.append(NEW_SVC)
        if not kinds:
            continue
        delta = now - base if NEW_SVC not in kinds or UP in kinds else sum(x["delta"] for x in new_svcs)
        out.append({"entity": entity, "source": "cur", "tag_key": v["tag_key"], "tag_value": v["tag_value"], "kinds": kinds,
                    "first_month": next((m for m in covered if rt[m] > NEW_EPS * now), last), "last_month": last,
                    "cost_by_month": [{"month": m, "cost": _r(v["by_month"].get(m, 0.0))} for m in months
                                      if m in rt or m in v["by_month"]],
                    "run_rate_last": _r(now), "base": _r(base), "delta": _r(delta),
                    "growth_pct": _r((now / base - 1) * 100) if base > 0 and NEW not in kinds else None,
                    "share_pct": _r(now / total_last * 100) if total_last > 0 else None,
                    "services": sorted(svcs, key=lambda x: (-x["cost_last"], x["service"]))[:TOP_SERVICES],
                    "new_services": [x["service"] for x in new_svcs][:TOP_SERVICES],
                    "accounts": list(v.get("accounts") or [])[:3], "regions": list(v.get("regions") or [])[:3]})
    out.sort(key=lambda r: (-r["delta"], r["tag_key"], r["tag_value"]))
    return out[:TOP]


def cot_signals(tag_services, account_resources, accounts):
    """COT signals from the COH snapshot: is_new tag services, and tag services living in new/fast-growing accounts."""
    acct = {(r["entity"], r["account"]): r for r in accounts}
    where = {}
    for r in account_resources:
        if r.get("tag_service"):
            k = (r["entity"], r["tag_service"])
            where.setdefault(k, {}).setdefault(r["account"], 0.0)
            where[k][r["account"]] += r.get("est_monthly_cost") or 0.0
    out = []
    for s in tag_services:
        if s["source_key"].startswith("("):  # untagged, commitment bucket
            continue
        trend = []
        for a, _ in sorted(where.get((s["entity"], s["service"]), {}).items(), key=lambda kv: (-kv[1], kv[0])):
            r = acct.get((s["entity"], a))
            if r and r.get("kind") in (NEW, UP):
                first = next((r[m] for m in ("m1", "m2", "m3", "m4", "m5") if r.get(m) is not None), None)
                trend.append({"account": a, "name": r.get("name") or "", "kind": r["kind"], "first": first, "last": r.get("m6"),
                              "pct_h2_h1": r.get("pct_h2_h1")})
        kinds = ([NEW] if s.get("is_new") else []) + ([NEW_ACCT] if trend else [])
        if not kinds:
            continue
        out.append({"entity": s["entity"], "source": "cot", "tag_key": s["source_key"], "tag_value": s["service"], "kinds": kinds,
                    "est_monthly_cost": s["est_monthly_cost"], "resources": s["resources"],
                    "created_recent_pct": s.get("created_recent_pct"), "first_created": s.get("first_created"),
                    "last_created": s.get("last_created"), "account_trend": trend[:3], "regions": s.get("regions", [])[:3],
                    "resource_kinds": s.get("resource_kinds", {}), "top_instance_types": s.get("top_instance_types", [])[:3]})
    out.sort(key=lambda r: (r["entity"], NEW not in r["kinds"], -r["est_monthly_cost"], r["tag_value"]))
    keep, n = [], {}
    for r in out:
        n[r["entity"]] = n.get(r["entity"], 0) + 1
        if n[r["entity"]] <= TOP:
            keep.append(r)
    return keep


def _load(d, n):
    p = os.path.join(d, n + ".json")
    return json.load(open(p, encoding="utf-8")) if os.path.exists(p) else []


def main(d):
    rows = cot_signals(_load(d, "tag_services"), _load(d, "account_resources"), _load(d, "accounts"))
    with open(os.path.join(d, "tag_signals.json"), "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)
    print("rows: tag_signals", len(rows), file=sys.stderr)


def ensure(d):
    """tag_signals.json from the COT datasets in d unless it is already there (a rerun of an older version)."""
    if not os.path.exists(os.path.join(d, "tag_signals.json")):
        main(d)


if __name__ == "__main__":
    main(sys.argv[1])
