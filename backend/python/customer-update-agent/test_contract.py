import copy
import json
import unittest
from pathlib import Path

import contract
from contract import ContractError, parse_run

CONTRACT_PATH = Path(__file__).resolve().parents[3] / "contracts" / "customer-updates" / "run-v1.json"


def example():
    with open(CONTRACT_PATH, encoding="utf-8") as f:
        return copy.deepcopy(json.load(f)["example"])


class ParseRunTest(unittest.TestCase):
    def code_of(self, payload):
        with self.assertRaises(ContractError) as cm:
            parse_run(payload)
        return cm.exception.code

    def test_example_parses(self):
        run = parse_run(example())
        self.assertEqual(run.run_id, "0123456789abcdef0123456789abcdef")
        self.assertEqual(run.items[0].url_hash, "a" * 64)
        acct = run.accounts[0]
        self.assertEqual(acct.name, "한빛리테일")
        self.assertEqual(acct.opportunities[0].project_id, "proj-0001")
        self.assertEqual(acct.drafts_remaining, 3)
        self.assertEqual(run.budgets.max_research_pairs, 10)
        self.assertEqual(run.budgets.max_drafts, 20)

    def test_defaults(self):
        p = example()
        for k in ("services", "opportunities", "news", "insights", "notes", "blockList"):
            del p["accounts"][0][k]
        del p["budgets"]
        run = parse_run(p)
        self.assertEqual(run.accounts[0].services, ())
        self.assertEqual(run.accounts[0].block_list, ())
        self.assertEqual(run.budgets.max_research_pairs, 10)
        self.assertEqual(run.budgets.max_drafts, 30)

    def test_too_many_items(self):
        p = example()
        p["items"] = [{"urlHash": f"{i:064x}", "url": f"https://e.com/{i}"} for i in range(31)]
        self.assertEqual(self.code_of(p), "items")

    def test_no_items_or_accounts(self):
        p = example()
        p["items"] = []
        self.assertEqual(self.code_of(p), "items")
        p = example()
        p["accounts"] = []
        self.assertEqual(self.code_of(p), "accounts")

    def test_too_many_accounts(self):
        p = example()
        base = p["accounts"][0]
        p["accounts"] = [dict(base, accountId=f"a{i}") for i in range(41)]
        self.assertEqual(self.code_of(p), "accounts")

    def test_bad_run_id(self):
        for bad in ("XYZ", "A" * 32, "0" * 31, 5, None):
            p = example()
            p["runId"] = bad
            self.assertEqual(self.code_of(p), "run_id", bad)

    def test_bad_version(self):
        p = example()
        p["version"] = 2
        self.assertEqual(self.code_of(p), "version")
        p["version"] = True
        self.assertEqual(self.code_of(p), "version")

    def test_http_url(self):
        p = example()
        p["items"][0]["url"] = "http://example.com/a"
        self.assertEqual(self.code_of(p), "item_url")

    def test_bad_url_hash(self):
        p = example()
        p["items"][0]["urlHash"] = "abc"
        self.assertEqual(self.code_of(p), "item_hash")

    def test_oversized_account(self):
        p = example()
        p["accounts"][0]["notes"] = ["한" * 3000]  # 9,000 bytes UTF-8
        self.assertEqual(self.code_of(p), "account_size")

    def test_account_under_limit_with_multibyte(self):
        p = example()
        p["accounts"][0]["notes"] = ["한" * 2000]  # 6,000 bytes
        parse_run(p)

    def test_account_id_rules(self):
        for bad in ("", "a" * 65, "a b", "a#b", "한글", 3):
            p = example()
            p["accounts"][0]["accountId"] = bad
            self.assertEqual(self.code_of(p), "account_id", bad)
        p = example()
        p["accounts"][0]["accountId"] = "a" * 64
        parse_run(p)

    def test_duplicates(self):
        p = example()
        p["items"].append(dict(p["items"][0], url="https://example.com/other"))
        self.assertEqual(self.code_of(p), "duplicate")
        p = example()
        p["accounts"].append(copy.deepcopy(p["accounts"][0]))
        self.assertEqual(self.code_of(p), "duplicate")

    def test_decided_accounts(self):
        p = example()
        p["accounts"].append(dict(copy.deepcopy(p["accounts"][0]), accountId="acct-2"))
        p["items"][0]["decidedAccounts"] = ["acct-2"]
        self.assertEqual(parse_run(p).items[0].decided_accounts, frozenset({"acct-2"}))
        del p["items"][0]["decidedAccounts"]
        self.assertEqual(parse_run(p).items[0].decided_accounts, frozenset())
        for bad, code in (("acct-2", "shape"), ([5], "shape"), (None, "shape"),
                          (["acct-9"], "decided_accounts"),
                          (["acct-2", "acct-2"], "duplicate")):
            p["items"][0]["decidedAccounts"] = bad
            self.assertEqual(self.code_of(p), code, bad)

    def test_drafts_remaining(self):
        for bad in (-1, 1.5, "3", True, None):
            p = example()
            p["accounts"][0]["draftsRemaining"] = bad
            self.assertEqual(self.code_of(p), "shape", bad)
        p = example()
        p["accounts"][0]["draftsRemaining"] = 0
        self.assertEqual(parse_run(p).accounts[0].drafts_remaining, 0)

    def test_budgets_over_cap_rejected(self):
        for key, val in (("maxResearchPairs", 11), ("maxDrafts", 31), ("maxDrafts", -1)):
            p = example()
            p["budgets"][key] = val
            self.assertEqual(self.code_of(p), "budgets", (key, val))
        p = example()
        p["budgets"] = {"maxResearchPairs": 0, "maxDrafts": 0}
        self.assertEqual(parse_run(p).budgets.max_drafts, 0)

    def test_unknown_top_level_key(self):
        p = example()
        p["extra"] = 1
        self.assertEqual(self.code_of(p), "shape")

    def test_non_dict_payload(self):
        self.assertEqual(self.code_of([]), "shape")

    def test_bad_nested_shape(self):
        p = example()
        p["accounts"][0]["services"] = "Amazon Bedrock"
        self.assertEqual(self.code_of(p), "shape")
        p = example()
        p["accounts"][0]["opportunities"] = [{"projectId": "x"}]
        self.assertEqual(self.code_of(p), "shape")

    def test_trailing_newline_rejected(self):
        p = example()
        p["runId"] += "\n"
        self.assertEqual(self.code_of(p), "run_id")
        p = example()
        p["items"][0]["urlHash"] += "\n"
        self.assertEqual(self.code_of(p), "item_hash")
        p = example()
        p["accounts"][0]["accountId"] += "\n"
        self.assertEqual(self.code_of(p), "account_id")

    def test_url_hardening(self):
        for bad in (
            "https:///x",
            "https://",
            "https://exa mple.com/",
            "https://example.com/a b",
            "https://example.com/a\n",
            "https://example.com/\x00",
            "https://example.com/" + "a" * 2048,
            "https://[::1/x",
            None,
            5,
        ):
            p = example()
            p["items"][0]["url"] = bad
            self.assertEqual(self.code_of(p), "item_url", repr(bad))
        p = example()
        p["items"][0]["url"] = "https://example.com/" + "a" * (2048 - len("https://example.com/"))
        parse_run(p)

    def test_unknown_account_key(self):
        p = example()
        p["accounts"][0]["extra"] = 1
        self.assertEqual(self.code_of(p), "shape")

    def test_unserializable_or_deep_account(self):
        p = example()
        p["accounts"][0]["notes"] = [object()]
        self.assertEqual(self.code_of(p), "shape")
        p = example()
        deep = []
        for _ in range(100000):
            deep = [deep]
        p["accounts"][0]["extra"] = deep
        self.assertEqual(self.code_of(p), "shape")

    def test_wrong_types(self):
        def mutate(fn):
            p = example()
            fn(p)
            return self.code_of(p)

        self.assertEqual(mutate(lambda p: p["budgets"].update(maxDrafts=1.5)), "budgets")
        self.assertEqual(mutate(lambda p: p["budgets"].update(maxResearchPairs=1.5)), "budgets")
        self.assertEqual(mutate(lambda p: p.update(items="abc")), "items")
        self.assertEqual(mutate(lambda p: p.update(accounts="abc")), "accounts")
        self.assertEqual(mutate(lambda p: p.update(items=["x"])), "shape")
        self.assertEqual(mutate(lambda p: p.update(accounts=["x"])), "shape")
        self.assertEqual(mutate(lambda p: p["accounts"][0].update(name=5)), "shape")
        self.assertEqual(mutate(lambda p: p.update(budgets=[1])), "budgets")
        self.assertEqual(mutate(lambda p: p["budgets"].update(extra=1)), "budgets")

    def test_results_are_immutable(self):
        run = parse_run(example())
        with self.assertRaises(Exception):
            run.run_id = "x"

    def test_error_code_is_message(self):
        self.assertEqual(str(ContractError("items")), "items")


class ValidUrlTest(unittest.TestCase):
    # The Go run worker applies the same rule to item URLs before it builds a payload.
    def test_item_url_rule(self):
        cases = {
            "https://example.com/a?q=hello": True,
            "https://example.com/a?q=hello world": False,
            "https://example.com/a?q=a b": False,
            "https://example.com/a?q=a　b": False,
            "https://example.com/a?q=a b": False,
            "https://example.com/a?q=a\tb": False,
            "https://example.com/a?q=a\x7fb": False,
            "https://example.com/a?q=a\x01b": False,
            "http://example.com/a": False,
            "https:///a": False,
            "https://example.com/" + "a" * 2028: True,
            "https://example.com/" + "a" * 2029: False,
        }
        for url, want in cases.items():
            self.assertEqual(contract._valid_url(url), want, url)


if __name__ == "__main__":
    unittest.main()
