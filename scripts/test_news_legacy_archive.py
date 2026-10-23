import copy
from datetime import datetime, timedelta, timezone
from decimal import Decimal
import json
import importlib.util
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import signal
import threading
import time
from urllib.request import urlopen
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import boto3
from botocore.response import StreamingBody
from moto import mock_aws

import news_legacy_archive as archive


class LocalArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.store = archive.LocalArchive(self.temp.name)

    def test_content_hash_and_checkpoint_are_stable(self):
        state = {"rows": ["a" * (3 * 1024 * 1024)]}
        first = self.store.checkpoint(state)
        self.assertEqual(self.store.checkpoint(state), first)
        self.assertEqual(self.store.resume(), state)
        self.assertEqual((Path(self.temp.name) / first).stat().st_mode & 0o777, 0o600)

    def test_corruption_and_symlinks_fail_closed(self):
        sha = self.store.put(b"expected")
        path = Path(self.temp.name) / sha
        path.write_bytes(b"changed")
        with self.assertRaises(archive.ArchiveRejected):
            self.store.get(sha, 100)
        path.unlink()
        path.symlink_to("/etc/passwd")
        with self.assertRaises(OSError):
            self.store.put(b"expected")

    def test_failed_write_never_publishes_a_partial_content_hash(self):
        with mock.patch.object(archive.os, "fsync", side_effect=OSError("interrupted")):
            with self.assertRaises(OSError):
                self.store.put(b"test")
        self.assertFalse((Path(self.temp.name) / archive.digest(b"test")).exists())
        self.assertEqual(self.store.put(b"test"), archive.digest(b"test"))

    def test_metadata_preserves_binary_decimal_sets_and_nested_types(self):
        value = {"text": "뉴스", "number": Decimal("0.190025"), "bytes": b"\x00\xff",
                 "strings": {"b", "a"}, "numbers": {Decimal("2"), Decimal("1")},
                 "binarySet": {b"\x00", b"\xff"}, "nested": [None, True, {"x": 3}]}
        result = archive.typed_row(value)
        self.assertEqual(result["bytes"], {"B": "AP8="})
        self.assertEqual(result["number"], {"N": "0.190025"})
        self.assertEqual(result["strings"], {"SS": ["a", "b"]})
        self.assertEqual(result["binarySet"], {"BS": ["/w==", "AA=="]})
        self.assertEqual(result["nested"]["L"][:2], [{"NULL": True}, {"BOOL": True}])
        self.assertEqual(archive.encode(result), archive.encode(archive.typed_row(value)))

    def test_deadline_interrupts_live_dripping_http_body_and_releases_lock(self):
        stop = threading.Event()
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Length", "10000")
                self.end_headers()
                try:
                    while not stop.wait(0.01):
                        self.wfile.write(b"x")
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
            def log_message(self, *_args):
                pass
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        previous = signal.getsignal(signal.SIGALRM)
        started = time.monotonic()
        try:
            with self.assertRaisesRegex(archive.ArchiveRejected, "RESUME_TIME_BUDGET"):
                with archive.wall_deadline(0.2), self.store.lock():
                    with urlopen(f"http://127.0.0.1:{server.server_port}", timeout=1) as response:
                        with StreamingBody(response, 10000) as body:
                            body.read(10000)
            self.assertLess(time.monotonic() - started, 2)
            self.assertEqual(signal.getsignal(signal.SIGALRM), previous)
            self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0.0, 0.0))
            with self.store.lock():
                pass
        finally:
            stop.set()
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_existing_alarm_is_not_clobbered(self):
        signal.setitimer(signal.ITIMER_REAL, 30)
        try:
            with self.assertRaisesRegex(archive.ArchiveRejected, "EXISTING_PROCESS_TIMER"):
                with archive.wall_deadline(1):
                    self.fail("existing timer was replaced")
            self.assertGreater(signal.getitimer(signal.ITIMER_REAL)[0], 25)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)

    def test_archive_directory_must_be_private(self):
        os.chmod(self.temp.name, 0o755)
        with self.assertRaises(archive.ArchiveRejected):
            archive.LocalArchive(self.temp.name)


@mock_aws
class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.session = boto3.Session(region_name="us-east-1")
        db = self.session.resource("dynamodb")
        self.table = db.create_table(
            TableName="test-table", KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"},
                                               {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": "PK", "AttributeType": "S"},
                                  {"AttributeName": "SK", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST")
        self.s3 = self.session.client("s3")
        self.s3.create_bucket(Bucket="legacy-news-test")
        self.s3.put_bucket_versioning(Bucket="legacy-news-test", VersioningConfiguration={"Status": "Enabled"})
        self.now = datetime.now(timezone.utc)
        self.functions = mock.Mock()
        self.configs = {
            name: {"State": "Active", "LastUpdateStatus": "Successful",
                   "LastModified": (self.now - timedelta(hours=1)).isoformat(),
                   "Timeout": 840, "CodeSha256": "reviewed-code", "RevisionId": name + "-revision",
                   "Environment": {"Variables": {
                       "NEWS_LEGACY_WRITES": "off", "NEWS_PIPELINE_MODE": "canary",
                       "NEWS_CANARY_SOURCE_ID": "isolated-canary"}}}
            for name in ("ttobak-crawler-news", "ttobak-news-worker", "ttobak-news-discover")}
        self.functions.get_function_configuration.side_effect = lambda FunctionName: copy.deepcopy(self.configs[FunctionName])
        self.binding = {"accountId": "123456789012", "region": "us-east-1", "table": "test-table",
                        "sourceBucket": "legacy-news-test", "legacyWriterCodeSha256": "reviewed-code",
                        "expectedLegacyCount": 1}
        self.local = archive.LocalArchive(self.temp.name)
        self.migration = self.new_migration()
        self.key = {"PK": "CRAWLER#은행", "SK": "DOC#0123456789abcdef"}
        self.object_key = "shared/news/은행/0123456789abcdef.md"
        self.row = dict(self.key, type="news", docHash="0123456789abcdef",
                        s3Key=self.object_key, title="기존 뉴스", score=Decimal("0.5"))
        self.table.put_item(Item=self.row)
        self.table.put_item(Item={"PK": self.key["PK"], "SK": "CONFIG", "sourceId": "은행",
                                  "ownerId": "owner", "aliases": ["은행"]})
        self.s3.put_object(Bucket="legacy-news-test", Key=self.object_key, Body=b"Original legacy body")
        self.binding["expectedLegacyKeysSha256"] = archive.legacy_keys_digest([self.key])

    def new_migration(self):
        return archive.Migration(self.table, self.s3, self.functions, self.local, self.binding, lambda: self.now)

    def test_actual_legacy_producer_row_is_selected_and_preserved(self):
        path = Path(__file__).resolve().parents[1] / "backend/python/crawler/news_crawler.py"
        spec = importlib.util.spec_from_file_location("archive_test_news_producer", path)
        producer = importlib.util.module_from_spec(spec)
        with mock.patch.dict(os.environ, {"AWS_DEFAULT_REGION": "us-east-1"}):
            spec.loader.exec_module(producer)
        with mock.patch.object(producer, "table", self.table):
            producer._write_metadata("은행", "0123456789abcdef", "https://example.test/news",
                                     "기존 뉴스", "2026-10-20")
        row = self.table.get_item(Key=self.key, ConsistentRead=True)["Item"]
        self.assertEqual(row["type"], "news")
        self.assertNotIn("docType", row)
        self.assertEqual(self.migration.run(True)["legacyCount"], 1)
        entry = self.local.resume()["entries"][0]
        self.assertEqual(json.loads(self.local.get(entry["rowSha256"], 10000)), archive.typed_row(row))

    def test_zero_and_mismatched_inventory_cannot_complete(self):
        for expected in (0, -1, 10001, True, None):
            with self.subTest(expected=expected), mock.patch.dict(self.binding, {"expectedLegacyCount": expected}):
                with self.assertRaisesRegex(archive.ArchiveRejected, "INVALID_EXPECTED"):
                    self.new_migration()
        for expected in (2, 986):
            with self.subTest(expected=expected), mock.patch.dict(self.binding, {"expectedLegacyCount": expected}):
                with self.assertRaisesRegex(archive.ArchiveRejected, "COUNT_MISMATCH"):
                    self.new_migration().run(True)
        self.table.delete_item(Key=self.key)
        with self.assertRaisesRegex(archive.ArchiveRejected, "COUNT_MISMATCH"):
            self.migration.run(True)
        self.assertIsNone(self.local.resume())

    def test_declared_news_with_missing_canonical_type_is_not_silently_skipped(self):
        row = dict(self.row, GSI4PK="DOC#news")
        del row["type"]
        self.table.put_item(Item=row)
        with self.assertRaisesRegex(archive.ArchiveRejected, "INVALID_LEGACY_IDENTITY"):
            self.migration.run(True)

    def test_malformed_declared_news_outside_document_keys_cannot_complete(self):
        for changed_key in ({"PK": "OTHER#은행"}, {"SK": "ARTICLE#0123456789abcdef"}):
            for declaration in ("canonical", "index"):
                row = dict(self.row, **changed_key)
                if declaration == "index":
                    row.pop("type")
                    row["GSI4PK"] = "DOC#news"
                key = {name: row[name] for name in ("PK", "SK")}
                self.table.put_item(Item=row)
                try:
                    for count, error in ((1, "COUNT_MISMATCH"), (2, "INVALID_LEGACY_IDENTITY")):
                        with self.subTest(key=key, declaration=declaration, count=count):
                            with tempfile.TemporaryDirectory() as directory:
                                local = archive.LocalArchive(directory)
                                migration = archive.Migration(
                                    self.table, self.s3, self.functions, local,
                                    dict(self.binding, expectedLegacyCount=count,
                                         expectedLegacyKeysSha256=archive.legacy_keys_digest(
                                             [self.key, key][:count])), lambda: self.now)
                                with self.assertRaisesRegex(archive.ArchiveRejected, error):
                                    migration.run(True)
                                self.assertNotEqual(
                                    (local.resume() or {}).get("status"), "COMPLETE_RETAINED_LEGACY")
                    self.assertEqual(self.table.get_item(Key=key)["Item"], row)
                finally:
                    self.table.delete_item(Key=key)

    def test_native_news_history_is_excluded_while_documents_remain_protected(self):
        history = {"PK": self.key["PK"], "SK": "HISTORY#2026-10-21T19:10:00Z",
                   "type": "news", "schemaVersion": 1, "runId": "scheduled-history"}
        protected = dict(self.row, SK="DOC#1111111111111111", schemaVersion=1,
                         qualityStatus="PUBLISHED")
        self.table.put_item(Item=history)
        self.table.put_item(Item=protected)
        self.assertEqual(self.migration.scan(), ([self.key], 1))
        result = self.new_migration().run(True)
        self.assertEqual(result["status"], "COMPLETE_RETAINED_LEGACY")
        self.assertEqual(result["protectedCount"], 1)
        for row in (history, protected):
            self.assertEqual(self.table.get_item(
                Key={name: row[name] for name in ("PK", "SK")})["Item"], row)

    def test_resumed_final_verification_rechecks_earlier_rows_and_archive_blobs(self):
        # Interrupt an actual reconciliation checkpoint, then change a member of
        # the verified prefix. Each independent mutation must prevent completion.
        for number in range(25):
            row = dict(self.row, SK=f"DOC#{number:016x}", docHash=f"{number:016x}",
                       s3Key=f"shared/news/은행/{number:016x}.md")
            self.table.put_item(Item=row)
            self.s3.put_object(Bucket="legacy-news-test", Key=row["s3Key"], Body=f"row {number}".encode())
        self.binding["expectedLegacyCount"] = 26
        self.binding["expectedLegacyKeysSha256"] = archive.legacy_keys_digest(
            [self.key] + [{"PK": self.key["PK"], "SK": f"DOC#{number:016x}"} for number in range(25)])
        real_checkpoint = self.local.checkpoint
        def interrupt(state):
            sha = real_checkpoint(state)
            if state["phase"] == "RECONCILE" and state["verifiedCount"] == 25:
                raise OSError("interrupted after verified prefix")
            return sha
        with mock.patch.object(self.local, "checkpoint", side_effect=interrupt):
            with self.assertRaisesRegex(OSError, "verified prefix"):
                self.new_migration().run(True)
        state = self.local.resume()
        self.assertEqual(state["verifiedCount"], 25)
        entry = state["entries"][0]
        original_row = self.table.get_item(Key=entry["key"])["Item"]
        original_body = self.local.get(entry["bodySha256"], 100)
        body_path = Path(self.temp.name) / entry["bodySha256"]
        for mutation in ("row", "object", "archive"):
            with self.subTest(mutation=mutation):
                self.local.checkpoint(state)
                if mutation == "row":
                    self.table.put_item(Item=dict(original_row, title="changed"))
                elif mutation == "object":
                    changed = self.s3.put_object(Bucket="legacy-news-test", Key=entry["sourceKey"], Body=b"changed")
                else:
                    body_path.unlink()
                try:
                    with self.assertRaises((archive.ArchiveRejected, FileNotFoundError)):
                        self.new_migration().run(True)
                    self.assertNotEqual(self.local.resume()["phase"], "COMPLETE")
                finally:
                    if mutation == "row":
                        self.table.put_item(Item=original_row)
                    elif mutation == "object":
                        self.s3.delete_object(Bucket="legacy-news-test", Key=entry["sourceKey"], VersionId=changed["VersionId"])
                    else:
                        self.local.put(original_body)
        self.assertEqual(self.new_migration().run(True)["legacyCount"], 26)

    def test_plan_has_no_archive_content_or_production_writes(self):
        result = self.migration.run()
        self.assertEqual(result, {"legacyCount": 1, "legacyKeysSha256": archive.legacy_keys_digest([self.key]),
                                  "protectedCount": 0, "productionWrites": 0})
        self.assertEqual(list(Path(self.temp.name).iterdir()), [])

    def test_equal_count_identity_replacement_cannot_complete(self):
        replacement = dict(self.row, SK="DOC#1111111111111111", docHash="1111111111111111",
                           s3Key="shared/news/은행/1111111111111111.md")
        self.table.delete_item(Key=self.key)
        self.table.put_item(Item=replacement)
        self.s3.put_object(Bucket="legacy-news-test", Key=replacement["s3Key"], Body=b"replacement")
        for apply in (False, True):
            with self.subTest(apply=apply), self.assertRaisesRegex(archive.ArchiveRejected, "KEYS_MISMATCH"):
                self.new_migration().run(apply)
        self.assertIsNone(self.local.resume())

    def test_apply_requires_a_valid_recorded_identity_digest(self):
        for value in ("", "A" * 64, "0" * 63, 1):
            with self.subTest(value=value), mock.patch.dict(self.binding, {"expectedLegacyKeysSha256": value}):
                with self.assertRaisesRegex(archive.ArchiveRejected, "INVALID_EXPECTED_LEGACY_KEYS"):
                    self.new_migration()
        with mock.patch.dict(self.binding, {"expectedLegacyKeysSha256": None}):
            # A plan without the digest reports it for the dated inventory record.
            self.assertEqual(self.new_migration().run()["legacyKeysSha256"],
                             archive.legacy_keys_digest([self.key]))
            with self.assertRaisesRegex(archive.ArchiveRejected, "KEYS_REQUIRED"):
                self.new_migration().run(True)
        self.assertIsNone(self.local.resume())

    def test_real_dynamodb_and_versioned_s3_archive_and_resume(self):
        before = self.s3.list_object_versions(Bucket="legacy-news-test")
        result = self.migration.run(True)
        self.assertEqual(result["status"], "COMPLETE_RETAINED_LEGACY")
        self.assertEqual(result["modelCalls"], 0)
        state = self.local.resume()
        entry = state["entries"][0]
        self.assertEqual(entry["disposition"], "RETAINED_LEGACY")
        self.assertEqual(self.local.get(entry["bodySha256"], 100), b"Original legacy body")
        self.assertEqual(json.loads(self.local.get(entry["rowSha256"], 10000)), archive.typed_row(self.row))
        self.assertEqual(self.table.get_item(Key=self.key)["Item"], self.row)
        self.assertEqual(before["Versions"], self.s3.list_object_versions(Bucket="legacy-news-test")["Versions"])
        self.assertEqual(self.new_migration().run(True)["manifestSha256"], result["manifestSha256"])

    def test_failed_completed_archive_recheck_clears_current_success_markers(self):
        self.migration.run(True)
        completed = self.local.resume()
        entry = completed["entries"][0]
        body_path = Path(self.temp.name) / entry["bodySha256"]
        original_body = body_path.read_bytes()
        body_path.write_bytes(b"corrupt archived body")
        self.now += timedelta(minutes=1)

        with self.assertRaisesRegex(archive.ArchiveRejected, "ARCHIVE_HASH_MISMATCH"):
            self.new_migration().run(True)

        current = self.local.resume()
        self.assertEqual(current["phase"], "RECONCILE")
        self.assertEqual(current["verifiedCount"], 0)
        self.assertNotIn("status", current)
        self.assertNotIn("completedAt", current)
        self.assertEqual(current["entries"], completed["entries"])
        # Restoring the original bytes permits a newly verified completion.
        body_path.write_bytes(original_body)
        self.new_migration().run(True)
        current = self.local.resume()
        self.assertEqual(current["status"], "COMPLETE_RETAINED_LEGACY")
        self.assertEqual(current["completedAt"], self.now.isoformat())
        self.assertNotEqual(current["completedAt"], completed["completedAt"])

    def test_empty_legacy_bytes_are_preserved_without_claiming_quality(self):
        self.s3.put_object(Bucket="legacy-news-test", Key=self.object_key, Body=b"")
        self.migration.run(True)
        entry = self.local.resume()["entries"][0]
        self.assertEqual(self.local.get(entry["bodySha256"], 1), b"")
        self.assertEqual(entry["disposition"], "RETAINED_LEGACY")

    def test_incomplete_capture_resumes_without_a_false_complete_receipt(self):
        real_checkpoint = self.local.checkpoint
        calls = 0
        def checkpoint(state):
            nonlocal calls
            calls += 1
            if calls == 2:
                raise OSError("interrupted before progress checkpoint")
            return real_checkpoint(state)
        with mock.patch.object(self.local, "checkpoint", side_effect=checkpoint):
            with self.assertRaises(OSError):
                self.migration.run(True)
        self.assertNotIn("completedAt", self.local.resume())
        self.assertEqual(self.new_migration().run(True)["legacyCount"], 1)

    def test_changed_original_prevents_resume_completion(self):
        self.migration.run(True)
        self.s3.put_object(Bucket="legacy-news-test", Key=self.object_key, Body=b"Changed original")
        with self.assertRaisesRegex(archive.ArchiveRejected, "SNAPSHOT_CHANGED"):
            self.new_migration().run(True)

    def test_source_changes_and_canonical_deletion_are_not_terminal_rejections(self):
        self.migration.run(True)
        self.table.update_item(Key={"PK": self.key["PK"], "SK": "CONFIG"},
                               UpdateExpression="SET aliases = :aliases", ExpressionAttributeValues={":aliases": ["changed"]})
        with self.assertRaisesRegex(archive.ArchiveRejected, "SNAPSHOT_CHANGED"):
            self.new_migration().run(True)
        self.table.delete_item(Key=self.key)
        with self.assertRaisesRegex(archive.ArchiveRejected, "COUNT_MISMATCH"):
            self.new_migration().run(True)

    def test_versioned_tombstone_and_future_schema_are_never_reclassified(self):
        for number, fields in enumerate(({"schemaVersion": 1, "qualityStatus": "DELETED"},
                                        {"schemaVersion": 99}, {"qualityStatus": "PUBLISHED"})):
            self.table.put_item(Item={"PK": self.key["PK"], "SK": f"DOC#{number:016x}", "type": "news", **fields})
        result = self.migration.run(True)
        self.assertEqual(result["legacyCount"], 1)
        self.assertEqual(result["protectedCount"], 3)

    def test_disabled_drained_reviewed_writers_are_required(self):
        variants = (
            ("ttobak-crawler-news", "CodeSha256", "wrong", "LEGACY_WRITER"),
            ("ttobak-crawler-news", "LastModified", self.now.isoformat(), "NOT_DRAINED"),
            ("ttobak-news-worker", "LastUpdateStatus", "InProgress", "UPDATE_INCOMPLETE"),
        )
        for name, field, value, error in variants:
            with self.subTest(field=field), mock.patch.dict(self.configs[name], {field: value}):
                with self.assertRaisesRegex(archive.ArchiveRejected, error):
                    self.new_migration().run(True)
        with mock.patch.dict(self.configs["ttobak-news-worker"]["Environment"]["Variables"],
                             {"NEWS_PIPELINE_MODE": "all"}):
            with self.assertRaisesRegex(archive.ArchiveRejected, "NOT_RESTRICTED"):
                self.new_migration().run(True)
        for name in ("ttobak-news-worker", "ttobak-news-discover"):
            variables = self.configs[name]["Environment"]["Variables"]
            variables.pop("NEWS_PIPELINE_MODE")
            variables.pop("NEWS_CANARY_SOURCE_ID")
        # An absent mode is the runtime's default off configuration.
        self.assertEqual(self.new_migration().run(True)["status"], "COMPLETE_RETAINED_LEGACY")

    def test_canary_cannot_write_a_selected_legacy_source(self):
        with mock.patch.dict(self.configs["ttobak-news-worker"]["Environment"]["Variables"],
                             {"NEWS_CANARY_SOURCE_ID": "은행"}):
            with self.assertRaisesRegex(archive.ArchiveRejected, "CANARY_OVERLAPS"):
                self.migration.run(True)

    def test_bad_identity_object_size_and_selection_cap_fail_closed(self):
        for change in ({"s3Key": "shared/news/other/x.md"}, {"docHash": "wrong"}, {"sourceId": "other"}):
            with self.subTest(change=change), self.assertRaises(archive.ArchiveRejected):
                archive.legacy_identity(dict(self.row, **change))
        with self.assertRaises(archive.ArchiveRejected):
            archive.object_identity({"ETag": '"x"', "ContentLength": archive.MAX_BODY + 1})
        with mock.patch.object(archive, "MAX_ROWS", 0):
            with self.assertRaisesRegex(archive.ArchiveRejected, "SCAN_ROW_LIMIT"):
                self.migration.run(True)


if __name__ == "__main__":
    unittest.main()
