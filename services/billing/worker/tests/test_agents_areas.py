"""agents.py area runs: CUR and business areas, What's New market picks, FSI peer cases. Fake model only."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, B, cur, ds  # noqa: E402,F401 (shared datasets, moved from test_agents.py)


from fileutil import wr, jload, jdump  # noqa: E402
from test_agents import FEED, CurModel, FakeModel, FsiModel, MarketModel, NOTE, RESULTS, read, write_dir  # noqa: E402,F401


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

    def test_safe_query_short_korean_name_of_vocabulary_words(self):
        # a four-syllable Korean name made of two vocabulary words passes the allowlist when spaced
        D = ds()
        D["kpis"][0]["entity_ko"] = "카드은행"
        p = agents.private_terms(D)
        for q in ("카드 은행 AWS 사례", "카드은행 AWS 사례", "AWS 카드/은행 사례"):
            self.assertFalse(agents.safe_query(q, p), q)
        self.assertTrue(agents.safe_query("은행 카드 AWS 사례", p))  # the words in another order name nothing

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


if __name__ == "__main__":
    unittest.main()
