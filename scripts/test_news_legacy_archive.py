import copy
from datetime import datetime, timedelta, timezone
from decimal import Decimal
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import boto3
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
                        "sourceBucket": "legacy-news-test", "legacyWriterCodeSha256": "reviewed-code"}
        self.local = archive.LocalArchive(self.temp.name)
        self.migration = self.new_migration()
        self.key = {"PK": "CRAWLER#은행", "SK": "DOC#0123456789abcdef"}
        self.object_key = "shared/news/은행/0123456789abcdef.md"
        self.row = dict(self.key, docType="news", docHash="0123456789abcdef",
                        s3Key=self.object_key, title="기존 뉴스", score=Decimal("0.5"))
        self.table.put_item(Item=self.row)
        self.table.put_item(Item={"PK": self.key["PK"], "SK": "CONFIG", "sourceId": "은행",
                                  "ownerId": "owner", "aliases": ["은행"]})
        self.s3.put_object(Bucket="legacy-news-test", Key=self.object_key, Body=b"Original legacy body")

    def new_migration(self):
        return archive.Migration(self.table, self.s3, self.functions, self.local, self.binding, lambda: self.now)

    def test_plan_has_no_archive_content_or_production_writes(self):
        result = self.migration.run()
        self.assertEqual(result, {"legacyCount": 1, "protectedCount": 0, "productionWrites": 0})
        self.assertEqual(list(Path(self.temp.name).iterdir()), [])

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
        with self.assertRaisesRegex(archive.ArchiveRejected, "RESUME_BINDING_CHANGED"):
            self.new_migration().run(True)

    def test_versioned_tombstone_and_future_schema_are_never_reclassified(self):
        for number, fields in enumerate(({"schemaVersion": 1, "qualityStatus": "DELETED"},
                                        {"schemaVersion": 99}, {"qualityStatus": "PUBLISHED"})):
            self.table.put_item(Item={"PK": self.key["PK"], "SK": f"DOC#{number:016x}", "docType": "news", **fields})
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
