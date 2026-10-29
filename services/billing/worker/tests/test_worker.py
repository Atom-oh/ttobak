"""worker/main.py: one upload job end to end with fake s3/ddb/sts and a fake Popen. No AWS calls.
WORKER_SMOKE=1 also runs the real COT runner (synthetic non-zip upload) and, where duckdb imports under -I, the real
CUR pipeline on a synthetic CUR zip."""
import datetime, io, json, os, re, shutil, signal, subprocess, sys, tempfile, threading, time, unittest, uuid
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "worker"))
import main as W  # noqa: E402

A, B = "acct-a", "acct-b"
UP = "u20261020123456abcdef"
VID = "v20261020123456abcdef"
BUCKET, TABLE = "data-bucket", "platform-table"
ROLE = "arn:aws:iam::111122223333:role/data"


from fileutil import rd, wr, jload  # noqa: E402

class FakeClientError(Exception):
    def __init__(self, code):
        super().__init__(code)
        self.response = {"Error": {"Code": code}}


class FakeS3:
    """One object store; each client keeps its own call log."""

    def __init__(self, store, name):
        self.store, self.name, self.calls = store, name, []
        self.sizes = {}  # Key -> ContentLength reported by head_object instead of the stored length

    def get_object(self, Bucket, Key):
        self.calls.append(("get", Key))
        if Key not in self.store:
            raise FakeClientError("NoSuchKey")
        return {"Body": io.BytesIO(self.store[Key])}

    def head_object(self, Bucket, Key):
        self.calls.append(("head", Key))
        if Key not in self.store:
            raise FakeClientError("404")
        return {"ContentLength": self.sizes.get(Key, len(self.store[Key]))}

    def put_object(self, Bucket, Key, Body, **kw):
        self.calls.append(("put", Key))
        self.store[Key] = Body.read() if hasattr(Body, "read") else Body
        return {}

    def list_objects_v2(self, Bucket, Prefix, **kw):
        self.calls.append(("list", Prefix))
        return {"Contents": [{"Key": k} for k in sorted(self.store) if k.startswith(Prefix)], "IsTruncated": False}


class FakeSTS:
    def __init__(self):
        self.calls = []

    def assume_role(self, **kw):
        self.calls.append(kw)
        exp = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)
        return {"Credentials": {"AccessKeyId": "AK", "SecretAccessKey": "SK", "SessionToken": "ST", "Expiration": exp}}


def _split_top(s):
    out, depth, cur = [], 0, ""
    for ch in s:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            out.append(cur.strip())
            cur = ""
        else:
            cur += ch
    if cur.strip():
        out.append(cur.strip())
    return out


class Cond:
    """Evaluates the DynamoDB condition forms the worker uses: AND/OR/NOT, parentheses, = <> < <= > >=, IN (...),
    attribute_exists/attribute_not_exists, #names and :values, dotted paths."""
    TOK = re.compile(r"\s*(<>|<=|>=|=|<|>|\(|\)|,|[#:]?[A-Za-z_][A-Za-z0-9_.#]*)")

    def __init__(self, expr, item, names, vals):
        self.toks, pos = [], 0
        expr = expr.strip()
        while pos < len(expr):
            m = self.TOK.match(expr, pos)
            assert m, expr[pos:]
            self.toks.append(m.group(1))
            pos = m.end()
            while pos < len(expr) and expr[pos] == " ":
                pos += 1
        self.i, self.item, self.names, self.vals = 0, item, names or {}, vals or {}

    def peek(self):
        return self.toks[self.i] if self.i < len(self.toks) else None

    def take(self, want=None):
        t = self.toks[self.i]
        assert want is None or t == want, (t, want)
        self.i += 1
        return t

    def value(self, tok):
        if tok.startswith(":"):
            return W.from_av(self.vals[tok])
        cur = {"M": self.item}
        for p in tok.split("."):
            p = self.names.get(p, p)
            if "M" not in cur or p not in cur["M"]:
                return None
            cur = cur["M"][p]
        return W.from_av(cur)

    def run(self):
        v = self.expr()
        assert self.peek() is None
        return v

    def expr(self):
        v = self.conj()
        while self.peek() == "OR":
            self.take()
            v = self.conj() or v
        return v

    def conj(self):
        v = self.unary()
        while self.peek() == "AND":
            self.take()
            v = self.unary() and v
        return v

    def unary(self):
        if self.peek() == "NOT":
            self.take()
            return not self.unary()
        if self.peek() == "(":
            self.take("(")
            v = self.expr()
            self.take(")")
            return v
        t = self.take()
        if t in ("attribute_exists", "attribute_not_exists"):
            self.take("(")
            path = self.take()
            self.take(")")
            exists = self.value(path) is not None or self._raw_exists(path)
            return exists if t == "attribute_exists" else not exists
        left = self.value(t)
        op = self.take()
        if op == "IN":
            self.take("(")
            opts = [self.value(self.take())]
            while self.peek() == ",":
                self.take()
                opts.append(self.value(self.take()))
            self.take(")")
            return left in opts
        right = self.value(self.take())
        if left is None or right is None:
            return op == "<>" and left != right
        return {"=": left == right, "<>": left != right, "<": left < right, "<=": left <= right,
                ">": left > right, ">=": left >= right}[op]

    def _raw_exists(self, path):
        return self.names.get(path, path) in self.item


class FakeDDB:
    """Applies the SET/REMOVE forms the worker uses and evaluates its conditions; `fail` forces a condition failure."""

    def __init__(self, items=None, fail_conditions=()):
        self.items = {k: dict(v) for k, v in (items or {}).items()}
        self.calls = []
        self.fail = set(fail_conditions)

    def _key(self, Key):
        return (Key["pk"]["S"], Key["sk"]["S"])

    def get_item(self, TableName, Key, **kw):
        self.calls.append(("get", self._key(Key), kw))
        it = self.items.get(self._key(Key))
        return {"Item": dict(it)} if it is not None else {}

    def _check(self, k, cond, names, vals):
        if k in self.fail:
            raise FakeClientError("ConditionalCheckFailedException")
        if cond and not Cond(cond, self.items.get(k, {}), names, vals).run():
            raise FakeClientError("ConditionalCheckFailedException")

    def update_item(self, TableName, Key, UpdateExpression, ExpressionAttributeValues=None, ExpressionAttributeNames=None,
                    ConditionExpression=None, **kw):
        k = self._key(Key)
        self.calls.append(("update", k, UpdateExpression, ExpressionAttributeValues, ExpressionAttributeNames, ConditionExpression))
        names = ExpressionAttributeNames or {}
        vals = ExpressionAttributeValues or {}
        self._check(k, ConditionExpression, names, vals)
        self._apply(k, UpdateExpression, names, vals)
        return {}

    def transact_write_items(self, TransactItems):
        reqs = [t["Update"] for t in TransactItems]
        self.calls.append(("transact", [self._key(r["Key"]) for r in reqs]))
        codes = []
        for r in reqs:
            k = self._key(r["Key"])
            self.calls.append(("tx-update", k, r["UpdateExpression"], r.get("ExpressionAttributeValues"),
                               r.get("ExpressionAttributeNames"), r.get("ConditionExpression")))
            try:
                self._check(k, r.get("ConditionExpression"), r.get("ExpressionAttributeNames"), r.get("ExpressionAttributeValues"))
                codes.append("None")
            except FakeClientError:
                codes.append("ConditionalCheckFailed")
        if "ConditionalCheckFailed" in codes:
            e = FakeClientError("TransactionCanceledException")
            e.response["CancellationReasons"] = [{"Code": c} for c in codes]
            raise e
        for r in reqs:
            self._apply(self._key(r["Key"]), r["UpdateExpression"], r.get("ExpressionAttributeNames") or {},
                        r.get("ExpressionAttributeValues") or {})
        return {}

    def _apply(self, k, UpdateExpression, names, vals):
        item = self.items.setdefault(k, {"pk": {"S": k[0]}, "sk": {"S": k[1]}})
        m = re.match(r"^SET (.*?)(?: REMOVE (.*))?$", UpdateExpression)
        assert m, UpdateExpression
        for part in _split_top(m.group(1)):
            lhs, rhs = [x.strip() for x in part.split("=", 1)]
            path = [names.get(p, p) for p in lhs.split(".")]
            fm = re.match(r"if_not_exists\(([^,]+),\s*(:\w+)\)", rhs)
            if fm:
                if path[0] in item:
                    continue
                v = vals[fm.group(2)]
            else:
                v = vals[rhs]
            tgt = item
            for p in path[:-1]:
                tgt = tgt[p]["M"]
            tgt[path[-1]] = v
        if m.group(2):
            for lhs in _split_top(m.group(2)):
                item.pop(names.get(lhs, lhs), None)

    def delete_item(self, TableName, Key, ConditionExpression=None, ExpressionAttributeValues=None, **kw):
        k = self._key(Key)
        self.calls.append(("delete", k, ConditionExpression, ExpressionAttributeValues))
        self._check(k, ConditionExpression, kw.get("ExpressionAttributeNames"), ExpressionAttributeValues)
        self.items.pop(k, None)
        return {}

    def py(self, k):
        return W.from_av({"M": self.items[k]})


def write_json(d, name, rows):
    with open(os.path.join(d, name), "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)


PROCS = {}


class FakeProc:
    """Popen stand-in: stdout yields the scripted lines (line_hook runs before each); block=True never finishes
    until killed through the patched KILLPG."""
    _next = [900000000]

    def __init__(self, lines, rc, block=False, line_hook=None, hold=False):
        self._next[0] += 1
        self.pid = self._next[0]
        self.rc, self.block, self.released = rc, block, threading.Event()
        self.hold = hold  # lead exits at once, but a "descendant" keeps stdout open until the group is killed
        self.killed = False
        PROCS[self.pid] = self
        self.stdout = self._iter(lines, line_hook)

    def _iter(self, lines, hook):
        for line in lines:
            if hook:
                hook(line, self)
            yield line + "\n"
        if self.block or self.hold:
            self.released.wait(30)

    def wait(self, timeout=None):
        if self.block and not self.released.is_set():
            if not self.released.wait(timeout):
                raise subprocess.TimeoutExpired("fake", timeout)
        return -9 if self.killed and self.block else self.rc  # killing the group after the lead exited keeps its rc

    def poll(self):
        return None if self.block and not self.released.is_set() else self.rc


def wait_for(pred, limit=5.0):
    end = time.monotonic() + limit
    while time.monotonic() < end:
        if pred():
            return True
        time.sleep(0.01)
    return False


def fake_killpg(pid, sig):
    KILLS.append((pid, sig))
    p = PROCS.get(pid)
    if p:
        p.killed = True
        p.released.set()


KILLS = []


class Runner:
    """Plays the pipeline and agent children the worker spawns. hooks[tag](cmd, kw) runs at spawn time."""

    def __init__(self, cot_rc=0, cot_lines=None, ai="ok", reason=None, raise_on=None, block_on=None, cur_check_rc=0,
                 hooks=None, line_hook=None, hold_on=None):
        self.cmds, self.kw, self.procs = [], [], []
        self.hold_on = hold_on
        self.cot_rc, self.cot_lines, self.ai, self.reason = cot_rc, cot_lines, ai, reason
        self.raise_on, self.block_on, self.cur_check_rc = raise_on, block_on, cur_check_rc
        self.hooks, self.line_hook = hooks or {}, line_hook

    @staticmethod
    def tag(cmd):
        tool = os.path.basename(cmd[2] if cmd[0] == "python3" and cmd[1] == "-I" else cmd[1])
        return cmd[3] if tool == "ai_step.py" else tool

    def __call__(self, cmd, **kw):
        self.cmds.append(list(cmd))
        self.kw.append(kw)
        tag = self.tag(cmd)
        if tag in self.hooks:
            self.hooks[tag](cmd, kw)
        if self.raise_on == tag:
            raise KeyError("boom")
        rc, lines = self.play(tag, cmd)
        p = FakeProc(lines, rc, block=self.block_on == tag, line_hook=self.line_hook if tag == "run_account.sh" else None,
                     hold=self.hold_on == tag)
        self.procs.append(p)
        return p

    def play(self, tag, cmd):
        if tag == "run_account.sh":
            out, acct = cmd[4], cmd[5]
            write_json(out, "kpis.json", [{"entity": acct, "last_spend": 1}])
            write_json(out, "period.json", [{"entity": acct, "source": "cot", "months": ["2026-05", "2026-06"],
                                             "first": "2026-05", "prev": "2026-05", "last": "2026-06", "labels": {}}])
            write_json(out, "checks.json", [{"name": "x", "ok": self.cot_rc == 0}])
            lines = self.cot_lines
            if lines is None:
                lines = ["STEP %s %s" % (s, x) for s in ("validate", "extract", "datasets") for x in ("running", "done")]
                lines += ["STEP check running", "STEP check " + ("done" if self.cot_rc == 0 else "needs_review")]
            if self.reason is not None:
                lines = lines + [json.dumps({"reason": self.reason}, ensure_ascii=False)]
            return self.cot_rc, lines
        if tag == "stage_cur.py":
            work = cmd[4]
            os.makedirs(os.path.join(work, "cur_norm"), exist_ok=True)
            with open(os.path.join(work, "cur_norm", "part-0.parquet"), "wb") as f:
                f.write(b"PAR1new")
            return 0, [json.dumps({"months": ["2026-06"]})]
        if tag == "datasets.py":
            out, acct = cmd[4], cmd[5]
            write_json(out, "cur_summary.json", [{"entity": acct}])
            write_json(out, "period.json", [{"entity": acct, "source": "cur", "months": ["2026-06"], "first": "2026-06",
                                             "prev": None, "last": "2026-06", "labels": {}}])
            return 0, []
        if tag == "check.py":
            write_json(cmd[4], "checks.json", [{"name": "single_currency", "ok": self.cur_check_rc == 0}])
            return self.cur_check_rc, []
        if tag == "agents":
            d = cmd[4]
            write_json(d, "agent_notes.json", [{"kind": "meta"}])
            if cmd[5] == "cot":
                write_json(d, "sessions.json", [])
                write_json(d, "market.json", [])
            return 0, [json.dumps({"ai": self.ai})]
        if tag == "report":
            with open(cmd[5], "w", encoding="utf-8") as f:
                f.write("# report\n")
            return 0, ["wrote"]
        raise AssertionError("unexpected command %r" % (cmd,))


def up_item(acct=A, src="cot", status="queued"):
    return {"pk": {"S": "ACCT#" + acct}, "sk": {"S": "UP#" + UP}, "uploadId": {"S": UP}, "accountId": {"S": acct},
            "source": {"S": src}, "status": {"S": status}, "fileName": {"S": "x.zip"}}


def meta_item(acct=A, name="한빛금융", **extra):
    it = {"pk": {"S": "ACCT#" + acct}, "sk": {"S": "META"}, "parentId": {"S": "root"}}
    if name is not None:
        it["name"] = {"S": name}
    for k, v in extra.items():
        it[k] = {"S": v}
    return it


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        self.work_root = os.path.join(self.tmp, "work")
        os.makedirs(self.work_root)
        self.store = {}
        self.task_s3 = FakeS3(self.store, "task")
        self.data_s3 = FakeS3(self.store, "data")
        self.sts = FakeSTS()
        self.creds = []
        self.hardened = []
        KILLS.clear()
        self._killpg = W.KILLPG
        W.KILLPG = fake_killpg
        self._globals = (W.SUBREAPER[0], W.CHILDREN, W.KILL, W.REAP, W.ORPHAN_LIMIT_S)

    def tearDown(self):
        W.KILLPG = self._killpg
        W.SUBREAPER[0], W.CHILDREN, W.KILL, W.REAP, W.ORPHAN_LIMIT_S = self._globals
        W.TERM["got"] = W.TERM["armed"] = False
        for p in PROCS.values():
            p.released.set()
        PROCS.clear()
        shutil.rmtree(self.tmp, ignore_errors=True)

    def env(self, **kw):
        e = {"JOB_KEY": "jobs/%s.json" % UP, "BUCKET": BUCKET, "TABLE": TABLE, "DATA_ROLE_ARN": ROLE,
             "WORK_ROOT": self.work_root, "PUBLIC_URL": "https://dash.example.test", "JOB_TIMEOUT_S": "3600"}
        e.update(kw)
        return e

    def job(self, acct=A, src="cot", kind="analyze", **extra):
        j = {"kind": kind, "accountId": acct, "uploadId": UP, "source": src}
        j.update(extra)
        self.store["jobs/%s.json" % UP] = json.dumps(j).encode()
        self.store["accounts/%s/uploads/%s/source.zip" % (acct, UP)] = b"PK\x03\x04synthetic"
        self.store["data/whats_new_feed.json"] = b"[]"

    def factory(self, creds):
        self.creds.append(creds)
        return self.data_s3

    def run_main(self, ddb, runner, env=None):
        return W.main(env=env or self.env(), s3=self.task_s3, ddb=ddb, sts=self.sts, runner=runner, s3_factory=self.factory,
                      harden=lambda: self.hardened.append(1))

    def ddb(self, src="cot", acct=A, meta=None, fail=(), status="queued", lock_owner=UP):
        items = {("ACCT#" + acct, "UP#" + UP): up_item(acct, src, status), ("ACCT#" + acct, "LOCK#" + src): {
            "pk": {"S": "ACCT#" + acct}, "sk": {"S": "LOCK#" + src}, "uploadId": {"S": lock_owner}}}
        if meta is not False:
            items[("ACCT#" + acct, "META")] = meta if meta is not None else meta_item(acct)
        return FakeDDB(items, fail)

    def up(self, ddb, acct=A):
        return ddb.py(("ACCT#" + acct, "UP#" + UP))

    def writes(self, ddb):
        return [c for c in ddb.calls if c[0] in ("update", "delete", "tx-update")]

    def meta_updates(self, ddb):
        return [c for c in ddb.calls if c[0] in ("update", "tx-update") and c[1][1] == "META"]

    def up_updates(self, ddb):
        return [c for c in ddb.calls if c[0] in ("update", "tx-update") and c[1][1].startswith("UP#")]


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

    @unittest.skipUnless(sys.platform.startswith("linux"), "prctl is Linux only")
    def test_harden_process_clears_dumpable(self):
        code = ("import ctypes, sys; sys.path.insert(0, sys.argv[1]); import main; ok = main.harden_process(); "
                "print(ok, ctypes.CDLL(None).prctl(3, 0, 0, 0, 0))")
        r = subprocess.run([sys.executable, "-I", "-c", code, os.path.dirname(W.__file__)], capture_output=True, text=True,
                           timeout=60)
        self.assertEqual(r.stdout.split(), ["True", "0"], r.stderr[-300:])

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
                         ["s3:GetObject", "s3:PutObject"])
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
            ups = self.writes(ddb)
            self.assertEqual(len(ups), 2)
            # M4: the version's own UP row gets the fresh ai (rollback copies it), only while that row exists
            self.assertEqual(ups[0][1], ("ACCT#acct-a", "UP#" + UP))
            self.assertEqual(ups[0][2], "SET ai = :a")
            self.assertEqual(ups[0][5], "attribute_exists(pk)")
            self.assertEqual(ups[1][1], ("ACCT#acct-a", "META"))
            self.assertEqual(ups[1][5], "current_cot = :v")
            self.assertEqual(ddb.py(("ACCT#acct-a", "META"))["ai_cot"], "ok" if updated else "none")
            after = ddb.py(("ACCT#acct-a", "UP#" + UP))
            self.assertEqual(after["ai"], "ok")
            self.assertEqual({k: v for k, v in after.items() if k != "ai"}, {k: v for k, v in before.items() if k != "ai"})
            self.assertIn(("ACCT#acct-a", "LOCK#cot"), ddb.items)
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


class TestAiStep(unittest.TestCase):
    def test_agents_args_per_source(self):
        import ai_step
        sys.path.insert(0, os.path.join(HERE, "..", "scripts"))
        import agents
        seen = []
        orig = agents.run
        agents.run = lambda d, **kw: (seen.append((d, kw)), {"ai": "none"})[1]
        tmp = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        buf, out = io.StringIO(), sys.stdout
        try:
            sys.stdout = buf
            self.assertEqual(ai_step.main(["agents", tmp, "cot", "/f.json", os.path.join(tmp, "c")]), 0)
            self.assertTrue(os.path.isfile(os.path.join(tmp, "tag_signals.json")))  # an older version gets its tag signals
            self.assertEqual(ai_step.main(["agents", tmp, "cur", "-", os.path.join(tmp, "c")]), 0)
            self.assertEqual(ai_step.main(["agents", tmp, "xls", "-", os.path.join(tmp, "c")]), 2)
        finally:
            sys.stdout = out
            agents.run = orig
            shutil.rmtree(tmp, ignore_errors=True)
        self.assertEqual(seen[0][1], {"cache": os.path.join(tmp, "c"), "feed_path": "/f.json"})
        self.assertEqual(seen[1][1], {"cache": os.path.join(tmp, "c"), "feed_path": None, "areas": ["business", "cur"], "synth": False,
                                      "extras": False})
        self.assertEqual(buf.getvalue().splitlines()[-1], '{"ai": "none"}')

    def test_report_runs_isolated(self):
        """ai_step.py under python3 -I still finds its scripts (report needs no boto3 credentials)."""
        tmp = tempfile.mkdtemp(dir=os.environ.get("TMPDIR"))
        try:
            md = os.path.join(tmp, "report.md")
            r = subprocess.run(["python3", "-I", os.path.join(HERE, "..", "worker", "ai_step.py"), "report", tmp, md,
                                "https://dash.example.test"], capture_output=True, text=True, timeout=120,
                               env={"PATH": os.environ.get("PATH", ""), "HOME": tmp, "PYTHONNOUSERSITE": "1"})
            self.assertEqual(r.returncode, 0, r.stderr[-500:])
            self.assertTrue(os.path.isfile(md))
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


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
