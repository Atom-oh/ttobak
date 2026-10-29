"""agents.py: verifier, digests, run() wiring, cache, Bedrock adapter. Synthetic data and a fake model only."""
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


FEED = [  # whats_new_feed.json rows: no id/services; `entities` is a legacy classification that must be ignored
    {"date": "2026-09-01", "title": "Amazon CloudFront adds origin shield metrics", "url": "https://aws.amazon.com/about-aws/whats-new/2026/09/cf-a/",
     "entities": [], "topics": [], "source": "rss"},
    {"date": "2026-10-01", "title": "New edge locations", "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/edge/",
     "entities": [], "topics": ["CloudFront"], "source": "rss"},
    {"date": "2026-10-05", "title": "Amazon Connect adds a feature", "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/connect/",
     "entities": ["acct-a"], "topics": ["Contact Center"], "source": "rss"},
]


class Verify(unittest.TestCase):
    def setUp(self):
        self.D = ds()
        self.dg = agents.digest(self.D, A, "commitments")
        self.ix = agents.index(self.dg, agents.deny_terms(self.D, A, self.dg))

    def ok(self, text):
        self.assertTrue(agents.verify(text, self.ix), text)

    def bad(self, text):
        self.assertFalse(agents.verify(text, self.ix), text)

    def test_typed_forms_accept(self):
        for t in ["c7i 계열 SP가 2026-12-16에 끝나 월 $1.9K가 늘어납니다.", "월 $1,876 증가", "시간당 $84 약정",
                  "Compute SP 권고는 월 $108.8K", "RDS 커버리지 52%", "커버리지 52.3%", "59일 남음",
                  "c7i 계열", "name-acct-a 계정", "9월 기준", "Compute SP와 EC2 약정"]:
            self.ok(t)

    def test_rounding_boundaries(self):
        self.ok("월 $1.88K")  # 1876.25 within ±5
        self.bad("월 $1.89K")
        self.ok("$108.8K")
        self.bad("$108.9K")

    def test_signs(self):
        self.ok("월 +$1.9K")
        self.bad("월 −$1.9K")

    def test_rejects(self):
        self.bad("월 $9.9K 증가")            # not in digest
        self.bad("43건 만료")                 # no 43 in the count pool
        self.bad("월 9.47만 달러")            # Korean money word
        self.bad("2026-11-30 만료")           # date not in digest
        self.bad("name-acct-b 계정")  # other entity's account
        self.bad("누리증권과 비교")           # other entity's name
        self.bad("123456 시간")               # bare number not in digest
        self.bad("c9z.metal 전환")            # identifier not in digest

    def test_strict_forms(self):
        self.bad("$84k")            # lowercase or unknown suffix is not $84
        self.bad("$84m")
        self.bad("Graviton4 사용 중")  # identifier with digits absent from the input
        self.bad("gp3 볼륨")          # not in the commitments digest
        self.bad("11월 약정 만료")     # no November in the input
        self.bad("1분기 구매")         # no Q1 month in the input
        self.ok("9월과 12월")          # months present (2026-09, 2026-12-16)
        self.ok("4분기 만료")          # 2026-12 is in Q4
        self.bad("$ 59")               # currency sign the money pattern does not take
        self.bad("$+59")
        self.bad("prod2026-09 계정")    # identifier embedding an input month is still an identifier

    def test_short_foreign_names(self):
        D = ds()
        D["accounts"].append({"entity": B, "account": "1", "name": "qa", "m1": 1.0, "m6": 2.0, "kind": "유지"})
        dg = agents.digest(D, A, "commitments")
        ix = agents.index(dg, agents.deny_terms(D, A, dg))
        self.assertFalse(agents.verify("qa 계정 점검", ix))
        self.assertTrue(agents.verify("qanda 점검", ix))  # whole-token match only

    def test_joined_names_must_come_from_input(self):
        mod = agents.digest(self.D, A, "modernization")
        mix = agents.index(mod, agents.deny_terms(self.D, A, mod))
        self.assertTrue(agents.verify("wl-acct-a 워크로드부터", mix))
        self.assertFalse(agents.verify("unknown-prod 워크로드부터", mix))
        self.assertFalse(agents.verify("new_cluster 확인", mix))
        self.assertTrue(agents.verify("on-demand 비중과 Well-Architected 리뷰", mix))

    def test_no_added_precision_from_rounded_sources(self):
        ix = agents.index({"briefing": [{"title": "만료: SP 미갱신 시 월 $1.9K 증가", "evidence": ["커버리지 51%"]}]})
        self.assertTrue(agents.verify("월 $1.9K", ix))
        self.assertTrue(agents.verify("월 $2K", ix))
        self.assertFalse(agents.verify("월 $1,900", ix))
        self.assertFalse(agents.verify("월 $1.90K", ix))
        self.assertTrue(agents.verify("커버리지 51%", ix))
        self.assertFalse(agents.verify("커버리지 51.0%", ix))

    def test_named_accounts_and_dotted_names(self):
        mod = agents.digest(self.D, A, "modernization")
        mix = agents.index(mod, agents.deny_terms(self.D, A, mod))
        self.assertFalse(agents.verify("phantom 계정 점검", mix))
        self.assertFalse(agents.verify("unknown.prod 워크로드", mix))
        self.assertTrue(agents.verify("EKS 클러스터와 dev 계정", mix))
        empty = agents.index({})
        self.assertFalse(agents.verify("phantom 계정", empty))

    def test_type_confusion(self):
        self.bad("$59")         # days_left is a count, not money
        self.bad("84건")        # hourly is money, not a count
        self.bad("84   건")     # collapsed to "84 건" when rendered: still a count
        self.bad("84\t건")

    def test_currency_words_rejected(self):
        # coverage_pct 52.3 and cost 30100 are in the input: no currency form but $ may carry them as money
        for t in ("월 비용 52달러", "월 52.3 달러", "월 30,100원", "월 30100 원", "USD 84", "84 USD", "84 dollars", "84usd",
                  "₩30,100", "€84", "£84", "¥84", "US$84"):
            self.bad(t)
        for t in ("원인을 확인합니다.", "RDS 커버리지 52%의 원인", "시간당 $84 약정"):
            self.ok(t)

    def test_spaced_scale_words_rejected(self):
        # a scale word after the amount is not folded: "$1.2 million" must not verify as $1.2 (nor as 1.2M)
        ix = agents.index({"cost": 1.2, "commitments": [{"est_monthly": 1200000.0}], "note": "1.2"})
        self.assertTrue(agents.verify("월 $1.2", ix))
        self.assertTrue(agents.verify("월 $1.2M", ix))
        for t in ("월 $1.2 million", "월 $1.2 Million", "월 $1.2 K", "월 $1.2 k", "월 $1.2 M", "월 $1.2 B", "월 $1.2 bn",
                  "월 $1.2 thousand", "월 $1.2M million", "월 1.2 million", "월 $1.2 억", "월 1.2 억원", "월 1.2 만",
                  "월 1.2 천", "월 $1.2\t억"):
            self.assertFalse(agents.verify(t, ix), t)
        for t in ("$1.2 Savings Plans", "$1.2 만료", "1.2 만료"):  # a following word that is not a scale
            self.assertTrue(agents.verify(t, ix), t)
        self.assertNotIn(("bad", "money-scale"), agents._scan("$1.2 T3 인스턴스"))  # an instance family, not a scale

    def test_korean_calendar_year_from_an_iso_month(self):
        ix = agents.index({"months": ["2026-08", "2026-09"], "commitments": [{"days_left": 3}]})
        self.assertTrue(agents.verify("2026년 9월 기준", ix))
        self.assertTrue(agents.verify("2026년", ix))
        self.assertFalse(agents.verify("2027년 9월", ix))
        self.assertFalse(agents.verify("3년 약정", ix))  # a duration is still a count (days_left 3 is 일, not 년)

    def test_count_kpis_are_not_money(self):
        D = ds()
        D["kpis"][0].update(last_spend=700, accounts_last=12, accounts_first=9, bedrock_models_last=3)
        ix = agents.area_index(D, A, "spend")
        for t in ("활성 계정 12개", "계정 9개에서", "모델 3개", "월 비용 $700"):
            self.assertTrue(agents.verify(t, ix), t)
        for t in ("월 비용 $12", "$9 증가", "$3"):
            self.assertFalse(agents.verify(t, ix), t)

    def test_identifiers_ignored_digits(self):
        mod = agents.digest(self.D, A, "modernization")
        self.assertTrue(agents.verify("c7i에서 c8g로 전환", agents.index(mod, agents.deny_terms(self.D, A, mod))))
        gro = agents.digest(self.D, A, "growth")
        self.assertTrue(agents.verify("ap-northeast-2 리전의 r7i.4xlarge", agents.index(gro, agents.deny_terms(self.D, A, gro))))
        self.bad("r8g로 전환")  # r8g is in no digest


class Digest(unittest.TestCase):
    def test_entity_only_and_keys(self):
        D = ds()
        for area, keys in agents.AREA_KEYS.items():
            dg = agents.digest(D, A, area)
            self.assertEqual(set(dg) - {"entity", "entity_ko", "area", "briefing", "months"}, set(keys), area)
            blob = json.dumps(dg, ensure_ascii=False)
            self.assertNotIn(B, blob, area)
            self.assertNotIn("누리증권", blob, area)
            self.assertEqual([b["rank"] for b in dg["briefing"]], [1, 2])

    def test_commitments_window(self):
        dg = agents.digest(ds(), A, "commitments")
        self.assertTrue(all(r["days_left"] <= 365 for r in dg["commitments"]))


class FakeModel:
    """Returns canned outputs; records calls. Area output mentions a bad number in one finding."""

    def __init__(self, bad_entity_field=False, shuffle=False):
        self.calls, self.lock, self.shuffle, self.bad_entity_field = [], threading.Lock(), shuffle, bad_entity_field
        self.synth_inputs = []

    def __call__(self, system, user, name, schema):
        with self.lock:
            self.calls.append(name)
        if self.shuffle:
            time.sleep(random.random() / 50)
        u = json.loads(user)
        e = u["entity"]
        if name == "submit_findings":
            out = {"findings": [
                {"title": "c7i 약정 만료", "detail": "2026-12-16 만료", "so_what": "Compute SP로 갱신", "datasets": ["commitments", "evil"], "confidence": "높음"},
                {"title": "근거 없는 숫자", "detail": "월 $9.9K", "so_what": "x", "datasets": [], "confidence": "낮음"},
            ]}
            if self.bad_entity_field:
                out["entity"] = B
            return out
        if name == "submit_signals":
            return {"signals": [
                {"title": "태그 근거 없는 가설", "tag": "made-up", "detail": "고객이 새 사업을 준비합니다.", "so_what": "인터뷰합니다.",
                 "help": "Demo 시연", "questions": [], "datasets": [], "confidence": "낮음"},
                {"title": "새 워크로드 확장", "tag": f"wl-new-{e.lower()}", "detail": "COH 리소스 7개, 월 $2.5K",
                 "so_what": "출시 전 아키텍처 리뷰를 제안합니다.", "help": "아키텍처 리뷰", "questions": ["출시 일정은?", "월 $9.9K 예산은?"],
                 "datasets": ["tag_signals", "evil"], "confidence": "중간"}]}
        if name == "submit_sessions":
            return {"sessions": [{"title": "c7i 약정과 Graviton 전환 워크숍", "type": "아키텍처 제안", "signal": "",
                                  "why": "c7i 계열 SP가 2026-12-16에 끝납니다.",
                                  "audience": "인프라팀·SA", "agenda": ["c7i 계열 PoC 범위", "c8g 전환 순서"]}]}
        if name == "submit_market":
            return {"items": [{"item": 1, "why": "CloudFront 지출이 가장 큰 서비스입니다."}]}
        with self.lock:
            self.synth_inputs.append(u)
        return {"headline": "c7i 약정 만료가 Graviton 전환 시점을 정합니다.", "entity": B, "action_id": "zzz",
                "actions": [{"rank": 1, "next_step": "c7i 계열 PoC를 제안합니다.", "questions": ["c7i 워크로드의 아키텍처 제약은?", "월 $9.9K 예산은?"]},
                            {"rank": 7, "next_step": "없는 항목", "questions": []}],
                "links": [{"title": "약정 × 현대화", "detail": "c7i 약정과 c8g 전환"}]}


class Run(unittest.TestCase):
    def run_once(self, model, cache):
        return agents.generate(ds(), model, cache, model_id="test-model", workers=4)["notes"]

    def test_rows_identity_and_drops(self):
        with tempfile.TemporaryDirectory() as c:
            rows = self.run_once(FakeModel(bad_entity_field=True), c)
        mine = [r for r in rows if r["entity"] == A]
        kinds = {r["kind"] for r in mine}
        self.assertEqual(kinds, {"meta", "finding", "headline", "action", "link"})
        acts = [r for r in mine if r["kind"] == "action"]
        self.assertEqual([r["action_id"] for r in acts], ["acct-aaaaaaa"])  # rank 7 dropped; code-assigned id
        self.assertEqual(acts[0]["questions"], ["c7i 워크로드의 아키텍처 제약은?"])  # unverifiable question dropped
        fins = [r for r in mine if r["kind"] == "finding"]
        self.assertTrue(all("$9.9K" not in json.dumps(f, ensure_ascii=False) for f in fins))
        self.assertTrue(all(f["datasets"] in ([], ["commitments"]) for f in fins if f["area"] != "business"))
        meta = next(r for r in mine if r["kind"] == "meta")
        self.assertGreater(meta["dropped"], 0)
        self.assertEqual(meta["model"], "test-model")
        self.assertEqual(meta["source_ids"], sorted(r["id"] for r in ds()["briefing"] if r["entity"] == A))
        self.assertTrue(all(r["entity"] in (A, B) for r in rows))
        self.assertTrue(all(r.get("source_fp") for r in rows if r["kind"] != "meta"))

    def test_synthesis_cannot_add_precision(self):
        class Precise(FakeModel):
            def __call__(self, system, user, name, schema):
                if name == "submit_findings":
                    return {"findings": [{"title": "c7i 만료 영향 월 $1.9K", "detail": "", "so_what": "", "datasets": [], "confidence": "높음"}]}
                return {"headline": "월 $1,900 증가가 걸려 있습니다.", "actions": [{"rank": 1, "next_step": "월 $1.9K 영향부터 봅니다.", "questions": []}], "links": []}
        with tempfile.TemporaryDirectory() as c:
            rows = self.run_once(Precise(), c)
        mine = [r for r in rows if r["entity"] == A]
        self.assertFalse(any(r["kind"] == "headline" for r in mine))          # $1,900 is not in the data (1876.25)
        self.assertEqual([r["next_step"] for r in mine if r["kind"] == "action"], ["월 $1.9K 영향부터 봅니다."])

    def test_synthesis_sees_only_verified(self):
        m = FakeModel()
        with tempfile.TemporaryDirectory() as c:
            self.run_once(m, c)
        for u in m.synth_inputs:
            self.assertNotIn("$9.9K", json.dumps(u, ensure_ascii=False))

    def test_deterministic_under_shuffle_and_cached(self):
        with tempfile.TemporaryDirectory() as c1, tempfile.TemporaryDirectory() as c2:
            a = json.dumps(self.run_once(FakeModel(shuffle=True), c1), ensure_ascii=False)
            b = json.dumps(self.run_once(FakeModel(shuffle=True), c2), ensure_ascii=False)
            self.assertEqual(a, b)
            m = FakeModel()
            c = json.dumps(self.run_once(m, c1), ensure_ascii=False)
            self.assertEqual(m.calls, [])
            self.assertEqual(a, c)

    def test_cache_check_without_git_fails_closed(self):
        from unittest import mock
        with tempfile.TemporaryDirectory() as c, mock.patch.dict(os.environ, {"AGENT_CACHE": c}), \
                mock.patch.object(agents.subprocess, "run", side_effect=FileNotFoundError("git")):
            with self.assertRaises(SystemExit):
                agents.cache_dir()
            with mock.patch.object(agents, "run") as run:
                self.assertEqual(agents.main([c]), 2)
            run.assert_not_called()

    def test_corrupt_cache_is_miss(self):
        with tempfile.TemporaryDirectory() as c:
            self.run_once(FakeModel(), c)
            for f in os.listdir(c):
                wr("{not json", os.path.join(c, f), "w")
            m = FakeModel()
            self.run_once(m, c)
            self.assertGreater(len(m.calls), 0)

    def test_model_error_fails_closed(self):
        def boom(*a):
            raise agents.AgentError("down")
        with tempfile.TemporaryDirectory() as c:
            rows = agents.generate(ds(), boom, c, model_id="m", workers=2)["notes"]
        self.assertEqual({r["kind"] for r in rows}, {"meta"})
        self.assertTrue(all(r["errors"] for r in rows))


def write_dir(d, D, names=None):
    for n, rows in D.items():
        if names is None or n in names:
            jdump(rows, os.path.join(d, n + ".json"), "w", encoding="utf-8", ensure_ascii=False)


def read(d, n):
    return jload(os.path.join(d, n + ".json"), encoding="utf-8")


class CurModel(FakeModel):
    def __call__(self, system, user, name, schema):
        with self.lock:
            self.calls.append(name)
        u = json.loads(user)
        if name == "submit_findings" and u.get("area") == "cur":
            return {"findings": [
                {"title": "9월 CUR 지출 $1,234", "detail": "AmazonEC2가 $900로 가장 큽니다.", "so_what": "team 태그 커버리지 81%부터 봅니다.",
                 "datasets": ["cur_summary", "cur_trend", "kpis"], "confidence": "높음"},
                {"title": "근거 없는 지출 $9,999", "detail": "x", "so_what": "y", "datasets": [], "confidence": "낮음"}]}
        return super().__call__(system, user, name, schema)


class CurArea(unittest.TestCase):
    def test_cur_digest_scoped_and_verified(self):
        D = ds()
        dg = agents.digest(D, A, "cur")
        blob = json.dumps(dg, ensure_ascii=False)
        self.assertNotIn(B, blob)
        self.assertNotIn("7777", blob)  # day grain stays out of the month digest
        self.assertEqual(dg["months"], ["2026-08", "2026-09"])
        self.assertEqual({r["dim"] for r in dg["cur_trend"]}, {"service", "tag:team"})  # top tag key only, no region dim
        self.assertEqual({r["service"] for r in dg["cur_trend"] if r["dim"] != "service"}, {"AmazonEC2", "AmazonS3", "AmazonRDS"})
        self.assertTrue(all(r["month"] == "2026-09" for r in dg["cur_tag_coverage"]))
        m = CurModel()
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, m, c, model_id="m", workers=2, areas=["cur"], synth=False, extras=False)
        self.assertEqual(set(m.calls), {"submit_findings"})
        mine = [r for r in out["notes"] if r["entity"] == A]
        fins = [r for r in mine if r["kind"] == "finding"]
        self.assertEqual([f["title"] for f in fins], ["9월 CUR 지출 $1,234"])
        self.assertEqual(fins[0]["area"], "cur")
        self.assertEqual(fins[0]["datasets"], ["cur_summary", "cur_trend"])  # kpis is not a cur source
        self.assertNotIn("$9,999", json.dumps(out["notes"], ensure_ascii=False))
        meta = next(r for r in mine if r["kind"] == "meta")
        self.assertEqual(meta["dropped"], 3)
        self.assertEqual(meta["areas"], ["cur"])
        self.assertEqual(out["ai"], "ok")

    def test_cur_only_dir_needs_no_cot_datasets(self):
        D = {n: [] for n in agents.SOURCES}
        cur(D, A)
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as c:
            write_dir(d, D, {"cur_summary", "cur_trend", "cur_tag_coverage", "cur_resources"})
            res = agents.run(d, areas=["cur"], synth=False, extras=False, invoke=CurModel(), cache=c, model_id="m")
            self.assertEqual(res, {"ai": "ok"})
            notes = read(d, "agent_notes")
            self.assertFalse(os.path.exists(os.path.join(d, "sessions.json")))
            self.assertFalse(os.path.exists(os.path.join(d, "market.json")))
        self.assertEqual([r["entity"] for r in notes if r["kind"] == "meta"], [A])
        self.assertTrue(any(r["kind"] == "finding" and r["area"] == "cur" for r in notes))

    def test_non_usd_cur_is_not_relabeled_as_dollars(self):
        D = ds()
        D["cur_summary"][0]["currency"] = "KRW"  # acct-a only
        m = CurModel()
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, m, c, model_id="m", workers=2, areas=agents.CUR_AREAS, synth=False, extras=False)
        fins = {(r["entity"], r["area"]) for r in out["notes"] if r["kind"] == "finding"}
        self.assertFalse({x for x in fins if x[0] == A})  # neither the CUR area nor CUR-backed business signals
        self.assertIn((B, "business"), fins)
        self.assertEqual(next(r for r in out["notes"] if r["entity"] == B and r["kind"] == "meta")["errors"], [])
        meta = next(r for r in out["notes"] if r["entity"] == A and r["kind"] == "meta")
        self.assertEqual(sorted(meta["errors"]), ["business: unsupported CUR currency KRW", "cur: unsupported CUR currency KRW"])
        self.assertEqual(m.calls.count("submit_findings"), 1)  # acct-b's cur only; acct-a was never sent
        D["cur_summary"][1]["currency"] = "<b>x"
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, CurModel(), c, model_id="m", workers=2, areas=["cur"], synth=False, extras=False)
        meta = next(r for r in out["notes"] if r["entity"] == B and r["kind"] == "meta")
        self.assertEqual(meta["errors"], ["cur: unsupported CUR currency ?"])

    def test_prune_tracks_cur_area(self):
        D = ds()
        with tempfile.TemporaryDirectory() as c:
            notes = agents.generate(D, CurModel(), c, model_id="m", workers=2, areas=["cur"], synth=False, extras=False)["notes"]
        self.assertEqual(agents.prune(D, notes), notes)
        D["cur_resources"][0]["cost_last"] = 301.0  # acct-a's CUR input changed
        kept = agents.prune(D, notes)
        self.assertEqual({r["entity"] for r in kept}, {B})


class Business(unittest.TestCase):
    def test_digest_puts_tag_signals_first_and_is_scoped(self):
        dg = agents.digest(ds(), A, "business")
        self.assertEqual(list(dg)[:6], ["entity", "entity_ko", "area", "months", "cur_tag_signals", "tag_signals"])
        self.assertEqual([r["tag_value"] for r in dg["tag_signals"]], ["wl-new-acct-a"])
        self.assertEqual(dg["cur_tag_signals"][0]["new_services"], ["AmazonBedrock"])
        self.assertEqual([r["change"] for r in dg["service_changes"]], [])  # CloudFront fell: neither new nor growing
        self.assertNotIn(B, json.dumps(dg, ensure_ascii=False))
        ix = agents.area_index(ds(), A, "business")
        self.assertTrue(agents.verify("team-acct-a 태그가 월 $617에서 늘었습니다(+54.3%).", ix))
        self.assertTrue(agents.verify("기준 월 $400 대비 $217 증가", ix))  # base and delta are money
        D = ds()
        D["cur_tag_signals"][0]["share_pct"] = 0.35
        ix = agents.area_index(D, A, "business")
        self.assertTrue(agents.verify("계정 비용의 0.35%", ix))
        self.assertFalse(agents.verify("계정 비용의 35%", ix))  # share_pct is already a percent

    def test_benchmark_share_is_a_percent_not_a_fraction(self):
        D = ds()
        for r in D["benchmark"]:
            r["graviton_share"] = 0.8  # 0.8% Graviton, as briefing.py emits it
        ix = agents.area_index(D, A, "spend")
        self.assertTrue(agents.verify("Graviton 비중 0.8%", ix))
        self.assertFalse(agents.verify("Graviton 비중 80%", ix))  # not scaled by 100

    def test_service_changes_new_and_growing(self):
        D = ds()
        D["services"] += [{"entity": A, "service": "Amazon Bedrock", "m1": None, "m2": 0, "m3": None, "m4": 50.0, "m5": 80.0, "m6": 120.0},
                          {"entity": A, "service": "Amazon EKS", "m1": 100.0, "m2": 120.0, "m3": 150.0, "m4": 200.0, "m5": 250.0, "m6": 300.0}]
        sc = agents.digest(D, A, "business")["service_changes"]
        self.assertEqual([(r["change"], r["service"]) for r in sc], [("신규", "Amazon Bedrock"), ("증가", "Amazon EKS")])

    def test_service_changes_ignore_padded_slots(self):
        # a two-month period: m1..m4 are padding (null), not observed zero spend
        D = ds()
        pad = dict.fromkeys(["m1", "m2", "m3", "m4"])
        D["services"] = [{"entity": A, "service": "Amazon EC2", **pad, "m5": 100.0, "m6": 90.0},
                         {"entity": A, "service": "Amazon S3", **pad, "m5": 50.0, "m6": 80.0},
                         {"entity": A, "service": "Amazon SQS", **pad, "m5": None, "m6": 10.0}]
        sc = agents.digest(D, A, "business")["service_changes"]
        self.assertEqual([(r["change"], r["service"]) for r in sc], [("증가", "Amazon S3")])  # EC2 fell; SQS has no base

    def test_signals_cleaned_tag_backed_first(self):
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(ds(), FakeModel(), c, model_id="m", workers=2)
        biz = [r for r in out["notes"] if r["entity"] == A and r["kind"] == "finding" and r["area"] == "business"]
        self.assertEqual([(r["title"], r["tag"]) for r in biz], [("새 워크로드 확장", "wl-new-acct-a"), ("태그 근거 없는 가설", "")])
        self.assertEqual(biz[0]["questions"], ["출시 일정은?"])  # $9.9K is not in the data
        self.assertEqual(biz[0]["help"], "아키텍처 리뷰")
        self.assertEqual(biz[0]["datasets"], ["tag_signals"])
        meta = next(r for r in out["notes"] if r["entity"] == A and r["kind"] == "meta")
        self.assertEqual(meta["areas"], agents.COT_AREAS)
        self.assertEqual(agents.COT_AREAS[0], "business")

    def test_cur_job_runs_business_on_cur_signals(self):
        D = {n: [] for n in agents.SOURCES}
        cur(D, A)
        seen = []

        class M(CurModel):
            def __call__(self, system, user, name, schema):
                if name == "submit_signals":
                    seen.append(json.loads(user))
                    return {"signals": [{"title": "팀 단위 GenAI 도입", "tag": f"team-{A}", "detail": "AmazonBedrock가 새로 붙었습니다.",
                                         "so_what": "GenAI 데모를 제안합니다.", "help": "Demo 시연", "questions": [], "datasets": ["cur_tag_signals"],
                                         "confidence": "중간"}]}
                return super().__call__(system, user, name, schema)
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, M(), c, model_id="m", workers=2, areas=agents.CUR_AREAS, synth=False, extras=False)
        self.assertEqual(seen[0]["cur_tag_signals"][0]["tag_value"], f"team-{A}")
        biz = [r for r in out["notes"] if r["kind"] == "finding" and r["area"] == "business"]
        self.assertEqual([(r["tag"], r["help"]) for r in biz], [(f"team-{A}", "Demo 시연")])


class SessionModel(FakeModel):
    def __init__(self, sessions):
        super().__init__()
        self.sessions, self.session_inputs = sessions, []

    def __call__(self, system, user, name, schema):
        if name == "submit_sessions":
            with self.lock:
                self.calls.append(name)
                self.session_inputs.append(json.loads(user))
            return {"sessions": self.sessions}
        return super().__call__(system, user, name, schema)


class Sessions(unittest.TestCase):
    def gen(self, model, D=None):
        with tempfile.TemporaryDirectory() as c:
            return agents.generate(D or ds(), model, c, model_id="m", workers=2)

    def test_unverified_agenda_item_dropped(self):
        m = SessionModel([
            {"title": "c7i 약정 갱신 워크숍", "type": "AWS 세션 진행", "signal": "새 워크로드 확장",
             "why": "c7i 계열 SP가 2026-12-16에 끝납니다.", "audience": "인프라팀",
             "agenda": ["c7i 계열 PoC 범위", "월 $9.9K 예산 확인", "c8g 전환 순서"]},
            {"title": "월 $9.9K 절감 세션", "type": "AWS 세션 진행", "signal": "", "why": "근거 없음", "audience": "재무팀", "agenda": ["x"]},
            {"title": "RDS 커버리지 점검", "type": "딥다이브 비교분석", "signal": "없는 신호", "why": "커버리지가 2026-11-30에 떨어집니다.", "audience": "DBA", "agenda": []}])
        out = self.gen(m)
        mine = [r for r in out["sessions"] if r["entity"] == A]
        self.assertEqual([r["title"] for r in mine], ["c7i 약정 갱신 워크숍", "RDS 커버리지 점검"])
        self.assertEqual([r["rank"] for r in mine], [1, 2])
        self.assertEqual(mine[0]["agenda"], ["c7i 계열 PoC 범위", "c8g 전환 순서"])
        self.assertIsNone(mine[1]["why"])  # unverifiable date
        for r in mine:
            self.assertEqual(r["origin"], "generated")
            self.assertEqual(set(r), {"entity", "origin", "rank", "title", "type", "signal", "why", "audience", "agenda"})
        self.assertEqual([(r["type"], r["signal"]) for r in mine], [("AWS 세션 진행", "새 워크로드 확장"), ("딥다이브 비교분석", "")])
        self.assertNotIn("$9.9K", json.dumps(out["sessions"], ensure_ascii=False))
        meta = next(r for r in out["notes"] if r["entity"] == A and r["kind"] == "meta")
        self.assertGreaterEqual(meta["dropped"], 4 + 1 + 1)  # session title kills its 4 fields; 1 agenda; 1 why

    def test_session_input_is_verified_only(self):
        m = SessionModel([])
        self.gen(m)
        u = next(x for x in m.session_inputs if x["entity"] == A)
        self.assertEqual([a["rank"] for a in u["actions"]], [1, 2])
        self.assertEqual(u["actions"][0]["next_step"], "c7i 계열 PoC를 제안합니다.")  # verified synthesis step replaces the rule one
        self.assertNotIn("$9.9K", json.dumps(m.session_inputs, ensure_ascii=False))
        self.assertTrue(u["findings"])
        self.assertFalse([f for f in u["findings"] if f["area"] == "business"])  # business findings go in as signals
        self.assertEqual([x["title"] for x in u["signals"]], ["새 워크로드 확장", "태그 근거 없는 가설"])
        self.assertNotIn(B, json.dumps(u, ensure_ascii=False))


class MarketModel(FakeModel):
    def __init__(self, items):
        super().__init__()
        self.items, self.market_inputs = items, []

    def __call__(self, system, user, name, schema):
        if name == "submit_market":
            with self.lock:
                self.calls.append(name)
                self.market_inputs.append(json.loads(user))
            return {"items": self.items}
        return super().__call__(system, user, name, schema)


NOTE = {"origin": "generated", "kind": "note", "text": "FSI 사례 조사(웹 검색)는 아직 연결되지 않았습니다."}


class Market(unittest.TestCase):
    def gen(self, model, feed=FEED):
        with tempfile.TemporaryDirectory() as c:
            return agents.generate(ds(), model, c, model_id="m", workers=2, feed=feed)

    def test_candidates_match_services_newest_first(self):
        m = MarketModel([])
        self.gen(m)
        u = next(x for x in m.market_inputs if x["entity"] == A)
        self.assertEqual(u["services"], ["Amazon CloudFront"])
        self.assertEqual([(i["item"], i["title"]) for i in u["items"]],
                         [(1, "New edge locations"), (2, "Amazon CloudFront adds origin shield metrics")])  # legacy entities ignored
        big = [dict(FEED[0], date=f"2026-0{1 + i % 9}-1{i % 10}", url=f"https://aws.amazon.com/x/{i}") for i in range(60)]
        m = MarketModel([])
        self.gen(m, big)
        self.assertEqual(len(m.market_inputs[0]["items"]), 40)

    def test_service_name_forms(self):
        c = agents.feed_candidates([{"date": "2026-10-01", "title": "Amazon EC2 C8g instances in more regions", "url": "https://aws.amazon.com/a", "topics": []},
                                    {"date": "2026-10-02", "title": "Something else", "url": "https://aws.amazon.com/b", "topics": ["RDS·Aurora"]},
                                    {"date": "2026-10-03", "title": "Amazon S3 Tables", "url": "https://aws.amazon.com/c", "topics": []},
                                    {"date": "2026-10-04", "title": "Amazon EC2X unrelated", "url": "https://aws.amazon.com/d", "topics": []}],
                                   ["Amazon Elastic Compute Cloud - Compute", "AmazonRDS"])
        self.assertEqual([r["url"][-1] for r in c], ["b", "a"])

    def test_feed_urls_need_the_link_shape(self):
        bad = ["https://aws.amazon.com/a b", "https://aws.amazon.com/a\"x", "https://aws.amazon.com/a)(b", "https://u@evil.invalid/",
               "https://aws.amazon.com:8443/x", "https://aws.amazon.com\\x", "https://aws.amazon.com/<b>", "https://aws.amazon.com/" + "a" * 2000]
        feed = [{"date": "2026-10-0%d" % (i % 9 + 1), "title": "Amazon CloudFront item %d" % i, "url": u, "topics": []} for i, u in enumerate(bad)]
        feed.append({"date": "2026-10-09", "title": "Amazon CloudFront ok", "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/ok/"})
        self.assertEqual([r["title"] for r in agents.feed_candidates(feed, ["Amazon CloudFront"])], ["Amazon CloudFront ok"])

    def test_unknown_item_index_dropped(self):
        out = self.gen(MarketModel([{"item": 99, "why": "CloudFront 지출"}, {"item": 0, "why": "CloudFront"},
                                    {"item": 2, "why": "CloudFront 지출이 가장 큽니다."}, {"item": 2, "why": "CloudFront 중복"}]))
        mine = [r for r in out["market"] if r["entity"] == A and r.get("kind") != "note"]
        self.assertEqual([r["title"] for r in mine], ["Amazon CloudFront adds origin shield metrics"])

    def test_title_url_copied_from_feed_not_model(self):
        out = self.gen(MarketModel([{"item": 1, "why": "CloudFront 지출이 가장 큰 서비스입니다.", "title": "가짜 제목",
                                     "url": "https://evil.invalid/x", "date": "2030-01-01"},
                                    {"item": 2, "why": "월 $9.9K 절감"}]))
        mine = [r for r in out["market"] if r["entity"] == A and r.get("kind") != "note"]
        self.assertEqual(mine, [{"entity": A, "origin": "generated", "title": "New edge locations", "date": "2026-10-01",
                                 "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/edge/",
                                 "why": "CloudFront 지출이 가장 큰 서비스입니다."}])  # unverified why drops item 2

    def test_no_fsi_note_present(self):
        for feed in (FEED, None):
            m = MarketModel([{"item": 1, "why": "CloudFront"}])
            out = self.gen(m, feed)
            notes = [r for r in out["market"] if r["entity"] == A and r.get("kind") == "note"]
            self.assertEqual(notes, [{"entity": A, **NOTE}])
        self.assertNotIn("submit_market", m.calls)  # no feed: no model call, only the note
        self.assertEqual([r for r in out["market"] if r["entity"] == A], [{"entity": A, **NOTE}])


class FsiModel(FakeModel):
    """Query agent: one safe query, one naming the customer, one with a digit. Case agent: `cases`."""

    def __init__(self, cases):
        super().__init__()
        self.cases, self.inputs = cases, {}

    def __call__(self, system, user, name, schema):
        if name in ("submit_queries", "submit_cases"):
            with self.lock:
                self.calls.append(name)
                self.inputs.setdefault(name, []).append(json.loads(user))
            if name == "submit_queries":
                return {"queries": [{"query": "AWS securities firm new workload case study", "signal": "새 워크로드 확장"},
                                    {"query": "한빛금융 AWS bank case", "signal": ""},
                                    {"query": "AWS bank 2026 migration", "signal": ""}]}
            return {"cases": self.cases}
        return super().__call__(system, user, name, schema)


RESULTS = [{"query": 0, "items": [
    {"title": "Example Securities builds a trading platform on Amazon EKS", "url": "https://example.com/case-1", "date": "2026-05-01",
     "text": "Example Securities moved its trading platform to Amazon EKS."},
    {"title": "Dup", "url": "https://example.com/case-1", "date": "", "text": ""},
    {"title": "Insecure", "url": "http://example.com/x", "date": "", "text": ""},
    {"title": "Userinfo", "url": "https://aws.amazon.com@evil.test/x", "date": "", "text": ""},
    {"title": "Backslash", "url": "https://good.test\\@evil.test/", "date": "", "text": ""},
    {"title": "Careers at Example", "url": "https://example.com/jobs", "date": "not-a-date", "text": "join us"}]}]


class Fsi(unittest.TestCase):
    def test_korean_org_followed_by_particle(self):
        items = [{"title": "한빛은행이 AWS로 코어뱅킹을 옮겼습니다", "url": "https://x.example/k", "date": "", "text": "", "signal": ""}]
        rows = agents._clean_cases(ds(), A, {"cases": [{"item": 1, "org": "한빛은행", "signal": "", "why": "코어뱅킹 전환 사례입니다."}]},
                                   items, [], {"checked": 0, "dropped": 0})
        self.assertEqual([r["org"] for r in rows], ["한빛은행"])

    def gen(self, model, search):
        with tempfile.TemporaryDirectory() as c:
            return agents.generate(ds(), model, c, model_id="m", workers=2, search=search)

    def test_safe_query(self):
        private = agents.private_terms(ds())
        self.assertIn("한빛금융", private)
        self.assertIn("acct-a-newbiz", private)
        self.assertIn("wl-new-acct-a", private)
        ok = lambda q: agents.safe_query(q, private)  # noqa: E731
        self.assertTrue(ok("AWS bank EKS migration case study"))
        self.assertTrue(ok("증권사 AWS 클라우드 전환 사례"))
        self.assertFalse(ok("AWS EKS migration case study"))  # no industry word
        self.assertFalse(ok("한빛금융 AWS bank"))             # customer name
        self.assertFalse(ok("acct-a bank on AWS"))            # entity id
        self.assertFalse(ok("wl-new-acct-a fintech AWS"))     # tag value
        self.assertFalse(ok("AWS bank 2026"))                 # digits
        self.assertFalse(ok("x" * 121 + " bank"))
        D = ds()
        D["tag_services"].append({"entity": A, "service": "payments"})
        D["tag_services"].append({"entity": A, "service": "pay"})
        p = agents.private_terms(D)
        self.assertTrue(agents.safe_query("AWS payments platform case", p))  # an industry-word tag value blocks nothing
        self.assertFalse(agents.safe_query("AWS pay app fintech", p))
        self.assertTrue(agents.safe_query("AWS payment fintech", p))         # "pay" only as a whole word

    def test_safe_query_respelled_and_partial_names(self):
        D = ds()
        D["kpis"][0]["entity"], D["kpis"][0]["entity_ko"] = "AcmeCore", "에이크미 금융"
        D["accounts"].append({"entity": A, "account": "100000000009", "name": "acme-payments-prod"})
        D["tag_signals"].append({"entity": A, "tag_value": "orbit_ledger"})
        p = agents.private_terms(D)
        for q in ("Acme Core bank AWS", "acme-core bank AWS", "ＡＣＭＥ bank AWS", "AWS bank Acme", "에이크미금융 AWS 은행",
                  "orbit ledger fintech AWS", "orbitledger fintech AWS", "AWS bank ledger orbit"):
            self.assertFalse(agents.safe_query(q, p), q)
        for q in ("AWS core banking EKS case", "AWS payments platform production case", "AWS bank mainframe migration"):
            self.assertTrue(agents.safe_query(q, p), q)  # common parts and industry words alone block nothing
        self.assertTrue(agents.safe_query("AWS bank ledger migration", p))  # a part that is a vocabulary word blocks nothing

    def test_safe_query_allowlist_short_and_hidden_names(self):
        D = ds()
        D["kpis"][0]["entity_ko"] = "한빛"
        D["kpis"][1]["entity"] = "tb"
        p = agents.private_terms(D)
        for q in ("한빛 은행 AWS 사례", "TB bank AWS", "누리 증권 AWS bank", "누리\u200b증권 bank AWS",  # short, spaced, zero-width
                  "Hanbit Pay fintech AWS", "Hanbit bank AWS",  # translated or unknown names: not in the vocabulary
                  "AWS bank <b>case</b>", "AWS bank case site:example.com"):
            self.assertFalse(agents.safe_query(q, p), q)
        self.assertTrue(agents.safe_query("AWS bank Bedrock generative AI case study", p))
        for q in ("Korea Investment & Securities AWS case", "Korea Exchange bank AWS", "Digital Asset bank"):
            self.assertFalse(agents.safe_query(q, p), q)  # brand-shaped phrases of generic words
        D["tag_signals"].append({"entity": A, "tag_key": "app", "tag_value": "ai-platform"})
        D["accounts"].append({"entity": A, "account": "1", "name": "kafka-stream"})
        p = agents.private_terms(D)
        self.assertTrue(agents.safe_query("AWS bank generative AI MSK Kafka case", p))  # vocabulary parts block nothing
        self.assertFalse(agents.safe_query("AWS bank ai-platform case", p))           # the whole name still does
        v = agents.query_vocab(["Amazon Elastic Compute Cloud - Compute"])
        self.assertTrue(agents.safe_query("Elastic Compute bank case", p, v))
        self.assertFalse(agents.safe_query("Elastic Compute bank case", p))  # service words only with that entity's services

    def test_no_signal_no_search_and_unsafe_note(self):
        D = ds()
        sent = []

        class NoSignals(FsiModel):
            def __call__(self, system, user, name, schema):
                if name == "submit_signals":
                    return {"signals": []}
                return super().__call__(system, user, name, schema)
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, NoSignals([]), c, model_id="m", workers=2, search=lambda qs: sent.append(qs) or RESULTS)
        self.assertEqual(sent, [])
        self.assertEqual({r["text"] for r in out["market"]}, {agents.FSI_NO_SIGNAL})

        class Unsafe(FsiModel):
            def __call__(self, system, user, name, schema):
                if name == "submit_queries":
                    return {"queries": [{"query": "한빛금융 AWS bank case", "signal": ""}]}
                return super().__call__(system, user, name, schema)
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(ds(), Unsafe([]), c, model_id="m", workers=2, search=lambda qs: sent.append(qs) or RESULTS)
        self.assertEqual(sent, [])
        self.assertEqual({r["text"] for r in out["market"] if r["entity"] == A}, {agents.FSI_UNSAFE})

    def test_only_safe_queries_sent_and_cases_copied(self):
        sent = []

        def search(qs):
            sent.append(qs)
            return RESULTS
        m = FsiModel([{"item": 1, "org": "Example Securities", "signal": "새 워크로드 확장",
                       "why": "Amazon EKS로 거래 플랫폼을 옮긴 증권사 사례로, 새 워크로드 아키텍처 리뷰에 참고가 됩니다."},
                      {"item": 1, "org": "Example Securities", "signal": "", "why": "중복"},
                      {"item": 2, "org": "Made Up Bank", "signal": "", "why": "채용"},
                      {"item": 9, "org": "x", "signal": "", "why": "x"}])
        out = self.gen(m, search)
        self.assertEqual(sent, [["AWS securities firm new workload case study"]] * 2)
        qin = m.inputs["submit_queries"]
        self.assertTrue(all(set(u) == {"signals", "services", "vocabulary"} for u in qin))
        self.assertTrue(all("한빛금융" not in json.dumps(u, ensure_ascii=False) and A not in json.dumps(u) for u in qin))
        cin = m.inputs["submit_cases"][0]
        self.assertEqual([(i["item"], i["url"] if "url" in i else None, i["date"]) for i in cin["items"]],
                         [(1, None, "2026-05-01"), (2, None, "")])  # https, deduplicated, bad date blanked, url never sent
        mine = [r for r in out["market"] if r["entity"] == A]
        self.assertEqual(mine, [{"entity": A, "origin": "generated", "kind": "peer_case",
                                 "title": "Example Securities builds a trading platform on Amazon EKS", "url": "https://example.com/case-1",
                                 "date": "2026-05-01", "org": "Example Securities", "signal": "새 워크로드 확장",
                                 "snippet": "Example Securities moved its trading platform to Amazon EKS.",
                                 "why": "Amazon EKS로 거래 플랫폼을 옮긴 증권사 사례로, 새 워크로드 아키텍처 리뷰에 참고가 됩니다."}])
        self.assertTrue(agents.verify(mine[0]["why"], agents.case_index(ds(), A, mine[0])))

    def test_unverified_why_dropped_and_empty_note(self):
        out = self.gen(FsiModel([{"item": 1, "org": "Example Securities", "signal": "", "why": "월 $9.9K를 아꼈습니다."}]),
                       lambda qs: RESULTS)
        self.assertEqual([r for r in out["market"] if r["entity"] == A], [{"entity": A, "origin": "generated", "kind": "note",
                                                                           "text": agents.FSI_EMPTY}])

    def test_search_failure_note_and_no_search(self):
        def boom(qs):
            raise agents.AgentError("down")
        m = FsiModel([])
        out = self.gen(m, boom)
        self.assertEqual([r["text"] for r in out["market"] if r["entity"] == A], [agents.FSI_FAILED])
        self.assertNotIn("submit_cases", m.calls)
        self.assertTrue(any("fsi_search" in x for x in next(r for r in out["notes"] if r["entity"] == A and r["kind"] == "meta")["errors"]))
        self.assertEqual(out["ai"], "ok")  # a search outage is not a model failure
        m = FsiModel([])
        out = self.gen(m, None)
        self.assertNotIn("submit_queries", m.calls)
        self.assertEqual([r["text"] for r in out["market"] if r["entity"] == A], [agents.FSI_NOTE])

    def test_lambda_search(self):
        class Payload:
            def __init__(self, b):
                self.b = b

            def read(self):
                return self.b

        class Client:
            def __init__(self, body, err=None):
                self.body, self.err, self.sent = body, err, []

            def invoke(self, FunctionName, Payload):
                self.sent.append((FunctionName, json.loads(Payload)))
                r = {"Payload": Payload_(json.dumps(self.body).encode())}
                if self.err:
                    r["FunctionError"] = self.err
                return r
        Payload_ = Payload
        c = Client({"results": RESULTS, "errors": []})
        self.assertEqual(agents.lambda_search("fn", c)(["q"]), RESULTS)
        self.assertEqual(c.sent, [("fn", {"queries": ["q"]})])
        for body, err in (({"errorMessage": "x"}, "Unhandled"), ({"results": [], "errors": [0]}, None), ([], None)):
            with self.assertRaises(agents.AgentError):
                agents.lambda_search("fn", Client(body, err))(["q"])


class RunDir(unittest.TestCase):
    def test_all_calls_fail_returns_ai_none(self):
        def boom(*a):
            raise agents.AgentError("down")
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as c:
            write_dir(d, ds())
            jdump(FEED, os.path.join(d, "whats_new_feed.json"), "w")
            self.assertEqual(agents.run(d, invoke=boom, cache=c, model_id="m", workers=2), {"ai": "none"})
            self.assertEqual({r["kind"] for r in read(d, "agent_notes")}, {"meta"})
            self.assertEqual(read(d, "sessions"), [])
            self.assertEqual(read(d, "market"), [{"entity": e, **NOTE} for e in (A, B)])

    def test_partial_failure_is_ok(self):
        class Half(FakeModel):
            def __call__(self, system, user, name, schema):
                if name != "submit_findings":
                    raise agents.AgentError("down")
                return super().__call__(system, user, name, schema)
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as c:
            write_dir(d, ds())
            self.assertEqual(agents.run(d, invoke=Half(), cache=c, model_id="m", workers=2), {"ai": "ok"})
            metas = [r for r in read(d, "agent_notes") if r["kind"] == "meta"]
            self.assertTrue(all(m["errors"] for m in metas))

    def test_outputs_written_and_hand_rows_kept(self):
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as c:
            write_dir(d, ds())
            hand = [{"entity": A, "title": "직접 작성 세션"}]
            jdump(hand, os.path.join(d, "sessions.json"), "w")
            jdump([{"entity": A, "origin": "generated", "title": "old", "date": "", "url": "", "why": ""}], os.path.join(d, "market.json"), "w")
            feed = os.path.join(c, "feed.json")
            jdump(FEED, feed, "w")
            self.assertEqual(agents.run(d, invoke=FakeModel(), cache=os.path.join(c, "cache"), model_id="m", feed_path=feed), {"ai": "ok"})
            ss, mk = read(d, "sessions"), read(d, "market")
        self.assertEqual(ss[0], hand[0])
        self.assertTrue(any(r.get("origin") == "generated" and r["entity"] == A for r in ss))
        self.assertNotIn("old", [r.get("title") for r in mk])
        self.assertTrue(any(r.get("title") == "New edge locations" for r in mk))

    def test_prune_filters_generated_rows(self):
        D = ds()
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, FakeModel(), c, model_id="m", workers=2, feed=FEED)
        D["briefing"] = [r for r in D["briefing"] if not (r["entity"] == A and r["rank"] == 2)]
        fresh = agents.fresh_entities(D, out["notes"])
        self.assertEqual(fresh, {B})
        hand = {"entity": A, "title": "직접 작성"}
        kept = agents.prune_generated(out["sessions"] + [hand], fresh)
        self.assertEqual({r["entity"] for r in kept if r.get("origin") == "generated"}, {B})
        self.assertIn(hand, kept)


class FakeClient:
    def __init__(self, replies):
        self.replies, self.n = list(replies), 0

    def converse(self, **kw):
        self.n += 1
        self.kw = kw
        r = self.replies.pop(0)
        if isinstance(r, Exception):
            raise r
        return r


def reply(content, stop="tool_use"):
    return {"output": {"message": {"content": content}}, "stopReason": stop}


SCHEMA = agents.SCHEMAS["submit_findings"]
GOOD = {"findings": [{"title": "t", "detail": "d", "so_what": "s", "datasets": [], "confidence": "높음"}]}


class Adapter(unittest.TestCase):
    def inv(self, client):
        return agents.bedrock_invoke(client=client, model_id="m", sleep=lambda s: None)

    def test_tool_use(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        self.assertNotIn("temperature", json.dumps(c.kw.get("inferenceConfig", {})))
        self.assertEqual(c.kw["toolConfig"]["toolChoice"], {"auto": {}})

    def test_json_in_text(self):
        c = FakeClient([reply([{"text": "결과: " + json.dumps(GOOD, ensure_ascii=False)}], "end_turn")])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)

    def test_retries_then_succeeds(self):
        thr = agents.ClientError({"Error": {"Code": "ThrottlingException", "Message": "slow"}}, "Converse")
        c = FakeClient([reply([{"text": "cut"}], "max_tokens"), thr, reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        self.assertEqual(c.n, 3)

    def test_fail_closed_after_three(self):
        c = FakeClient([reply([{"text": "no json"}], "end_turn")] * 3)
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(c.n, 3)

    def test_token_limit(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.inv(c)("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(c.kw["inferenceConfig"], {"maxTokens": agents.MAX_TOKENS})

    def test_transport_errors_retry(self):
        class Timeout(agents.BotoCoreError):
            pass
        c = FakeClient([Timeout(), Timeout(), reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)
        c = FakeClient([Timeout()] * 3)
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)

    def test_blocked_stop_reason_rejected(self):
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "content_filtered"),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "guardrail_intervened"),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}], "content_filtered")])
        with self.assertRaises(agents.AgentError):
            self.inv(c)("s", "u", "submit_findings", SCHEMA)

    def test_client_error_message_not_carried(self):
        deny = agents.ClientError({"Error": {"Code": "AccessDeniedException",
                                             "Message": "User: arn:aws:sts::123456789012:assumed-role/r/x is not authorized"}}, "Converse")
        with self.assertRaises(agents.AgentError) as cm:
            self.inv(FakeClient([deny]))("s", "u", "submit_findings", SCHEMA)
        self.assertEqual(str(cm.exception), "submit_findings: AccessDeniedException")

    def test_published_errors_carry_no_sdk_text(self):
        def boom(*a):
            raise RuntimeError("arn:aws:lambda:ap-northeast-2:123456789012:function:fsi")
        with tempfile.TemporaryDirectory() as c:
            rows = agents.generate(ds(), boom, c, model_id="m", workers=2)["notes"]
        errs = [x for r in rows for x in r["errors"]]
        self.assertTrue(errs)
        self.assertNotIn("123456789012", json.dumps(errs))
        self.assertIn("synthesis: RuntimeError", errs)

    def test_unexpected_exception_fails_closed(self):
        def boom(*a):
            raise RuntimeError("socket")
        with tempfile.TemporaryDirectory() as c:
            rows = agents.generate(ds(), boom, c, model_id="m", workers=2)["notes"]
        self.assertEqual({r["kind"] for r in rows}, {"meta"})

    def test_schema_violation_retries(self):
        bad = {"findings": [{"title": 1}]}
        c = FakeClient([reply([{"toolUse": {"name": "submit_findings", "input": bad}}]),
                        reply([{"toolUse": {"name": "submit_findings", "input": GOOD}}])])
        self.assertEqual(self.inv(c)("s", "u", "submit_findings", SCHEMA), GOOD)


if __name__ == "__main__":
    unittest.main()
