"""agents.py published-text verifier (agent_verify.py, re-exported by agents). Synthetic data only."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, B, cur, ds  # noqa: E402,F401 (shared datasets, moved from test_agents.py)


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

    def test_spelled_out_percent_is_a_percent(self):
        # recs 25 is a count in the input; coverage_pct 52.3 is the only percent
        ix = agents.index({"recs": 25, "coverage_pct": 52.3})
        for t in ("커버리지 25퍼센트", "커버리지 25 퍼센트", "커버리지 25프로", "커버리지 25 프로가", "커버리지 25%p",
                  "커버리지 25퍼센트포인트 하락", "커버리지 25포인트 하락", "coverage 25 percent", "coverage 25 Percent",
                  "coverage 25 percentage points", "coverage 25 pct", "coverage 25 pp", "커버리지 25％"):
            self.assertFalse(agents.verify(t, ix), t)
        for t in ("커버리지 52.3퍼센트", "커버리지 52프로", "coverage 52.3 percent", "커버리지 52.3％", "권고 25건"):
            self.assertTrue(agents.verify(t, ix), t)
        self.assertTrue(agents.verify("권고 25 프로젝트", ix))  # a word starting with 프로 is not a percent
        self.assertEqual([t[0] for t in agents._scan("25 percentile")], ["bare"])

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


if __name__ == "__main__":
    unittest.main()
