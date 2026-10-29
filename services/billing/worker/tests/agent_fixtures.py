"""Shared agent test datasets (moved from test_agents.py): two entities, every dataset the digests read."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402

A, B = "acct-a", "acct-b"


from fileutil import wr, jload, jdump  # noqa: E402

def ds():
    """Two entities, every dataset the digests read."""
    rows = {n: [] for n in agents.SOURCES}
    for e, scale in ((A, 1), (B, 2)):
        rows["kpis"].append({"entity": e, "entity_ko": {A: "한빛금융", B: "누리증권"}[e], "last_spend": 1234567 * scale,
                             "prev_spend": 1200000, "mom_pct": 12.5, "top_service": "Amazon CloudFront", "top_service_last": 500000})
        rows["services"].append({"entity": e, "service": "Amazon CloudFront", "m1": 700000, "m5": 400000, "m6": 500000, "last": 500000})
        rows["data_transfer"].append({"entity": e, "category": "인터넷 아웃", "month": "2026-09", "cost": 5000, "gb": 120000})
        rows["benchmark"].append({"entity": e, "ec2_vcpu_hr": 0.0399, "rds_coverage": 52.3, "od_share": 44.4})
        rows["commitments"].append({"entity": e, "kind": "SP", "type": "EC2InstanceSavingsPlans", "account_name": f"name-{e}",
                                    "detail": "c7i", "end": "2026-12-16", "days_left": 59, "hourly": 84.0,
                                    "monthly_net_savings": 1876.25, "monthly_commit": 61320.0})
        rows["commitments"].append({"entity": e, "kind": "RI", "type": "RDS", "account_name": f"name-{e}", "detail": "db.r6g.large Aurora",
                                    "end": "2028-01-01", "days_left": 805, "hourly": 1.0, "monthly_net_savings": None, "monthly_commit": 730.0})
        rows["sp_purchase"].append({"entity": e, "plan": "COMPUTE_SP", "term": "ONE_YEAR", "payment": "NO_UPFRONT",
                                    "hourly_commit": 10.0, "monthly_savings": 108800.0, "upfront": 0.0, "od_hourly": 30.0})
        rows["unit_metrics"].append({"entity": e, "service": "RDS", "month": "2026-09", "cost": 30100.0, "coverage_pct": 52.3})
        rows["modernization"].append({"entity": e, "kind": "Graviton", "family": "c7i", "target": "c8g", "path": "Immediately Migratable",
                                      "service": "EC2", "instance_type": "c7i.8xlarge", "monthly_cost": 345600.0, "est_savings": 86400.0})
        rows["coh_by_workload"].append({"entity": e, "workload": f"wl-{e.lower()}", "action": "Rightsize", "action_ko": "라이트사이징",
                                        "recs": 21, "est_savings": 6000.0, "est_cost": 9000.0, "top_changes": []})
        rows["coh_actions"].append({"entity": e, "action": "Delete", "action_ko": "유휴 리소스 삭제", "est_savings": 5100.0, "recs": 512})
        rows["elasticity"].append({"entity": e, "service": "EC2", "month": "2026-09", "weekday_avg": 8100.0, "weekend_avg": 7850.0,
                                   "elasticity_pct": 3.1, "savings_at_goal": 12345.6})
        rows["storage_tiers"].append({"entity": e, "service": "EBS", "month": "2026-09", "tier": "gp2", "cost": 2222.2})
        rows["accounts"].append({"entity": e, "account": "100000000001" if e == A else "100000000002", "name": f"{e.lower()}-newbiz",
                                 "m1": 120.0, "m6": 10500.0, "kind": "급증", "services_by_tag": ["wl-cluster-a0"], "regions": ["ap-northeast-2"]})
        rows["focus"].append({"entity": e, "focus": "해외 확장", "evidence": "리전 스토리지 $5 → $2,000", "confidence": "높음"})
        rows["ec2_type_growth"].append({"entity": e, "instance_type": "r7i.4xlarge", "m1": 1000.0, "m5": 9000.0, "m6": 14300.0, "d_h2_h1": 13300.0})
        rows["bedrock_models"].append({"entity": e, "model": "Model A 2.0", "month": "2026-09", "cost": 150})
        rows["tag_services"].append({"entity": e, "service": f"svc-only-{e.lower()}"})
        rows["tag_signals"].append({"entity": e, "source": "cot", "tag_key": "app", "tag_value": f"wl-new-{e.lower()}", "kinds": ["신규"],
                                    "est_monthly_cost": 2500.0, "resources": 7, "created_recent_pct": 85.7, "first_created": "2026-07-02",
                                    "account_trend": [], "regions": ["ap-northeast-2"], "resource_kinds": {"EC2": 7}, "top_instance_types": []})
        rows["period"].append({"entity": e, "source": "cot", "months": ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"]})
        cur(rows, e, scale)
        rows["briefing"] += [
            {"entity": e, "rank": 1, "id": f"{e[:6].lower()}aaaaaa"[:12], "kind": "절감", "owner": "SA", "title": "Graviton 전환: 월 $91.2K 절감",
             "impact": 91234.5, "evidence": ["c7i→c8g 월 비용 $345.6K"], "next_step": "PoC 제안"},
            {"entity": e, "rank": 2, "id": f"{e[:6].lower()}bbbbbb"[:12], "kind": "위험", "owner": "TAM", "title": "RDS 커버리지 70% → 52%",
             "impact": 14321.5, "evidence": ["9월 RDS 비용 $30.1K"], "next_step": "RI 추가"},
        ]
    return rows


def cur(rows, e, scale=1):
    """CUR datasets (Task 8 shapes) for one entity."""
    for n in ("cur_summary", "cur_trend", "cur_tag_coverage", "cur_resources", "cur_tag_signals"):
        rows.setdefault(n, [])
    rows["cur_tag_signals"].append({"entity": e, "source": "cur", "tag_key": "team", "tag_value": f"team-{e}", "kinds": ["급증", "신규 서비스"],
                                    "first_month": "2026-08", "last_month": "2026-09",
                                    "cost_by_month": [{"month": "2026-08", "cost": 400.0 * scale}, {"month": "2026-09", "cost": 617.0 * scale}],
                                    "run_rate_last": 617.0 * scale, "base": 400.0 * scale, "delta": 217.0 * scale, "growth_pct": 54.3,
                                    "share_pct": 50.0, "services": [{"service": "AmazonBedrock", "cost_last": 120.0, "delta": 120.0, "is_new": True}],
                                    "new_services": ["AmazonBedrock"], "accounts": ["100000000001"], "regions": ["us-east-1"]})
    rows["cur_summary"].append({"entity": e, "format": "cur2-parquet", "currency": "USD", "first_day": "2026-08-01",
                                "last_day": "2026-09-30", "months": ["2026-08", "2026-09"], "rows": 5000, "linked_accounts": 3,
                                "cost_by_month": {"2026-08": 1100.0 * scale, "2026-09": 1234.0 * scale}, "dropped_rows": 0})
    t = lambda **kw: rows["cur_trend"].append({"entity": e, "grain": "month", **kw})
    for svc, c8, c9 in (("AmazonEC2", 800.0, 900.0), ("AmazonS3", 200.0, 250.0), ("AmazonRDS", 90.0, 70.0), ("AWSLambda", 10.0, 14.0)):
        t(period="2026-08", dim="service", key=svc, service=svc, cost=c8 * scale)
        t(period="2026-09", dim="service", key=svc, service=svc, cost=c9 * scale)
        t(period="2026-09", dim="tag:team", key=f"team-{e}", service=svc, cost=c9 * scale / 2)
        t(period="2026-09", dim="tag:env", key="prod", service=svc, cost=c9 * scale / 3)
    t(period="2026-09", dim="region", key="ap-northeast-2", service="AmazonEC2", cost=900.0 * scale)
    rows["cur_trend"].append({"entity": e, "grain": "day", "period": "2026-09-30", "dim": "service", "key": "AmazonEC2",
                              "service": "AmazonEC2", "cost": 7777.0})
    rows["cur_tag_coverage"] += [
        {"entity": e, "month": "2026-09", "tag_key": "team", "tagged_cost": 1000.0 * scale, "total_cost": 1234.0 * scale, "pct": 81.0},
        {"entity": e, "month": "2026-09", "tag_key": "env", "tagged_cost": 400.0 * scale, "total_cost": 1234.0 * scale, "pct": 32.4},
        {"entity": e, "month": "2026-08", "tag_key": "team", "tagged_cost": 555.5, "total_cost": 1100.0 * scale, "pct": 50.5}]
    rows["cur_resources"].append({"entity": e, "resource_id": f"i-0{e[-1]}abc123", "service": "AmazonEC2", "usage_type": "BoxUsage:c7i.8xlarge",
                                  "region": "ap-northeast-2", "linked_account": "100000000001", "tags": {"team": f"team-{e}"},
                                  "cost_last": 300.0 * scale, "cost_prev": 250.0 * scale, "cost_total": 550.0 * scale})
