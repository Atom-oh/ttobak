"""Tag keys are detected from COH resources, never named after a customer. Synthetic data only."""
import json, os, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "scripts"))
import tagkeys  # noqa: E402


from fileutil import jload  # noqa: E402

class TagKeys(unittest.TestCase):
    def test_rank_orders_by_coverage(self):
        recs = [{"tags": {"a:team": "x", "env": "p"}}, {"tags": {"env": "d"}}]
        self.assertEqual(tagkeys.rank(recs), ["env", "a:team"])

    def test_rank_ties_by_name_and_aws_filter(self):
        recs = [{"_tags": {"b": "1", "a": "1", "aws:createdBy": "u", "aws:eks:cluster-name": "c"}}]
        self.assertEqual(tagkeys.rank(recs), ["a", "aws:eks:cluster-name", "b"])

    def test_team_and_created(self):
        self.assertEqual(tagkeys.team_key(["env", "org/team"]), "org/team")
        self.assertEqual(tagkeys.created_key(["x/created-at"]), "x/created-at")
        self.assertIsNone(tagkeys.team_key(["env"]))
        self.assertIsNone(tagkeys.created_key([]))

    def test_team_word_priority(self):
        self.assertEqual(tagkeys.team_key(["o/owner", "o/department", "t/team"]), "t/team")
        self.assertEqual(tagkeys.team_key(["o/department", "o/owner"]), "o/owner")

    def test_grouping_keys_by_role_else_top_six(self):
        recs = [{"tags": {"x/team": "a", "x/project": "b", "aws:eks:cluster-name": "c", "env": "p", "Name": "n"}},
                {"tags": {"env": "d", "x/team": "e", "k%d" % 1: "v"}}]
        self.assertEqual(tagkeys.precedence(recs), ["aws:eks:cluster-name", "x/project", "x/team", "Name"])
        self.assertEqual(tagkeys.tag_keys(recs), ["x/team", "aws:eks:cluster-name", "x/project"])
        self.assertTrue(set(tagkeys.top(recs)) <= set(tagkeys.dims(recs)))
        plain = [{"tags": {"k%d" % i: "v" for i in range(9)}}]
        self.assertEqual(tagkeys.precedence(plain), tagkeys.rank(plain)[:6])
        self.assertEqual(len(tagkeys.tag_keys(plain)), 6)

    def test_controller_labels_are_not_grouping_keys(self):
        recs = [{"tags": {"kubernetes.io/cluster/x": "owned", "app.kubernetes.io/name": "web", "x/team": "t",
                          "k8s.io/cluster-name": "c", "sharedkey/cluster": "shared"}}] * 3
        for keys in (tagkeys.precedence(recs), tagkeys.dims(recs), tagkeys.tag_keys(recs)):
            self.assertEqual([k for k in keys if "kubernetes.io" in k or "k8s.io" in k or k == "sharedkey/cluster"], [])
        self.assertEqual(tagkeys.precedence(recs), ["x/team"])

    def test_team_key_word_boundaries(self):
        self.assertIsNone(tagkeys.team_key(["steam", "ownerReferences", "downer"]))
        self.assertEqual(tagkeys.team_key(["steam", "x/team-name"]), "x/team-name")

    def test_empty_parsed_tags_ignore_raw_string(self):
        self.assertEqual(tagkeys.rank([{"_tags": {}, "tags": "[]"}]), [])

    def test_company_key(self):
        recs = [{"tags": {"user:Company": "a", "env": "p"}}]
        self.assertEqual(tagkeys.role_key(recs, "company"), "user:Company")
        self.assertIsNone(tagkeys.role_key([{"tags": {"env": "p"}}], "company"))

    def test_no_tags_returns_empty(self):
        self.assertEqual(tagkeys.rank([]), [])
        self.assertEqual(tagkeys.precedence([]), [])
        import tag_services
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as out:
            os.makedirs(os.path.join(out, "acct-a"))
            with mock.patch.object(tag_services, "ENTITIES", ["acct-a"]):
                tag_services.main(out, out)
            for fn in ("tag_keys.json", "tag_services.json"):
                self.assertEqual(jload(os.path.join(out, fn)), [])


if __name__ == "__main__":
    unittest.main()
