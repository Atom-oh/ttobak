"""agents.py area digests: entity-scoped rows and the commitments window. Synthetic data only."""
import json, os, random, sys, tempfile, threading, time, unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))
import agents  # noqa: E402
from agent_fixtures import A, B, cur, ds  # noqa: E402,F401 (shared datasets, moved from test_agents.py)


class Digest(unittest.TestCase):
    def test_entity_only_and_keys(self):
        D = ds()
        for area, keys in agents.AREA_KEYS.items():
            dg = agents.digest(D, A, area)
            self.assertEqual(set(dg) - {"entity", "entity_ko", "area", "briefing", "months"}, set(keys), area)
            blob = json.dumps(dg, ensure_ascii=False)
            self.assertNotIn(B, blob, area)
            self.assertNotIn("누리증권", blob, area)
            self.assertEqual([b["rank"] for b in dg["briefing"]], [1, 2])

    def test_commitments_window(self):
        dg = agents.digest(ds(), A, "commitments")
        self.assertTrue(all(r["days_left"] <= 365 for r in dg["commitments"]))


if __name__ == "__main__":
    unittest.main()
