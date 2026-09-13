"""AWS is replaced at the client boundary; no credentials or network are used."""
import copy
import io
import json
import unittest
from datetime import datetime
from unittest.mock import ANY, Mock, patch

import boto3
from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
from botocore.config import Config
from botocore.exceptions import ClientError, ReadTimeoutError
from botocore.stub import Stubber

from contract import Limits, encoded, failure
from parsers import extract_bytes
import handler
from handler import handle_event
from worker import run_parser
from fixtures import pdf


def wire(values):
    return {key: TypeSerializer().serialize(value) for key, value in values.items()}


def native(values):
    return {key: TypeDeserializer().deserialize(value) for key, value in values.items()}


def assignments(operation):
    names = operation["ExpressionAttributeNames"]
    values = native(operation["ExpressionAttributeValues"])
    return {names[name.strip()]: values[value.strip()]
            for name, value in (entry.split("=") for entry in operation["UpdateExpression"][4:].split(","))}


def guard(operation):
    text = operation["ConditionExpression"]
    for name, value in operation["ExpressionAttributeNames"].items():
        text = text.replace(name, value)
    for name, value in sorted(native(operation["ExpressionAttributeValues"]).items(), key=lambda pair: -len(pair[0])):
        text = text.replace(name, repr(value))
    return text


class HandlerTests(unittest.TestCase):
    def setUp(self):
        # Explicit dummy credentials avoid SDK discovery/IMDS; Stubber/mock intercept all calls.
        session = boto3.Session(aws_access_key_id="test", aws_secret_access_key="test", region_name="ap-northeast-2")
        config = Config(retries={"total_max_attempts": 1}, connect_timeout=1, read_timeout=1)
        self.client = session.client("dynamodb", config=config)
        self.ddb = Mock()
        self.ddb.exceptions = self.client.exceptions
        self.s3 = Mock()
        self.now = 1_800_000_000_000
        self.context = Mock()
        self.context.get_remaining_time_in_millis.return_value = 90_000
        self.event = {
            "source": "ttobak.upload", "detail-type": "DocumentUploadCompleted",
            "detail": {"bucket": "test-bucket", "key": "files/uploader/meeting/노트.md",
                       "meetingId": "meeting", "ownerId": "owner", "userId": "uploader",
                       "attachmentId": "attachment", "runId": "run"},
        }
        self.state = {
            "PK": "MEETING#meeting", "SK": "ATTEXT#attachment", "runId": "run", "status": "queued",
            "leaseUntil": self.now + 300_000, "sourceKey": self.event["detail"]["key"],
            "ownerId": "owner", "uploaderId": "uploader", "entityType": "ATTACHMENT_TEXT",
            "resultKey": "old-result", "sourceETag": '"old"', "unitCount": 7, "complete": True,
        }
        self.parent = {"PK": "USER#owner", "SK": "MEETING#meeting", "userId": "owner", "meetingId": "meeting"}
        self.attachment = {
            "PK": "MEETING#meeting", "SK": "ATTACH#attachment", "attachmentId": "attachment",
            "userId": "uploader", "meetingId": "meeting", "originalKey": self.state["sourceKey"],
        }
        self.rows = {row["SK"]: row for row in (self.state, self.parent, self.attachment)}
        self.ddb.get_item.side_effect = lambda **args: {"Item": wire(self.rows.get(native(args["Key"])["SK"], {}))}
        self.data = "회의 첨부 내용".encode()
        self.body = io.BytesIO(self.data)
        self.s3.head_object.return_value = {"ContentLength": len(self.data), "ETag": '"new"'}
        self.s3.get_object.return_value = {"Body": self.body, "ContentLength": len(self.data), "ETag": '"new"'}
        self.parser = Mock(side_effect=extract_bytes)

    def invoke(self, **overrides):
        return handle_event(self.event, self.context, s3=self.s3, ddb=self.ddb,
                            bucket="test-bucket", table="test-table", parser=self.parser,
                            clock_ms=lambda: self.now, **overrides)

    def terminal(self):
        return self.ddb.transact_write_items.call_args.kwargs["TransactItems"][2]["Update"]

    def test_success_uses_canonical_source_and_publishes_provenance_atomically(self):
        self.event["detail"]["key"] = "files/other/other/secret.md"
        self.assertEqual(self.invoke()["status"], "succeeded")
        self.s3.get_object.assert_called_once_with(Bucket="test-bucket", Key=self.state["sourceKey"], IfMatch='"new"')
        self.assertTrue(self.body.closed)
        self.assertEqual(self.ddb.transact_write_items.call_count, 2)
        for call in self.ddb.get_item.call_args_list:
            self.assertTrue(call.kwargs["ConsistentRead"])
        keys = [native(call.kwargs["Key"]) for call in self.ddb.get_item.call_args_list]
        self.assertIn({"PK": "MEETING#meeting", "SK": "ATTACH#attachment"}, keys)
        put = self.s3.put_object.call_args.kwargs
        self.assertEqual(put["Key"], "files/uploader/meeting/text/attachment/run.json")
        self.assertEqual(put["IfNoneMatch"], "*")
        result = json.loads(put["Body"])
        self.assertEqual(result["source"]["eTag"], '"new"')
        self.assertEqual(result["units"][0]["text"], self.data.decode())
        self.assertLessEqual(len(put["Body"]), Limits().max_result_bytes)
        updated = assignments(self.terminal())
        self.assertEqual(updated["status"], "succeeded")
        self.assertEqual(updated["sourceETag"], '"new"')
        self.assertTrue(updated["complete"])
        self.assertEqual(updated["leaseUntil"], 0)
        self.assertTrue(updated["updatedAt"].endswith("Z"))
        self.assertEqual(datetime.fromisoformat(updated["updatedAt"]).timestamp(), self.now / 1000)

    def test_duplicates_stale_events_and_foreign_bucket_do_not_parse(self):
        for changes in ({"status": "running"}, {"status": "succeeded"}, {"runId": "later"},
                        {"ownerId": "other"}, {"uploaderId": "other"}, {"leaseUntil": self.now}):
            with self.subTest(changes=changes):
                self.setUp()
                self.state.update(changes)
                self.assertEqual(self.invoke()["status"], "ignored")
                self.s3.head_object.assert_not_called()
                self.parser.assert_not_called()
        self.setUp()
        self.event["detail"]["bucket"] = "foreign"
        self.assertEqual(self.invoke()["status"], "ignored")
        self.ddb.get_item.assert_not_called()

    def test_failure_preserves_previous_result_metadata(self):
        self.parser.side_effect = None
        self.parser.return_value = failure("md", "INVALID_ENCODING")
        self.assertEqual(self.invoke(), {"status": "failed", "errorCode": "INVALID_ENCODING"})
        updated = assignments(self.ddb.update_item.call_args.kwargs)
        self.assertEqual(set(updated), {"status", "errorCode", "leaseUntil", "updatedAt"})
        self.assertEqual(updated["errorCode"], "INVALID_ENCODING")
        self.s3.put_object.assert_not_called()

    def test_oversized_head_stops_before_get(self):
        self.s3.head_object.return_value["ContentLength"] = Limits().max_input_bytes + 1
        self.assertEqual(self.invoke()["errorCode"], "SOURCE_TOO_LARGE")
        self.s3.get_object.assert_not_called()
        self.parser.assert_not_called()

    def test_parent_attachment_identity_and_deleted_rows_are_rechecked(self):
        cases = [(self.parent, "userId", "other"), (self.parent, "meetingId", "other"),
                 (self.attachment, "userId", "other"), (self.attachment, "meetingId", "other"),
                 (self.attachment, "attachmentId", "other"), (self.attachment, "originalKey", "changed")]
        for row, field, changed in cases:
            with self.subTest(field=field, key=row["SK"]):
                old = row[field]
                row[field] = changed
                self.assertEqual(self.invoke()["errorCode"], "SOURCE_CHANGED")
                self.parser.assert_not_called()
                self.s3.head_object.assert_not_called()
                row[field] = old
        for key in ("ATTACH#attachment", "MEETING#meeting"):
            with self.subTest(deleted=key):
                old = self.rows.pop(key)
                self.assertEqual(self.invoke()["errorCode"], "SOURCE_UNAVAILABLE")
                self.parser.assert_not_called()
                self.rows[key] = old

    def test_source_path_must_be_exact_uploader_meeting_basename(self):
        for key in ("files/other/meeting/a.md", "files/uploader/other/a.md", "docs/uploader/a.md",
                    "files/uploader/meeting/../a.md", "files/uploader/meeting/..",
                    "files/uploader/meeting/a\\b.md"):
            with self.subTest(key=key):
                self.state["sourceKey"] = self.attachment["originalKey"] = key
                self.assertEqual(self.invoke()["errorCode"], "INVALID_SOURCE")
                self.parser.assert_not_called()
        self.s3.head_object.assert_not_called()

    def test_canonical_basename_is_an_s3_key_not_a_url(self):
        key = "files/uploader/meeting/검토#1?100%.md"
        self.state["sourceKey"] = self.attachment["originalKey"] = key
        self.assertEqual(self.invoke()["status"], "succeeded")
        self.assertEqual(self.s3.get_object.call_args.kwargs["Key"], key)

    def canceled(self, index):
        return self.client.exceptions.TransactionCanceledException(
            {"Error": {"Code": "TransactionCanceledException"},
             "CancellationReasons": [{"Code": "ConditionalCheckFailed" if i == index else "None"} for i in range(3)]},
            "TransactWriteItems")

    def test_competing_claim_does_not_parse_or_fail_winner(self):
        self.ddb.transact_write_items.side_effect = self.canceled(2)
        self.ddb.update_item.side_effect = self.client.exceptions.ConditionalCheckFailedException(
            {"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem")
        self.assertEqual(self.invoke()["status"], "ignored")
        self.parser.assert_not_called()
        self.assertIn("status = 'queued'", guard(self.ddb.update_item.call_args.kwargs))

    def test_claim_and_completion_guards_pin_source_run_lease_and_parent(self):
        self.invoke()
        for i, call in enumerate(self.ddb.transact_write_items.call_args_list):
            parent, attachment, state = call.kwargs["TransactItems"]
            self.assertEqual(native(parent["ConditionCheck"]["Key"]), {"PK": "USER#owner", "SK": "MEETING#meeting"})
            self.assertEqual(native(attachment["ConditionCheck"]["Key"]), {"PK": "MEETING#meeting", "SK": "ATTACH#attachment"})
            self.assertEqual(native(state["Update"]["Key"]), {"PK": "MEETING#meeting", "SK": "ATTEXT#attachment"})
            for op in (parent["ConditionCheck"], attachment["ConditionCheck"], state["Update"]):
                self.assertIn("attribute_exists(PK)", guard(op))
            for text in ("userId = 'owner'", "meetingId = 'meeting'"):
                self.assertIn(text, guard(parent["ConditionCheck"]))
            for text in ("userId = 'uploader'", "meetingId = 'meeting'", "attachmentId = 'attachment'",
                         "originalKey = 'files/uploader/meeting/노트.md'"):
                self.assertIn(text, guard(attachment["ConditionCheck"]))
            condition = guard(state["Update"])
            for text in ("runId = 'run'", "ownerId = 'owner'", "uploaderId = 'uploader'",
                         "sourceKey = 'files/uploader/meeting/노트.md'", "leaseUntil = ", "leaseUntil > "):
                self.assertIn(text, condition)
            self.assertIn("status = " + repr("queued" if i == 0 else "running"), condition)

    def test_deletion_source_change_or_lease_loss_during_parse_cannot_publish(self):
        for index in range(3):
            with self.subTest(condition=index):
                self.setUp()
                self.ddb.transact_write_items.side_effect = [{}, self.canceled(index)]
                if index == 2:
                    self.ddb.update_item.side_effect = self.client.exceptions.ConditionalCheckFailedException(
                        {"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem")
                result = self.invoke()
                self.assertEqual(result["status"], "ignored" if index == 2 else "failed")
                # The final failure update cannot upsert or overwrite a new/deleted run.
                condition = guard(self.ddb.update_item.call_args.kwargs)
                self.assertIn("attribute_exists(PK)", condition)
                self.assertIn("status = 'running'", condition)
                self.assertIn("runId = 'run'", condition)
                self.assertIn("leaseUntil = ", condition)
                self.s3.delete_object.assert_not_called()

    def test_ambiguous_dynamo_writes_surface_without_cleanup_or_second_parse(self):
        for stage in ("claim", "completion"):
            with self.subTest(stage=stage):
                self.setUp()
                error = ReadTimeoutError(endpoint_url="https://example.invalid")
                self.ddb.transact_write_items.side_effect = error if stage == "claim" else [{}, error]
                with self.assertRaisesRegex(RuntimeError, "^STATE_WRITE_FAILED$"):
                    self.invoke()
                self.assertEqual(self.parser.call_count, 0 if stage == "claim" else 1)
                self.ddb.update_item.assert_not_called()
                self.s3.delete_object.assert_not_called()
        self.setUp()
        self.parser.side_effect = None
        self.parser.return_value = failure("md", "INVALID_ENCODING")
        self.ddb.update_item.side_effect = ReadTimeoutError(endpoint_url="https://example.invalid")
        with self.assertRaisesRegex(RuntimeError, "^STATE_WRITE_FAILED$"):
            self.invoke()

    def test_nonconditional_transaction_error_is_not_misreported_as_duplicate(self):
        self.ddb.transact_write_items.side_effect = self.client.exceptions.TransactionCanceledException(
            {"Error": {"Code": "TransactionCanceledException"},
             "CancellationReasons": [{"Code": "TransactionConflict"}]}, "TransactWriteItems")
        with self.assertRaisesRegex(RuntimeError, "^STATE_WRITE_FAILED$"):
            self.invoke()
        self.parser.assert_not_called()

    def test_source_etag_change_before_get_after_parse_and_after_put(self):
        for stage in ("get", "parsed", "stored"):
            with self.subTest(stage=stage):
                self.setUp()
                original = self.s3.head_object.return_value
                changed = {**original, "ETag": '"changed"'}
                if stage == "get":
                    self.s3.get_object.side_effect = ClientError(
                        {"Error": {"Code": "PreconditionFailed", "Message": "private source"}}, "GetObject")
                else:
                    self.s3.head_object.side_effect = [original, changed] if stage == "parsed" else [original, original, changed]
                self.assertEqual(self.invoke()["errorCode"], "SOURCE_CHANGED")
                self.assertEqual(self.ddb.transact_write_items.call_count, 1)
                self.assertEqual(self.s3.put_object.call_count, int(stage == "stored"))
                self.s3.delete_object.assert_not_called()

    def test_bounded_read_closes_body_on_bad_length_etag_timeout(self):
        for case in ("long", "short", "etag", "timeout"):
            with self.subTest(case=case):
                self.setUp()
                body = self.body = io.BytesIO(self.data + b"x" if case == "long" else self.data[:-1])
                self.s3.get_object.return_value["Body"] = body
                if case == "etag":
                    self.s3.get_object.return_value["ETag"] = '"different"'
                if case == "timeout":
                    self.body = Mock(wraps=body)
                    self.body.read.side_effect = ReadTimeoutError(endpoint_url="https://example.invalid")
                    self.s3.get_object.return_value["Body"] = self.body
                self.assertEqual(self.invoke()["status"], "failed")
                self.assertTrue(body.closed)
                self.parser.assert_not_called()

    def test_partial_pdf_publishes_truthful_incomplete_result(self):
        self.state["sourceKey"] = self.attachment["originalKey"] = "files/uploader/meeting/input.pdf"
        self.data = pdf(("텍스트 페이지", None))
        self.s3.head_object.return_value["ContentLength"] = len(self.data)
        self.s3.get_object.return_value = {"Body": io.BytesIO(self.data), "ContentLength": len(self.data), "ETag": '"new"'}
        self.assertEqual(self.invoke()["status"], "partial")
        updated = assignments(self.terminal())
        self.assertFalse(updated["complete"])
        self.assertEqual(updated["errorCode"], "PARTIAL_EXTRACTION")
        result = json.loads(self.s3.put_object.call_args.kwargs["Body"])
        self.assertEqual(result["warnings"][0]["location"]["page"], 2)

    def test_invalid_or_oversized_parser_result_is_not_published(self):
        for invalid in ({}, {**extract_bytes(self.data, "md"), "units": []},
                        {**extract_bytes(self.data, "md"), "extension": "x" * Limits().max_result_bytes}):
            self.parser.side_effect = None
            self.parser.return_value = invalid
            self.assertEqual(self.invoke()["errorCode"], "WORKER_FAILED")
            self.body = io.BytesIO(self.data)
            self.s3.get_object.return_value["Body"] = self.body
        self.s3.put_object.assert_not_called()

    def test_result_json_budget_includes_added_source_provenance(self):
        def near_limit(data, fmt, limits):
            result = extract_bytes(data, fmt)
            result["extension"] = ""
            result["extension"] = "x" * (limits.max_result_bytes - len(encoded(result)))
            self.assertEqual(len(encoded(result)), limits.max_result_bytes)
            return result
        self.parser.side_effect = near_limit
        self.assertEqual(self.invoke()["status"], "succeeded")
        body = self.s3.put_object.call_args.kwargs["Body"]
        self.assertGreater(len(body), Limits().max_result_bytes - 4096)
        self.assertLessEqual(len(body), Limits().max_result_bytes)
        self.assertIn("source", json.loads(body))

    def test_s3_errors_are_fixed_codes_and_cannot_remove_retained_results(self):
        for phase, expected in (("read", "SOURCE_UNAVAILABLE"), ("write", "RESULT_WRITE_FAILED")):
            with self.subTest(phase=phase):
                self.setUp()
                error = ClientError({"Error": {"Code": "AccessDenied", "Message": "private document text"}}, "GetObject")
                if phase == "read":
                    self.s3.head_object.side_effect = error
                else:
                    self.s3.put_object.side_effect = error
                with patch("sys.stdout", new_callable=io.StringIO) as stdout, patch("sys.stderr", new_callable=io.StringIO) as stderr:
                    self.assertEqual(self.invoke(), {"status": "failed", "errorCode": expected})
                    self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")
                self.assertEqual(set(assignments(self.ddb.update_item.call_args.kwargs)),
                                 {"status", "errorCode", "leaseUntil", "updatedAt"})
                self.s3.delete_object.assert_not_called()

    def test_deadline_skips_start_or_reduces_child_wall_allowance(self):
        self.context.get_remaining_time_in_millis.return_value = 20_000
        self.assertEqual(self.invoke()["errorCode"], "TIMEOUT")
        self.parser.assert_not_called()
        self.ddb.transact_write_items.assert_not_called()
        self.context.get_remaining_time_in_millis.return_value = 30_000
        self.assertEqual(self.invoke()["status"], "succeeded")
        limits = self.parser.call_args.args[2]
        self.assertLess(limits.wall_seconds, Limits().wall_seconds)
        self.assertGreater(limits.wall_seconds, 0)

    def test_real_child_markdown_with_actual_sdk_request_validation(self):
        # Stubber runs botocore request validation/serialization, not just Mock
        # method calls; the parser is the real resource-limited child process.
        expected_reads = (self.state, self.parent, self.attachment)
        captured = []
        self.client.meta.events.register("before-parameter-build.dynamodb.TransactWriteItems",
                                         lambda params, **_: captured.append(copy.deepcopy(params)))
        with Stubber(self.client) as stub:
            for row in expected_reads:
                stub.add_response("get_item", {"Item": wire(row)}, {
                    "TableName": "test-table", "Key": wire({"PK": row["PK"], "SK": row["SK"]}),
                    "ConsistentRead": True, "ProjectionExpression": ANY, "ExpressionAttributeNames": ANY,
                })
            stub.add_response("transact_write_items", {}, {"TransactItems": ANY})
            stub.add_response("transact_write_items", {}, {"TransactItems": ANY})
            self.ddb, self.parser = self.client, run_parser
            self.assertEqual(self.invoke()["status"], "succeeded")
            stub.assert_no_pending_responses()
        self.assertEqual(len(captured), 2)
        self.assertIn("status = 'running'", guard(captured[1]["TransactItems"][2]["Update"]))
        self.assertEqual(json.loads(self.s3.put_object.call_args.kwargs["Body"])["units"][0]["text"], self.data.decode())

    def test_sdk_clients_are_lazy_reused_and_have_bounded_no_retry_config(self):
        session = Mock()
        session.client.side_effect = [self.s3, self.ddb]
        with patch.dict("os.environ", {"BUCKET_NAME": "test-bucket", "TABLE_NAME": "test-table"}, clear=True), \
                patch.object(handler, "_clients", None), patch.object(handler.boto3, "Session", return_value=session), \
                patch.object(handler, "handle_event", return_value={"status": "ignored"}):
            for _ in range(2):
                self.assertEqual(handler.lambda_handler(self.event, self.context)["status"], "ignored")
            self.assertEqual(session.client.call_count, 2)
            for call in session.client.call_args_list:
                config = call.kwargs["config"]
                self.assertEqual(config.retries["total_max_attempts"], 1)
                self.assertLessEqual(config.connect_timeout + config.read_timeout, 6)
        with patch.dict("os.environ", {}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "^CONFIGURATION_ERROR$"):
                handler.lambda_handler(self.event, self.context)


if __name__ == "__main__":
    unittest.main()
