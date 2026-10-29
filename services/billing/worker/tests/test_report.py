"""report.py: deterministic Markdown from datasets; agent text escaped, re-verified per entity, stale notes ignored."""
import copy, os, re, sys, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agents  # noqa: E402
import report  # noqa: E402
from test_agents import A, B, ds  # noqa: E402


def with_notes(D, notes):
    D = copy.deepcopy(D)
    fp = {e: agents.source_fp(D, e) for e in (A, B)}
    D["agent_notes"] = [{**n, "source_fp": n.get("source_fp", fp[n["entity"]])} for n in notes]
    for n in D["agent_notes"]:  # a meta note written from these inputs (the freshness check also needs the digests)
        if n["kind"] == "meta":
            n.setdefault("digest_fp", agents.digest_fp(D, n["entity"], n.get("areas")))
    return D


def meta(e, checked=10, dropped=2):
    return {"entity": e, "kind": "meta", "model": "m", "checked": checked, "dropped": dropped, "errors": []}


class Report(unittest.TestCase):
    def test_empty_dashboard_url_leaves_the_link_out(self):
        md = report.render(with_notes(ds(), []), "")
        self.assertNotIn("대시보드:", md)
        self.assertIn("자동 생성:", md)
        self.assertIn("대시보드: https://example.invalid", self.render(with_notes(ds(), [])))

    def render(self, D):
        return report.render(D, "https://example.invalid")

    def test_summary_and_sections(self):
        md = self.render(with_notes(ds(), []))
        rows = [l for l in md.splitlines() if l.startswith("| ") and ("한빛금융" in l or "누리증권" in l)]
        self.assertGreaterEqual(len(rows), 2)
        for h in ("## 1. 요약", "## 2. 사업 신호 (서비스 태그 항목)", "## 3. SA 세션 (AI 생성)", "## 4. 법인별 실행 항목 (비용)",
                  "## 5. 시장 동향 (What's New)", "## 6. FSI 동종업계 사례 (웹 검색)", "## 7. 법인 비교", "## 8. CUR 분석",
                  "## 9. 데이터 한계", "## 10. 다시 만들기"):
            self.assertIn(h, md)
        self.assertIn("AI 재실행", md)
        self.assertNotIn("run_" + "pipeline.sh", md)  # the local wrapper is not part of this repo
        sec = md.split("법인별 실행 항목 (비용)")[1].split("### 한빛금융")[1].split("###")[0]
        self.assertLess(sec.index("Graviton 전환"), sec.index("RDS 커버리지"))
        self.assertIn("SA", sec)
        self.assertIn("PoC 제안", sec)  # rule-based next step when no note

    def test_null_mom_pct_renders_a_dash(self):
        D = ds()
        k = next(r for r in D["kpis"] if r["entity"] == A)
        k["mom_pct"] = None
        rows = [l for l in report.summary(D, [A, B], 1) if l.startswith("| ") and not l.startswith("| 법인")]
        self.assertEqual(rows[0].split(" | ")[2], "—")
        self.assertRegex(rows[1].split(" | ")[2], r"^[+-]\d+\.\d%$")  # a non-null value keeps its format

    def test_note_replaces_next_step_only_on_id_match(self):
        D = ds()
        aid = next(r["id"] for r in D["briefing"] if r["entity"] == A and r["rank"] == 1)
        D = with_notes(D, [meta(A), {"entity": A, "kind": "action", "rank": 1, "action_id": aid,
                                     "next_step": "c7i 계열부터 PoC를 제안합니다.", "questions": ["c7i 워크로드 제약은?"]},
                           {"entity": A, "kind": "action", "rank": 2, "action_id": "stale0000000",
                            "next_step": "오래된 다음 행동", "questions": []}])
        md = self.render(D)
        self.assertIn("AI 해석(숫자 검증)", md)
        self.assertIn("c7i 계열부터 PoC를 제안합니다.", md)
        self.assertIn("c7i 워크로드 제약은?", md)
        self.assertNotIn("오래된 다음 행동", md)
        self.assertIn("RI 추가", md)  # rank 2 keeps the rule-based step
        sec = md.split("### 한빛금융")[1].split("###")[0]
        self.assertNotIn("PoC 제안", sec)  # rank 1's rule-based step is replaced

    def test_stale_fingerprint_ignored(self):
        D = with_notes(ds(), [meta(A), {"entity": A, "kind": "headline", "text": "c7i 약정 만료가 먼저입니다.", "source_fp": "old"}])
        self.assertNotIn("c7i 약정 만료가 먼저입니다.", self.render(D))

    def test_agent_text_escaped(self):
        hostile = "<script>alert(1)</script> [x](http://evil.invalid) ![i](http://evil.invalid/a.png) a|b `c` help@evil.invalid www.evil.invalid"
        D = with_notes(ds(), [meta(A), {"entity": A, "kind": "link", "index": 0, "title": "c7i 연결", "text": hostile}])
        md = self.render(D)
        self.assertNotIn("<script>", md)
        self.assertNotIn("evil.invalid", md)  # URLs removed, so no link or GFM autolink survives
        self.assertNotIn("a|b", md)
        self.assertNotIn("`c`", md)
        e = report.esc(hostile)  # whether or not verify() keeps the note, the escaper alone neutralizes it
        self.assertIn("&lt;script&gt;", e)
        for bad in ("<script>", "evil.invalid", "a|b", "`c`"):
            self.assertNotIn(bad, e)
        self.assertNotRegex(e, r"(?<!\\)[\[\]]")  # every bracket escaped, so no link or image syntax

    def test_finding_confidence_escaped(self):
        bad = "<b>x</b>[y](http://evil.invalid)"
        D = with_notes(ds(), [meta(A), {"entity": A, "kind": "finding", "area": "commitments", "index": 0, "title": "c7i 약정 만료",
                                        "detail": None, "so_what": None, "confidence": bad, "datasets": []},
                              {"entity": A, "kind": "finding", "area": "business", "index": 1, "title": "새 워크로드 확장", "tag": "",
                               "help": "", "detail": None, "so_what": None, "confidence": bad, "datasets": []}])
        md = self.render(D)
        self.assertIn("c7i 약정 만료", md)
        self.assertIn("새 워크로드 확장", md)
        self.assertNotIn("<b>", md)
        self.assertNotIn("evil.invalid", md)

    def test_foreign_amount_rejected(self):
        D = ds()
        foreign = next(r for r in D["kpis"] if r["entity"] == B)["last_spend"]  # only B has this spend
        D = with_notes(D, [meta(A), {"entity": A, "kind": "headline", "text": f"9월 지출 ${foreign:,}"},
                           {"entity": A, "kind": "link", "index": 0, "title": "누리증권 비교", "text": "c7i"}])
        md = self.render(D)
        sec = md.split("### 한빛금융")[1].split("###")[0]
        self.assertNotIn(f"${foreign:,}", sec)
        self.assertNotIn("누리증권 비교", sec)

    def test_agent_sections_verify_against_own_entity(self):
        D = ds()
        aid = next(r["id"] for r in D["briefing"] if r["entity"] == A and r["rank"] == 1)
        D = with_notes(D, [meta(A), {"entity": A, "kind": "headline", "text": "c7i 계열 SP가 2026-12-16에 끝나 월 $1.9K가 늘어납니다."},
                           {"entity": A, "kind": "action", "rank": 1, "action_id": aid, "next_step": "월 $108.8K 구매 계획", "questions": []}])
        md = self.render(D)
        sec = md.split("### 한빛금융")[1].split("###")[0]
        ix = report.entity_index(D, A)
        for line in sec.splitlines():
            if "AI" in line and "$" in line:
                self.assertTrue(agents.verify(report.unescape(line.split(":", 1)[-1]), ix), line)

    def test_dataset_text_escaped(self):
        D = ds()
        D["briefing"][0]["evidence"] = ["계정 <img src=x onerror=alert(1)> [x](http://evil.invalid)"]
        md = self.render(with_notes(D, []))
        self.assertNotIn("<img", md)
        self.assertNotIn("evil.invalid", md)

    def test_everything_generation_publishes_the_report_shows(self):
        from test_agents import FakeModel
        import tempfile
        D = ds()
        with tempfile.TemporaryDirectory() as c:
            D["agent_notes"] = agents.generate(D, FakeModel(), c, model_id="m", workers=2)["notes"]
        md = self.render(D)
        shown = [n for n in D["agent_notes"] if n["kind"] in ("headline", "action", "link")]
        self.assertTrue(shown)
        for n in shown:
            for t in [n.get("text"), n.get("next_step"), n.get("title")] + (n.get("questions") or []):
                if t:
                    self.assertIn(report.esc(t), md, t)
        for f in (n for n in D["agent_notes"] if n["kind"] == "finding"):
            self.assertIn(report.esc(f["title"]), md, f["title"])

    def test_title_and_month_from_data(self):
        md = self.render(with_notes(ds(), []))
        title = md.splitlines()[0]
        self.assertEqual(title, "# 빌링 인사이트 — 계정팀(SA·TAM·AM)용")
        for name in (A, B, "한빛금융", "누리증권"):
            self.assertNotIn(name, title)
        self.assertIn("| 법인 | 9월 지출 |", md)  # label of the period's last month (2026-09 in the fixture)

    def test_deterministic(self):
        D = with_notes(ds(), [meta(A)])
        self.assertEqual(self.render(D), self.render(copy.deepcopy(D)))


class GeneratedSections(unittest.TestCase):
    def render(self, D):
        return report.render(D, "https://example.invalid")

    def gen(self, D):
        from test_agents import FakeModel, FEED
        import tempfile
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, FakeModel(), c, model_id="m", workers=2, feed=FEED)
        D["agent_notes"], D["sessions"], D["market"] = out["notes"], out["sessions"], out["market"]
        return D

    def test_sessions_and_market_rendered(self):
        D = self.gen(ds())
        md = self.render(D)
        sec = md.split("SA 세션 (AI 생성)")[1].split("\n## ")[0]
        self.assertIn(report.esc("c7i 약정과 Graviton 전환 워크숍"), sec)
        self.assertIn("c8g 전환 순서", sec)
        mk = md.split("시장 동향")[1].split("\n## ")[0]
        self.assertIn("[New edge locations](https://aws.amazon.com/about-aws/whats-new/2026/10/edge/)", mk)
        self.assertIn("CloudFront 지출이 가장 큰 서비스입니다.", mk)
        self.assertNotIn("FSI 사례 조사", mk)
        fsi = md.split("FSI 동종업계 사례 (웹 검색)")[1].split("\n## ")[0]
        self.assertIn("FSI 사례 조사(웹 검색)는 아직 연결되지 않았습니다.", fsi)  # no search function: the note

    def test_fsi_cases_rendered_and_reverified(self):
        from test_agents import FsiModel, RESULTS, FEED
        import tempfile
        D = ds()
        why = "Amazon EKS로 거래 플랫폼을 옮긴 증권사 사례로, 새 워크로드 아키텍처 리뷰에 참고가 됩니다."
        with tempfile.TemporaryDirectory() as c:
            out = agents.generate(D, FsiModel([{"item": 1, "org": "Example Securities", "signal": "새 워크로드 확장", "why": why}]), c,
                                  model_id="m", workers=2, feed=FEED, search=lambda qs: RESULTS)
        D["agent_notes"], D["sessions"], D["market"] = out["notes"], out["sessions"], out["market"]
        sec = self.render(D).split("FSI 동종업계 사례 (웹 검색)")[1].split("\n## ")[0]
        self.assertIn("- Example Securities — [Example Securities builds a trading platform on Amazon EKS](https://example.com/case-1) "
                      "· example.com (2026-05-01): " + report.esc(why), sec)
        self.assertIn("   - 돕는 사업 신호: 새 워크로드 확장", sec)
        self.assertNotIn("Example Securities builds", self.render(D).split("시장 동향")[1].split("\n## ")[0])
        for r in D["market"]:
            if r.get("kind") == "peer_case":
                r["why"], r["url"] = "월 $9.9K를 아꼈습니다.", "https://example.com/a)(b"
        self.assertNotIn("Example Securities —", self.render(D))  # a tampered why fails the re-check
        for r in D["market"]:
            if r.get("kind") == "peer_case":
                r["why"] = why
        sec = self.render(D).split("FSI 동종업계 사례 (웹 검색)")[1].split("\n## ")[0]
        self.assertNotIn("](https://example.com/a)(b)", sec)  # a URL that would break the link stays plain text
        self.assertIn("Example Securities — Example Securities builds", sec)
        for r in D["market"]:
            if r.get("kind") == "peer_case":
                r["url"] = "https://www.example.com/case"
        sec = self.render(D).split("FSI 동종업계 사례 (웹 검색)")[1].split("\n## ")[0]
        self.assertIn("(https://www.example.com/case) · www.example.com", sec)  # a www. host is shown, not removed

    def test_business_signals_come_before_cost_and_carry_help(self):
        md = self.render(self.gen(ds()))
        self.assertLess(md.index("사업 신호 (서비스 태그 항목)"), md.index("법인별 실행 항목 (비용)"))
        sec = md.split("사업 신호 (서비스 태그 항목)")[1].split("\n## ")[0]
        self.assertIn("| app | wl-new-acct-a | 신규 | $2.5K | 7 | 86% | — |", sec)
        self.assertIn("- [아키텍처 리뷰·중간] wl-new-acct-a — 새 워크로드 확장", sec)
        self.assertIn("   - 확인 질문: 출시 일정은?", sec)
        self.assertNotIn("$9.9K", sec)
        self.assertNotIn("새 워크로드 확장", md.split("법인별 실행 항목 (비용)")[1].split("\n## ")[0])  # not repeated with cost findings
        ss = md.split("SA 세션 (AI 생성)")[1].split("\n## ")[0]
        self.assertIn("1. [아키텍처 제안] **", ss)

    def test_generated_rows_reverified_and_links_restricted(self):
        D = self.gen(ds())
        D["sessions"].append({"entity": A, "origin": "generated", "rank": 9, "title": "월 $9.9K 세션", "why": None, "audience": None, "agenda": []})
        D["sessions"].append({"entity": A, "origin": "generated", "rank": 10, "title": "누리증권 비교 세션", "why": None, "audience": None, "agenda": []})
        D["market"].append({"entity": A, "origin": "generated", "title": "CloudFront 발표", "date": "2026-10-01",
                            "url": "https://evil.invalid/x", "why": "CloudFront 지출"})
        D["market"].append({"entity": A, "origin": "generated", "title": "CloudFront 두번째", "date": "2026-10-01",
                            "url": "https://aws.amazon.com/a", "why": "월 $9.9K 절감"})
        md = self.render(D)
        self.assertNotIn("$9.9K", md)
        self.assertNotIn("누리증권 비교 세션", md)
        self.assertNotIn("evil.invalid", md)
        self.assertIn("CloudFront 발표", md)  # shown as plain text without the link
        self.assertNotIn("CloudFront 두번째", md)

    def test_stale_generated_rows_hidden(self):
        D = self.gen(ds())
        D["briefing"] = [r for r in D["briefing"] if not (r["entity"] == A and r["rank"] == 2)]
        md = self.render(D)
        sec = md.split("SA 세션 (AI 생성)")[1].split("\n## ")[0]
        self.assertNotIn("한빛금융", sec)
        self.assertIn("누리증권", sec)

    def test_cur_only_report(self):
        from test_agents import cur, CurModel
        import tempfile
        D = {n: [] for n in agents.SOURCES}
        cur(D, A)
        with tempfile.TemporaryDirectory() as c:
            D["agent_notes"] = agents.generate(D, CurModel(), c, model_id="m", workers=2, areas=["cur"], synth=False, extras=False)["notes"]
        md = self.render(D)
        self.assertNotIn("## 1. 요약", md)
        self.assertIn("## 1. 사업 신호 (서비스 태그 항목)", md)
        self.assertIn("| team | team-acct-a | 급증, 신규 서비스 | $617 | +$217 | +54% | AmazonBedrock |", md)
        self.assertIn("## 2. CUR 분석", md)
        self.assertIn("$1,234", md)  # 2026-09 cost
        self.assertIn(report.esc("9월 CUR 지출 $1,234"), md)  # verified cur finding
        self.assertIn("team", md)
        self.assertNotIn("$9,999", md)

    def test_changed_digest_hides_notes_with_the_same_briefing(self):
        # CUR-only: no briefing rows, so source_fp stays the same however the CUR inputs change
        from test_agents import cur, CurModel
        import tempfile
        D = {n: [] for n in agents.SOURCES}
        cur(D, A)
        with tempfile.TemporaryDirectory() as c:
            D["agent_notes"] = agents.generate(D, CurModel(), c, model_id="m", workers=2, areas=["cur"], synth=False, extras=False)["notes"]
        self.assertIn(report.esc("9월 CUR 지출 $1,234"), self.render(D))
        D["cur_summary"][0]["cost_by_month"] = {"2026-08": 1234.0, "2026-09": 1100.0}  # same tokens, other months
        self.assertEqual(agents.fresh_entities(D, D["agent_notes"]), set())
        self.assertFalse(report.fresh_notes(D, A)["fresh"])
        self.assertNotIn(report.esc("9월 CUR 지출 $1,234"), self.render(D))

    def test_non_usd_cur_amounts_keep_their_currency(self):
        from test_agents import cur
        D = {n: [] for n in agents.SOURCES}
        cur(D, A)
        D["cur_summary"][0]["currency"] = "KRW"
        D["agent_notes"] = []
        md = self.render(D)
        self.assertNotIn("$", md.split("## 1. 사업 신호")[1].split("## 3.")[0])
        self.assertIn("| team | team-acct-a | 급증, 신규 서비스 | 617 KRW | +217 KRW | +54% | AmazonBedrock |", md)
        self.assertIn("| 2026-09 | 1.2K KRW |", md)


if __name__ == "__main__":
    unittest.main()
