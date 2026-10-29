"""cohtags.py: COH recommendation rows with malformed tag cells. Synthetic data only."""
import os, sys, unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import cohtags  # noqa: E402


def rec(i, tags):
    return {"recommendationId": "r%d" % i, "tags": tags, "estimatedMonthlyCost": "10", "estimatedMonthlySavings": "2"}


class Recs(unittest.TestCase):
    def test_malformed_tag_cells_are_untagged(self):
        rows = [rec(0, "[{'key': 'team', 'value': 'a'}]"), rec(1, "{'key': 'team'}"), rec(2, "5"), rec(3, "'plain'"),
                rec(4, "['x', 'y']"), rec(5, "[{'key': 'team'}]"), rec(6, "[{'key': 'env', 'value': 'prod'}, 3]")]
        with mock.patch.object(cohtags.L, "load", return_value=rows):
            got = cohtags.recs("acct-a")
        self.assertEqual([r["_tags"] for r in got], [{"team": "a"}, {}, {}, {}, {}, {}, {"env": "prod"}])

    def test_non_string_keys_and_values_are_skipped(self):
        # as tagkeys.parse_cell: only string keys with string values survive, so analyze never slices or
        # hashes an int, None or list
        cell = ("[{'key': 'team', 'value': 1}, {'key': 'env', 'value': None}, {'key': 'app', 'value': ['x']},"
                " {'key': 2, 'value': 'y'}, {'key': 'owner', 'value': 'b'}]")
        rows = [dict(rec(i, cell), actionType="Stop", currentResourceType="Ec2Instance", currentResourceSummary="",
                     accountId="111111111111", region="r") for i in range(3)]
        with mock.patch.object(cohtags.L, "load", return_value=rows):
            got = cohtags.recs("acct-a")
            self.assertEqual([r["_tags"] for r in got], [{"owner": "b"}] * 3)
            res = cohtags.analyze("acct-a", ["111111111111"])
        self.assertEqual(res["n"], 3)

    def test_missing_cost_columns_count_as_zero(self):
        with mock.patch.object(cohtags.L, "load", return_value=[{"recommendationId": "r0", "tags": "[]"}]):
            got = cohtags.recs("acct-a")
        self.assertEqual((got[0]["_cost"], got[0]["_sav"]), (0.0, 0.0))


if __name__ == "__main__":
    unittest.main()
