import json
import unittest
from pathlib import Path

import checks

CONTRACTS = Path(__file__).resolve().parents[3] / "contracts" / "customer-updates"


def load(name):
    with open(CONTRACTS / name, encoding="utf-8") as f:
        return json.load(f)


def body_of(c):
    if "bodyRepeat" in c:
        return c["bodyRepeat"]["text"] * c["bodyRepeat"]["count"]
    return c["body"]


class ContractCasesTest(unittest.TestCase):
    def test_contract_cases(self):
        for c in load("draft-checks-v1.json")["cases"]:
            with self.subTest(c["name"]):
                got = checks.check_draft(
                    body_of(c), c["links"], set(c["allowedUrls"]), c["blockList"], c["stages"]
                )
                self.assertEqual(got, c["expect"])

    def test_case_names_unique(self):
        names = [c["name"] for c in load("draft-checks-v1.json")["cases"]]
        self.assertEqual(len(names), len(set(names)))

    def test_escape_cases(self):
        for c in load("draft-checks-v1.json")["escapeCases"]:
            with self.subTest(c["in"]):
                self.assertEqual(checks.escape_slack(c["in"]), c["out"])

    def test_every_code_has_a_case(self):
        seen = {code for c in load("draft-checks-v1.json")["cases"] for code in c["expect"]}
        self.assertEqual(seen, set(checks.CODES))

    def test_figure_examples_covered(self):
        cases = load("draft-checks-v1.json")["cases"]
        for ex in load("draft-terms-ko-v1.json")["figureExamples"]:
            covering = [c for c in cases if ex in body_of(c)]
            self.assertTrue(covering, ex)
            for c in covering:
                self.assertIn("figure", c["expect"], c["name"])


class MirrorTest(unittest.TestCase):
    def test_terms_mirror(self):
        terms = load("draft-terms-ko-v1.json")
        self.assertEqual(list(checks.OPPORTUNITY_WORDS), terms["opportunityWords"])
        self.assertEqual(list(checks.MEETING_OFFER_WORDS), terms["meetingOfferWords"])
        self.assertEqual(list(checks.FIGURE_UNITS), terms["figureUnits"])

    def test_internal_suffix_mirror(self):
        self.assertEqual(list(checks.INTERNAL_SUFFIXES), load("internal-hosts-v1.json")["suffixes"])

    def test_figure_examples_fail(self):
        for ex in load("draft-terms-ko-v1.json")["figureExamples"]:
            self.assertTrue(checks.has_figure(ex), ex)


if __name__ == "__main__":
    unittest.main()
