"""agents.py: verifier, digests, run() wiring, cache, Bedrock adapter. Synthetic data and a fake model only."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, B, cur, ds  # noqa: E402,F401 (shared datasets, moved from test_agents.py)


from fileutil import wr, jload, jdump  # noqa: E402


FEED = [  # whats_new_feed.json rows: no id/services; `entities` is a legacy classification that must be ignored
    {"date": "2026-09-01", "title": "Amazon CloudFront adds origin shield metrics", "url": "https://aws.amazon.com/about-aws/whats-new/2026/09/cf-a/",
     "entities": [], "topics": [], "source": "rss"},
    {"date": "2026-10-01", "title": "New edge locations", "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/edge/",
     "entities": [], "topics": ["CloudFront"], "source": "rss"},
    {"date": "2026-10-05", "title": "Amazon Connect adds a feature", "url": "https://aws.amazon.com/about-aws/whats-new/2026/10/connect/",
     "entities": ["acct-a"], "topics": ["Contact Center"], "source": "rss"},
]


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


if __name__ == "__main__":
    unittest.main()
