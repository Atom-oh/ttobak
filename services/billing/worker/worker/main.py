"""Worker entrypoint: one ECS Fargate task per job object jobs/{jobId}.json.

Env: JOB_KEY, BUCKET, TABLE, DATA_ROLE_ARN (required); PUBLIC_URL, JOB_TIMEOUT_S (default 10800), WORK_ROOT (default /work).
analyze: claim the upload (claim token on UP#), download it, run the COT (run_account.sh, streamed) or CUR
(stage_cur/datasets/check) pipeline, then agents and report, record steps.<name> on ACCT#{acct}/UP#{uploadId}, and
publish a version unless the check failed (META and UP in one transaction).
rerun-agents: rerun agents and report over a published version and refresh ai_<source>.
The task role reads only the job object and writes DynamoDB; every other S3 call uses a session assumed from
DATA_ROLE_ARN whose policy allows only accounts/{acct}/* and data/whats_new_feed.json.
Children run in their own process group, which is killed after every child (and on timeout or SIGTERM); with the
subreaper bit set, orphans that left the group are killed too. Children that read the upload or its derived data get no
AWS_* variables and an empty per-job HOME; only the agents child keeps the task role (Bedrock). The worker creates its
own files in the job dir with O_EXCL|O_NOFOLLOW and reads published files with O_NOFOLLOW + fstat.
Logs carry step names, states, reason kinds, exception class names and counts only, never reason text.
Exit: 0 job recorded (done, needs_review, skipped); 1 job failed; 2 bad job key, config or job ids (nothing written).
"""
import datetime, json, os, queue, re, shutil, signal, stat, subprocess, sys, threading, time, uuid

APP = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RUN_ACCOUNT = os.path.join(APP, "run_account.sh")
CUR_DIR = os.path.join(APP, "cur")
AI_STEP = os.path.join(APP, "worker", "ai_step.py")

# fullmatch everywhere; [0-9] not \d (which takes any Unicode digit)
JOB_KEY_RE = re.compile(r"jobs/[A-Za-z0-9_-]{1,80}\.json")
ACCT_RE = re.compile(r"[A-Za-z0-9_-]{1,64}")
UPLOAD_RE = re.compile(r"u[0-9]{14}[0-9a-f]{6}")
VERSION_RE = re.compile(r"v[0-9]{14}[0-9a-f]{6}")
DATA_FILE_RE = re.compile(r"[A-Za-z0-9_.-]{1,128}\.json")
PARQUET_RE = re.compile(r"part-[0-9]{1,6}\.parquet")
STEP_RE = re.compile(r"STEP (validate|extract|datasets|check) (running|done|failed|needs_review)")
STEPS = ("validate", "extract", "datasets", "check", "agents", "report", "publish")
SOURCES = ("cot", "cur")
# upload size limits (bytes); the server checks the declared size and the completed object, this is defence in depth
SIZE_LIMITS = {"cot": 209715200, "cur": 5368709120}
TOO_BIG_REASON = "업로드 크기가 너무 큽니다"
KINDS = ("analyze", "rerun-agents")
# tag_signals.json: versions published before it existed get it on their first rerun (ai_step computes it)
RERUN_FILES = ("agent_notes.json", "sessions.json", "market.json", "tag_signals.json")
FEED_KEY = "data/whats_new_feed.json"
MAX_JOB_BYTES = 64 * 1024
MAX_STDOUT_CHARS = 1 << 20
CHUNK = 8 << 20
REFRESH_S = 600
EXIT_GRACE_S = 1.0  # after the lead child exits, how long to wait for output still in the pipe
TIMEOUT_REASON = "처리 시간 초과"
ABORT_REASON = "처리 중단"
DEFAULT_TIMEOUT = 10800
PR_SET_DUMPABLE = 4
PR_SET_CHILD_SUBREAPER = 36
KILLPG = os.killpg  # tests replace it
SUBREAPER = [False]  # set by harden_process; enables the orphan sweep
TERM = {"armed": False, "got": False}  # SIGTERM raises Aborted only while armed; otherwise it is remembered


class JobTimeout(Exception):
    pass


class Aborted(BaseException):
    """SIGTERM (ECS stop). A BaseException, so no `except Exception` on the way can swallow it."""


class OrphanProcesses(Exception):
    """A descendant of an untrusted child survived the sweep: no later child may run."""


class PipelineError(Exception):
    """A stage exited with an unexpected code (the reason stays generic)."""


class MissingAccount(Exception):
    """META is missing: the server creates it on the first upload (R4)."""


class StaleVersion(Exception):
    """META already points at a newer version."""


class ClaimLost(Exception):
    """The upload is no longer ours (discarded, or claimed by another delivery)."""


class Rejected(Exception):
    """The upload is invalid; reason is the stage's own reason text (stored, never logged)."""

    def __init__(self, reason):
        super().__init__("rejected")
        self.reason = reason


class CheckFailed(Exception):
    pass


def log(event, **kw):
    kw["event"] = event
    print(json.dumps(kw, ensure_ascii=False, sort_keys=True), file=sys.stderr, flush=True)


def err_code(e):
    try:
        return e.response["Error"]["Code"]
    except (AttributeError, KeyError, TypeError):
        return ""


def harden_process():
    """Non-dumpable: same-uid children cannot read /proc/<ppid>/environ or mem. Child subreaper: descendants that
    leave their process group (setsid) are re-parented here when their parent exits, so the sweep can kill them.
    Best effort."""
    ok = True
    try:
        import ctypes
        libc = ctypes.CDLL(None, use_errno=True)
        if libc.prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) != 0:
            raise OSError(ctypes.get_errno(), "prctl")
        if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
            raise OSError(ctypes.get_errno(), "prctl")
        SUBREAPER[0] = True
    except Exception as e:
        log("harden_failed", cls=type(e).__name__)
        ok = False
    return ok


def _ppid(pid):
    try:
        with open("/proc/%s/stat" % pid, "rb") as f:
            s = f.read()
        return int(s[s.rindex(b")") + 2:].split()[1])
    except (OSError, ValueError, IndexError):
        return None


def direct_children():
    me = os.getpid()
    return [int(d) for d in os.listdir("/proc") if d.isdigit() and int(d) != me and _ppid(d) == me]


def _reap(pid):
    try:
        os.waitpid(pid, os.WNOHANG)
    except OSError:
        pass


ORPHAN_LIMIT_S = 5.0
CHILDREN, KILL, REAP = direct_children, os.kill, _reap  # tests replace them


def sweep_orphans():
    """Kills and reaps every process whose parent is this one (once the lead child was reaped, only orphans
    re-parented to the subreaper are left) until /proc shows none, for ORPHAN_LIMIT_S at most. Returns the number
    killed; raises OrphanProcesses when some are still alive at the limit."""
    n, end = 0, time.monotonic() + ORPHAN_LIMIT_S
    while True:
        found = CHILDREN()
        if not found:
            return n
        if time.monotonic() >= end:
            log("orphans_alive", count=len(found))
            raise OrphanProcesses()
        for pid in found:
            try:
                KILL(pid, signal.SIGKILL)
                n += 1
            except OSError:
                pass
            REAP(pid)
        time.sleep(0.01)


# ---------- files in the job dir ----------

def remove_path(p):
    if os.path.islink(p) or (os.path.lexists(p) and not os.path.isdir(p)):
        os.unlink(p)
    elif os.path.isdir(p):
        shutil.rmtree(p)


def open_private(p):
    """The only way the worker creates a file in the job dir: whatever is at p goes, then a new file is created
    without following a symlink planted in between."""
    remove_path(p)
    return os.fdopen(os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb")


def write_private(p, data):
    with open_private(p) as f:
        f.write(data.encode("utf-8") if isinstance(data, str) else data)


def private_dir(p):
    remove_path(p)
    os.mkdir(p, 0o700)
    return p


def open_dir(base, *parts):
    """Directory fd walked from base without following symlinks at any component below base."""
    fd = os.open(base, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    for part in parts:
        try:
            nfd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
        finally:
            os.close(fd)
        fd = nfd
    return fd


def open_regular(dfd, name):
    """Read fd of a regular file in dfd, or None (symlink, fifo, device, missing). fstat on the opened descriptor."""
    try:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=dfd)
    except OSError:
        return None
    if not stat.S_ISREG(os.fstat(fd).st_mode):
        os.close(fd)
        return None
    return fd


def each_regular(base, parts, pattern, fn, names=None):
    """fn(name, file) for each regular file matching pattern in base/parts (or the given names), opened, handed over
    and closed one at a time; returns the count. Nothing when the dir is missing or a symlink."""
    try:
        dfd = open_dir(base, *parts)
    except OSError:
        return 0
    n = 0
    try:
        for name in (names if names is not None else sorted(os.listdir(dfd))):
            if not pattern.fullmatch(name):
                continue
            fd = open_regular(dfd, name)
            if fd is None:
                continue
            try:
                f = os.fdopen(fd, "rb")
            except BaseException:
                os.close(fd)
                raise
            with f:
                fn(name, f)
            n += 1
        return n
    finally:
        os.close(dfd)


# ---------- DynamoDB attribute values ----------

def to_av(v):
    if v is None:
        return {"NULL": True}
    if isinstance(v, bool):
        return {"BOOL": v}
    if isinstance(v, (int, float)):
        return {"N": repr(v)}
    if isinstance(v, str):
        return {"S": v}
    if isinstance(v, dict):
        return {"M": {str(k): to_av(x) for k, x in v.items()}}
    if isinstance(v, (list, tuple)):
        return {"L": [to_av(x) for x in v]}
    raise TypeError(type(v).__name__)


def from_av(av):
    (t, v), = av.items()
    if t == "NULL":
        return None
    if t == "BOOL":
        return bool(v)
    if t == "N":
        return int(v) if re.fullmatch(r"-?[0-9]+", v) else float(v)
    if t == "S":
        return v
    if t == "M":
        return {k: from_av(x) for k, x in v.items()}
    if t == "L":
        return [from_av(x) for x in v]
    raise TypeError(t)


def session_policy(bucket, acct):
    return {"Version": "2012-10-17", "Statement": [
        {"Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject"],
         "Resource": "arn:aws:s3:::%s/accounts/%s/*" % (bucket, acct)},
        {"Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::%s" % bucket,
         "Condition": {"StringLike": {"s3:prefix": "accounts/%s/*" % acct}}},
        {"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::%s/%s" % (bucket, FEED_KEY)},
    ]}


def default_s3_factory(creds):
    import boto3
    return boto3.client("s3", aws_access_key_id=creds["AccessKeyId"], aws_secret_access_key=creds["SecretAccessKey"],
                        aws_session_token=creds["SessionToken"])


def _as_utc(x):
    if isinstance(x, datetime.datetime):
        return x if x.tzinfo else x.replace(tzinfo=datetime.timezone.utc)
    try:
        return datetime.datetime.fromisoformat(str(x).replace("Z", "+00:00"))
    except ValueError:
        return None


class DataSession:
    """Account-scoped S3 client, re-assumed when the credentials are within REFRESH_S of expiry (role chaining caps
    sessions at 1 h, so a long job refreshes between phases)."""

    def __init__(self, sts, factory, role, bucket, acct, upload_id, timeout_s):
        self.sts, self.factory, self.role, self.bucket = sts, factory, role, bucket
        self.policy = json.dumps(session_policy(bucket, acct), separators=(",", ":"))
        self.name = "worker-" + upload_id
        self.duration = max(900, min(43200, timeout_s + 600))
        self.client, self.expiry = None, None

    def _assume(self, duration):
        return self.sts.assume_role(RoleArn=self.role, RoleSessionName=self.name, Policy=self.policy,
                                    DurationSeconds=duration)

    def get(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        if self.client is not None and self.expiry is not None and (self.expiry - now).total_seconds() > REFRESH_S:
            return self.client
        try:
            r = self._assume(self.duration)
        except Exception as e:
            # a chained role session (task role -> data role) refuses more than 1 h
            if err_code(e) != "ValidationError" or self.duration <= 3600:
                raise
            self.duration = 3600
            r = self._assume(self.duration)
        c = r["Credentials"]
        self.expiry = _as_utc(c.get("Expiration"))
        self.client = self.factory(c)
        log("data_session", refreshed=True)
        return self.client


def s3_download(client, bucket, key, path):
    """Returns False when the object does not exist."""
    try:
        r = client.get_object(Bucket=bucket, Key=key)
    except Exception as e:
        if err_code(e) in ("NoSuchKey", "404", "NotFound"):
            return False
        raise
    body = r["Body"]
    with open_private(path) as f:
        while True:
            buf = body.read(CHUNK)
            if not buf:
                break
            f.write(buf)
    return True


def s3_upload_file(client, bucket, key, f, ctype):
    client.put_object(Bucket=bucket, Key=key, Body=f, ContentType=ctype)


def exited(p):
    """True once the child has exited, without reaping it (WNOWAIT), so its pid stays reserved for killpg."""
    try:
        r = os.waitid(os.P_PID, p.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
        return r is not None
    except (ChildProcessError, OSError, TypeError, AttributeError):
        return p.poll() is not None


def last_json(stdout, key):
    found = None
    for line in (stdout or "").splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            v = json.loads(line)
        except ValueError:
            continue
        if isinstance(v, dict) and key in v:
            found = v
    return found


def _id(job, k, rx):
    v = job.get(k)
    return isinstance(v, str) and rx.fullmatch(v) is not None


def validate_job(job):
    if not isinstance(job, dict):
        return False
    if job.get("kind") not in KINDS or job.get("source") not in SOURCES:
        return False
    if not _id(job, "accountId", ACCT_RE) or not _id(job, "uploadId", UPLOAD_RE):
        return False
    if job["kind"] == "rerun-agents":
        return _id(job, "versionId", VERSION_RE)
    return True


def arm():
    """From here SIGTERM raises Aborted; one that arrived while disarmed raises now."""
    if TERM["got"]:
        TERM["armed"] = False
        raise Aborted()
    TERM["armed"] = True


def disarm():
    """Recording (claim, transaction, failure path, lock release) runs to the end; a SIGTERM is only remembered."""
    TERM["armed"] = False


def _on_sigterm(signum, frame):
    TERM["got"] = True
    if TERM["armed"]:
        TERM["armed"] = False
        raise Aborted()


class Job:
    def __init__(self, env, job, s3, ddb, sts, runner, s3_factory):
        self.bucket, self.table = env["BUCKET"], env["TABLE"]
        self.url = env.get("PUBLIC_URL", "")
        try:
            self.timeout_s = max(0, int(env.get("JOB_TIMEOUT_S") or DEFAULT_TIMEOUT))
        except ValueError:
            self.timeout_s = DEFAULT_TIMEOUT
        self.deadline = time.monotonic() + self.timeout_s
        self.kind, self.acct, self.upload_id, self.src = job["kind"], job["accountId"], job["uploadId"], job["source"]
        self.version_id = job.get("versionId") if self.kind == "rerun-agents" else "v" + self.upload_id[1:]
        self.ddb, self.popen = ddb, runner
        self.data = DataSession(sts, s3_factory or default_s3_factory, env["DATA_ROLE_ARN"], self.bucket, self.acct,
                                self.upload_id, self.timeout_s)
        root = env.get("WORK_ROOT") or "/work"
        self.wd = os.path.join(root, self.upload_id if self.kind == "analyze" else self.upload_id + "-rerun")
        self.steps = {s: "waiting" for s in STEPS}
        self.pk = "ACCT#" + self.acct
        self.prefix = "accounts/%s/" % self.acct
        self.token = uuid.uuid4().hex

    # ---------- children ----------

    def remaining(self):
        left = self.deadline - time.monotonic()
        if left <= 0:
            raise JobTimeout()
        return left

    def child_env(self, keep_aws):
        """keep_aws only for the agents child (Bedrock via the task role). Every other child reads untrusted data:
        no AWS_* variable at all (credentials, container credential URIs, region) and an empty per-job HOME."""
        if keep_aws:
            e = dict(os.environ)
        else:
            e = {k: v for k, v in os.environ.items() if not k.startswith("AWS_")}
            e["HOME"] = self.path("home")
        e["PYTHONNOUSERSITE"] = "1"
        e.setdefault("AS_OF", datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d"))
        return e

    @staticmethod
    def killpg(p):
        try:
            KILLPG(p.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError, TypeError):
            pass

    def end_group(self, p):
        """After every child, however it ended (its group was already killed): reap the lead and, with the subreaper
        bit, kill any orphan that left the group; OrphanProcesses when one survives."""
        try:
            p.wait(timeout=5)
        except Exception:
            pass
        if SUBREAPER[0]:
            n = sweep_orphans()
            if n:
                log("orphans_killed", count=n)

    def no_orphans(self):
        """Before any child (above all the credentialed agents child): no descendant of an earlier one is alive."""
        if SUBREAPER[0] and CHILDREN():
            sweep_orphans()

    def spawn(self, cmd, keep_aws=False, on_line=None):
        """Runs one child in its own session; stdout is read line by line (on_line sees each line as it arrives) and
        the job deadline is enforced while reading. Once the lead child has exited, output still in the pipe is
        drained for EXIT_GRACE_S at most: a descendant holding stdout open cannot hold the step. Returns
        (returncode, stdout)."""
        if TERM["got"]:
            TERM["armed"] = False
            raise Aborted()
        self.remaining()
        self.no_orphans()
        p = self.popen(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                       env=self.child_env(keep_aws), cwd=self.wd, start_new_session=True,
                       encoding="utf-8", errors="replace")
        lines, size = [], 0
        grace_end = None
        try:
            q = queue.Queue()

            def pump():
                try:
                    for line in p.stdout:
                        q.put(line)
                except Exception:
                    pass
                finally:
                    q.put(None)
            threading.Thread(target=pump, daemon=True).start()
            while True:
                now = time.monotonic()
                if now >= self.deadline:
                    raise JobTimeout()
                if grace_end is None and exited(p):
                    grace_end = now + EXIT_GRACE_S
                if grace_end is not None and now >= grace_end:
                    break
                wait = min(0.1, self.deadline - now, (grace_end - now) if grace_end is not None else 0.1)
                try:
                    line = q.get(timeout=max(0.001, wait))
                except queue.Empty:
                    continue
                if line is None:
                    break
                line = line.rstrip("\r\n")
                if size < MAX_STDOUT_CHARS:
                    lines.append(line)
                    size += len(line) + 1
                if on_line is not None:
                    on_line(line)
            while not exited(p):  # stdout closed but the lead still runs
                if time.monotonic() >= self.deadline:
                    raise JobTimeout()
                time.sleep(0.02)
        finally:
            # first: the lead is a zombie (not reaped) here, so its pgid cannot have been reused
            self.killpg(p)
            self.end_group(p)
            try:
                p.stdout.close()
            except Exception:
                pass
        return p.wait(timeout=5), "\n".join(lines)

    # ---------- DynamoDB ----------

    def key(self, sk):
        return {"pk": {"S": self.pk}, "sk": {"S": sk}}

    def req(self, sk, expr, values=None, names=None, cond=None):
        kw = {"TableName": self.table, "Key": self.key(sk), "UpdateExpression": expr}
        if values:
            kw["ExpressionAttributeValues"] = {k: to_av(v) for k, v in values.items()}
        if names:
            kw["ExpressionAttributeNames"] = names
        if cond:
            kw["ConditionExpression"] = cond
        return kw

    def update(self, sk, expr, values=None, names=None, cond=None):
        return self.ddb.update_item(**self.req(sk, expr, values, names, cond))

    def up_req(self, expr, values, names):
        """Every UP write after the claim: only while the upload is running and still carries our claim token."""
        values = dict(values, **{":tok": self.token, ":running": "running"})
        names = dict(names or {}, **{"#st": "status"})
        return self.req("UP#" + self.upload_id, expr, values, names, "claim = :tok AND #st = :running")

    def update_up(self, expr, values, names):
        try:
            self.ddb.update_item(**self.up_req(expr, values, names))
        except Exception as e:
            if err_code(e) == "ConditionalCheckFailedException":
                raise ClaimLost()
            raise

    def set_step(self, name, state):
        self.update_up("SET steps.#n = :s", {":s": state}, {"#n": name})
        self.steps[name] = state
        log("step", step=name, state=state)

    def running_step(self):
        for s in STEPS:
            if self.steps[s] == "running":
                return s
        return None

    def s3_key(self, rel):
        k = self.prefix + rel
        assert k.startswith(self.prefix) and ".." not in k.split("/")
        return k

    def path(self, *p):
        return os.path.join(self.wd, *p)

    def fresh_workspace(self):
        shutil.rmtree(self.wd, ignore_errors=True)
        os.makedirs(self.wd, mode=0o700)
        for d in ("work", "out", "home"):
            os.makedirs(self.path(d), mode=0o700)

    def feed(self, s3):
        p = self.path("whats_new_feed.json")
        return p if s3_download(s3, self.bucket, FEED_KEY, p) else None

    def set_step_if(self, name, state):
        # reruns never touch the original upload's steps (only its ai flag, at the end of _rerun)
        if self.kind == "analyze":
            self.set_step(name, state)

    def ai_steps(self, data_dir):
        """agents (task role, for Bedrock) then report (no credentials); returns the ai flag. The cache and the feed
        are created only now, after every untrusted step ended."""
        self.set_step_if("agents", "running")
        cache = private_dir(self.path("agent_cache"))
        feed = self.feed(self.data.get()) if self.src == "cot" else None
        rc, out = self.spawn(["python3", "-I", AI_STEP, "agents", data_dir, self.src, feed or "-", cache], keep_aws=True)
        res = last_json(out, "ai")
        if rc != 0 or not res or res["ai"] not in ("ok", "none"):
            raise PipelineError("agents")
        ai = res["ai"]
        log("agents", ai=ai)
        self.set_step_if("agents", "done")
        self.set_step_if("report", "running")
        md = self.path("report.md")
        remove_path(md)
        rc, _ = self.spawn(["python3", "-I", AI_STEP, "report", data_dir, md, self.url])
        if rc != 0:
            raise PipelineError("report")
        if not each_regular(self.wd, (), re.compile(r"report\.md"), lambda n, f: None, names=["report.md"]):
            raise PipelineError("report")
        self.set_step_if("report", "done")
        return ai

    def upload_report(self, s3, vbase):
        key = self.s3_key(vbase + "report.md")
        if not each_regular(self.wd, (), re.compile(r"report\.md"), names=["report.md"],
                            fn=lambda n, f: s3_upload_file(s3, self.bucket, key, f, "text/markdown; charset=utf-8")):
            raise PipelineError("report")

    # ---------- analyze ----------

    def claim(self):
        """Only a queued upload of this source runs, once: a duplicate delivery finds it running and leaves."""
        try:
            # claimedAt: the server's stale rule counts the job timeout from here (the Billing BFF)
            at = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
            self.update("UP#" + self.upload_id,
                        "SET #st = :running, steps = :steps, claim = :tok, claimedAt = :at REMOVE reason",
                        {":running": "running", ":queued": "queued", ":src": self.src, ":steps": self.steps,
                         ":tok": self.token, ":at": at},
                        {"#st": "status", "#src": "source"}, "#st = :queued AND #src = :src")
        except Exception as e:
            if err_code(e) == "ConditionalCheckFailedException":
                log("skip", why="not queued")
                return False
            raise
        return True

    def meta(self):
        r = self.ddb.get_item(TableName=self.table, Key=self.key("META"), ConsistentRead=True)
        if not r.get("Item"):
            raise MissingAccount()
        return {k: from_av(v) for k, v in r["Item"].items()}

    def analyze(self):
        disarm()
        if not self.claim():
            return 0
        outcome, reason, kind, cls = "failed", None, None, None
        try:
            try:
                arm()  # a SIGTERM during the claim raises here
                self._analyze()
                outcome = "done"
            finally:
                disarm()  # from here on SIGTERM is only remembered: the outcome is recorded in full
        except CheckFailed:
            outcome, kind = "needs_review", "check_failed"
        except ClaimLost:
            outcome, kind = "lost", "claim_lost"
        except Rejected as e:
            reason, kind = e.reason[:300], "rejected"
        except JobTimeout:
            reason, kind = TIMEOUT_REASON, "timeout"
        except Aborted:
            reason, kind = ABORT_REASON, "aborted"
        except Exception as e:
            cls = type(e).__name__
            reason, kind = "내부 오류: " + cls, "internal"
        finally:
            shutil.rmtree(self.wd, ignore_errors=True)
        try:
            if outcome in ("failed", "needs_review"):
                try:
                    if outcome == "failed":
                        self.fail_running()
                    self.finish(outcome, reason)
                except ClaimLost:
                    outcome, kind = "lost", "claim_lost"
        finally:
            self.release_lock()  # even when recording failed (conditioned on our uploadId)
        log("job", status=outcome, kind=kind, cls=cls)
        return 1 if outcome == "failed" else 0

    def _analyze(self):
        self.fresh_workspace()
        self.set_step("validate", "running")
        meta = self.meta()
        name = meta.get("name") if isinstance(meta.get("name"), str) and meta.get("name") else self.acct
        s3 = self.data.get()
        src_key = self.s3_key("uploads/%s/source.zip" % self.upload_id)
        try:
            size = int(s3.head_object(Bucket=self.bucket, Key=src_key).get("ContentLength") or 0)
        except Exception as e:
            if err_code(e) in ("NoSuchKey", "404", "NotFound"):
                raise PipelineError("missing upload")
            raise
        if size > SIZE_LIMITS[self.src]:
            raise Rejected(TOO_BIG_REASON)  # never downloaded
        if not s3_download(s3, self.bucket, src_key, self.path("source.zip")):
            raise PipelineError("missing upload")
        if self.src == "cot":
            write_private(self.path("names.json"), json.dumps({self.acct: name}, ensure_ascii=False))
            self.cot_pipeline()
        else:
            prev, cot_spend = self.cur_inputs(s3, meta)
            self.cur_pipeline(prev, cot_spend)
        ai = self.ai_steps(self.path("out"))
        self.publish(ai, name)

    def fail_running(self):
        """Marks the running step failed (or, between steps, the next one); later steps stay waiting."""
        if "failed" in self.steps.values():
            return
        s = self.running_step()
        if s is None:
            s = next((x for x in STEPS if self.steps[x] == "waiting"), None)
        if s is not None:
            self.set_step(s, "failed")

    def finish(self, status, reason):
        vals = {":st": status}
        expr = "SET #st = :st"
        if reason is not None:
            expr += ", reason = :r"
            vals[":r"] = reason
        self.update_up(expr, vals, None)

    def release_lock(self):
        try:
            self.ddb.delete_item(TableName=self.table, Key=self.key("LOCK#" + self.src),
                                 ConditionExpression="uploadId = :u", ExpressionAttributeValues={":u": {"S": self.upload_id}})
        except Exception as e:
            if err_code(e) != "ConditionalCheckFailedException":
                raise
            log("lock", released=False)

    def on_step_line(self, line):
        m = STEP_RE.fullmatch(line.strip())
        if m and self.steps[m.group(1)] != m.group(2):
            self.set_step(m.group(1), m.group(2))

    def cot_pipeline(self):
        rc, out = self.spawn(["bash", RUN_ACCOUNT, self.path("source.zip"), self.path("work"), self.path("out"), self.acct,
                              self.path("names.json")], on_line=self.on_step_line)
        if rc == 0 and self.steps["check"] == "done":
            return
        if rc == 1 and self.steps["check"] == "needs_review":
            raise CheckFailed()
        if rc == 3:
            r = last_json(out, "reason")
            if r and isinstance(r["reason"], str):
                raise Rejected(r["reason"])
        raise PipelineError("exit %d" % rc)

    def cur_inputs(self, s3, meta):
        prev = cot_spend = None
        cur = meta.get("current_cur")
        if isinstance(cur, str) and VERSION_RE.fullmatch(cur):
            private_dir(self.path("prev"))
            if s3_download(s3, self.bucket, self.s3_key("cur/%s/normalized/part-0.parquet" % cur),
                           self.path("prev", "part-0.parquet")):
                prev = self.path("prev")
        cot = meta.get("current_cot")
        if isinstance(cot, str) and VERSION_RE.fullmatch(cot):
            if s3_download(s3, self.bucket, self.s3_key("versions/%s/data/spend_monthly.json" % cot), self.path("cot_spend.json")):
                cot_spend = self.path("cot_spend.json")
        log("cur_inputs", prev=prev is not None, cot_spend=cot_spend is not None)
        return prev, cot_spend

    def cur(self, script, *args):
        # never import normalize here: it sets a process-wide mallopt at import
        return self.spawn(["python3", "-I", os.path.join(CUR_DIR, script)] + list(args))

    def cur_pipeline(self, prev, cot_spend):
        work, out = self.path("work"), self.path("out")
        norm = os.path.join(work, "cur_norm")
        rc, so = self.cur("stage_cur.py", self.path("source.zip"), work, self.acct, *([prev] if prev else []))
        if rc == 3:
            r = last_json(so, "reason")
            raise Rejected(r["reason"]) if r and isinstance(r["reason"], str) else PipelineError("exit 3")
        if rc != 0:
            raise PipelineError("exit %d" % rc)
        self.set_step("validate", "done")
        self.set_step("extract", "running")
        summary = last_json(so, "months")
        sargs = []
        if summary is not None:
            write_private(self.path("summary.json"), json.dumps(summary, ensure_ascii=False))
            sargs = [self.path("summary.json")]
        self.set_step("extract", "done")
        self.set_step("datasets", "running")
        rc, _ = self.cur("datasets.py", norm, out, self.acct, *sargs)
        if rc != 0:
            raise PipelineError("exit %d" % rc)
        self.set_step("datasets", "done")
        self.set_step("check", "running")
        rc, _ = self.cur("check.py", norm, out, self.acct, *([cot_spend] if cot_spend else []))
        if rc == 0:
            self.set_step("check", "done")
            return
        if rc == 1 and os.path.isfile(os.path.join(out, "checks.json")):
            self.set_step("check", "needs_review")
            raise CheckFailed()
        raise PipelineError("exit %d" % rc)

    def period(self):
        got = []
        try:
            each_regular(self.wd, ("out",), re.compile(r"period\.json"), names=["period.json"],
                         fn=lambda n, f: got.append(json.loads(f.read().decode("utf-8"))))
        except (OSError, ValueError):
            return None
        rows = got[0] if got else None
        for r in rows if isinstance(rows, list) else []:
            if isinstance(r, dict) and r.get("entity") == self.acct and r.get("source", self.src) == self.src:
                return {"first": r.get("first"), "last": r.get("last"), "months": list(r.get("months") or [])}
        return None

    def publish(self, ai, name):
        self.set_step("publish", "running")
        s3 = self.data.get()
        vbase = "versions/%s/" % self.version_id
        n = 0

        def data_file(fn, f):
            rel = vbase + ("checks.json" if fn == "checks.json" else "data/" + fn)
            s3_upload_file(s3, self.bucket, self.s3_key(rel), f, "application/json")
        n += each_regular(self.wd, ("out",), DATA_FILE_RE, data_file)
        self.upload_report(s3, vbase)
        n += 1
        if self.src == "cur":
            n += each_regular(self.wd, ("work", "cur_norm"), PARQUET_RE, lambda fn, f: s3_upload_file(
                s3, self.bucket, self.s3_key("cur/%s/normalized/%s" % (self.version_id, fn)), f,
                "application/vnd.apache.parquet"))
        log("uploaded", files=n)
        period = self.period()
        s = self.src
        # one transaction: META points at the version only if UP is still ours and running (no discarded version
        # ever becomes current); <= keeps a redelivered publish of the same version idempotent
        meta = self.req("META", "SET current_%s = :v, period_%s = :p, ai_%s = :a, #nm = if_not_exists(#nm, :nm)" % (s, s, s),
                        {":v": self.version_id, ":p": period, ":a": ai, ":nm": name}, {"#nm": "name"},
                        "attribute_exists(pk) AND (attribute_not_exists(current_%s) OR current_%s <= :v)" % (s, s))
        up = self.up_req("SET #st = :st, published = :t, versionId = :v, period = :p, ai = :a, steps.#n = :done",
                         {":st": "done", ":t": True, ":v": self.version_id, ":p": period, ":a": ai, ":done": "done"},
                         {"#n": "publish"})
        disarm()  # the transaction and its bookkeeping are not interrupted
        try:
            self.ddb.transact_write_items(TransactItems=[{"Update": meta}, {"Update": up}])
        except Exception as e:
            if err_code(e) != "TransactionCanceledException":
                raise
            codes = [(r or {}).get("Code") for r in (getattr(e, "response", {}).get("CancellationReasons") or [])]
            if len(codes) > 1 and codes[1] == "ConditionalCheckFailed":
                raise ClaimLost()
            if codes and codes[0] == "ConditionalCheckFailed":
                r = self.ddb.get_item(TableName=self.table, Key=self.key("META"), ConsistentRead=True)
                raise MissingAccount() if not r.get("Item") else StaleVersion()
            raise
        self.steps["publish"] = "done"
        log("step", step="publish", state="done")

    # ---------- rerun-agents ----------

    def rerun(self):
        try:
            try:
                arm()
                return self._rerun()
            finally:
                disarm()
        except JobTimeout:
            log("job", status="failed", kind="timeout")
            return 1
        except Aborted:
            log("job", status="failed", kind="aborted")
            return 1
        except Exception as e:
            log("job", status="failed", kind="internal", cls=type(e).__name__)
            return 1
        finally:
            shutil.rmtree(self.wd, ignore_errors=True)

    def _rerun(self):
        self.fresh_workspace()
        s3 = self.data.get()
        data = self.path("out")
        prefix = self.s3_key("versions/%s/data/" % self.version_id)
        n, token = 0, None
        while True:
            kw = {"Bucket": self.bucket, "Prefix": prefix}
            if token:
                kw["ContinuationToken"] = token
            r = s3.list_objects_v2(**kw)
            for o in r.get("Contents") or []:
                rel = o["Key"][len(prefix):]
                if DATA_FILE_RE.fullmatch(rel) and s3_download(s3, self.bucket, o["Key"], os.path.join(data, rel)):
                    n += 1
            if not r.get("IsTruncated"):
                break
            token = r.get("NextContinuationToken")
        log("rerun_inputs", files=n)
        if not n:
            raise PipelineError("no data")
        ai = self.ai_steps(data)
        s3 = self.data.get()
        vbase = "versions/%s/" % self.version_id
        each_regular(self.wd, ("out",), DATA_FILE_RE, names=list(RERUN_FILES), fn=lambda fn, f: s3_upload_file(
            s3, self.bucket, self.s3_key(vbase + "data/" + fn), f, "application/json"))
        self.upload_report(s3, vbase)
        disarm()
        # the version's own UP row first: a later rollback to this version copies UP.ai into META (M4)
        try:
            self.update("UP#" + self.upload_id, "SET ai = :a", {":a": ai}, None, "attribute_exists(pk)")
        except Exception as e:
            if err_code(e) != "ConditionalCheckFailedException":
                raise
            log("rerun_up", updated=False)
        try:
            self.update("META", "SET ai_%s = :a" % self.src, {":a": ai, ":v": self.version_id}, None,
                        "current_%s = :v" % self.src)
        except Exception as e:
            if err_code(e) != "ConditionalCheckFailedException":
                raise
            log("rerun_meta", updated=False)
        log("job", status="done", kind="rerun-agents", ai=ai)
        return 0


def _install_sigterm():
    TERM["armed"], TERM["got"] = False, False
    try:
        return signal.signal(signal.SIGTERM, _on_sigterm)
    except ValueError:  # not the main thread
        return None


def main(env=os.environ, s3=None, ddb=None, sts=None, runner=subprocess.Popen, s3_factory=None, harden=harden_process):
    harden()
    key = env.get("JOB_KEY") or ""
    if not JOB_KEY_RE.fullmatch(key):
        log("bad_job_key")
        return 2
    if not all(env.get(k) for k in ("BUCKET", "TABLE", "DATA_ROLE_ARN")):
        log("bad_config")
        return 2
    if s3 is None or ddb is None or sts is None:
        import boto3
        s3 = s3 or boto3.client("s3")
        ddb = ddb or boto3.client("dynamodb")
        sts = sts or boto3.client("sts")
    try:
        body = s3.get_object(Bucket=env["BUCKET"], Key=key)["Body"].read(MAX_JOB_BYTES + 1)
        job = json.loads(body) if len(body) <= MAX_JOB_BYTES else None
    except Exception as e:
        log("bad_job", cls=type(e).__name__)
        return 2
    if not validate_job(job):
        log("bad_job", cls="invalid ids")
        return 2
    j = Job(env, job, s3, ddb, sts, runner, s3_factory)
    log("job_start", kind=j.kind, source=j.src)
    prev = _install_sigterm()
    try:
        return j.analyze() if j.kind == "analyze" else j.rerun()
    except Exception as e:  # recording the outcome itself failed
        log("error", cls=type(e).__name__, where="record")
        return 1
    finally:
        disarm()
        if prev is not None:
            signal.signal(signal.SIGTERM, prev)


if __name__ == "__main__":
    sys.exit(main())
