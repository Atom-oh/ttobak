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


class NormName(unittest.TestCase):
    LEGACY = r"(-[0-9a-f]{6,}|-\d+|-asg|-node(group)?s?)+$"

    def legacy(self, v):
        import re
        return re.sub(r"^eks-", "", re.sub(self.LEGACY, "", v.strip(), flags=re.I))

    def test_known_outputs(self):
        from tag_services import norm_name
        cases = {"web-asg": "web", "eks-api-nodegroup-1a2b3c4d": "api", "batch-Nodes-12": "batch",
                 "db-01": "db", " svc-ABCDEF0-asg ": "svc", "eks-x": "x", "a--1": "a-", "-123": "",
                 "name-12ab": "name-12ab", "node": "node", "my-app": "my-app", "app-node-x": "app-node-x",
                 "eks-eks-1": "eks", "v-nodegroups-NODE-7": "v"}
        for v, want in cases.items():
            self.assertEqual(norm_name(v), want, v)
            self.assertEqual(self.legacy(v), want, v)

    def test_matches_legacy_regex_on_short_inputs(self):
        import random
        from tag_services import norm_name
        rnd = random.Random(7)
        parts = ["-", "1", "a", "f", "A", "g", "0", "-asg", "-node", "-nodegroup", "s", "eks-", " ", "x", "-123456"]
        for _ in range(5000):
            v = "".join(rnd.choice(parts) for _ in range(rnd.randint(0, 8)))
            self.assertEqual(norm_name(v), self.legacy(v), repr(v))

    def test_adversarial_value_is_linear(self):
        import time
        from tag_services import norm_name
        # every "-123456" segment matches two alternatives; the legacy regex backtracked 2**n times on the "!"
        for v in ("svc" + "-123456" * 40 + "!", "-1" * 500000 + "x"):
            t = time.monotonic()
            self.assertEqual(norm_name(v), v)
            self.assertLess(time.monotonic() - t, 1.0)


if __name__ == "__main__":
    unittest.main()
