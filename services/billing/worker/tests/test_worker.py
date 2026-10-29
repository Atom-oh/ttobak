"""worker/main.py: one upload job end to end with fake s3/ddb/sts and a fake Popen. No AWS calls.
WORKER_SMOKE=1 also runs the real COT runner (synthetic non-zip upload) and, where duckdb imports under -I, the real
CUR pipeline on a synthetic CUR zip."""
import datetime, io, json, os, re, shutil, signal, subprocess, sys, tempfile, threading, time, unittest, uuid
from unittest import mock

from worker_fakes import *  # noqa: F401,F403 (fakes, constants, Base; also puts worker/ on sys.path)
from worker_fakes import W, _split_top  # noqa: E402,F401


class TestWorker(Base):
    def test_claim_records_claimed_at_in_the_claim_update(self):
        self.job()
        ddb = self.ddb()
        before = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=1)
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        claim = self.up_updates(ddb)[0]
        self.assertIn("claimedAt = :at", claim[2])
        self.assertIn("claim = :tok", claim[2])
        self.assertEqual(claim[5], "#st = :queued AND #src = :src")  # the claim condition is unchanged
        at = datetime.datetime.fromisoformat(self.up(ddb)["claimedAt"].replace("Z", "+00:00"))
        self.assertTrue(before <= at <= datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=1))
        self.assertTrue(self.up(ddb)["claimedAt"].endswith("Z"))
        self.assertEqual(self.up(ddb)["status"], "done")

    def test_bad_job_key_exits_without_writes(self):
        for key in ("jobs/../x.json", "accounts/acct-a/x.json", "jobs/a b.json", "jobs/%s.json" % ("x" * 81), "",
                    "jobs/%s.json\n" % UP, "\njobs/%s.json" % UP):
            ddb = self.ddb()
            rc = self.run_main(ddb, Runner(), env=self.env(JOB_KEY=key))
            self.assertEqual(rc, 2, repr(key))
            self.assertEqual(ddb.calls, [])
            self.assertEqual(self.task_s3.calls, [])
            self.assertEqual(self.sts.calls, [])

    def test_invalid_job_ids_exit_without_writes(self):
        for bad in ({"accountId": "a/b"}, {"uploadId": "u123"}, {"source": "xls"}, {"kind": "other"},
                    {"kind": "rerun-agents", "versionId": "v1"}, {"accountId": A + "\n"}, {"uploadId": UP + "\n"},
                    {"kind": "rerun-agents", "versionId": VID + "\n"}, {"uploadId": "u" + "٠" * 14 + "abcdef"},
                    {"accountId": 5}):
            j = {"kind": "analyze", "accountId": A, "uploadId": UP, "source": "cot"}
            j.update(bad)
            self.store["jobs/%s.json" % UP] = json.dumps(j).encode()
            ddb = self.ddb()
            self.assertEqual(self.run_main(ddb, Runner()), 2, bad)
            self.assertEqual(self.writes(ddb), [])
            self.assertEqual(self.sts.calls, [])

    def test_step_lines_need_exact_match(self):
        job = W.Job(self.env(), {"kind": "analyze", "accountId": A, "uploadId": UP, "source": "cot"},
                    None, None, None, None, None)
        seen = []
        job.set_step = lambda n, s: seen.append((n, s))
        for line in ("STEP validate done\nSTEP check done", "xSTEP validate done", "STEP validate donex"):
            job.on_step_line(line)
        self.assertEqual(seen, [])

    def test_hardens_process_at_startup(self):
        self.run_main(self.ddb(), Runner(), env=self.env(JOB_KEY="bad"))
        self.assertEqual(self.hardened, [1])

    def fake_prctl(self, fail_op):
        calls = []
        orig = W.PRCTL
        self.addCleanup(setattr, W, "PRCTL", orig)

        def prctl(op, arg):
            calls.append((op, arg))
            if op == fail_op:
                raise OSError(1, "prctl")
        W.PRCTL = lambda: prctl
        return calls

    def test_dumpable_failure_fails_closed_before_claim(self):
        calls = self.fake_prctl(W.PR_SET_DUMPABLE)
        self.job()
        ddb = self.ddb()
        log = self.capture_log(lambda: self.assertEqual(
            W.main(env=self.env(), s3=self.task_s3, ddb=ddb, sts=self.sts, runner=Runner(), s3_factory=self.factory), 2))
        self.assertEqual(calls, [(W.PR_SET_DUMPABLE, 0)])
        self.assertEqual((ddb.calls, self.task_s3.calls, self.sts.calls), ([], [], []))
        self.assertEqual(self.up(ddb)["status"], "queued")
        self.assertIn('"what": "dumpable"', log)
        self.assertFalse(W.SUBREAPER[0])

    def test_subreaper_failure_is_logged_and_the_job_runs(self):
        W.SUBREAPER[0] = False
        calls = self.fake_prctl(W.PR_SET_CHILD_SUBREAPER)
        self.job()
        ddb = self.ddb()
        log = self.capture_log(lambda: self.assertEqual(
            W.main(env=self.env(), s3=self.task_s3, ddb=ddb, sts=self.sts, runner=Runner(), s3_factory=self.factory), 0))
        self.assertEqual(calls, [(W.PR_SET_DUMPABLE, 0), (W.PR_SET_CHILD_SUBREAPER, 1)])
        self.assertIn('"what": "subreaper"', log)
        self.assertFalse(W.SUBREAPER[0], "no orphan sweep without the bit")
        self.assertEqual(self.up(ddb)["status"], "done")
        self.fake_prctl(None)
        self.assertTrue(W.harden_process())
        self.assertTrue(W.SUBREAPER[0])

    @unittest.skipUnless(sys.platform.startswith("linux"), "prctl is Linux only")
    def test_harden_process_clears_dumpable(self):
        code = ("import ctypes, sys; sys.path.insert(0, sys.argv[1]); import main; ok = main.harden_process(); "
                "print(ok, ctypes.CDLL(None).prctl(3, 0, 0, 0, 0))")
        r = subprocess.run([sys.executable, "-I", "-c", code, os.path.dirname(W.__file__)], capture_output=True, text=True,
                           timeout=60)
        self.assertEqual(r.stdout.split(), ["True", "0"], r.stderr[-300:])

    def test_worker_ignores_shared_aws_files_before_any_client_or_child(self):
        # the untrusted children share the worker's uid and could plant ~/.aws/config (endpoint_url, region,
        # credential_process): the worker's own clients must never read it, including those created later
        self.job()
        ddb = self.ddb()
        seen = []
        files = ("AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE")

        def snap(what):
            seen.append((what, tuple(os.environ.get(k) for k in files)))

        def client(name, **kw):
            snap("client:" + name)
            return {"s3": self.task_s3, "dynamodb": ddb, "sts": self.sts}[name]

        def factory(creds):
            snap("factory")
            return self.factory(creds)

        def any_child(cmd, kw):
            snap("child")
        runner = Runner(hooks={t: any_child for t in ("run_account.sh", "agents", "report")})
        with mock.patch.dict(os.environ, {"AWS_CONFIG_FILE": "/home/worker/.aws/config"}), \
                mock.patch("boto3.client", side_effect=client):
            os.environ.pop("AWS_SHARED_CREDENTIALS_FILE", None)
            rc = W.main(env=self.env(), runner=runner, s3_factory=factory, harden=lambda: True)
        self.assertEqual(rc, 0)
        self.assertEqual([w for w, _ in seen][:3], ["client:s3", "client:dynamodb", "client:sts"])
        self.assertIn("factory", [w for w, _ in seen])
        self.assertIn("child", [w for w, _ in seen])
        for what, vals in seen:
            self.assertEqual(vals, (os.devnull, os.devnull), what)

    def test_image_env_ignores_shared_aws_files(self):
        with open(os.path.join(os.path.dirname(os.path.dirname(W.__file__)), "Dockerfile.ingest"), encoding="utf-8") as f:
            lines = f.read().replace("\\\n", " ").splitlines()  # join continuation lines
        env = " ".join(l for l in lines if l.startswith("ENV ")).split()
        self.assertIn("AWS_CONFIG_FILE=/dev/null", env)
        self.assertIn("AWS_SHARED_CREDENTIALS_FILE=/dev/null", env)

    def test_session_policy_scoped_to_account(self):
        self.job()
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertTrue(self.sts.calls)
        call = self.sts.calls[0]
        self.assertEqual(call["RoleArn"], ROLE)
        self.assertEqual(call["DurationSeconds"], min(43200, 3600 + 600))
        pol = json.loads(call["Policy"])
        res = []
        for st in pol["Statement"]:
            self.assertEqual(st["Effect"], "Allow")
            r = st["Resource"]
            res += r if isinstance(r, list) else [r]
        self.assertEqual(sorted(res), sorted(["arn:aws:s3:::%s/accounts/acct-a/*" % BUCKET, "arn:aws:s3:::%s" % BUCKET,
                                              "arn:aws:s3:::%s/data/whats_new_feed.json" % BUCKET]))
        by_res = {json.dumps(st["Resource"]): st for st in pol["Statement"]}
        lst = by_res[json.dumps("arn:aws:s3:::%s" % BUCKET)]
        self.assertEqual(lst["Action"], "s3:ListBucket")
        self.assertEqual(lst["Condition"], {"StringLike": {"s3:prefix": "accounts/acct-a/*"}})
        self.assertEqual(sorted(by_res[json.dumps("arn:aws:s3:::%s/accounts/acct-a/*" % BUCKET)]["Action"]),
                         ["s3:AbortMultipartUpload", "s3:GetObject", "s3:PutObject"])
        # the multipart cleanup action only on the Account prefix PutObject has
        for st in pol["Statement"]:
            if "s3:AbortMultipartUpload" in st["Action"]:
                self.assertEqual(st["Resource"], "arn:aws:s3:::%s/accounts/acct-a/*" % BUCKET)
                self.assertNotIn("Condition", st)
        self.assertEqual(len(pol["Statement"]), 3)
        self.assertNotIn("ListMultipartUploadParts", call["Policy"])
        self.assertEqual(by_res[json.dumps("arn:aws:s3:::%s/data/whats_new_feed.json" % BUCKET)]["Action"], "s3:GetObject")
        self.assertNotIn("acct-b", call["Policy"])
        # every S3 call after the job read goes through the scoped session
        self.assertEqual(self.task_s3.calls, [("get", "jobs/%s.json" % UP)])
        self.assertEqual(self.creds[0]["AccessKeyId"], "AK")

    def test_duration_capped_at_12h(self):
        self.job()
        self.run_main(self.ddb(), Runner(), env=self.env(JOB_TIMEOUT_S="90000"))
        self.assertEqual(self.sts.calls[0]["DurationSeconds"], 43200)

    def test_chained_role_falls_back_to_one_hour(self):
        self.job()
        sts = self.sts
        orig = sts.assume_role

        def assume(**kw):
            if kw["DurationSeconds"] > 3600:
                sts.calls.append(kw)
                raise FakeClientError("ValidationError")
            return orig(**kw)
        sts.assume_role = assume
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual([c["DurationSeconds"] for c in sts.calls[:2]], [4200, 3600])
        self.assertEqual(self.up(ddb)["status"], "done")

    def test_success_publishes_and_releases_lock(self):
        self.job()
        ddb = self.ddb()
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        base = "accounts/acct-a/versions/%s/" % VID
        for k in ("data/kpis.json", "data/period.json", "data/agent_notes.json", "data/sessions.json", "data/market.json",
                  "checks.json", "report.md"):
            self.assertIn(base + k, self.store, k)
        self.assertNotIn(base + "data/checks.json", self.store)
        self.assertNotIn(base + "data/whats_new_feed.json", self.store)
        up = self.up(ddb)
        self.assertEqual(up["status"], "done")
        self.assertIs(up["published"], True)
        self.assertEqual(up["versionId"], VID)
        self.assertEqual(up["ai"], "ok")
        self.assertEqual(up["steps"], {s: "done" for s in W.STEPS})
        self.assertEqual(up["period"]["last"], "2026-06")
        meta = ddb.py(("ACCT#acct-a", "META"))
        self.assertEqual(meta["current_cot"], VID)
        self.assertEqual(meta["ai_cot"], "ok")
        self.assertEqual(meta["period_cot"], {"first": "2026-05", "last": "2026-06", "months": ["2026-05", "2026-06"]})
        self.assertEqual(meta["name"], "한빛금융")
        self.assertEqual(meta["parentId"], "root")
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
        mu = self.meta_updates(ddb)
        self.assertEqual(len(mu), 1)
        self.assertIn("attribute_exists(pk)", mu[0][5])
        self.assertIn("current_cot <= :v", mu[0][5])
        self.assertIn("if_not_exists", mu[0][2])
        dl = [c for c in ddb.calls if c[0] == "delete"]
        self.assertEqual(len(dl), 1)
        self.assertIn("uploadId", dl[0][2])
        self.assertEqual(dl[0][3][":u"], {"S": UP})
        ra = runner.cmds[0]
        self.assertEqual(ra[0], "bash")
        zip_path, work, out, acct, names = ra[2:7]
        self.assertTrue(os.path.relpath(out, work).startswith(".."))
        self.assertEqual(os.path.basename(zip_path), "source.zip")
        self.assertEqual(acct, A)
        self.assertEqual(os.listdir(self.work_root), [])  # workspace removed
        self.assertEqual(runner.cmds[1][:4], ["python3", "-I", W.AI_STEP, "agents"])
        self.assertEqual(runner.cmds[1][5], "cot")
        self.assertNotEqual(runner.cmds[1][6], "-")  # feed downloaded
        self.assertEqual(runner.cmds[2][:4], ["python3", "-I", W.AI_STEP, "report"])
        self.assertEqual(runner.cmds[2][6], "https://dash.example.test")
        for kw in runner.kw:
            self.assertIs(kw["start_new_session"], True)
            self.assertEqual(kw["errors"], "replace")
            self.assertEqual(kw["encoding"], "utf-8")
            self.assertEqual(kw["env"]["PYTHONNOUSERSITE"], "1")
        # every child's group is killed after it exits, also on success
        self.assertEqual(sorted(pid for pid, _ in KILLS), sorted(p.pid for p in runner.procs))
        tx = [c for c in ddb.calls if c[0] == "transact"]
        self.assertEqual(tx, [("transact", [("ACCT#acct-a", "META"), ("ACCT#acct-a", "UP#" + UP)])])

    def test_claim_token_conditions_every_later_up_write(self):
        self.job()
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        ups = self.up_updates(ddb)
        claim = ups[0]
        self.assertIn("claim = :tok", claim[2])
        self.assertEqual(claim[5], "#st = :queued AND #src = :src")
        tok = claim[3][":tok"]
        for c in ups[1:]:
            self.assertEqual(c[5], "claim = :tok AND #st = :running", c[2])
            self.assertEqual(c[3][":tok"], tok)
        self.assertIn("published", ups[-1][2])

    def test_duplicate_delivery_exits_without_touching_status(self):
        self.job()
        ddb = self.ddb(status="running")
        before = self.up(ddb)
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(runner.cmds, [])
        self.assertEqual(self.sts.calls, [])
        self.assertEqual(self.up(ddb), before)
        self.assertEqual(len(self.writes(ddb)), 1)  # the refused claim only
        self.assertIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_discard_during_run_is_not_overwritten(self):
        self.job()
        ddb = self.ddb()

        def discard(cmd, kw):
            ddb.items[("ACCT#acct-a", "UP#" + UP)]["status"] = {"S": "discarded"}
        self.assertEqual(self.run_main(ddb, Runner(hooks={"report": discard})), 0)
        up = self.up(ddb)
        self.assertEqual(up["status"], "discarded")
        self.assertNotIn("published", up)
        self.assertEqual(self.meta_updates(ddb), [])
        self.assertNotIn("current_cot", ddb.py(("ACCT#acct-a", "META")))

    def test_final_up_update_conditioned(self):
        self.job()
        ddb = self.ddb()
        orig = ddb.transact_write_items

        def transact(**kw):  # discarded between the last step write and the publish transaction
            ddb.items[("ACCT#acct-a", "UP#" + UP)]["status"] = {"S": "discarded"}
            return orig(**kw)
        ddb.transact_write_items = transact
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(self.up(ddb)["status"], "discarded")
        self.assertNotIn("published", self.up(ddb))
        meta = ddb.py(("ACCT#acct-a", "META"))
        self.assertNotIn("current_cot", meta)  # the transaction never let META point at the discarded version
        self.assertNotIn("ai_cot", meta)
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_meta_update_idempotent_for_same_version(self):
        self.job()
        ddb = self.ddb(meta=meta_item(current_cot=VID))
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(self.up(ddb)["status"], "done")

    def test_newer_version_not_replaced(self):
        self.job()
        newer = "v20991231000000ffffff"
        ddb = self.ddb(meta=meta_item(current_cot=newer))
        self.assertEqual(self.run_main(ddb, Runner()), 1)
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["current_cot"], newer)
        up = self.up(ddb)
        self.assertEqual(up["reason"], "내부 오류: StaleVersion")
        self.assertEqual(up["steps"]["publish"], "failed")
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_missing_meta_fails_missing_account(self):
        self.job()
        ddb = self.ddb(meta=False)
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "내부 오류: MissingAccount")
        self.assertEqual(up["steps"]["validate"], "failed")
        self.assertEqual(runner.cmds, [])
        self.assertNotIn(("ACCT#acct-a", "META"), ddb.items)

    def test_meta_removed_before_publish_fails_missing_account(self):
        self.job()
        ddb = self.ddb()

        def drop(cmd, kw):
            ddb.items.pop(("ACCT#acct-a", "META"))
        self.assertEqual(self.run_main(ddb, Runner(hooks={"report": drop})), 1)
        self.assertEqual(self.up(ddb)["reason"], "내부 오류: MissingAccount")
        self.assertNotIn(("ACCT#acct-a", "META"), ddb.items)

    def test_untrusted_children_get_no_aws_env_and_own_home(self):
        self.job()
        ddb = self.ddb()
        seen = {}
        outside = os.path.join(self.tmp, "outside")
        os.makedirs(outside)

        def pipeline(cmd, kw):
            e = kw["env"]
            seen["pipe_aws"] = [k for k in e if k.startswith("AWS_")]
            seen["home"] = e["HOME"]
            seen["home_empty"] = os.path.isdir(e["HOME"]) and os.listdir(e["HOME"]) == []
            seen["cache_before"] = os.path.lexists(os.path.join(kw["cwd"], "agent_cache"))
            os.symlink(outside, os.path.join(kw["cwd"], "agent_cache"))  # planted by a hostile upload
            os.symlink(outside, os.path.join(kw["cwd"], "report.md"))
            os.symlink(outside, os.path.join(kw["cwd"], "agents_home"))

        def agents(cmd, kw):
            seen["agents_aws"] = sorted(k for k in kw["env"] if k.startswith("AWS_"))
            seen["agents_home"] = kw["env"]["HOME"]
            seen["agents_home_empty"] = os.path.isdir(kw["env"]["HOME"]) and os.listdir(kw["env"]["HOME"]) == []
            seen["agents_config"] = (kw["env"]["AWS_CONFIG_FILE"], kw["env"]["AWS_SHARED_CREDENTIALS_FILE"])
            c = cmd[7]
            seen["cache_real"] = os.path.isdir(c) and not os.path.islink(c) and os.listdir(c) == []

        def report(cmd, kw):
            seen["report_aws"] = [k for k in kw["env"] if k.startswith("AWS_")]
            seen["report_md_link"] = os.path.lexists(cmd[5])
        fake_env = {"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/creds/x", "AWS_ACCESS_KEY_ID": "AK",
                    "AWS_REGION": "ap-northeast-2", "HOME": "/home/parent"}
        with mock.patch.dict(os.environ, fake_env):
            rc = self.run_main(ddb, Runner(hooks={"run_account.sh": pipeline, "agents": agents, "report": report}))
        self.assertEqual(rc, 0)
        self.assertEqual(seen["pipe_aws"], [])
        self.assertNotEqual(seen["home"], "/home/parent")
        self.assertTrue(seen["home"].startswith(self.work_root))
        self.assertTrue(seen["home_empty"])
        self.assertFalse(seen["cache_before"])
        self.assertLessEqual({"AWS_ACCESS_KEY_ID", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_REGION"}, set(seen["agents_aws"]))
        self.assertTrue(seen["cache_real"])
        self.assertNotIn(seen["agents_home"], ("/home/parent", seen["home"]))
        self.assertTrue(seen["agents_home"].startswith(self.work_root))
        self.assertTrue(seen["agents_home_empty"])
        self.assertEqual(seen["agents_config"], (os.devnull, os.devnull))
        self.assertEqual(seen["report_aws"], [])
        self.assertFalse(seen["report_md_link"])
        self.assertEqual(os.listdir(outside), [])

    def test_cur_children_get_no_aws_env(self):
        self.job(src="cur")
        ddb = self.ddb(src="cur")
        runner = Runner()
        with mock.patch.dict(os.environ, {"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://x", "AWS_SESSION_TOKEN": "t"}):
            self.assertEqual(self.run_main(ddb, runner), 0)
        for cmd, kw in zip(runner.cmds, runner.kw):
            aws = [k for k in kw["env"] if k.startswith("AWS_")]
            if Runner.tag(cmd) == "agents":
                self.assertTrue(aws)
            else:
                self.assertEqual(aws, [], cmd)

    def test_step_states_written_live(self):
        self.job()
        ddb = self.ddb()
        at_extract = []

        def hook(line, proc):
            # the child is still running (about to print its next line): the earlier lines must already be recorded
            if line == "STEP extract running":
                at_extract.append(wait_for(lambda: self.up(ddb)["steps"]["validate"] == "done"))
        self.assertEqual(self.run_main(ddb, Runner(line_hook=hook)), 0)
        self.assertEqual(at_extract, [True])

    def test_puts_happen_before_meta_update(self):
        self.job()
        order = []
        ddb = self.ddb()
        put, tx = self.data_s3.put_object, ddb.transact_write_items
        self.data_s3.put_object = lambda **kw: (order.append("put"), put(**kw))[1]
        ddb.transact_write_items = lambda **kw: (order.append("meta"), tx(**kw))[1]
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertLess(max(i for i, x in enumerate(order) if x == "put"), order.index("meta"))

    def test_names_json_falls_back_to_account_id(self):
        self.job()
        captured = {}

        def spy(cmd, kw):
            captured.update(jload(cmd[6], encoding="utf-8"))
        ddb = self.ddb(meta=meta_item(name=None))
        self.assertEqual(self.run_main(ddb, Runner(hooks={"run_account.sh": spy})), 0)
        self.assertEqual(captured, {A: A})

    def test_check_failure_needs_review_not_published(self):
        self.job()
        ddb = self.ddb(meta=meta_item(current_cot="v20260101000000aaaaaa"))
        self.assertEqual(self.run_main(ddb, Runner(cot_rc=1)), 0)
        self.assertFalse([k for k in self.store if "/versions/" in k])
        self.assertEqual(self.meta_updates(ddb), [])
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["current_cot"], "v20260101000000aaaaaa")
        up = self.up(ddb)
        self.assertEqual(up["status"], "needs_review")
        self.assertEqual(up["steps"]["check"], "needs_review")
        for s in ("agents", "report", "publish"):
            self.assertEqual(up["steps"][s], "waiting")
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_invalid_zip_failed_with_reason(self):
        self.job()
        ddb = self.ddb()
        reason = "zip 형식이 아닙니다" + "가" * 400
        runner = Runner(cot_rc=3, cot_lines=["STEP validate running", "STEP validate failed"], reason=reason)
        self.assertEqual(self.run_main(ddb, runner), 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], reason[:300])
        self.assertEqual(up["steps"]["validate"], "failed")
        for s in W.STEPS[1:]:
            self.assertEqual(up["steps"][s], "waiting")
        self.assertEqual(self.meta_updates(ddb), [])
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
        self.assertEqual(len(runner.cmds), 1)
        self.assertEqual(os.listdir(self.work_root), [])

    def oversized(self, src, limit):
        self.job(src=src)
        key = "accounts/acct-a/uploads/%s/source.zip" % UP
        ddb = self.ddb(src=src)
        self.data_s3.sizes[key] = limit + 1
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 1)
        up = self.up(ddb)
        self.assertEqual((up["status"], up["reason"]), ("failed", "업로드 크기가 너무 큽니다"))
        self.assertEqual(up["steps"]["validate"], "failed")
        self.assertIn(("head", key), self.data_s3.calls)
        self.assertNotIn(("get", key), self.data_s3.calls, "never downloaded")
        self.assertEqual(runner.cmds, [])
        self.assertNotIn(("ACCT#acct-a", "LOCK#" + src), ddb.items)

    def test_oversized_cot_fails_before_download(self):
        self.oversized("cot", 209715200)

    def test_oversized_cur_fails_before_download(self):
        self.oversized("cur", 5368709120)

    def test_source_at_limit_is_downloaded(self):
        self.job()
        key = "accounts/acct-a/uploads/%s/source.zip" % UP
        ddb = self.ddb()
        self.data_s3.sizes[key] = 209715200
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertIn(("get", key), self.data_s3.calls)

    def test_agents_none_publishes_with_ai_none(self):
        self.job()
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner(ai="none")), 0)
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")
        up = self.up(ddb)
        self.assertEqual(up["status"], "done")
        self.assertEqual(up["ai"], "none")
        self.assertEqual(up["steps"]["agents"], "done")

    def test_job_for_other_account_cannot_write_outside_prefix(self):
        self.job(acct=A)
        self.store["accounts/acct-b/uploads/%s/source.zip" % UP] = b"other"
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        for op, key in self.data_s3.calls:
            self.assertTrue(key.startswith("accounts/acct-a/") or (op == "get" and key == "data/whats_new_feed.json"), key)
        self.assertTrue(any(op == "put" for op, _ in self.data_s3.calls))
        for c in ddb.calls:
            for k in (c[1] if c[0] == "transact" else [c[1]]):
                self.assertEqual(k[0], "ACCT#acct-a")
        self.assertFalse([k for k in self.store if k.startswith("accounts/acct-b/versions")])

    def test_exception_reason_is_class_name_only(self):
        self.job()
        ddb = self.ddb()
        self.assertEqual(self.run_main(ddb, Runner(raise_on="agents")), 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "내부 오류: KeyError")
        self.assertEqual(up["steps"]["agents"], "failed")
        self.assertEqual(up["steps"]["report"], "waiting")
        self.assertEqual(up["steps"]["publish"], "waiting")
        self.assertFalse([k for k in self.store if "/versions/" in k])
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
        self.assertEqual(os.listdir(self.work_root), [])

    def test_pipeline_crash_is_internal_error(self):
        self.job()
        ddb = self.ddb()
        lines = ["STEP validate running", "STEP validate done", "STEP extract running", "STEP extract failed"]
        self.assertEqual(self.run_main(ddb, Runner(cot_rc=4, cot_lines=lines)), 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "내부 오류: PipelineError")
        self.assertEqual(up["steps"]["validate"], "done")
        self.assertEqual(up["steps"]["extract"], "failed")
        self.assertEqual(up["steps"]["datasets"], "waiting")

    def test_timeout_marks_step_failed_and_kills_group(self):
        self.job()
        ddb = self.ddb()
        runner = Runner(block_on="report")
        self.assertEqual(self.run_main(ddb, runner, env=self.env(JOB_TIMEOUT_S="1")), 1)
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "처리 시간 초과")
        self.assertEqual(up["steps"]["agents"], "done")
        self.assertEqual(up["steps"]["report"], "failed")
        self.assertEqual(up["steps"]["publish"], "waiting")
        self.assertIn((runner.procs[-1].pid, signal.SIGKILL), KILLS)
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_timeout_in_streamed_pipeline_keeps_live_states(self):
        self.job()
        ddb = self.ddb()
        runner = Runner(block_on="run_account.sh", cot_lines=["STEP validate running", "STEP validate done",
                                                              "STEP extract running"])
        self.assertEqual(self.run_main(ddb, runner, env=self.env(JOB_TIMEOUT_S="1")), 1)
        up = self.up(ddb)
        self.assertEqual(up["reason"], "처리 시간 초과")
        self.assertEqual(up["steps"]["validate"], "done")
        self.assertEqual(up["steps"]["extract"], "failed")
        self.assertEqual(up["steps"]["datasets"], "waiting")
        self.assertEqual(KILLS, [(runner.procs[0].pid, signal.SIGKILL)])

    def test_expired_budget_fails_without_running(self):
        self.job()
        ddb = self.ddb()
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner, env=self.env(JOB_TIMEOUT_S="0")), 1)
        self.assertEqual(runner.cmds, [])
        self.assertEqual(self.up(ddb)["reason"], "처리 시간 초과")

    def test_sigterm_fails_job_and_kills_group(self):
        self.job()
        ddb = self.ddb()

        def hook(line, proc):
            if line == "STEP extract running":
                wait_for(lambda: self.up(ddb)["steps"]["validate"] == "done")
                proc.block = True
                os.kill(os.getpid(), signal.SIGTERM)
        before = signal.getsignal(signal.SIGTERM)
        runner = Runner(line_hook=hook)
        rc = self.run_main(ddb, runner)
        self.assertEqual(rc, 1)
        self.assertIs(signal.getsignal(signal.SIGTERM), before)  # restored
        up = self.up(ddb)
        self.assertEqual(up["status"], "failed")
        self.assertEqual(up["reason"], "처리 중단")
        self.assertEqual(up["steps"]["validate"], "done")
        self.assertEqual(up["steps"]["extract"], "failed")
        self.assertEqual(KILLS, [(runner.procs[0].pid, signal.SIGKILL)])
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
        self.assertEqual(os.listdir(self.work_root), [])

    def test_lock_not_ours_kept(self):
        self.job()
        other = "u20261020999999ffffff"
        ddb = self.ddb(lock_owner=other)
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertEqual(ddb.py(("ACCT#acct-a", "LOCK#cot"))["uploadId"], other)

    def test_discarded_upload_not_processed(self):
        self.job()
        ddb = self.ddb(status="discarded")
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(runner.cmds, [])
        self.assertEqual(self.sts.calls, [])
        self.assertIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
        self.assertEqual(self.up(ddb)["status"], "discarded")

    def test_cur_success_uploads_parquet_and_uses_prev(self):
        prev = "v20260901000000aaaaaa"
        cot = "v20260902000000bbbbbb"
        self.job(src="cur")
        self.store["accounts/acct-a/cur/%s/normalized/part-0.parquet" % prev] = b"PAR1old"
        self.store["accounts/acct-a/versions/%s/data/spend_monthly.json" % cot] = b"[]"
        ddb = self.ddb(src="cur", meta=meta_item(current_cur=prev, current_cot=cot))
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual([Runner.tag(c) for c in runner.cmds], ["stage_cur.py", "datasets.py", "check.py", "agents", "report"])
        stage = runner.cmds[0]
        self.assertEqual(stage[:2], ["python3", "-I"])
        self.assertEqual(len(stage), 7)
        self.assertTrue(stage[6].endswith("prev"))
        self.assertEqual(len(runner.cmds[1]), 7)  # summary.json passed
        self.assertTrue(runner.cmds[2][6].endswith("cot_spend.json"))
        self.assertEqual(runner.cmds[3][5], "cur")
        self.assertEqual(runner.cmds[3][6], "-")  # no feed for CUR
        self.assertEqual(self.store["accounts/acct-a/cur/%s/normalized/part-0.parquet" % VID], b"PAR1new")
        # the Parquet goes through the managed (multipart) transfer, the small JSON files through put_object
        pq = "accounts/acct-a/cur/%s/normalized/part-0.parquet" % VID
        self.assertEqual([t[0] for t in self.data_s3.transfers], [pq])
        key, extra, cfg = self.data_s3.transfers[0]
        self.assertEqual(extra, {"ContentType": "application/vnd.apache.parquet"})
        from boto3.s3.transfer import TransferConfig
        self.assertIsInstance(cfg, TransferConfig)
        self.assertEqual((cfg.multipart_threshold, cfg.multipart_chunksize), (W.MULTIPART_BYTES, W.MULTIPART_BYTES))
        self.assertNotIn(("put", pq), self.data_s3.calls)
        self.assertIn(("put", "accounts/acct-a/versions/%s/data/cur_summary.json" % VID), self.data_s3.calls)
        self.assertIn("accounts/acct-a/versions/%s/data/cur_summary.json" % VID, self.store)
        self.assertIn("accounts/acct-a/versions/%s/checks.json" % VID, self.store)
        meta = ddb.py(("ACCT#acct-a", "META"))
        self.assertEqual(meta["current_cur"], VID)
        self.assertEqual(meta["current_cot"], cot)
        self.assertEqual(meta["period_cur"]["months"], ["2026-06"])
        self.assertEqual(self.up(ddb)["steps"], {s: "done" for s in W.STEPS})
        self.assertNotIn(("ACCT#acct-a", "LOCK#cur"), ddb.items)
        self.assertNotIn("data/whats_new_feed.json", [k for _, k in self.data_s3.calls])

    def test_cur_without_prev_or_cot(self):
        self.job(src="cur")
        ddb = self.ddb(src="cur")
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(len(runner.cmds[0]), 6)
        self.assertEqual(len(runner.cmds[2]), 6)

    def test_cur_publish_requires_the_predecessor_it_consumed(self):
        # a concurrent CUR run (e.g. after the source lock expired) published an older-id version mid-run: this run
        # merged months onto a predecessor that is no longer current, so it must not publish over the other one
        prev, other = "v20260901000000aaaaaa", "v20261001000000cccccc"
        self.assertLess(other, VID)  # the version-order check alone would let this run replace it
        for before in (prev, None):
            with self.subTest(before=before):
                self.job(src="cur")
                if before:
                    self.store["accounts/acct-a/cur/%s/normalized/part-0.parquet" % before] = b"PAR1old"
                ddb = self.ddb(src="cur", meta=meta_item(**({"current_cur": before} if before else {})))

                def race(cmd, kw):
                    ddb.items[("ACCT#acct-a", "META")]["current_cur"] = {"S": other}
                self.assertEqual(self.run_main(ddb, Runner(hooks={"report": race})), 1)
                meta = ddb.py(("ACCT#acct-a", "META"))
                self.assertEqual(meta["current_cur"], other)
                self.assertNotIn("period_cur", meta)
                self.assertNotIn("ai_cur", meta)
                up = self.up(ddb)
                self.assertEqual(up["status"], "failed")
                self.assertEqual(up["reason"], "내부 오류: StaleVersion")
                self.assertEqual(up["steps"]["publish"], "failed")
                self.assertNotIn("published", up)
                self.assertNotIn(("ACCT#acct-a", "LOCK#cur"), ddb.items)

    def test_cur_publish_condition_names_the_predecessor(self):
        prev = "v20260901000000aaaaaa"
        self.job(src="cur")
        ddb = self.ddb(src="cur", meta=meta_item(current_cur=prev))
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        meta_tx = [c for c in self.meta_updates(ddb) if c[0] == "tx-update"]
        self.assertEqual(len(meta_tx), 1)
        self.assertIn("current_cur = :prev", meta_tx[0][5])
        self.assertEqual(meta_tx[0][3][":prev"], {"S": prev})
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["current_cur"], VID)

    def test_cur_check_failure_needs_review(self):
        self.job(src="cur")
        ddb = self.ddb(src="cur")
        self.assertEqual(self.run_main(ddb, Runner(cur_check_rc=1)), 0)
        up = self.up(ddb)
        self.assertEqual(up["status"], "needs_review")
        self.assertEqual(up["steps"]["check"], "needs_review")
        self.assertEqual(up["steps"]["datasets"], "done")
        self.assertEqual(up["steps"]["agents"], "waiting")
        self.assertFalse([k for k in self.store if "/versions/" in k or "/cur/" in k])

    def test_rerun_updates_ai_only_when_current(self):
        for current, updated in ((VID, True), ("v20991231000000ffffff", False)):
            self.store.clear()
            self.task_s3.calls.clear()
            self.data_s3.calls.clear()
            self.job(kind="rerun-agents", versionId=VID)
            base = "accounts/acct-a/versions/%s/" % VID
            self.store[base + "data/kpis.json"] = b"[]"
            self.store[base + "data/agent_notes.json"] = b"[]"
            ddb = self.ddb(meta=meta_item(current_cot=current, ai_cot="none"))
            before = ddb.py(("ACCT#acct-a", "UP#" + UP))
            runner = Runner(ai="ok")
            self.assertEqual(self.run_main(ddb, runner), 0)
            self.assertEqual([Runner.tag(c) for c in runner.cmds], ["agents", "report"])
            for k in ("agent_notes.json", "sessions.json", "market.json"):
                self.assertIn(base + "data/" + k, self.store)
            self.assertEqual(self.store[base + "report.md"], b"# report\n")
            puts = [k for op, k in self.data_s3.calls if op == "put"]
            self.assertEqual(sorted(puts), sorted([base + "data/agent_notes.json", base + "data/sessions.json",
                                                   base + "data/market.json", base + "report.md"]))
            ups = [c for c in self.writes(ddb) if not c[1][1].startswith("RERUN#")]
            self.assertEqual(len(ups), 2)
            # M4: the version's own UP row gets the fresh ai (rollback copies it), only while that row exists
            self.assertEqual(ups[0][1], ("ACCT#acct-a", "UP#" + UP))
            self.assertEqual(ups[0][2], "SET ai = :a REMOVE rerunError, rerunFailedAt")
            self.assertEqual(ups[0][5], "attribute_exists(pk)")
            self.assertEqual(ups[1][1], ("ACCT#acct-a", "META"))
            self.assertEqual(ups[1][5], "current_cot = :v")
            self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok" if updated else "none")
            after = ddb.py(("ACCT#acct-a", "UP#" + UP))
            self.assertEqual(after["ai"], "ok")
            self.assertEqual({k: v for k, v in after.items() if k != "ai"}, {k: v for k, v in before.items() if k != "ai"})
            self.assertIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
            self.assertNotIn(LEASE, ddb.items, "lease released")
            for op, key in self.data_s3.calls:
                self.assertTrue(key.startswith("accounts/acct-a/") or key == "data/whats_new_feed.json", key)

    def test_rerun_without_up_row_still_updates_meta(self):
        self.job(kind="rerun-agents", versionId=VID)
        base = "accounts/acct-a/versions/%s/" % VID
        self.store[base + "data/kpis.json"] = b"[]"
        ddb = self.ddb(meta=meta_item(current_cot=VID, ai_cot="none"))
        del ddb.items[("ACCT#acct-a", "UP#" + UP)]
        self.assertEqual(self.run_main(ddb, Runner(ai="ok")), 0)
        self.assertNotIn(("ACCT#acct-a", "UP#" + UP), ddb.items, "no row created")
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok")

    # ---------- rerun lease ----------

    def rerun_setup(self, lease=None):
        self.job(kind="rerun-agents", versionId=VID)
        base = "accounts/acct-a/versions/%s/" % VID
        self.store[base + "data/kpis.json"] = b"[]"
        ddb = self.ddb(status="done", meta=meta_item(current_cot=VID, ai_cot="none"))
        if lease is not None:
            ddb.items[LEASE] = lease
        return ddb, base

    def test_rerun_while_lease_live_writes_nothing(self):
        ddb, base = self.rerun_setup(lease_item("other", time.time() + 900))
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(runner.cmds, [])
        self.assertEqual(self.data_s3.calls, [])
        self.assertEqual(self.sts.calls, [])
        self.assertEqual(self.writes(ddb), [])
        self.assertEqual([c[0] for c in ddb.calls], ["put", "get"], "only the refused acquire and its owner check")
        self.assertEqual(ddb.calls[1][1:], (LEASE, {"ConsistentRead": True}))
        self.assertEqual(ddb.py(LEASE)["owner"], "other")
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")

    def test_expired_lease_taken_over_for_rerun_timeout_plus_grace(self):
        ddb, base = self.rerun_setup(lease_item("other", time.time() - 1))
        seen = []
        runner = Runner(hooks={"agents": lambda cmd, kw: seen.append(ddb.py(LEASE))})
        t0 = int(time.time())
        self.assertEqual(self.run_main(ddb, runner, env=self.env(RERUN_TIMEOUT_S="600")), 0)
        put = [c for c in ddb.calls if c[0] == "put"][0]
        self.assertEqual(put[3], "attribute_not_exists(pk) OR expiresAt < :now")
        self.assertNotEqual(seen[0]["owner"], "other")
        self.assertTrue(re.fullmatch(r"[0-9a-f]{32}", seen[0]["owner"]))
        self.assertTrue(t0 + 600 + W.LEASE_GRACE_S <= seen[0]["expiresAt"] <= t0 + 600 + W.LEASE_GRACE_S + 3)
        self.assertEqual(seen[0]["ttl"], seen[0]["expiresAt"])
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok")
        self.assertNotIn(LEASE, ddb.items, "released on success")
        rel = [c for c in ddb.calls if c[0] == "delete" and c[1] == LEASE]
        self.assertEqual(len(rel), 1)
        self.assertEqual(rel[0][2:], ("#o = :o", {":o": {"S": seen[0]["owner"]}}))

    def test_lease_put_committed_with_lost_response_is_ours(self):
        """The lease put commits, its response is lost and the SDK retry fails the condition: the stored owner is ours."""
        ddb, base = self.rerun_setup()
        put = ddb.put_item

        def committed_then_retried(**kw):
            r = put(**kw)
            if kw["Item"]["sk"]["S"] == LEASE[1]:
                raise FakeClientError("ConditionalCheckFailedException")
            return r
        ddb.put_item = committed_then_retried
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertTrue(runner.cmds, "the rerun ran")
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok")
        self.assertNotIn(LEASE, ddb.items, "released on success")
        reads = [c for c in ddb.calls if c[0] == "get" and c[1] == LEASE]
        self.assertEqual(reads[0][2], {"ConsistentRead": True})

    def test_lease_released_on_failure(self):
        for kw in ({"raise_on": "agents"}, {"ai": "bad"}):
            ddb, base = self.rerun_setup()
            self.assertEqual(self.run_main(ddb, Runner(**kw)), 1)
            self.assertNotIn(LEASE, ddb.items, kw)
            self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")

    def test_publish_skipped_when_lease_lost(self):
        ddb, base = self.rerun_setup()

        def steal(cmd, kw):
            ddb.items[LEASE] = lease_item("other", time.time() + 900)
        self.assertEqual(self.run_main(ddb, Runner(hooks={"report": steal})), 0)
        self.assertFalse([k for op, k in self.data_s3.calls if op == "put"])
        self.assertNotIn(base + "report.md", self.store)
        self.assertFalse([c for c in self.writes(ddb) if c[0] in ("update", "tx-update") and c[1] != LEASE])
        self.assertEqual(ddb.py(LEASE)["owner"], "other", "another rerun's lease is not released")
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")

    def test_rerun_failure_recorded_on_up_row(self):
        cases = (("internal", {"raise_on": "agents"}, {}),
                 ("timeout", {"block_on": "agents"}, {"RERUN_TIMEOUT_S": "1"}),
                 ("aborted", {"hooks": {"agents": lambda cmd, kw: os.kill(os.getpid(), signal.SIGTERM)}}, {}))
        for kind, rkw, ekw in cases:
            ddb, base = self.rerun_setup()
            before = self.up(ddb)
            t0 = int(time.time())
            self.assertEqual(self.run_main(ddb, Runner(**rkw), env=self.env(**ekw)), 1, kind)
            up = self.up(ddb)
            self.assertEqual(up["rerunError"], kind)
            self.assertTrue(t0 <= up["rerunFailedAt"] <= int(time.time()))
            self.assertEqual({k: v for k, v in up.items() if k not in ("rerunError", "rerunFailedAt")}, before)
            self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")
            self.assertNotIn(LEASE, ddb.items, kind)
            # recorded before the lease is released
            rec = [i for i, c in enumerate(ddb.calls) if c[0] in ("update", "tx-update") and "rerunError" in c[2]]
            rel = [i for i, c in enumerate(ddb.calls) if c[0] == "delete" and c[1] == LEASE]
            self.assertEqual(len(rec), 1)
            self.assertLess(rec[0], rel[0])
            # the next successful rerun clears the record
            self.assertEqual(self.run_main(ddb, Runner()), 0)
            up = self.up(ddb)
            self.assertEqual(up["ai"], "ok")
            self.assertNotIn("rerunError", up)
            self.assertNotIn("rerunFailedAt", up)

    def test_takeover_during_publish_fences_later_writes(self):
        """The lease expires mid-publish and another rerun takes the version: the older worker writes nothing more."""
        for after in ("data/agent_notes.json", "report.md"):
            self.store.clear()
            ddb, base = self.rerun_setup()
            put = self.data_s3.put_object

            def put_then_lose(Bucket, Key, Body, **kw):
                r = put(Bucket=Bucket, Key=Key, Body=Body, **kw)
                if Key == base + after:
                    ddb.items[LEASE] = lease_item("successor", time.time() + 900)
                return r
            with mock.patch.object(self.data_s3, "put_object", put_then_lose):
                self.data_s3.calls.clear()
                self.assertEqual(self.run_main(ddb, Runner()), 0, after)
            puts = [k for op, k in self.data_s3.calls if op == "put"]
            self.assertEqual(puts[-1], base + after, "no S3 overwrite after the takeover")
            self.assertNotIn("ai", self.up(ddb), after)
            self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none", after)
            self.assertEqual(ddb.py(LEASE)["owner"], "successor")
            # every publish write ran under a renewal conditioned on our unexpired lease
            renews = [c for c in ddb.calls if c[0] in ("update", "tx-update") and c[1] == LEASE]
            self.assertTrue(renews)
            for c in renews:
                self.assertEqual(c[5], "#o = :o AND expiresAt > :now")

    def test_expired_own_lease_publishes_nothing(self):
        ddb, base = self.rerun_setup()

        def expire(cmd, kw):
            ddb.items[LEASE]["expiresAt"] = {"N": str(int(time.time()) - 1)}
        self.assertEqual(self.run_main(ddb, Runner(hooks={"report": expire})), 0)
        self.assertFalse([k for op, k in self.data_s3.calls if op == "put"])
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "none")

    def test_rerun_updates_commit_with_the_lease_renewal(self):
        ddb, base = self.rerun_setup()
        t0 = int(time.time())
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        tx = [c[1] for c in ddb.calls if c[0] == "transact"]
        self.assertEqual(tx, [[LEASE, ("ACCT#acct-a", "UP#" + UP)], [LEASE, ("ACCT#acct-a", "META")]])
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok")
        renews = [c for c in ddb.calls if c[0] in ("update", "tx-update") and c[1] == LEASE]
        for c in renews:  # pushed out to at least the grace from now, never shortened
            self.assertGreaterEqual(int(c[3][":e"]["N"]), t0 + W.LEASE_GRACE_S)
            self.assertEqual(c[4], {"#o": "owner", "#t": "ttl"})

    def test_failure_not_recorded_after_takeover(self):
        ddb, base = self.rerun_setup()

        def lose(cmd, kw):
            ddb.items[LEASE] = lease_item("successor", time.time() + 900)
        self.assertEqual(self.run_main(ddb, Runner(hooks={"agents": lose}, raise_on="agents")), 1)
        self.assertNotIn("rerunError", self.up(ddb), "a successor's result is not overwritten")
        self.assertEqual(ddb.py(LEASE)["owner"], "successor")

    def test_slow_step_write_keeps_buffered_lines_after_exit(self):
        """The child exited with its last STEP lines queued; writing an earlier one outlasts the exit grace."""
        self.job()
        ddb = self.ddb()
        update = ddb.update_item

        def slow(**kw):
            if (kw.get("ExpressionAttributeValues") or {}).get(":s") == {"S": "running"} and \
                    (kw.get("ExpressionAttributeNames") or {}).get("#n") == "check":
                time.sleep(0.4)
            return update(**kw)
        ddb.update_item = slow
        lines = ["STEP %s %s" % (s, x) for s in ("validate", "extract", "datasets") for x in ("running", "done")]
        with mock.patch.object(W, "EXIT_GRACE_S", 0.2):
            self.assertEqual(self.run_main(ddb, Runner(cot_lines=lines + ["STEP check running", "STEP check done"])), 0)
        up = self.up(ddb)
        self.assertEqual(up["status"], "done")
        self.assertEqual(up["steps"]["check"], "done")

    def test_claim_committed_with_lost_response_is_ours(self):
        """The claim commits, its response is lost and the SDK retry fails the condition: the stored token is ours."""
        self.job()
        ddb = self.ddb()
        update = ddb.update_item

        def committed_then_retried(**kw):
            r = update(**kw)
            if kw["UpdateExpression"].startswith("SET #st = :running, steps"):
                raise FakeClientError("ConditionalCheckFailedException")
            return r
        ddb.update_item = committed_then_retried
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertTrue(runner.cmds)
        reads = [c for c in ddb.calls if c[0] == "get" and c[1][1] == "UP#" + UP]
        self.assertEqual(reads[0][2], {"ConsistentRead": True})

    def test_claim_held_by_another_token_skips(self):
        self.job()
        ddb = self.ddb(status="running")
        ddb.items[("ACCT#" + A, "UP#" + UP)]["claim"] = {"S": "f" * 32}
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertEqual(runner.cmds, [])
        self.assertEqual(self.up(ddb)["claim"], "f" * 32)

    def test_feed_access_denied_is_a_missing_feed(self):
        """No ListBucket on the feed key: S3 answers a missing feed with AccessDenied."""
        for code in ("AccessDenied", "NoSuchKey"):
            self.job()
            del self.store["data/whats_new_feed.json"]
            get = FakeS3.get_object

            def get_object(s3, Bucket, Key):
                if Key == W.FEED_KEY:
                    s3.calls.append(("get", Key))
                    raise FakeClientError(code)
                return get(s3, Bucket, Key)
            ddb = self.ddb()
            runner = Runner()
            with mock.patch.object(FakeS3, "get_object", get_object):
                log = self.capture_log(lambda: self.assertEqual(self.run_main(ddb, runner), 0, code))
            self.assertEqual(self.up(ddb)["status"], "done", code)
            feed = [json.loads(x) for x in log.splitlines() if '"event": "feed"' in x]
            self.assertEqual([(f["missing"], f["kind"]) for f in feed],
                             [(True, "access_denied" if code == "AccessDenied" else "not_found")])
            agents = [c for c in runner.cmds if Runner.tag(c) == "agents"][0]
            self.assertEqual(agents[6], "-", code)

    def test_feed_other_errors_still_fail(self):
        self.job()
        get = FakeS3.get_object

        def get_object(s3, Bucket, Key):
            if Key == W.FEED_KEY:
                raise FakeClientError("SlowDown")
            return get(s3, Bucket, Key)
        ddb = self.ddb()
        with mock.patch.object(FakeS3, "get_object", get_object):
            self.assertEqual(self.run_main(ddb, Runner()), 1)
        self.assertEqual(self.up(ddb)["status"], "failed")

    def test_rerun_failure_without_up_row_creates_none(self):
        ddb, base = self.rerun_setup()
        del ddb.items[("ACCT#acct-a", "UP#" + UP)]
        self.assertEqual(self.run_main(ddb, Runner(raise_on="agents")), 1)
        self.assertNotIn(("ACCT#acct-a", "UP#" + UP), ddb.items)
        self.assertNotIn(LEASE, ddb.items)

    def test_skipped_rerun_records_nothing(self):
        ddb, base = self.rerun_setup(lease_item("other", time.time() + 900))
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertNotIn("rerunError", self.up(ddb))

    def test_rerun_deadline_from_rerun_timeout(self):
        j = {"accountId": A, "uploadId": UP, "source": "cot"}
        rr = dict(j, kind="rerun-agents", versionId=VID)
        env = self.env(JOB_TIMEOUT_S="7200")
        self.assertEqual(W.Job(env, rr, None, None, None, None, None).timeout_s, 3600)
        self.assertEqual(W.Job(dict(env, RERUN_TIMEOUT_S="900"), rr, None, None, None, None, None).timeout_s, 900)
        self.assertEqual(W.Job(dict(env, RERUN_TIMEOUT_S="900"), dict(j, kind="analyze"), None, None, None, None,
                               None).timeout_s, 7200)

    # ---------- round 2 ----------

    def test_lead_exit_with_held_stdout_does_not_hold_step(self):
        self.job()
        ddb = self.ddb()
        runner = Runner(hold_on="run_account.sh")
        t0 = time.monotonic()
        self.assertEqual(self.run_main(ddb, runner), 0)
        self.assertLess(time.monotonic() - t0, 5)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertIn((runner.procs[0].pid, signal.SIGKILL), KILLS)

    def real_job(self):
        W.KILLPG = self._killpg
        j = W.Job(self.env(), {"kind": "analyze", "accountId": A, "uploadId": UP, "source": "cot"}, None, None, None,
                  subprocess.Popen, None)
        j.fresh_workspace()
        return j

    @staticmethod
    def procs_with(token):
        """pids whose command line contains token (a per-test random number)."""
        out = []
        for d in os.listdir("/proc"):
            if d.isdigit() and int(d) != os.getpid():
                try:
                    with open("/proc/%s/cmdline" % d, "rb") as f:
                        if token.encode() in f.read():
                            out.append(int(d))
                except OSError:
                    pass
        return out

    @staticmethod
    def token():
        return "30.%d" % (uuid.uuid4().int % 10 ** 9)

    @unittest.skipUnless(sys.platform.startswith("linux"), "needs /proc")
    def test_background_descendant_killed_after_rc0(self):
        """A real child backgrounds `sleep 30.517` that holds stdout, then exits 0."""
        j = self.real_job()
        tok = self.token()
        t0 = time.monotonic()
        rc, out = j.spawn(["bash", "-c", "sleep %s & echo hi; exit 0" % tok])
        self.assertEqual((rc, out), (0, "hi"))
        self.assertLess(time.monotonic() - t0, 5)
        self.assertTrue(wait_for(lambda: not self.procs_with(tok)), self.procs_with(tok))

    @unittest.skipUnless(sys.platform.startswith("linux"), "needs /proc and prctl")
    def test_setsid_orphan_killed_with_subreaper(self):
        """A descendant that left the group (setsid) is re-parented to the hardened worker and swept."""
        script = r"""
import os, subprocess, sys, time
sys.path.insert(0, sys.argv[1])
import main as W
assert W.harden_process()
j = W.Job({"BUCKET": "b", "TABLE": "t", "DATA_ROLE_ARN": "r", "WORK_ROOT": sys.argv[2], "JOB_TIMEOUT_S": "60"},
          {"kind": "analyze", "accountId": "acct-a", "uploadId": "u20261020123456abcdef", "source": "cot"},
          None, None, None, subprocess.Popen, None)
j.fresh_workspace()
t0 = time.monotonic()
rc, out = j.spawn(["bash", "-c", "python3 -c 'import os, time; os.setsid(); time.sleep(%s)' & sleep 0.5; echo hi" % sys.argv[3]])
print(rc, out, round(time.monotonic() - t0))
"""
        tok = self.token()
        r = subprocess.run([sys.executable, "-I", "-c", script, os.path.dirname(W.__file__), self.work_root, tok],
                           capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr[-500:])
        rc, out, secs = r.stdout.split()
        self.assertEqual((rc, out), ("0", "hi"))
        self.assertLess(int(secs), 5)
        self.assertIn("orphans_killed", r.stderr)
        self.assertTrue(wait_for(lambda: not self.procs_with("time.sleep(%s)" % tok)), self.procs_with(tok))

    def test_summary_symlink_not_followed(self):
        self.job(src="cur")
        ddb = self.ddb(src="cur")
        target = os.path.join(self.tmp, "target.json")
        wr("ORIG", target, "w")
        seen = {}

        def plant(cmd, kw):
            os.symlink(target, os.path.join(kw["cwd"], "summary.json"))

        def check(cmd, kw):
            p = cmd[6]
            seen["link"] = os.path.islink(p)
            seen["mode"] = oct(os.lstat(p).st_mode & 0o777)
            seen["data"] = jload(p, encoding="utf-8")
        self.assertEqual(self.run_main(ddb, Runner(hooks={"stage_cur.py": plant, "datasets.py": check})), 0)
        self.assertEqual(rd(target), "ORIG")
        self.assertEqual(seen, {"link": False, "mode": "0o600", "data": {"months": ["2026-06"]}})

    def test_names_json_symlink_not_followed(self):
        self.job()
        target = os.path.join(self.tmp, "names-target.json")
        wr("ORIG", target, "w")
        orig = W.Job.fresh_workspace

        def fresh(job):
            orig(job)
            os.symlink(target, job.path("names.json"))
        with mock.patch.object(W.Job, "fresh_workspace", fresh):
            self.assertEqual(self.run_main(self.ddb(), Runner()), 0)
        self.assertEqual(rd(target), "ORIG")

    def test_publish_skips_symlinks_and_fifos(self):
        self.job()
        ddb = self.ddb()
        secret = os.path.join(self.tmp, "secret.json")
        wr('["secret"]', secret, "w")

        def plant(cmd, kw):
            out = cmd[4]
            os.symlink(secret, os.path.join(out, "evil.json"))
            os.mkfifo(os.path.join(out, "pipe.json"))
        t0 = time.monotonic()
        self.assertEqual(self.run_main(ddb, Runner(hooks={"run_account.sh": plant})), 0)
        self.assertLess(time.monotonic() - t0, 10)
        base = "accounts/acct-a/versions/%s/data/" % VID
        self.assertNotIn(base + "evil.json", self.store)
        self.assertNotIn(base + "pipe.json", self.store)
        self.assertIn(base + "kpis.json", self.store)

    def test_open_regular_uses_fstat_on_descriptor(self):
        d = os.path.join(self.tmp, "d")
        os.makedirs(d)
        wr("[]", os.path.join(d, "f.json"), "w")
        os.symlink(os.path.join(d, "f.json"), os.path.join(d, "l.json"))
        os.mkdir(os.path.join(d, "sub.json"))
        names = []
        W.each_regular(self.tmp, ("d",), W.DATA_FILE_RE, lambda n, f: names.append(n))
        self.assertEqual(names, ["f.json"])
        os.symlink(d, os.path.join(self.tmp, "dl"))
        self.assertEqual(W.each_regular(self.tmp, ("dl",), W.DATA_FILE_RE, lambda n, f: names.append(n)), 0)

    def sigterm_on(self, ddb, method, pred):
        orig = getattr(ddb, method)

        def wrapped(**kw):
            if pred(kw):
                os.kill(os.getpid(), signal.SIGTERM)
            return orig(**kw)
        setattr(ddb, method, wrapped)

    def test_sigterm_during_claim_fails_aborted(self):
        self.job()
        ddb = self.ddb()
        self.sigterm_on(ddb, "update_item", lambda kw: kw.get("ConditionExpression") == "#st = :queued AND #src = :src")
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 1)
        up = self.up(ddb)
        self.assertEqual((up["status"], up["reason"]), ("failed", "처리 중단"))
        self.assertEqual(up["steps"]["validate"], "failed")
        self.assertEqual(runner.cmds, [])
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_sigterm_during_finish_does_not_cut_recording(self):
        self.job()
        ddb = self.ddb()
        self.sigterm_on(ddb, "update_item", lambda kw: kw["UpdateExpression"].startswith("SET #st = :st"))
        runner = Runner(cot_rc=3, cot_lines=["STEP validate running", "STEP validate failed"], reason="zip 형식이 아닙니다")
        self.assertEqual(self.run_main(ddb, runner), 1)
        up = self.up(ddb)
        self.assertEqual((up["status"], up["reason"]), ("failed", "zip 형식이 아닙니다"))
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_sigterm_during_fail_running_does_not_cut_recording(self):
        self.job()
        ddb = self.ddb()
        self.sigterm_on(ddb, "update_item", lambda kw: (kw.get("ExpressionAttributeValues") or {}).get(":s") == {"S": "failed"})
        self.assertEqual(self.run_main(ddb, Runner(raise_on="agents")), 1)
        up = self.up(ddb)
        self.assertEqual((up["status"], up["reason"]), ("failed", "내부 오류: KeyError"))
        self.assertEqual(up["steps"]["agents"], "failed")
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_sigterm_during_release_lock_still_releases(self):
        self.job()
        ddb = self.ddb()
        self.sigterm_on(ddb, "delete_item", lambda kw: True)
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_sigterm_during_publish_transaction_keeps_done(self):
        self.job()
        ddb = self.ddb()
        self.sigterm_on(ddb, "transact_write_items", lambda kw: True)
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(self.up(ddb)["status"], "done")
        self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["current_cot"], VID)
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_period_file_closed(self):
        import gc, warnings
        self.job()
        gc.collect()  # garbage left by earlier tests is not this job's
        with warnings.catch_warnings(record=True) as w:
            warnings.simplefilter("always", ResourceWarning)
            self.assertEqual(self.run_main(self.ddb(), Runner()), 0)
            gc.collect()
        self.assertEqual([str(x.message) for x in w if issubclass(x.category, ResourceWarning)
                          and self.work_root in str(x.message)], [])

    def test_period_reader_leaves_no_open_file(self):
        """period() alone: the old jload(...) warned 'unclosed file ... period.json'."""
        import gc, warnings
        j = W.Job(self.env(), {"kind": "analyze", "accountId": A, "uploadId": UP, "source": "cot"}, None, None, None, None, None)
        j.fresh_workspace()
        write_json(j.path("out"), "period.json", [{"entity": A, "source": "cot", "first": "a", "last": "b", "months": ["b"]}])
        gc.collect()
        with warnings.catch_warnings(record=True) as w:
            warnings.simplefilter("always", ResourceWarning)
            self.assertEqual(j.period(), {"first": "a", "last": "b", "months": ["b"]})
            gc.collect()
        self.assertEqual([str(x.message) for x in w if issubclass(x.category, ResourceWarning)
                          and "period.json" in str(x.message)], [])

    # ---------- round 3 ----------

    def fake_orphans(self, script):
        """CHILDREN() answers from script (a list of pid lists, the last one repeated); KILL is recorded."""
        calls, killed = [], []
        W.SUBREAPER[0] = True
        W.REAP = lambda pid: None
        W.KILL = lambda pid, sig: killed.append(pid)

        def children():
            calls.append(1)
            return list(script[min(len(calls), len(script)) - 1])
        W.CHILDREN = children
        return calls, killed

    def test_sweep_loops_past_20_rounds_until_none(self):
        calls, killed = self.fake_orphans([[4242]] * 30 + [[]])
        self.assertEqual(W.sweep_orphans(), 30)
        self.assertEqual(len(calls), 31)
        self.assertEqual(set(killed), {4242})

    def test_surviving_orphan_fails_job_before_agents(self):
        self.job()
        ddb = self.ddb()
        W.ORPHAN_LIMIT_S = 0.2
        calls, killed = self.fake_orphans([[], [4242]])  # none before the pipeline, one survives after it
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 1)
        self.assertEqual([Runner.tag(c) for c in runner.cmds], ["run_account.sh"])  # agents never started
        up = self.up(ddb)
        self.assertEqual((up["status"], up["reason"]), ("failed", "내부 오류: OrphanProcesses"))
        self.assertEqual(up["steps"]["agents"], "failed")
        self.assertIn(4242, killed)
        self.assertFalse([k for k in self.store if "/versions/" in k])
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_orphan_appearing_before_agents_blocks_them(self):
        """The sweep after the pipeline saw none, but one is alive when the agents child is about to start."""
        self.job()
        ddb = self.ddb()
        W.ORPHAN_LIMIT_S = 0.2
        calls, killed = self.fake_orphans([[], [], [5151]])  # before pipeline, after pipeline, before agents
        runner = Runner()
        self.assertEqual(self.run_main(ddb, runner), 1)
        self.assertEqual([Runner.tag(c) for c in runner.cmds], ["run_account.sh"])
        self.assertEqual(self.up(ddb)["reason"], "내부 오류: OrphanProcesses")

    def test_orphans_killed_then_job_continues(self):
        self.job()
        ddb = self.ddb()
        calls, killed = self.fake_orphans([[], [4242], [4242], []])
        self.assertEqual(self.run_main(ddb, Runner()), 0)
        self.assertEqual(killed, [4242, 4242])
        self.assertEqual(self.up(ddb)["status"], "done")

    def test_aborted_is_base_exception(self):
        self.assertTrue(issubclass(W.Aborted, BaseException))
        self.assertFalse(issubclass(W.Aborted, Exception))

    def test_spawn_rechecks_sigterm(self):
        j = self.real_job()
        W.KILLPG = fake_killpg
        spawned = []
        j.popen = lambda *a, **k: spawned.append(a)
        W.TERM["got"], W.TERM["armed"] = True, False
        with self.assertRaises(W.Aborted):
            j.spawn(["true"])
        self.assertEqual(spawned, [])

    def test_killpg_runs_before_end_group(self):
        self.job()
        ddb = self.ddb()
        runner = Runner()
        with mock.patch.object(W.Job, "end_group", side_effect=RuntimeError("x")):
            self.assertEqual(self.run_main(ddb, runner), 1)
        self.assertEqual(KILLS, [(runner.procs[0].pid, signal.SIGKILL)])
        self.assertEqual(self.up(ddb)["reason"], "내부 오류: RuntimeError")

    def flaky_update(self, ddb, pred):
        orig = ddb.update_item

        def update(**kw):
            if pred(kw):
                raise FakeClientError("ProvisionedThroughputExceededException")
            return orig(**kw)
        ddb.update_item = update

    def test_lock_released_when_finish_errors(self):
        self.job()
        ddb = self.ddb()
        self.flaky_update(ddb, lambda kw: kw["UpdateExpression"].startswith("SET #st = :st"))
        runner = Runner(cot_rc=3, cot_lines=["STEP validate running", "STEP validate failed"], reason="zip 형식이 아닙니다")
        self.assertEqual(self.run_main(ddb, runner), 1)
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    def test_lock_released_when_fail_running_errors(self):
        self.job()
        ddb = self.ddb()
        self.flaky_update(ddb, lambda kw: (kw.get("ExpressionAttributeValues") or {}).get(":s") == {"S": "failed"})
        self.assertEqual(self.run_main(ddb, Runner(raise_on="agents")), 1)
        self.assertNotIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)

    @staticmethod
    def open_fds():
        return len(os.listdir("/proc/self/fd"))

    @unittest.skipUnless(os.path.isdir("/proc/self/fd"), "needs /proc")
    def test_publish_holds_one_file_at_a_time(self):
        self.job()
        ddb = self.ddb()
        counts = []
        put = self.data_s3.put_object

        def counting(**kw):
            counts.append(self.open_fds())
            return put(**kw)
        self.data_s3.put_object = counting

        def extra(cmd, kw):
            for i in range(40):
                write_json(cmd[4], "extra_%02d.json" % i, [])
        self.assertEqual(self.run_main(ddb, Runner(hooks={"run_account.sh": extra})), 0)
        self.assertGreaterEqual(len(counts), 45)
        self.assertLessEqual(max(counts) - min(counts), 1)

    @unittest.skipUnless(os.path.isdir("/proc/self/fd"), "needs /proc")
    def test_upload_error_leaks_no_fd(self):
        self.job()
        ddb = self.ddb()
        put, n = self.data_s3.put_object, []

        def failing(**kw):
            n.append(1)
            if len(n) == 3:
                raise FakeClientError("SlowDown")
            return put(**kw)
        self.data_s3.put_object = failing
        before = self.open_fds()
        self.assertEqual(self.run_main(ddb, Runner()), 1)
        self.assertEqual(self.open_fds(), before)
        self.assertEqual(self.up(ddb)["reason"], "내부 오류: FakeClientError")

    def capture_log(self, fn):
        buf = io.StringIO()
        orig = sys.stderr
        sys.stderr = buf
        try:
            fn()
        finally:
            sys.stderr = orig
        return buf.getvalue()

    def test_logs_carry_no_reason_text(self):
        reasons = ["허용되지 않은 파일: secret_member_name.exe", "통화가 여러 개입니다: KRW, USD", "폴더가 여러 개: billing/x"]
        for reason in reasons:
            self.job()
            ddb = self.ddb()
            log = self.capture_log(lambda: self.run_main(ddb, Runner(
                cot_rc=3, cot_lines=["STEP validate running", "STEP validate failed"], reason=reason)))
            self.assertEqual(self.up(ddb)["reason"], reason)
            lines = [json.loads(x) for x in log.splitlines()]
            for word in re.split(r"[ :,/]+", reason):
                if len(word) >= 2:
                    self.assertNotIn(word, log)
            for rec in lines:
                self.assertNotIn("reason", rec)
            job = [r for r in lines if r["event"] == "job"]
            self.assertEqual(job[-1]["kind"], "rejected")
            self.assertNotIn("x.zip", log)

    def test_log_kinds(self):
        cases = ((Runner(cot_rc=1), "check_failed"), (Runner(raise_on="agents"), "internal"))
        for runner, kind in cases:
            self.job()
            log = self.capture_log(lambda: self.run_main(self.ddb(), runner))
            job = [json.loads(x) for x in log.splitlines() if '"job"' in x][-1]
            self.assertEqual(job["kind"], kind)
        self.assertEqual(job["cls"], "KeyError")

    def test_av_roundtrip(self):
        v = {"a": "s", "b": [1, 2.5], "c": None, "d": True, "e": {"f": []}}
        self.assertEqual(W.from_av(W.to_av(v)), v)

    def test_fake_condition_evaluator(self):
        item = {"status": {"S": "running"}, "claim": {"S": "t"}, "pk": {"S": "p"}}
        vals = {":t": {"S": "t"}, ":r": {"S": "running"}, ":q": {"S": "queued"}, ":v": {"S": "v2"}}
        self.assertTrue(Cond("claim = :t AND #st = :r", item, {"#st": "status"}, vals).run())
        self.assertFalse(Cond("#st IN (:q)", item, {"#st": "status"}, vals).run())
        self.assertTrue(Cond("attribute_exists(pk) AND (attribute_not_exists(cur) OR cur <= :v)", item, {}, vals).run())
        self.assertFalse(Cond("attribute_exists(nope)", item, {}, vals).run())


if __name__ == "__main__":
    unittest.main()
