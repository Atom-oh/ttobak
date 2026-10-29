"""Opt-in smoke (WORKER_SMOKE=1): the worker on the real COT runner and, where duckdb imports under -I, the real CUR
pipeline on a synthetic CUR zip."""
import datetime, io, json, os, re, shutil, signal, subprocess, sys, tempfile, threading, time, unittest, uuid
from unittest import mock

from worker_fakes import *  # noqa: F401,F403 (fakes, constants, Base; also puts worker/ on sys.path)
from worker_fakes import W, _split_top  # noqa: E402,F401


@unittest.skipUnless(os.environ.get("WORKER_SMOKE") == "1", "set WORKER_SMOKE=1 to run the real pipelines")
class TestWorkerSmoke(Base):
    def test_real_cot_runner_rejects_non_zip(self):
        """Real run_account.sh through the worker on a synthetic non-zip upload (a valid tiny COT zip needs the full
        five-file template set, which no fixture builds)."""
        self.job()
        self.store["accounts/acct-a/uploads/%s/source.zip" % UP] = b"not a zip at all"
        ddb = self.ddb()
        W.KILLPG = self._killpg
        rc = self.run_main(ddb, subprocess.Popen)
        self.assertEqual(rc, 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "zip 형식이 아닙니다")
        self.assertEqual(up["steps"]["validate"], "failed")
        self.assertEqual(os.listdir(self.work_root), [])

    def test_real_cur_pipeline(self):
        """Real stage_cur/datasets/check through the worker on a synthetic CUR zip; agents and report are faked.
        Needs duckdb importable under python3 -I (true in the worker image, not with a user-site install)."""
        if subprocess.run(["python3", "-I", "-c", "import duckdb"], capture_output=True).returncode != 0:
            self.skipTest("duckdb not importable under python3 -I")
        sys.path.insert(0, HERE)
        import test_cur as C
        rows = [C.row(line_item_usage_start_date="2026-0%d-03 00:00:00" % m, line_item_unblended_cost=2.0) for m in (7, 8)]
        pq = C.write_file(os.path.join(self.tmp, "cur.parquet"), rows, C.CUR2)
        zp = C.make_zip(os.path.join(self.tmp, "cur.zip"), {"export/data/cur-00001.snappy.parquet": pq})
        self.job(src="cur")
        self.store["accounts/acct-a/uploads/%s/source.zip" % UP] = rd(zp, "rb")
        ddb = self.ddb(src="cur")
        fake = Runner()
        W.KILLPG = self._killpg

        def runner(cmd, **kw):
            return fake(cmd, **kw) if cmd[2] == W.AI_STEP else subprocess.Popen(cmd, **kw)
        self.assertEqual(self.run_main(ddb, runner), 0, self.up(ddb).get("reason"))
        up = self.up(ddb)
        self.assertEqual(up["steps"], {s: "done" for s in W.STEPS})
        self.assertEqual(up["period"]["months"], ["2026-07", "2026-08"])
        self.assertIn("accounts/acct-a/cur/%s/normalized/part-0.parquet" % VID, self.store)
        self.assertIn("accounts/acct-a/versions/%s/data/cur_summary.json" % VID, self.store)
        self.assertIn("accounts/acct-a/versions/%s/checks.json" % VID, self.store)


if __name__ == "__main__":
    unittest.main()
