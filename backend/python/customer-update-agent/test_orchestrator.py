import json
import logging
import re
import threading
import time
import unittest
from unittest import mock

import orchestrator
import pipeline
import store
from contract import AccountPack, Budgets, Item, RunInput
from ledger import LedgerRefused
from test_store import RUN_ID, FakeKMS, FakeTable, open_row

logging.getLogger("customer-update-agent").addHandler(logging.NullHandler())

NOW = 1_800_000_000


def url_for(url_hash):
    return f"https://example.com/news/{url_hash[:6]}"


def account(aid, name=None, remaining=10):
    return AccountPack(aid, name or ("고객" + aid), ("Amazon Bedrock",), (), (), (), (),
                       (), remaining)


def run_input(hashes, accounts, pairs=10, drafts=30):
    return RunInput(RUN_ID, tuple(Item(x, url_for(x)) for x in hashes), tuple(accounts),
                    Budgets(pairs, drafts))


class FakeCache:
    """Cached rows by url hash: 'ready' rows carry a title; others only a status."""

    def __init__(self, statuses):
        self.statuses, self.gets = statuses, []

    def get(self, url_hash):
        self.gets.append(url_hash)
        status = self.statuses.get(url_hash, "ready")
        if status != "ready":
            return {"status": status}
        return {"status": "ready", "title": "제목-" + url_hash[:6],
                "summary": "Bedrock 기능 출시 요약", "services": ["Amazon Bedrock"],
                "topics": ["AI"]}

    def put_status(self, *a, **k):
        raise AssertionError("cache write not expected")

    put_ready = put_status


class FakeGate:
    """Candidate score per (title prefix, account id) and needed score per (title, name)."""

    backend = "luna-v1"

    def __init__(self, cand, need):
        self.cand, self.need, self.calls = cand, need, []

    def score_options(self, question, options, context):
        self.calls.append([o.id for o in options])
        title = re.search(r"Article title: 제목-(\w+)", context).group(1)
        ids = [o.id for o in options]
        if ids == ["needed", "not_needed"]:
            name = re.search(r"Customer: (\S+)", context).group(1)
            s = self.need.get((title, name), 0.0)
            return [orchestrator.gate.OptionScore("needed", s, "필요 이유"),
                    orchestrator.gate.OptionScore("not_needed", 1 - s, "불필요 이유")]
        return [orchestrator.gate.OptionScore(i, self.cand.get((title, i), 0.0), "")
                for i in ids]


class FakeWriter:
    def __init__(self):
        self.calls = 0

    def __call__(self, prompt, required):
        self.calls += 1
        link = re.search(r"^- (https://\S+)$", prompt, re.M).group(1)
        body = ("안녕하세요, Amazon Bedrock 에이전트 기능이 새로 나왔습니다. "
                "관심을 가지실 것 같아 공유드립니다. "
                f"자세한 내용은 {link} 에서 보실 수 있습니다. "
                "괜찮으시면 짧은 미팅으로 설명드리겠습니다.")
        return {"why": "Bedrock 고객이라 관심 & <확장>", "body": body, "links": [link]}


class Recorder:
    """Wraps a RunResults and records the order of row kinds written."""

    def __init__(self, inner, fail_on=None):
        self.inner, self.order, self.fail_on = inner, [], fail_on

    def __getattr__(self, name):
        method = getattr(self.inner, name)

        def call(*args, **kwargs):
            self.order.append(name)
            if name == self.fail_on:
                raise RuntimeError("write failed")
            return method(*args, **kwargs)
        return call


def make_deps(cand, need, statuses=None, orchestrate=None, fail_on=None):
    table, kms = FakeTable(), FakeKMS()
    writer_model = FakeWriter()
    pdeps = pipeline.Deps(
        ask_research=lambda prompt, tools: {"facts": []},
        ask_write=writer_model,
        ask_review=lambda prompt, required: {"ok": True, "reason": ""},
        web_search=lambda q: [],
        fetch_public=lambda u: "",
        run_id=RUN_ID,
    )
    res = Recorder(store.RunResults(table, RUN_ID, kms, "key-1", clock=lambda: NOW), fail_on)
    deps = orchestrator.Deps(
        cache=FakeCache(statuses or {}),
        ask_read=lambda p, r: (_ for _ in ()).throw(AssertionError("no fetch expected")),
        gate=FakeGate(cand, need),
        pipeline=pdeps,
        results=res,
        orchestrate=orchestrate or (lambda tools, prompt: None),
    )
    return deps, table, res, writer_model


def rows(table, prefix):
    return {p["Item"]["SK"]: p["Item"] for p in table.puts if p["Item"]["SK"].startswith(prefix)}


def sks(table):
    return [p["Item"]["SK"] for p in table.puts]


A, B, C = "a" * 64, "b" * 64, "c" * 64


class SweepTest(unittest.TestCase):
    def test_silent_orchestrator_sweep_decides_every_candidate_and_drafts(self):
        cand = {(A[:6], "acc1"): 0.9, (A[:6], "acc2"): 0.8, (B[:6], "acc2"): 0.6}
        need = {(A[:6], "고객acc1"): 0.95, (A[:6], "고객acc2"): 0.3, (B[:6], "고객acc2"): 0.75}
        deps, table, res, writer_model = make_deps(cand, need, statuses={C: "insufficient"})
        stats = orchestrator.run(run_input([A, B, C], [account("acc1"), account("acc2")]), deps)
        decs = rows(table, "DEC#")
        self.assertEqual(set(decs), {f"DEC#{A}#acc1", f"DEC#{A}#acc2", f"DEC#{B}#acc2"})
        self.assertEqual(decs[f"DEC#{A}#acc1"]["outcome"], "drafted")
        self.assertEqual(decs[f"DEC#{A}#acc2"]["outcome"], "")
        self.assertEqual(decs[f"DEC#{A}#acc2"]["choice"], "not_needed")
        self.assertEqual(decs[f"DEC#{B}#acc2"]["outcome"], "drafted")
        self.assertEqual(set(rows(table, "DRAFT#")),
                         {f"DRAFT#acc1#{A}", f"DRAFT#acc2#{B}"})
        items = rows(table, "ITEM#")
        self.assertEqual({k: (v["state"], v["reason"]) for k, v in items.items()}, {
            "ITEM#" + A: ("decided", ""), "ITEM#" + B: ("decided", ""),
            "ITEM#" + C: ("skipped", "insufficient")})
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")
        self.assertEqual(table.puts[-1]["Item"]["stats"], stats)
        self.assertEqual(stats["drafted"], 2)
        self.assertEqual(writer_model.calls, 2)

    def test_drafts_in_descending_score_order(self):
        cand = {(A[:6], "acc1"): 0.9, (B[:6], "acc1"): 0.9}
        need = {(A[:6], "고객acc1"): 0.75, (B[:6], "고객acc1"): 0.95}
        deps, table, _, _ = make_deps(cand, need)
        orchestrator.run(run_input([A, B], [account("acc1")], pairs=1), deps)
        decs = rows(table, "DEC#")
        self.assertEqual(decs[f"DEC#{B}#acc1"]["outcome"], "drafted")
        self.assertEqual(decs[f"DEC#{A}#acc1"]["outcome"], "deferred")
        self.assertEqual(decs[f"DEC#{A}#acc1"]["discardReason"], "research_budget")
        self.assertEqual(rows(table, "ITEM#")["ITEM#" + A]["state"], "deferred")
        self.assertEqual(rows(table, "ITEM#")["ITEM#" + A]["reason"], "budget")

    def test_twelve_needed_pairs_with_ten_pair_budget(self):
        hashes = [format(i, "x") * 64 for i in range(4)]
        accts = [account(f"acc{j}") for j in range(3)]
        cand = {(x[:6], a.account_id): 0.9 for x in hashes for a in accts}
        need = {(x[:6], a.name): 0.9 - 0.01 * (i * 3 + j)
                for i, x in enumerate(hashes) for j, a in enumerate(accts)}
        deps, table, _, writer_model = make_deps(cand, need)
        stats = orchestrator.run(run_input(hashes, accts, pairs=10), deps)
        outcomes = [d["outcome"] for d in rows(table, "DEC#").values()]
        self.assertEqual(outcomes.count("drafted"), 10)
        self.assertEqual(outcomes.count("deferred"), 2)
        self.assertEqual(stats["researched"], 10)
        self.assertEqual(stats["deferred"], 2)
        self.assertEqual(writer_model.calls, 10)
        deferred = {k.split("#")[1] for k, d in rows(table, "DEC#").items()
                    if d["outcome"] == "deferred"}
        self.assertEqual(deferred, {hashes[3]})  # lowest scores: last item's two pairs
        items = rows(table, "ITEM#")
        self.assertEqual(items["ITEM#" + hashes[3]]["state"], "deferred")
        for x in hashes[:3]:
            self.assertEqual(items["ITEM#" + x]["state"], "decided")

    def test_write_order_and_done_last(self):
        cand = {(A[:6], "acc1"): 0.9}
        need = {(A[:6], "고객acc1"): 0.9}
        deps, table, res, _ = make_deps(cand, need)
        orchestrator.run(run_input([A, B], [account("acc1")]), deps)
        self.assertEqual(res.order, ["claim", "put_decision", "put_draft", "put_item_state",
                                     "put_item_state", "put_done"])
        self.assertEqual([p["Item"]["SK"].split("#")[0] for p in table.puts],
                         ["CLAIM", "DEC", "DRAFT", "ITEM", "ITEM", "DONE"])

    def test_item_without_candidates_is_decided(self):
        deps, table, _, _ = make_deps({}, {})
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertEqual(rows(table, "ITEM#")["ITEM#" + A]["state"], "decided")
        self.assertEqual(rows(table, "DEC#"), {})

    def test_failed_item_is_skipped_failed(self):
        deps, table, _, _ = make_deps({}, {}, statuses={A: "failed"})
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        item = rows(table, "ITEM#")["ITEM#" + A]
        self.assertEqual((item["state"], item["reason"]), ("skipped", "failed"))


class SealedRowsTest(unittest.TestCase):
    def test_sealed_payloads_and_no_plaintext_customer_text(self):
        cand = {(A[:6], "acc1"): 0.9}
        need = {(A[:6], "고객acc1"): 0.9}
        deps, table, _, _ = make_deps(cand, need)
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        dec = rows(table, "DEC#")[f"DEC#{A}#acc1"]
        opened = open_row(dec)
        self.assertEqual(opened["reason"], "필요 이유")
        self.assertIn("Customer: 고객acc1", opened["gateInput"]["context"])
        draft = open_row(rows(table, "DRAFT#")[f"DRAFT#acc1#{A}"])
        self.assertEqual(draft["sourceUrl"], url_for(A))
        self.assertEqual(draft["sourceTitle"], "제목-" + A[:6])
        self.assertEqual(draft["why"], "Bedrock 고객이라 관심 & <확장>")  # sealed raw
        self.assertIn(url_for(A), draft["body"])
        self.assertEqual(orchestrator.pipeline.checks.check_draft(
            draft["body"], draft["links"], set(draft["fetchedUrls"]), [], []), [])
        self.assertEqual(draft["links"], [url_for(A)])
        self.assertIn(url_for(A), draft["fetchedUrls"])
        plaintext = json.dumps([p["Item"] for p in table.puts], ensure_ascii=False, default=str)
        for leaked in ("안녕하세요", "관심", "필요 이유", "불필요", "고객acc1", "Customer:",
                       url_for(A)):
            self.assertNotIn(leaked, plaintext)

    def test_missing_key_writes_nothing(self):
        table = FakeTable()
        with self.assertRaises(orchestrator.sealing.SealingError):
            store.RunResults(table, RUN_ID, FakeKMS(), "")
        self.assertEqual(table.puts, [])


def _tools(captured):
    def orchestrate(tools, prompt):
        captured["tools"] = {t.__name__: t for t in tools}
        captured["prompt"] = prompt
        fn = captured.get("script")
        if fn:
            fn(captured["tools"])
    return orchestrate


class ToolsTest(unittest.TestCase):
    def setUp(self):
        self.cand = {(A[:6], "acc1"): 0.9, (A[:6], "acc2"): 0.9}
        self.need = {(A[:6], "고객acc1"): 0.9, (A[:6], "고객acc2"): 0.1}
        self.captured = {}

    def run_with(self, script, **kw):
        self.captured["script"] = script
        deps, table, res, writer_model = make_deps(self.cand, self.need,
                                                   orchestrate=_tools(self.captured), **kw)
        self.out = []
        stats = orchestrator.run(run_input([A, B], [account("acc1"), account("acc2")]), deps)
        return stats, table, writer_model

    def test_tool_flow_and_idempotence(self):
        def script(t):
            self.out.append(json.loads(t["read"](A)))
            self.out.append(json.loads(t["find_candidates"](A)))
            self.out.append(json.loads(t["decide"](A, "acc1")))
            self.out.append(json.loads(t["draft"](A, "acc1")))
            self.out.append(json.loads(t["draft"](A, "acc1")))
            self.out.append(json.loads(t["decide"](A, "acc1")))
        stats, table, writer_model = self.run_with(script)
        self.assertEqual(self.out[0], {"status": "ready", "title": "제목-" + A[:6]})
        self.assertEqual(self.out[1], {"accounts": ["acc1", "acc2"]})
        self.assertEqual(self.out[2], {"choice": "needed", "score": 0.9})
        self.assertEqual(self.out[3], {"outcome": "drafted", "reason": ""})
        self.assertEqual(self.out[4], self.out[3])
        self.assertEqual(self.out[5], self.out[2])
        self.assertEqual(writer_model.calls, 1)
        self.assertEqual(stats["drafted"], 1)
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_repeated_calls_make_no_new_model_calls(self):
        gate_calls = []

        def script(t):
            t["find_candidates"](A)
            t["decide"](A, "acc1")
            gate_calls.append(len(self.captured["gate"].calls))
            t["find_candidates"](A)
            t["decide"](A, "acc1")
            t["read"](A)
            gate_calls.append(len(self.captured["gate"].calls))

        def orchestrate(tools, prompt):
            self.captured["tools"] = {f.__name__: f for f in tools}
            script(self.captured["tools"])

        deps, *_ = make_deps(self.cand, self.need, orchestrate=orchestrate)
        self.captured["gate"] = deps.gate
        cache = deps.cache
        orchestrator.run(run_input([A], [account("acc1"), account("acc2")]), deps)
        self.assertEqual(gate_calls[0], gate_calls[1])
        self.assertEqual(cache.gets.count(A), 1)

    def test_draft_on_unpassed_pair_is_refused_and_run_completes(self):
        def script(t):
            self.out.append(json.loads(t["draft"](A, "acc2")))  # no decision yet
            t["find_candidates"](A)
            t["decide"](A, "acc2")
            self.out.append(json.loads(t["draft"](A, "acc2")))  # gate said not_needed
        stats, table, writer_model = self.run_with(script)
        self.assertEqual(self.out, [{"refused": "not_passed"}, {"refused": "not_passed"}])
        decs = rows(table, "DEC#")
        self.assertEqual(decs[f"DEC#{A}#acc2"]["outcome"], "")
        self.assertEqual(decs[f"DEC#{A}#acc1"]["outcome"], "drafted")
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_unknown_ids_and_non_candidates(self):
        self.cand = {(A[:6], "acc1"): 0.9}

        def script(t):
            self.out.append(json.loads(t["read"]("f" * 64)))
            self.out.append(json.loads(t["find_candidates"]("nope")))
            self.out.append(json.loads(t["decide"](A, "ghost")))
            self.out.append(json.loads(t["draft"]("f" * 64, "acc1")))
            self.out.append(json.loads(t["decide"](A, "acc2")))
        self.run_with(script)
        self.assertEqual(self.out[:4], [{"error": "unknown"}] * 4)
        self.assertEqual(self.out[4], {"refused": "not_candidate"})

    def test_draft_tool_past_research_budget_is_deferred(self):
        self.need = {(A[:6], "고객acc1"): 0.9, (A[:6], "고객acc2"): 0.8}

        def script(t):
            t["find_candidates"](A)
            t["decide"](A, "acc1")
            t["decide"](A, "acc2")
            self.out.append(json.loads(t["draft"](A, "acc1")))
            self.out.append(json.loads(t["draft"](A, "acc2")))
        self.captured["script"] = script
        deps, table, _, writer_model = make_deps(self.cand, self.need,
                                                 orchestrate=_tools(self.captured))
        self.out = []
        orchestrator.run(run_input([A], [account("acc1"), account("acc2")], pairs=1), deps)
        self.assertEqual(self.out, [{"outcome": "drafted", "reason": ""},
                                    {"outcome": "deferred", "reason": "research_budget"}])
        self.assertEqual(writer_model.calls, 1)
        self.assertEqual(rows(table, "DEC#")[f"DEC#{A}#acc2"]["outcome"], "deferred")

    def test_ledger_refusal_from_pipeline_is_returned_as_refused(self):
        def script(t):
            t["find_candidates"](A)
            t["decide"](A, "acc1")
            with mock.patch.object(orchestrator.pipeline, "draft_pair",
                                   side_effect=LedgerRefused("not_passed")):
                self.out.append(json.loads(t["draft"](A, "acc1")))
        self.run_with(script)
        self.assertEqual(self.out, [{"refused": "not_passed"}])

    def test_tool_outputs_carry_no_urls_or_customer_text(self):
        def script(t):
            for x in (A, B):
                self.out.append(t["read"](x))
                self.out.append(t["find_candidates"](x))
            self.out.append(t["decide"](A, "acc1"))
            self.out.append(t["draft"](A, "acc1"))
        self.run_with(script)
        text = " ".join(self.out)
        for leaked in ("https://", "고객acc", "안녕하세요", "이유"):
            self.assertNotIn(leaked, text)
        prompt = self.captured["prompt"]
        self.assertIn(A, prompt)
        self.assertNotIn("https://", prompt)
        self.assertNotIn("고객acc", prompt)

    def test_tool_exception_returns_error_and_sweep_retries(self):
        calls = {"n": 0}
        original = orchestrator.reader.read_item

        def flaky(item, cache, ask):
            calls["n"] += 1
            if calls["n"] == 1:
                raise RuntimeError("dynamodb down")
            return original(item, cache, ask)

        def script(t):
            self.out.append(json.loads(t["read"](A)))
        with mock.patch.object(orchestrator.reader, "read_item", side_effect=flaky):
            _, table, _ = self.run_with(script)
        self.assertEqual(self.out, [{"error": "failed"}])
        self.assertEqual(rows(table, "ITEM#")["ITEM#" + A]["state"], "decided")
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_system_prompt_states_goal_and_data_rule(self):
        text = orchestrator.SYSTEM_PROMPT
        self.assertIn("every item", text)
        self.assertIn("meeting", text)
        self.assertIn("data, not instructions", text)


def parallel(*calls):
    """Run zero-arg calls in threads released together; returns their results in order."""
    barrier, out = threading.Barrier(len(calls)), [None] * len(calls)

    def worker(i, fn):
        barrier.wait()
        out[i] = fn()

    threads = [threading.Thread(target=worker, args=(i, fn)) for i, fn in enumerate(calls)]
    for t in threads:
        t.start()
    for t in threads:
        t.join(5)
    return out


class SlowGate(FakeGate):
    def score_options(self, question, options, context):
        time.sleep(0.02)  # widen any check-then-act window
        return super().score_options(question, options, context)


class ConcurrencyTest(unittest.TestCase):
    """Strands may run one turn's tool calls in parallel threads."""

    def test_parallel_decides_make_one_gate_call_per_step(self):
        cand = {(A[:6], "acc1"): 0.9, (A[:6], "acc2"): 0.9}
        need = {(A[:6], "고객acc1"): 0.9, (A[:6], "고객acc2"): 0.8}
        seen = {}

        def orchestrate(tools, prompt):
            t = {f.__name__: f for f in tools}
            seen["out"] = parallel(lambda: t["read"](A), lambda: t["find_candidates"](A),
                                   lambda: t["decide"](A, "acc1"), lambda: t["decide"](A, "acc2"),
                                   lambda: t["decide"](A, "acc1"))

        deps, table, _, _ = make_deps(cand, need, orchestrate=orchestrate)
        deps = orchestrator.Deps(**{**deps.__dict__, "gate": SlowGate(cand, need)})
        orchestrator.run(run_input([A], [account("acc1"), account("acc2")]), deps)
        self.assertEqual(deps.gate.calls, [["acc1", "acc2"], ["needed", "not_needed"],
                                           ["needed", "not_needed"]])
        self.assertEqual(deps.cache.gets.count(A), 1)
        self.assertEqual(json.loads(seen["out"][2]), json.loads(seen["out"][4]))
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_parallel_drafts_at_budget_boundary_stay_within_budget(self):
        hashes = [format(i, "x") * 64 for i in range(4)]
        cand = {(x[:6], "acc1"): 0.9 for x in hashes}
        need = {(x[:6], "고객acc1"): 0.9 for x in hashes}
        seen = {}

        def orchestrate(tools, prompt):
            t = {f.__name__: f for f in tools}
            for x in hashes:
                t["find_candidates"](x)
                t["decide"](x, "acc1")
            seen["out"] = [json.loads(o) for o in parallel(
                *[(lambda x=x: t["draft"](x, "acc1")) for x in hashes],
                lambda: t["draft"](hashes[0], "acc1"))]

        deps, table, _, writer_model = make_deps(cand, need, orchestrate=orchestrate)
        stats = orchestrator.run(run_input(hashes, [account("acc1")], pairs=2), deps)
        self.assertLessEqual(stats["researched"], 2)
        self.assertEqual(writer_model.calls, 2)
        outcomes = [o["outcome"] for o in seen["out"][:4]]
        self.assertEqual(sorted(outcomes), ["deferred", "deferred", "drafted", "drafted"])
        self.assertEqual(seen["out"][4], seen["out"][0])
        self.assertEqual(len(rows(table, "DRAFT#")), 2)

    def test_tool_call_after_sweep_is_refused(self):
        kept = {}

        def orchestrate(tools, prompt):
            kept.update({f.__name__: f for f in tools})

        deps, table, _, _ = make_deps({}, {}, orchestrate=orchestrate)
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        writes = len(table.puts)
        self.assertEqual(json.loads(kept["read"](A)), {"refused": "closed"})
        self.assertEqual(len(table.puts), writes)


class FakeClock:
    def __init__(self, now=0.0):
        self.now = now

    def __call__(self):
        return self.now


def with_clock(deps, clock):
    return orchestrator.Deps(**{**deps.__dict__, "clock": clock})


class ToolLimitTest(unittest.TestCase):
    cand = {(A[:6], "acc1"): 0.9}
    need = {(A[:6], "고객acc1"): 0.9}

    def test_call_past_limit_stops_the_agent_and_sweep_completes(self):
        out = []

        def orchestrate(tools, prompt):
            t = {f.__name__: f for f in tools}
            for _ in range(orchestrator.MAX_TOOL_CALLS):
                t["read"](B)
            try:
                t["find_candidates"](A)
            except orchestrator.llm.AgentLimit as limit:
                out.append(limit.code)
                raise
            out.append("not stopped")

        deps, table, _, _ = make_deps(self.cand, self.need, orchestrate=orchestrate)
        stats = orchestrator.run(run_input([A, B], [account("acc1")]), deps)
        self.assertEqual(out, ["tool_limit"])
        self.assertEqual(deps.gate.calls[0], ["acc1"])  # the sweep, not the refused tool
        self.assertEqual(stats["drafted"], 1)
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_agent_phase_ends_after_a_third_of_the_deadline(self):
        clock, out = FakeClock(), []

        def orchestrate(tools, prompt):
            t = {f.__name__: f for f in tools}
            out.append(json.loads(t["read"](A)))
            clock.now = orchestrator.DEFAULT_DEADLINE_SECONDS / 3 + 1
            with self.assertRaises(orchestrator.llm.AgentLimit) as ctx:
                t["read"](B)
            out.append(ctx.exception.code)
            raise ctx.exception

        deps, table, _, _ = make_deps(self.cand, self.need, orchestrate=orchestrate)
        stats = orchestrator.run(run_input([A, B], [account("acc1")]), with_clock(deps, clock))
        self.assertEqual(out[1], "deadline")
        self.assertEqual(stats["drafted"], 1)
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_agent_skipped_when_a_third_has_passed(self):
        clock = FakeClock()
        called = []

        def claim_late():
            clock.now = 600  # > 1500 / 3, before the agent phase
        deps, table, res, _ = make_deps(self.cand, self.need,
                                        orchestrate=lambda t, p: called.append(1))
        res.inner.claim = claim_late
        orchestrator.run(run_input([A], [account("acc1")]), with_clock(deps, clock))
        self.assertEqual(called, [])
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")


class DeadlineTest(unittest.TestCase):
    cand = {(A[:6], "acc1"): 0.9, (B[:6], "acc1"): 0.9}
    need = {(A[:6], "고객acc1"): 0.9, (B[:6], "고객acc1"): 0.9}

    def run_until(self, tick_after):
        """The fake clock passes the deadline after `tick_after` gate calls."""
        clock = FakeClock()
        deps, table, _, writer_model = make_deps(self.cand, self.need)
        inner = deps.gate.score_options

        def scoring(*args):
            if len(deps.gate.calls) + 1 >= tick_after:
                clock.now = 1501
            return inner(*args)
        deps.gate.score_options = scoring
        with mock.patch.dict("os.environ", {"RUN_DEADLINE_SECONDS": "1500"}):
            with self.assertRaises(orchestrator.RunDeadline):
                orchestrator.run(run_input([A, B], [account("acc1")]), with_clock(deps, clock))
        return table, writer_model

    def test_deadline_in_sweep_writes_no_results_and_no_done(self):
        for tick_after in (1, 2, 4):  # during find, during decide, before drafting/writing
            with self.subTest(tick_after=tick_after):
                table, _ = self.run_until(tick_after)
                self.assertEqual(sks(table), ["CLAIM"])

    def test_deadline_stops_before_next_step(self):
        _, writer_model = self.run_until(1)
        self.assertEqual(writer_model.calls, 0)


class GateErrorTest(unittest.TestCase):
    def test_candidate_scoring_error_defers_item_without_dec_rows(self):
        out = []

        def orchestrate(tools, prompt):
            out.append(json.loads({f.__name__: f for f in tools}["find_candidates"](A)))
        deps, table, _, _ = make_deps({}, {}, orchestrate=orchestrate)
        with mock.patch.object(orchestrator.gate, "candidates",
                               side_effect=orchestrator.gate.GateError()) as scoring:
            orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertEqual(out, [{"error": "gate_error"}])
        self.assertEqual(scoring.call_count, 2)  # the sweep retried once
        item = rows(table, "ITEM#")["ITEM#" + A]
        self.assertEqual((item["state"], item["reason"]), ("deferred", "gate_error"))
        self.assertEqual(rows(table, "DEC#"), {})
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_one_pair_error_defers_item_and_keeps_the_good_pair_row(self):
        cand = {(A[:6], "acc1"): 0.9, (A[:6], "acc2"): 0.9}
        need = {(A[:6], "고객acc1"): 0.9}
        deps, table, _, _ = make_deps(cand, need)
        original = orchestrator.gate.decide_pair

        def flaky(content, acct, g):
            if acct.account_id == "acc2":
                return orchestrator.gate.PairDecision("not_needed", 0.0, "gate_error", {},
                                                      error=True)
            return original(content, acct, g)
        with mock.patch.object(orchestrator.gate, "decide_pair", side_effect=flaky):
            stats = orchestrator.run(run_input([A], [account("acc1"), account("acc2")]), deps)
        self.assertEqual(set(rows(table, "DEC#")), {f"DEC#{A}#acc1"})
        item = rows(table, "ITEM#")["ITEM#" + A]
        self.assertEqual((item["state"], item["reason"]), ("deferred", "gate_error"))
        self.assertEqual(stats["gated"], 1)

    def test_real_no_candidates_stays_decided(self):
        deps, table, _, _ = make_deps({(A[:6], "acc1"): 0.1}, {})
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        item = rows(table, "ITEM#")["ITEM#" + A]
        self.assertEqual((item["state"], item["reason"]), ("decided", ""))


class ClaimTest(unittest.TestCase):
    def test_claim_before_any_model_call_and_reuse_refused(self):
        deps, table, res, _ = make_deps({(A[:6], "acc1"): 0.9}, {(A[:6], "고객acc1"): 0.9})
        orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertEqual(table.puts[0]["Item"]["SK"], "CLAIM")
        again, _, _, writer_model = make_deps({(A[:6], "acc1"): 0.9}, {},
                                              orchestrate=lambda t, p: self.fail("agent ran"))
        rerun = store.RunResults(table, RUN_ID, FakeKMS(), "key-1", clock=lambda: NOW)
        again = orchestrator.Deps(**{**again.__dict__, "results": rerun})
        puts = len(table.puts)
        with self.assertRaises(store.RunExists):
            orchestrator.run(run_input([A], [account("acc1")]), again)
        self.assertEqual(again.gate.calls, [])
        self.assertEqual(again.cache.gets, [])
        self.assertEqual(writer_model.calls, 0)
        self.assertEqual(len(table.puts), puts)


class FailureTest(unittest.TestCase):
    cand = {(A[:6], "acc1"): 0.9}
    need = {(A[:6], "고객acc1"): 0.9}

    def test_orchestrator_exception_still_completes(self):
        def boom(tools, prompt):
            tools[0](A)
            raise RuntimeError("model cannot call tools")
        deps, table, _, _ = make_deps(self.cand, self.need, orchestrate=boom)
        with self.assertLogs("customer-update-agent", "WARNING") as logs:
            stats = orchestrator.run(run_input([A, B], [account("acc1")]), deps)
        self.assertEqual(stats["drafted"], 1)
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")
        joined = "\n".join(logs.output)
        self.assertIn("RuntimeError", joined)
        self.assertNotIn("model cannot call tools", joined)

    def test_sweep_exception_leaves_no_done(self):
        deps, table, _, _ = make_deps(self.cand, self.need)
        with mock.patch.object(orchestrator.gate, "decide_pair", side_effect=RuntimeError("x")):
            with self.assertRaises(RuntimeError):
                orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertEqual(sks(table), ["CLAIM"])

    def test_put_draft_exception_leaves_no_done(self):
        deps, table, res, _ = make_deps(self.cand, self.need, fail_on="put_draft")
        with self.assertRaises(RuntimeError):
            orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertNotIn("put_done", res.order)
        self.assertNotIn("DONE", [p["Item"]["SK"] for p in table.puts])

    def test_kms_failure_leaves_no_done(self):
        deps, table, res, _ = make_deps(self.cand, self.need)
        res.inner._kms = FakeKMS(error=RuntimeError("kms down"))
        with self.assertRaises(orchestrator.sealing.SealingError):
            orchestrator.run(run_input([A], [account("acc1")]), deps)
        self.assertEqual(sks(table), ["CLAIM"])

    def test_sweep_not_passed_is_discarded_not_crash(self):
        deps, table, _, _ = make_deps(self.cand, self.need)
        with mock.patch.object(orchestrator.pipeline, "draft_pair",
                               side_effect=LedgerRefused("not_passed")):
            orchestrator.run(run_input([A], [account("acc1")]), deps)
        dec = rows(table, "DEC#")[f"DEC#{A}#acc1"]
        self.assertEqual((dec["outcome"], dec["discardReason"]), ("discarded", "not_passed"))
        self.assertEqual(table.puts[-1]["Item"]["SK"], "DONE")

    def test_build_deps_without_key_fails_before_models(self):
        env = {"TABLE_NAME": "t", "AWS_DEFAULT_REGION": "ap-northeast-2",
               "AWS_ACCESS_KEY_ID": "x", "AWS_SECRET_ACCESS_KEY": "x"}
        with mock.patch.dict("os.environ", env, clear=True), \
                mock.patch.object(orchestrator.llm, "build_agent",
                                  side_effect=AssertionError("no model expected")):
            with self.assertRaises(orchestrator.sealing.SealingError) as ctx:
                orchestrator.build_deps(run_input([A], [account("acc1")]))
        self.assertEqual(ctx.exception.code, "unavailable")

    def test_build_deps_passes_region_to_boto3(self):
        made = []

        class FakeResource:
            def Table(self, name):
                return name

        def resource(name, **kw):
            made.append((name, kw))
            return FakeResource()

        def client(name, **kw):
            made.append((name, kw))
            return object()

        for env, region in (({"AWS_REGION": "us-west-2"}, "us-west-2"),
                            ({"AWS_DEFAULT_REGION": "eu-west-1"}, "eu-west-1")):
            made.clear()
            env = dict(env, TABLE_NAME="t", KMS_KEY_ID="k")
            with self.subTest(region=region), mock.patch.dict("os.environ", env, clear=True), \
                    mock.patch("boto3.resource", resource), mock.patch("boto3.client", client), \
                    mock.patch.object(orchestrator.llm, "build_agent", lambda *a, **k: object()), \
                    mock.patch.object(orchestrator.pipeline, "build_deps", lambda run_id: None):
                orchestrator.build_deps(run_input([A], [account("acc1")]))
                self.assertEqual(made, [("dynamodb", {"region_name": region}),
                                        ("kms", {"region_name": region})])

    def test_logs_carry_no_urls_or_names(self):
        deps, _, _, _ = make_deps(self.cand, self.need)
        with self.assertLogs("customer-update-agent", "INFO") as logs:
            orchestrator.run(run_input([A], [account("acc1")]), deps)
        joined = "\n".join(logs.output)
        self.assertNotIn("https://", joined)
        self.assertNotIn("고객acc1", joined)
        self.assertNotIn(A, joined)


if __name__ == "__main__":
    unittest.main()
