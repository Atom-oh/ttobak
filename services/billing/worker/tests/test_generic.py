"""No fixed months in the pipeline; entities come from the run's dirs. Synthetic data only."""
import importlib, json, os, re, subprocess, sys, tempfile, unittest
from glob import glob
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, ".."))  # services/billing/worker
SCRIPTS = os.path.join(ROOT, "scripts")
sys.path.insert(0, SCRIPTS)

# any literal YYYY-MM date (20[0-9]{2}-[01][0-9]) as well: periods and run dates come from the data
FIXED = re.compile(r"2026-0\d|20[0-9]{2}-[01][0-9]|\bm0[4-9]\b|(?<![a-z])(sep|aug|apr)(?![a-z])|[4-9]월")
RUN_ACCOUNT = os.path.join(ROOT, "run_account.sh")

from fileutil import rd, wr, jload, jdump  # noqa: E402

def entities_reload(env):
    import cotload, entities
    with mock.patch.dict(os.environ, env, clear=False):
        for k in ("COT_ENTITIES", "COT_NAMES"):
            if k not in env:
                os.environ.pop(k, None)
        importlib.reload(cotload)
        return importlib.reload(entities)


class Generic(unittest.TestCase):
    def test_no_fixed_period_literals(self):
        files = glob(SCRIPTS + "/*.py") + [RUN_ACCOUNT]
        hits = [(os.path.basename(p), i + 1) for p in files for i, l in enumerate(rd(p, encoding="utf-8").splitlines()) if FIXED.search(l)]
        self.assertEqual(hits, [])

    def test_insights_have_no_curated_keyword_rules(self):
        src = rd(os.path.join(SCRIPTS, "insights.py"), encoding="utf-8")
        for kw in ("blockchain", "vibe", '"jp-"', '"br-"', "financial"):
            self.assertNotIn(kw, src)

    def test_entities_from_dirs(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            for e in ("acct-b", "acct-a"):
                os.makedirs(os.path.join(d, e))
            wr("", os.path.join(d, "stray.txt"))
            self.assertEqual(entities_reload({"COT_CSV_DIR": d}).ENTITIES, ["acct-a", "acct-b"])

    def test_entities_env_and_names(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            p = os.path.join(d, "names.json")
            jdump({"acct-a": "한빛금융"}, p, "w", encoding="utf-8", ensure_ascii=False)
            m = entities_reload({"COT_CSV_DIR": d, "COT_ENTITIES": "acct-b,acct-a", "COT_NAMES": p})
            self.assertEqual(m.ENTITIES, ["acct-b", "acct-a"])
            self.assertEqual(m.NAMES["acct-a"], "한빛금융")
            self.assertEqual(m.NAMES["acct-b"], "acct-b")  # falls back to the id
            self.assertEqual(m.NAMES["acct-z"], "acct-z")

    def tearDown(self):
        import cotload, entities
        importlib.reload(cotload)
        importlib.reload(entities)


class PeriodHelpers(unittest.TestCase):
    def test_present_sorted_and_capped(self):
        import period
        env = {"COT_MONTHS": "2026-09,2026-03,2026-04,2026-05,2026-06,2026-07,2026-08,2026-08"}
        self.addCleanup(importlib.reload, period)
        with mock.patch.dict(os.environ, env):
            p = importlib.reload(period)
        self.assertEqual(p.PRESENT, ["2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09"])
        self.assertEqual((p.FIRST, p.PREV, p.LAST), ("2026-04", "2026-08", "2026-09"))

    def test_entity_row_and_fallback(self):
        import period
        rows = [{"entity": "acct-a", "source": "cot", "months": ["2025-11", "2025-12", "2026-01"]}]
        p = period.for_entity(rows, "acct-a")
        self.assertEqual((p["first"], p["prev"], p["last"]), ("2025-11", "2025-12", "2026-01"))
        self.assertEqual(p["labels"]["2026-01"], "26년 1월")
        if not os.environ.get("COT_MONTHS"):
            q = period.for_entity(rows, "acct-b", ["2026-03", "2026-02"])
            self.assertEqual(q["months"], ["2026-02", "2026-03"])
        self.assertEqual(period.ko_month("2027-03"), "2027년 3월")
        lab = period.labels(["2026-04", "2026-05", "2026-06"])
        self.assertEqual(period.span(["2026-04", "2026-05", "2026-06"], lab), "4~6월")
        self.assertEqual(period.row("acct-a", ["2026-02", "2026-01"]),
                         {"entity": "acct-a", "source": "cot", "months": ["2026-01", "2026-02"], "first": "2026-01",
                          "prev": "2026-01", "last": "2026-02", "labels": {"2026-01": "1월", "2026-02": "2월"}})


class Sessions(unittest.TestCase):
    def test_generic_outputs(self):
        import sessions
        with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as d:
            src = os.path.join(d, "dashboard_data.json")
            jdump({"months": [], "entities": {}}, src, "w")
            sessions.main(src, d)
            self.assertEqual(jload(os.path.join(d, "focus.json")), [])
            wn = jload(os.path.join(d, "whats_new.json"), encoding="utf-8")
            self.assertEqual(len(wn), len(sessions.WN))
            self.assertFalse(os.path.exists(os.path.join(d, "sessions.json")))


if __name__ == "__main__":
    unittest.main()
