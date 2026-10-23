"""Archive and reconcile retained legacy news without mutating production data.

The archive is private, local, content-addressed and fsynced. Keep it outside
disposable worktrees. It preserves typed DynamoDB values and exact S3 bytes.
It is not a quality upgrade, deletion tool, model runner or all-mode switch.
"""

import argparse
import base64
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import sys
import tempfile
import time

import boto3
from boto3.dynamodb.conditions import Attr
from boto3.dynamodb.types import TypeSerializer
from botocore.config import Config

MAX_ROWS = 10000
MAX_PAGES = 1000
MAX_BODY = 2 * 1024 * 1024
MAX_METADATA = 1024 * 1024
MAX_SECONDS = 3600
MAX_ARCHIVE = 16 * 1024 * 1024
PREFIX = "shared/news/"


class ArchiveRejected(Exception):
    pass


@contextmanager
def wall_deadline(seconds):
    """Bound CLI network reads on POSIX, including continuously arriving bytes."""
    if signal.getitimer(signal.ITIMER_REAL) != (0.0, 0.0):
        raise ArchiveRejected("EXISTING_PROCESS_TIMER")
    previous = signal.getsignal(signal.SIGALRM)
    def expired(_signum, _frame):
        raise ArchiveRejected("RESUME_TIME_BUDGET")
    signal.signal(signal.SIGALRM, expired)
    try:
        signal.setitimer(signal.ITIMER_REAL, seconds)
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":"), allow_nan=False).encode("utf-8")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def typed_row(row):
    """Keep all DynamoDB types; binary values use base64, sets are sorted."""
    def normalize(av):
        kind, value = next(iter(av.items()))
        if kind == "B":
            value = base64.b64encode(value).decode("ascii")
        elif kind == "BS":
            value = sorted(base64.b64encode(item).decode("ascii") for item in value)
        elif kind in ("SS", "NS"):
            value = sorted(value)
        elif kind == "L":
            value = [normalize(item) for item in value]
        elif kind == "M":
            value = {key: normalize(item) for key, item in value.items()}
        return {kind: value}
    return normalize(TypeSerializer().serialize(row))["M"]


def legacy_identity(row):
    pk, sk = row.get("PK"), row.get("SK")
    if not isinstance(pk, str) or not pk.startswith("CRAWLER#"):
        raise ArchiveRejected("INVALID_LEGACY_IDENTITY")
    source = pk[8:]
    if (not 1 <= len(source) <= 128 or source.startswith("__")
            or not all(c.isalpha() or c.isdecimal() or c in "_-" for c in source)
            or not isinstance(sk, str) or not re.fullmatch(r"DOC#[0-9a-f]{16}", sk)
            or row.get("docHash", sk[4:]) != sk[4:]
            or row.get("sourceId", source) != source
            or row.get("type") != "news"
            or "schemaVersion" in row or "qualityStatus" in row):
        raise ArchiveRejected("INVALID_LEGACY_IDENTITY")
    key = f"{PREFIX}{source}/{sk[4:]}.md"
    if row.get("s3Key") != key:
        raise ArchiveRejected("INVALID_LEGACY_OBJECT")
    return source, {"PK": pk, "SK": sk}, key


def object_identity(response):
    etag, size = response.get("ETag"), response.get("ContentLength")
    version = response.get("VersionId")
    if (not isinstance(etag, str) or not etag or type(size) is not int
            or not 0 <= size <= MAX_BODY
            or (version is not None and not isinstance(version, str))):
        raise ArchiveRejected("INVALID_OBJECT_IDENTITY")
    return {"etag": etag, "bytes": size, "versionId": version}


class LocalArchive:
    def __init__(self, directory):
        self.root = Path(directory)
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        mode = self.root.lstat()
        if not stat.S_ISDIR(mode.st_mode) or mode.st_mode & 0o077:
            raise ArchiveRejected("ARCHIVE_DIRECTORY_MUST_BE_PRIVATE")

    @contextmanager
    def lock(self):
        fd = os.open(self.root / ".lock", os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            yield
        finally:
            os.close(fd)

    def put(self, data):
        if len(data) > MAX_ARCHIVE:
            raise ArchiveRejected("ARCHIVE_OBJECT_TOO_LARGE")
        sha = digest(data)
        target = self.root / sha
        fd, temporary = tempfile.mkstemp(prefix=".pending-", dir=self.root)
        try:
            with os.fdopen(fd, "wb") as file:
                file.write(data)
                file.flush()
                os.fsync(file.fileno())
            try:
                os.link(temporary, target)
            except FileExistsError:
                if self.get(sha, MAX_ARCHIVE) != data:
                    raise ArchiveRejected("ARCHIVE_HASH_CONFLICT")
            self.sync()
        finally:
            os.unlink(temporary)
        return sha

    def get(self, sha, limit):
        if not isinstance(sha, str) or not re.fullmatch("[0-9a-f]{64}", sha):
            raise ArchiveRejected("INVALID_ARCHIVE_HASH")
        fd = os.open(self.root / sha, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as file:
            if not stat.S_ISREG(os.fstat(file.fileno()).st_mode):
                raise ArchiveRejected("INVALID_ARCHIVE_FILE")
            data = file.read(limit + 1)
        if len(data) > limit or digest(data) != sha:
            raise ArchiveRejected("ARCHIVE_HASH_MISMATCH")
        return data

    def sync(self):
        fd = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def checkpoint(self, state):
        raw = encode(state)
        if len(raw) > 16 * 1024 * 1024:
            raise ArchiveRejected("JOURNAL_TOO_LARGE")
        # The immutable object precedes the small atomic journal pointer.
        sha = self.put(raw)
        temporary = self.root / "current.tmp"
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as file:
            file.write(sha + "\n")
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary, self.root / "current")
        self.sync()
        return sha

    def resume(self):
        try:
            fd = os.open(self.root / "current", os.O_RDONLY | os.O_NOFOLLOW)
        except FileNotFoundError:
            return None
        with os.fdopen(fd, "r") as file:
            sha = file.read(66).strip()
        return json.loads(self.get(sha, 16 * 1024 * 1024))


class Migration:
    def __init__(self, table, s3, functions, archive, binding, now=None):
        self.table, self.s3, self.functions = table, s3, functions
        self.archive, self.binding = archive, binding
        expected = binding.get("expectedLegacyCount")
        if type(expected) is not int or not 1 <= expected <= MAX_ROWS:
            raise ArchiveRejected("INVALID_EXPECTED_LEGACY_COUNT")
        self.now = now or (lambda: datetime.now(timezone.utc))
        self.deadline = time.monotonic() + MAX_SECONDS

    def check_time(self):
        if time.monotonic() >= self.deadline:
            raise ArchiveRejected("RESUME_TIME_BUDGET")

    def writer_state(self):
        self.check_time()
        result = {}
        for name in ("ttobak-crawler-news", "ttobak-news-worker", "ttobak-news-discover"):
            row = self.functions.get_function_configuration(FunctionName=name)
            env = row.get("Environment", {}).get("Variables", {})
            if row.get("State") != "Active" or row.get("LastUpdateStatus") != "Successful":
                raise ArchiveRejected("WRITER_UPDATE_INCOMPLETE")
            changed = datetime.fromisoformat(row["LastModified"])
            if (self.now() - changed).total_seconds() < row["Timeout"] + 30:
                raise ArchiveRejected("WRITER_NOT_DRAINED")
            if name == "ttobak-crawler-news":
                if (row["CodeSha256"] != self.binding["legacyWriterCodeSha256"]
                        or env.get("NEWS_LEGACY_WRITES") != "off"):
                    raise ArchiveRejected("LEGACY_WRITER_NOT_DISABLED")
            elif (env.get("NEWS_PIPELINE_MODE") not in ("off", "canary")
                  or (env.get("NEWS_PIPELINE_MODE") == "canary"
                      and not env.get("NEWS_CANARY_SOURCE_ID"))):
                raise ArchiveRejected("NATIVE_WRITERS_NOT_RESTRICTED")
            result[name] = {"revisionId": row["RevisionId"], "codeSha256": row["CodeSha256"],
                            "mode": env.get("NEWS_PIPELINE_MODE", "off"),
                            "canarySourceId": env.get("NEWS_CANARY_SOURCE_ID", "")}
        return result

    def scan(self):
        keys, protected = [], 0
        pages = self.table.meta.client.get_paginator("scan").paginate(
            TableName=self.table.name, ConsistentRead=True,
            FilterExpression=Attr("PK").begins_with("CRAWLER#")
                             & Attr("SK").begins_with("DOC#")
                             & (Attr("type").eq("news") | Attr("GSI4PK").eq("DOC#news")),
            ProjectionExpression="PK, SK, schemaVersion, qualityStatus",
            PaginationConfig={"PageSize": 100})
        for index, page in enumerate(pages, 1):
            self.check_time()
            if index > MAX_PAGES:
                raise ArchiveRejected("SCAN_PAGE_LIMIT")
            for row in page["Items"]:
                if "schemaVersion" in row or "qualityStatus" in row:
                    protected += 1
                else:
                    keys.append({"PK": row["PK"], "SK": row["SK"]})
                if len(keys) + protected > MAX_ROWS:
                    raise ArchiveRejected("SCAN_ROW_LIMIT")
        keys.sort(key=lambda key: (key["PK"], key["SK"]))
        if len({(key["PK"], key["SK"]) for key in keys}) != len(keys):
            raise ArchiveRejected("SCAN_CHANGED")
        return keys, protected

    def current(self, key):
        self.check_time()
        row = self.table.get_item(Key=key, ConsistentRead=True).get("Item")
        if not row:
            raise ArchiveRejected("LEGACY_ROW_CHANGED")
        return row

    def capture(self, key, writers):
        row = self.current(key)
        source, _, object_key = legacy_identity(row)
        if any(item["mode"] == "canary" and item["canarySourceId"] == source
               for item in writers.values()):
            raise ArchiveRejected("CANARY_OVERLAPS_LEGACY_SELECTION")
        config = self.current({"PK": key["PK"], "SK": "CONFIG"})
        head = self.s3.head_object(Bucket=self.binding["sourceBucket"], Key=object_key)
        identity = object_identity(head)
        args = {"Bucket": self.binding["sourceBucket"], "Key": object_key, "IfMatch": identity["etag"]}
        if identity["versionId"] is not None:
            args["VersionId"] = identity["versionId"]
        response = self.s3.get_object(**args)
        with response["Body"] as body:
            raw = body.read(MAX_BODY + 1)
        if object_identity(response) != identity or len(raw) != identity["bytes"]:
            raise ArchiveRejected("OBJECT_CHANGED")
        row_bytes, config_bytes = encode(typed_row(row)), encode(typed_row(config))
        if max(len(row_bytes), len(config_bytes)) > MAX_METADATA:
            raise ArchiveRejected("METADATA_TOO_LARGE")
        entry = {"key": key, "sourceId": source, "sourceKey": object_key,
                 "disposition": "RETAINED_LEGACY", "object": identity,
                 "rowSha256": digest(row_bytes), "configSha256": digest(config_bytes),
                 "bodySha256": digest(raw)}
        self.verify_current(entry)
        for data in (row_bytes, config_bytes, raw):
            self.archive.put(data)
        return entry

    def verify_current(self, entry):
        row = self.current(entry["key"])
        legacy_identity(row)
        config = self.current({"PK": entry["key"]["PK"], "SK": "CONFIG"})
        if (digest(encode(typed_row(row))) != entry["rowSha256"]
                or digest(encode(typed_row(config))) != entry["configSha256"]
                or object_identity(self.s3.head_object(Bucket=self.binding["sourceBucket"],
                                                      Key=entry["sourceKey"])) != entry["object"]):
            raise ArchiveRejected("LEGACY_SNAPSHOT_CHANGED")

    def run(self, apply=False):
        writers = self.writer_state()
        keys, protected = self.scan()
        if len(keys) != self.binding["expectedLegacyCount"]:
            raise ArchiveRejected("EXPECTED_LEGACY_COUNT_MISMATCH")
        if not apply:
            return {"legacyCount": len(keys), "protectedCount": protected, "productionWrites": 0}
        state = self.archive.resume()
        if state is None:
            state = {"schemaVersion": 1, "binding": self.binding, "keys": keys,
                     "protectedCount": protected, "writers": writers, "entries": [],
                     "phase": "CAPTURE", "verifiedCount": 0,
                     "startedAt": self.now().isoformat()}
            self.archive.checkpoint(state)
        if (state.get("schemaVersion") != 1 or state.get("binding") != self.binding
                or state.get("keys") != keys or state.get("writers") != writers
                or state.get("protectedCount") != protected):
            raise ArchiveRejected("RESUME_BINDING_CHANGED")
        if len(state["entries"]) > len(keys):
            raise ArchiveRejected("INVALID_JOURNAL")
        for index, key in enumerate(keys):
            self.check_time()
            if index < len(state["entries"]):
                entry = state["entries"][index]
                if entry["key"] != key:
                    raise ArchiveRejected("INVALID_JOURNAL")
            else:
                state["entries"].append(self.capture(key, writers))
            if (index + 1) % 25 == 0:
                if state["phase"] == "CAPTURE":
                    self.archive.checkpoint(state)
                print(f"Archived and checked {index + 1}/{len(keys)} legacy rows", flush=True)
        if state["phase"] not in ("CAPTURE", "RECONCILE", "COMPLETE"):
            raise ArchiveRejected("INVALID_JOURNAL")
        # Capture is resumable. Final verification must finish within ONE
        # invocation; an earlier verified prefix grants no trust on resume.
        state.update(phase="RECONCILE", verifiedCount=0)
        state.pop("status", None)
        state.pop("completedAt", None)
        self.archive.checkpoint(state)
        for index in range(len(keys)):
            entry = state["entries"][index]
            for field, limit in (("rowSha256", MAX_METADATA), ("configSha256", MAX_METADATA),
                                 ("bodySha256", MAX_BODY)):
                self.archive.get(entry[field], limit)
            self.verify_current(entry)
            state["verifiedCount"] = index + 1
            if (index + 1) % 25 == 0:
                self.archive.checkpoint(state)
        if self.scan() != (keys, protected) or self.writer_state() != writers:
            raise ArchiveRejected("FINAL_RECONCILIATION_CHANGED")
        # Configuration may legitimately change between resumable batches.
        # Check every source again at completion, including earlier batches.
        configs = {}
        for entry in state["entries"]:
            previous = configs.setdefault(entry["key"]["PK"], entry["configSha256"])
            if previous != entry["configSha256"]:
                raise ArchiveRejected("LEGACY_SNAPSHOT_CHANGED")
        for pk, expected in configs.items():
            if digest(encode(typed_row(self.current({"PK": pk, "SK": "CONFIG"})))) != expected:
                raise ArchiveRejected("LEGACY_SNAPSHOT_CHANGED")
        state.setdefault("completedAt", self.now().isoformat())
        state["phase"] = "COMPLETE"
        state["status"] = "COMPLETE_RETAINED_LEGACY"
        sha = self.archive.checkpoint(state)
        return {"status": state["status"], "legacyCount": len(keys), "protectedCount": protected,
                "manifestSha256": sha, "productionWrites": 0, "modelCalls": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--account-id", required=True)
    parser.add_argument("--region", required=True)
    parser.add_argument("--table", required=True)
    parser.add_argument("--source-bucket", required=True)
    parser.add_argument("--legacy-writer-code-sha256", required=True)
    parser.add_argument("--expected-legacy-count", required=True, type=int)
    parser.add_argument("--archive", required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not 1 <= args.expected_legacy_count <= MAX_ROWS:
        raise ArchiveRejected("INVALID_EXPECTED_LEGACY_COUNT")
    # SDK read_timeout only bounds inactivity. A POSIX process timer also
    # interrupts a streaming response that continually sends small chunks.
    with wall_deadline(MAX_SECONDS):
        session = boto3.Session(region_name=args.region)
        config = Config(connect_timeout=3, read_timeout=10, retries={"total_max_attempts": 1})
        if session.client("sts", config=config).get_caller_identity()["Account"] != args.account_id:
            raise ArchiveRejected("WRONG_AWS_ACCOUNT")
        binding = {"accountId": args.account_id, "region": args.region, "table": args.table,
                   "sourceBucket": args.source_bucket,
                   "legacyWriterCodeSha256": args.legacy_writer_code_sha256,
                   "expectedLegacyCount": args.expected_legacy_count}
        archive = LocalArchive(args.archive)
        with archive.lock():
            result = Migration(session.resource("dynamodb", config=config).Table(args.table),
                               session.client("s3", config=config), session.client("lambda", config=config),
                               archive, binding).run(args.apply)
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
