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

    def test_placeholder_creation_dates_are_undated(self):
        # "unknown" sorts after any YYYY-MM, so it used to count as a recent cohort and open its own month bucket
        def row(i, created):
            return dict(rec(i, "[{'key': 'CreatedAt', 'value': '%s'}]" % created), actionType="Stop",
                        currentResourceType="Ec2Instance", currentResourceSummary="", accountId="111111111111",
                        region="r")
        rows = [row(0, "2026-08-03"), row(1, "unknown"), row(2, "N/A"), row(3, "TBD"), row(4, "2025-01-10")]
        with mock.patch.object(cohtags.L, "load", return_value=rows), \
                mock.patch.object(cohtags.P, "PRESENT", ["2026-05", "2026-06", "2026-07", "2026-08"]):
            res = cohtags.analyze("acct-a", ["111111111111"])
        self.assertEqual(res["created_by_month"], {"2025-01": 1, "2026-08": 1})
        self.assertEqual(res["recent_n"], 1)

    def test_missing_cost_columns_count_as_zero(self):
        with mock.patch.object(cohtags.L, "load", return_value=[{"recommendationId": "r0", "tags": "[]"}]):
            got = cohtags.recs("acct-a")
        self.assertEqual((got[0]["_cost"], got[0]["_sav"]), (0.0, 0.0))


if __name__ == "__main__":
    unittest.main()
