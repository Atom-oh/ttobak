import unittest

from contract import AccountPack, Budgets
from ledger import LedgerRefused, RunLedger


def acct(account_id, remaining=3):
    return AccountPack(account_id, account_id, (), (), (), (), (), (), remaining)


def make(max_pairs=10, max_drafts=30, accounts=None, items=()):
    accounts = accounts if accounts is not None else [acct("a1", 99)]
    return RunLedger(Budgets(max_pairs, max_drafts), accounts, items=items)


class LedgerTestCase(unittest.TestCase):
    def code_of(self, fn, *args):
        with self.assertRaises(LedgerRefused) as cm:
            fn(*args)
        return cm.exception.code


class GateTest(LedgerTestCase):
    def test_unrecorded_refused(self):
        self.assertEqual(self.code_of(make().require_gate_pass, "h1", "a1"), "not_passed")

    def test_not_needed_refused(self):
        l = make()
        l.record_gate("h1", "a1", "not_needed", 0.1)
        self.assertEqual(self.code_of(l.require_gate_pass, "h1", "a1"), "not_passed")

    def test_needed_passes_only_for_its_pair(self):
        l = make()
        l.record_gate("h1", "a1", "needed", 0.9)
        l.require_gate_pass("h1", "a1")
        self.assertEqual(self.code_of(l.require_gate_pass, "h1", "a2"), "not_passed")


class ResearchBudgetTest(LedgerTestCase):
    def test_eleventh_pair_refused(self):
        l = make()
        for i in range(10):
            l.take_research(f"h{i}", "a1")
        self.assertEqual(self.code_of(l.take_research, "h10", "a1"), "research_budget")

    def test_account_budget(self):
        l = make(accounts=[acct("a1", 1)])
        l.take_research("h1", "a1")
        self.assertEqual(self.code_of(l.take_research, "h2", "a1"), "account_budget")

    def test_unknown_account_refused(self):
        self.assertEqual(self.code_of(make().take_research, "h1", "zz"), "account_budget")

    def test_draft_budget(self):
        l = make(max_pairs=10, max_drafts=2)
        l.take_research("h1", "a1")
        l.take_research("h2", "a1")
        self.assertEqual(self.code_of(l.take_research, "h3", "a1"), "draft_budget")

    def test_zero_budgets(self):
        self.assertEqual(self.code_of(make(max_pairs=0).take_research, "h", "a1"), "research_budget")
        self.assertEqual(self.code_of(make(max_drafts=0).take_research, "h", "a1"), "draft_budget")

    def test_same_pair_twice_refused_first(self):
        l = make(max_pairs=1)
        l.take_research("h1", "a1")
        self.assertEqual(self.code_of(l.take_research, "h1", "a1"), "already_drafted")

    def test_refusal_does_not_count(self):
        l = make(accounts=[acct("a1", 1), acct("a2", 5)], max_pairs=2)
        l.take_research("h1", "a1")
        self.code_of(l.take_research, "h2", "a1")
        l.take_research("h2", "a2")
        self.assertEqual(l.stats()["researched"], 2)


class PerPairBudgetTest(LedgerTestCase):
    def test_search_budget(self):
        l = make()
        pair = ("h1", "a1")
        for _ in range(5):
            l.take_search(pair)
        self.assertEqual(self.code_of(l.take_search, pair), "search_budget")
        l.take_search(("h2", "a1"))

    def test_fetch_budget(self):
        l = make()
        pair = ("h1", "a1")
        for _ in range(3):
            l.take_fetch(pair)
        self.assertEqual(self.code_of(l.take_fetch, pair), "fetch_budget")

    def test_rewrite_once(self):
        l = make()
        pair = ("h1", "a1")
        self.assertTrue(l.take_rewrite(pair))
        self.assertFalse(l.take_rewrite(pair))
        self.assertTrue(l.take_rewrite(("h2", "a1")))


class UrlsAndStatsTest(LedgerTestCase):
    def test_allowed_urls_returns_copy(self):
        l = make()
        pair = ("h1", "a1")
        self.assertEqual(l.allowed_urls(pair), set())
        l.add_fetched(pair, "https://e.com/a")
        got = l.allowed_urls(pair)
        got.add("https://evil.com")
        self.assertEqual(l.allowed_urls(pair), {"https://e.com/a"})
        self.assertEqual(l.allowed_urls(("h2", "a1")), set())

    def test_stats_counts_only(self):
        l = make(items=["h1", "h2"])
        l.record_gate("h1", "a1", "needed", 0.9)
        l.record_gate("h2", "a1", "not_needed", 0.1)
        l.take_research("h1", "a1")
        l.take_search(("h1", "a1"))
        l.take_fetch(("h1", "a1"))
        l.take_fetch(("h1", "a1"))
        l.record_draft(("h1", "a1"))
        l.record_discard(("h3", "a1"))
        l.record_defer(("h4", "a1"))
        self.assertEqual(
            l.stats(),
            {"items": 2, "gated": 2, "needed": 1, "researched": 1, "drafted": 1,
             "discarded": 1, "deferred": 1, "searches": 1, "fetches": 2},
        )

    def test_instances_independent(self):
        a, b = make(), make()
        a.take_research("h1", "a1")
        self.assertEqual(b.stats()["researched"], 0)


if __name__ == "__main__":
    unittest.main()
