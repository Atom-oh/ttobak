"""Fakes and the shared Base case for test_worker.py: fake s3/ddb/sts, a fake Popen and the job runner. No AWS calls."""
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
        self.transfers = []  # upload_fileobj (Key, ExtraArgs, Config)

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

    def upload_fileobj(self, Fileobj, Bucket, Key, ExtraArgs=None, Config=None):
        self.calls.append(("upload", Key))
        self.transfers.append((Key, ExtraArgs, Config))
        self.store[Key] = Fileobj.read()

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

    def put_item(self, TableName, Item, ConditionExpression=None, ExpressionAttributeValues=None,
                 ExpressionAttributeNames=None, **kw):
        k = self._key(Item)
        self.calls.append(("put", k, dict(Item), ConditionExpression))
        self._check(k, ConditionExpression, ExpressionAttributeNames, ExpressionAttributeValues)
        self.items[k] = dict(Item)
        return {}

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


LEASE = ("ACCT#" + A, "RERUN#cot#" + VID)


def lease_item(owner, expires_at):
    return {"pk": {"S": LEASE[0]}, "sk": {"S": LEASE[1]}, "owner": {"S": owner}, "expiresAt": {"N": str(int(expires_at))},
            "ttl": {"N": str(int(expires_at))}}


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
                      harden=lambda: self.hardened.append(1) or True)

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
