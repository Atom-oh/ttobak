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

    def test_missing_cost_columns_count_as_zero(self):
        with mock.patch.object(cohtags.L, "load", return_value=[{"recommendationId": "r0", "tags": "[]"}]):
            got = cohtags.recs("acct-a")
        self.assertEqual((got[0]["_cost"], got[0]["_sav"]), (0.0, 0.0))


if __name__ == "__main__":
    unittest.main()
