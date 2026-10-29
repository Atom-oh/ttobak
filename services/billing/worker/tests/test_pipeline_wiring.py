"""Server wiring for the agent step, the merge helpers and the cache ignore policy."""
import json, os, re, subprocess, sys, tempfile, unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))  # services/billing/worker
REPO = os.path.abspath(os.path.join(ROOT, "..", "..", ".."))  # TTOBAK repo root
sys.path.insert(0, os.path.join(ROOT, "scripts"))
import agents  # noqa: E402


from fileutil import rd, wr, jload, jdump  # noqa: E402

def git(*a, cwd):
    return subprocess.run(["git", *a], cwd=cwd, capture_output=True, text=True)


class RunAccountWiring(unittest.TestCase):
    """run_account.sh is the production per-account script the worker runs."""

    def setUp(self):
        self.ra = rd(os.path.join(ROOT, "run_account.sh"), encoding="utf-8")

    def test_steps_in_order_without_agents_or_report(self):
        order = [m.group(1) for m in re.finditer(r'^\s*step (validate|extract|datasets|check) running', self.ra, re.M)]
        self.assertEqual(order, ["validate", "extract", "datasets", "check"])
        self.assertIn('echo "STEP $1 $2"', self.ra)
        for absent in ("agents.py", "report.py"):
            self.assertNotIn(absent, self.ra)
        self.assertIn("set -euo pipefail", self.ra)
        self.assertRegex(self.ra, r"\^\[A-Za-z0-9_-\]\{1,64\}\$")

    def test_script_order(self):
        lines = self.ra.splitlines()
        at = lambda pat: next(i for i, l in enumerate(lines) if re.search(pat, l))  # noqa: E731
        for a, b in (("stage_cot", "xlsx2csv"), ("xlsx2csv", "period"), ("period", "build_data"), ("build_data", "cot360_metrics"),
                     ("cot360_metrics", "briefing"), ("briefing", "cot360_check")):
            self.assertLess(at(a + r"\.py"), at(b + r"\.py"), (a, b))


class Pipeline(unittest.TestCase):
    def test_server_allows_agent_notes(self):
        js = rd(os.path.join(REPO, "services", "billing", "bff", "app.js"), encoding="utf-8")
        for name in ("DATASETS", "RERUN_DATASETS"):
            body = re.search(r"const %s = new Set\(\[(.*?)\]\);" % name, js, re.S).group(1)
            self.assertIn("agent_notes", re.findall(r"'([a-z_]+)'", body), name)


def write(d, name, rows):
    os.makedirs(d, exist_ok=True)
    jdump(rows, os.path.join(d, name + ".json"), "w", encoding="utf-8", ensure_ascii=False)


class Merge(unittest.TestCase):
    def test_concatenates_in_entity_order(self):
        import merge_datasets
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as t:
            a, b, out = (os.path.join(t, x) for x in ("a", "b", "out"))
            write(a, "kpis", [{"entity": "acct-a", "v": 1}])
            write(b, "kpis", [{"entity": "acct-b", "v": 2}])
            write(b, "only_b", [{"entity": "acct-b"}])
            write(a, "accounts", [{"entity": "acct-a", "last": 1}])
            write(b, "accounts", [{"entity": "acct-b", "last": 9}])
            write(a, "whats_new", [{"id": "w1"}])
            write(b, "whats_new", [{"id": "w1"}])
            write(out, "agent_notes", [{"entity": "acct-a", "kind": "meta"}])  # existing notes are kept
            merge_datasets.merge_dirs(out, [b, a])  # given in the wrapper's entity order
            load = lambda n: jload(os.path.join(out, n + ".json"), encoding="utf-8")  # noqa: E731
            self.assertEqual([r["entity"] for r in load("kpis")], ["acct-b", "acct-a"])
            self.assertEqual([r["entity"] for r in load("accounts")], ["acct-a", "acct-b"])  # producer sorts by entity
            self.assertEqual(load("only_b"), [{"entity": "acct-b"}])
            self.assertEqual(load("whats_new"), [{"id": "w1"}])
            self.assertEqual(load("agent_notes"), [{"entity": "acct-a", "kind": "meta"}])

    def test_raw_catalog_union(self):
        import merge_datasets
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as t:
            a, b, out = (os.path.join(t, x) for x in ("a", "b", "out"))
            write(a, "raw_x", [{"entity": "acct-a", "c": 1}])
            write(b, "raw_x", [{"entity": "acct-b", "c": 2, "d": "s"}])
            write(a, "raw_catalog", [{"id": "raw_x", "sheet": "Data_X", "columns": [], "rows_by_entity": {"acct-a": 1}, "bytes": 1}])
            write(b, "raw_catalog", [{"id": "raw_x", "sheet": "Data_X", "columns": [], "rows_by_entity": {"acct-b": 1}, "bytes": 1},
                                     {"id": "raw_w", "sheet": "Data_W", "columns": [], "rows_by_entity": {"acct-b": 0}, "bytes": 2}])
            merge_datasets.merge_dirs(out, [a, b])
            cat = jload(os.path.join(out, "raw_catalog.json"), encoding="utf-8")
            self.assertEqual([c["id"] for c in cat], ["raw_w", "raw_x"])  # sheet order, as raw_export writes it
            x = cat[1]
            self.assertEqual(x["rows_by_entity"], {"acct-a": 1, "acct-b": 1})
            self.assertEqual([c["name"] for c in x["columns"]], ["entity", "c", "d"])
            self.assertEqual(x["bytes"], os.path.getsize(os.path.join(out, "raw_x.json")))

    def test_unknown_shape_refused(self):
        import merge_datasets
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as t:
            write(os.path.join(t, "a"), "odd", {"x": 1})
            with self.assertRaises(SystemExit):
                merge_datasets.merge_dirs(os.path.join(t, "out"), [os.path.join(t, "a")])

    def test_dashboard_json(self):
        import merge_datasets
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as t:
            ins = []
            for e in ("acct-a", "acct-b"):
                p = os.path.join(t, e + ".json")
                jdump({"months": ["2026-03"], "entities": {e: {"k": 1}}}, p, "w")
                ins.append(p)
            merge_datasets.merge_dashboard(os.path.join(t, "all.json"), ins)
            d = jload(os.path.join(t, "all.json"))
            self.assertEqual(list(d["entities"]), ["acct-a", "acct-b"])
            self.assertEqual(d["months"], ["2026-03"])


@unittest.skipUnless(git("rev-parse", "--show-toplevel", cwd=REPO).returncode == 0, "not a git checkout")
class IgnorePolicy(unittest.TestCase):
    def test_generated_outputs_ignored(self):
        for p in ("services/billing/worker/.agent_cache/x.json", "services/billing/worker/out/x.json"):
            self.assertEqual(git("check-ignore", "-q", p, cwd=REPO).returncode, 0, p)


class CacheOverride(unittest.TestCase):
    def repo(self, d, ignore):
        git("init", "-q", cwd=d)
        if ignore:
            wr("cache/\n", os.path.join(d, ".gitignore"), "w")

    def test_unignored_repo_cache_refused(self):
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as data:
            self.repo(d, ignore=False)
            jdump([], os.path.join(data, "kpis.json"), "w")
            with mock.patch.dict(os.environ, {"AGENT_CACHE": os.path.join(d, "cache")}), \
                    mock.patch.object(agents, "bedrock_invoke", side_effect=AssertionError("model called")):
                self.assertEqual(agents.main([data]), 2)
            self.assertFalse(os.path.exists(os.path.join(data, "agent_notes.json")))

    def test_selectively_ignored_cache_refused(self):
        with tempfile.TemporaryDirectory() as d:
            git("init", "-q", cwd=d)
            wr("cache/probe.json\n", os.path.join(d, ".gitignore"), "w")
            with mock.patch.dict(os.environ, {"AGENT_CACHE": os.path.join(d, "cache")}):
                with self.assertRaises(SystemExit):
                    agents.cache_dir()

    def test_ignored_repo_cache_accepted(self):
        with tempfile.TemporaryDirectory() as d:
            self.repo(d, ignore=True)
            with mock.patch.dict(os.environ, {"AGENT_CACHE": os.path.join(d, "cache")}):
                self.assertEqual(agents.cache_dir(), os.path.join(d, "cache"))

    def test_prune_makes_no_calls(self):
        with tempfile.TemporaryDirectory() as data:
            jdump([], os.path.join(data, "kpis.json"), "w")
            jdump([{"entity": "acct-a", "kind": "meta", "digest_fp": "old", "source_fp": "old"},
                       {"entity": "acct-a", "kind": "headline", "text": "x", "source_fp": "old"}], os.path.join(data, "agent_notes.json"), "w")
            with mock.patch.object(agents, "bedrock_invoke", side_effect=AssertionError("model called")):
                self.assertEqual(agents.main([data, "--prune"]), 0)
            self.assertEqual(jload(os.path.join(data, "agent_notes.json")), [])


if __name__ == "__main__":
    unittest.main()
