import copy
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unicodedata
import unittest
from unittest.mock import patch

from replay_pipeline_failure import Inspector, Rejected, execute, main, timestamp_ns, event_bounds

ACCOUNT, REGION = "111111111111", "ap-northeast-2"
NOW = 1791806400


def iso(seconds):
    return datetime.fromtimestamp(seconds, timezone.utc).isoformat()

def nano(seconds, fraction):
    return iso(seconds).replace("+00:00", "." + fraction + "+00:00")


class Table:
    def __init__(self):
        self.items = {
            ("USER#owner", "MEETING#meeting"): {
                "meetingId": "meeting", "userId": "owner", "status": "transcribing",
                "createdAt": iso(NOW - 600), "updatedAt": iso(NOW - 300),
                "audioKey": "audio/owner/meeting/final.wav",
            },
        }
        self.reads = []

    def get_item(self, **kwargs):
        assert kwargs["ConsistentRead"] is True
        fields = set(kwargs["ExpressionAttributeNames"].values())
        assert fields.isdisjoint({"content", "notes", "transcriptA", "transcriptB", "processedContent"})
        self.reads.append(kwargs["Key"])
        item = self.items.get((kwargs["Key"]["PK"], kwargs["Key"]["SK"]))
        return {"Item": copy.deepcopy(item)} if item else {}


class S3:
    def __init__(self):
        self.value = {"ETag": '"etag"', "VersionId": "v1",
                      "LastModified": datetime.fromtimestamp(NOW - 200, timezone.utc)}
        self.overrides = {}
        self.reads = []

    def head_object(self, **kwargs):
        self.reads.append(kwargs)
        return copy.deepcopy(self.overrides.get(kwargs.get("Key"), self.value))


def event(source="aws.s3", kind="Object Created", detail=None):
    return {"id": "event-1", "version": "0", "source": source, "detail-type": kind,
            "account": ACCOUNT, "region": REGION, "time": iso(NOW - 100),
            "detail": detail or {"bucket": {"name": "assets"}, "object": {
                "key": "audio/owner/meeting/final.wav", "etag": "etag", "version-id": "v1"}}}


def message(payload, kind="delivery", function="transcribe", rule="ttobak-audio-upload"):
    target = f"arn:aws:lambda:{REGION}:{ACCOUNT}:function:ttobak-{function}"
    body = payload
    if kind == "execution":
        body = {"version": "1.0", "requestContext": {
            "condition": "RetriesExhausted", "functionArn": target + ":$LATEST"},
            "requestPayload": payload, "responsePayload": {"errorMessage": "private provider detail"}}
    return {"Body": json.dumps(body), "MessageAttributes": {
        "RULE_ARN": {"StringValue": f"arn:aws:events:{REGION}:{ACCOUNT}:rule/{rule}"},
        "TARGET_ARN": {"StringValue": target}}}


class ReplayTests(unittest.TestCase):
    def setUp(self):
        self.table, self.s3 = Table(), S3()
        self.inspector = Inspector(self.table, self.s3, account=ACCOUNT, region=REGION,
                                   bucket="assets", owner_id="owner", meeting_id="meeting",
                                   attachment_id="attachment", document_id="document", now=NOW)

    @property
    def meeting(self):
        return self.table.items[("USER#owner", "MEETING#meeting")]

    def rejected(self, payload, code, kind="delivery"):
        with self.assertRaisesRegex(Rejected, "^" + code + "$"):
            self.inspector.plan(payload, kind)

    def test_both_failure_shapes_keep_original_payload_and_replay_has_no_body_output(self):
        original = event()
        for kind in ("delivery", "execution"):
            plan = self.inspector.plan(message(original, kind), kind)
            self.assertEqual(plan["event"], original)
            self.assertNotIn("responsePayload", plan)
            calls = []

            class Lambda:
                def invoke(self, **kwargs):
                    calls.append(kwargs)
                    return {"StatusCode": 202, "Payload": io.BytesIO()}

            result = execute(plan, Lambda())
            self.assertEqual(json.loads(calls[0]["Payload"]), original)
            self.assertEqual(calls[0]["InvocationType"], "Event")
            self.assertEqual(result, {"status": "accepted_not_completed", "planHash": plan["planHash"]})
            self.assertNotIn("private", json.dumps(result))

    def test_foreign_account_region_rule_target_and_destination_are_rejected(self):
        for field, value in (("account", "222222222222"), ("region", "us-east-1")):
            payload = event()
            payload[field] = value
            self.rejected(message(payload), "FOREIGN_EVENT")
        payload = message(event())
        payload["MessageAttributes"]["RULE_ARN"]["StringValue"] += "-other"
        self.rejected(payload, "FOREIGN_RULE")
        payload = message(event())
        payload["MessageAttributes"]["TARGET_ARN"]["StringValue"] += "-other"
        self.rejected(payload, "FOREIGN_TARGET")
        self.rejected(message(event(), "execution", "other"), "FOREIGN_FUNCTION", "execution")

    def test_rebound_audio_changed_etag_version_and_newer_source_are_rejected(self):
        self.meeting["audioKey"] = "audio/owner/meeting/new.wav"
        self.rejected(message(event()), "AUDIO_BINDING_CHANGED")
        self.meeting["audioKey"] = "audio/owner/meeting/final.wav"
        for field, value, code in (
                ("ETag", '"changed"', "SOURCE_ETAG_CHANGED"),
                ("VersionId", "v2", "SOURCE_VERSION_CHANGED"),
                ("LastModified", datetime.fromtimestamp(NOW, timezone.utc), "SOURCE_REPLACED_AFTER_EVENT")):
            with self.subTest(field=field):
                old = self.s3.value[field]
                self.s3.value[field] = value
                self.rejected(message(event()), code)
                self.s3.value[field] = old

    def test_current_identity_missing_row_and_recent_edits_are_rejected(self):
        self.meeting["userId"] = "someone-else"
        self.rejected(message(event()), "CURRENT_IDENTITY_MISMATCH")
        self.meeting["userId"] = "owner"
        self.meeting["updatedAt"] = iso(NOW - 10)
        transcript = event(detail={"bucket": {"name": "assets"}, "object": {
            "key": "transcripts/meeting.json", "etag": "etag", "version-id": "v1"}})
        self.rejected(message(transcript, function="summarize", rule="ttobak-transcript-upload"),
                      "MEETING_CHANGED_AFTER_EVENT")
        self.table.items.clear()
        self.rejected(message(event()), "CURRENT_RESOURCE_MISSING")

    def test_plan_hash_changes_when_current_object_version_changes(self):
        # Custom images have no event ETag: the inspected live version still
        # becomes part of the operator's required confirmation hash.
        self.image()
        payload = self.image_message()
        first = self.inspector.plan(payload, "delivery")["planHash"]
        self.s3.value["VersionId"] = "changed-before-event"
        self.assertNotEqual(first, self.inspector.plan(payload, "delivery")["planHash"])

    def test_terminal_summary_and_live_or_claimed_summary_require_supported_recovery(self):
        payload = message(event(detail={"bucket": {"name": "assets"},
                          "object": {"key": "transcripts/meeting.json", "etag": "etag"}}),
                          function="summarize", rule="ttobak-transcript-upload")
        for status in ("error", "done"):
            self.meeting["status"] = status
            self.rejected(payload, "USE_AUTHENTICATED_RESUMMARY")
        self.meeting["status"] = "summarizing"
        self.rejected(payload, "SUMMARY_STILL_ACTIVE")
        self.meeting["updatedAt"] = iso(NOW - 1800)
        self.assertEqual(self.inspector.plan(payload, "delivery")["event"]["detail"]["object"]["etag"], "etag")
        self.meeting["summarizeRetryClaimedAt"] = iso(NOW - 1800)
        self.rejected(payload, "SUMMARY_CLAIM_PRESENT")

    def test_queued_analysis_preserves_run_and_rejects_newer_or_expired_runs(self):
        for summary in (False, True):
            state = {"runId": "run-old", "status": "queued", "leaseUntil": (NOW + 60) * 1000,
                     "ownerId": "owner", "sourceHash": "source-identity"}
            self.table.items[("MEETING#meeting", "ANALYSIS#summary" if summary else "ANALYSIS#actionItems")] = state
            detail = {"meetingId": "meeting", "runId": "run-old"}
            if not summary:
                detail["ownerId"] = "owner"
            payload = message(event("ttobak.analysis", "SummaryRequested" if summary else "ActionItemsRequested", detail),
                              function="summarize",
                              rule="ttobak-saved-summary-requested" if summary else "ttobak-action-items-requested")
            self.assertEqual(self.inspector.plan(payload, "delivery")["event"]["detail"], detail)
            state["runId"] = "run-new"
            self.rejected(payload, "STALE_RUN")
            state["runId"], state["leaseUntil"] = "run-old", NOW * 1000
            self.rejected(payload, "USE_AUTHENTICATED_ANALYSIS_RETRY")

    def image(self):
        self.table.items[("MEETING#meeting", "ATTACH#attachment")] = {
            "attachmentId": "attachment", "meetingId": "meeting", "userId": "editor",
            "originalKey": "images/editor/meeting/source.png", "status": "uploaded",
            "type": "image", "createdAt": iso(NOW - 200)}

    def image_message(self):
        return message(event("ttobak.upload", "ImageUploadCompleted", {
            "meetingId": "meeting", "userId": "editor", "bucket": "assets",
            "key": "images/editor/meeting/source.png"}), function="process-image", rule="ttobak-image-upload")

    def test_image_matches_current_uploader_and_ignores_completed_attachment(self):
        self.image()
        self.assertIn("process-image", self.inspector.plan(self.image_message(), "delivery")["target"])
        att = self.table.items[("MEETING#meeting", "ATTACH#attachment")]
        att["status"] = "done"
        self.rejected(self.image_message(), "IMAGE_ALREADY_COMPLETED")
        att["status"], att["userId"] = "uploaded", "owner"
        self.rejected(self.image_message(), "IMAGE_BINDING_CHANGED")

    def test_all_parts_must_match_current_emission_and_complete_set(self):
        self.meeting.update(audioPartCount=2, audioPartsReadySet={0, 1}, allPartsEmittedAt=iso(NOW - 110))
        payload = message(event("ttobak.transcribe", "AllPartsTranscribed", {
            "meetingId": "meeting", "userId": "owner", "bucket": "assets", "partCount": 2}),
            function="summarize", rule="ttobak-all-parts-transcribed")
        self.assertEqual(self.inspector.plan(payload, "delivery")["event"]["detail"]["partCount"], 2)
        self.assertEqual(len(self.s3.reads), 2)
        self.meeting["audioPartsReadySet"] = {0}
        self.rejected(payload, "PART_SET_CHANGED")
        self.meeting.update(audioPartsReadySet={0, 1}, allPartsEmittedAt=iso(NOW - 500))
        self.rejected(payload, "STALE_PARTS_EVENT")

    def test_crop_requires_queued_current_source(self):
        self.table.items[("USER#owner", "MEETING#source")] = {"meetingId": "source", "userId": "owner"}
        self.meeting["audioCrop"] = {"state": "queued", "sourceMeetingId": "source",
                                    "sourceKey": "audio/owner/source/a.wav", "sourceETag": "etag"}
        payload = message(event("ttobak.audio", "AudioCropRequested", {
            "meetingId": "meeting", "userId": "owner"}), rule="ttobak-audio-crop")
        self.assertIn("transcribe", self.inspector.plan(payload, "delivery")["target"])
        self.meeting["audioCrop"]["state"] = "processing"
        self.rejected(payload, "CROP_NOT_REPLAYABLE")

    def test_crop_same_second_creation_is_allowed_but_a_later_edit_is_not(self):
        self.table.items[("USER#owner", "MEETING#source")] = {"meetingId": "source", "userId": "owner"}
        self.meeting["audioCrop"] = {"state": "queued", "sourceMeetingId": "source",
                                    "sourceKey": "audio/owner/source/a.wav", "sourceETag": "etag"}
        payload = message(event("ttobak.audio", "AudioCropRequested", {
            "meetingId": "meeting", "userId": "owner"}), rule="ttobak-audio-crop")
        self.meeting["updatedAt"] = nano(NOW - 100, "345000000")
        self.assertIn("transcribe", self.inspector.plan(payload, "delivery")["target"])
        self.meeting["updatedAt"] = iso(NOW - 99)
        self.rejected(payload, "MEETING_CHANGED_AFTER_EVENT")

    def test_part_transcript_replay_ignores_sibling_progress_but_not_its_own_application(self):
        key = "transcripts/meeting_part_000.json"
        self.meeting.update(audioPartCount=2, audioPartsReadySet={1}, updatedAt=iso(NOW - 50))
        payload = message(event(detail={"bucket": {"name": "assets"}, "object": {
            "key": key, "etag": "etag", "version-id": "v1"}}),
            function="summarize", rule="ttobak-transcript-upload")
        self.assertIn("summarize", self.inspector.plan(payload, "delivery")["target"])
        self.meeting["audioPartsReadySet"] = {0, 1}
        self.rejected(payload, "TRANSCRIPT_PART_ALREADY_APPLIED")

    def test_part_transcript_replay_is_bound_to_its_audio_generation(self):
        key = "transcripts/meeting_part_000.json"
        audio0, audio1 = "audio/owner/meeting/part_000_a.wav", "audio/owner/meeting/part_001_b.wav"
        self.meeting.update(audioPartCount=2, audioKeys=[audio0, audio1], audioPartsReadySet={1},
                            updatedAt=iso(NOW - 20))
        payload = message(event(detail={"bucket": {"name": "assets"}, "object": {
            "key": key, "etag": "etag", "version-id": "v1"}}),
            function="summarize", rule="ttobak-transcript-upload")
        self.s3.overrides[audio0] = {"ETag": '"a0"', "LastModified": datetime.fromtimestamp(NOW - 200, timezone.utc)}
        self.assertIn("summarize", self.inspector.plan(payload, "delivery")["target"])
        # A newer batch re-uploaded this part audio after the retained event.
        self.s3.overrides[audio0]["LastModified"] = datetime.fromtimestamp(NOW - 50, timezone.utc)
        self.rejected(payload, "SOURCE_REPLACED_AFTER_EVENT")
        self.meeting.update(audioKeys=[], audioKey=None)
        self.rejected(payload, "PART_GENERATION_UNKNOWN")

    def test_audio_replay_rejects_a_key_that_nfc_normalization_would_change(self):
        nfd = unicodedata.normalize("NFD", "audio/owner/meeting/final_한글.wav")
        self.assertNotEqual(nfd, unicodedata.normalize("NFC", nfd))
        self.rejected(message(event(detail={"bucket": {"name": "assets"}, "object": {
            "key": nfd, "etag": "etag", "version-id": "v1"}})), "NONCANONICAL_AUDIO_KEY")

    def test_document_replay_binds_current_file_and_preserves_literal_plus(self):
        key = "docs/owner/a+b.pptx"
        # The raw S3 event can precede the API call that creates the document.
        self.table.items[("USER#owner", "DOC#document")] = {
            "docId": "document", "fileKey": key, "createdAt": nano(NOW - 90, "123456789")}
        payload = message(event(detail={"bucket": {"name": "assets"},
                                       "object": {"key": key, "etag": "etag", "version-id": "v1"}}),
                          function="convert-doc", rule="ttobak-doc-slide-upload")
        self.assertEqual(self.inspector.plan(payload, "delivery")["event"]["detail"]["object"]["key"], key)
        self.table.items[("USER#owner", "DOC#document")]["fileKey"] = "docs/owner/other.pptx"
        self.rejected(payload, "DOCUMENT_BINDING_CHANGED")

    def test_rfc3339_nano_preserves_exact_order_and_timezone_on_supported_python_versions(self):
        base = timestamp_ns(iso(NOW))
        self.assertEqual(timestamp_ns(nano(NOW, "123456789")), base + 123456789)
        self.assertEqual(timestamp_ns(nano(NOW, "123456790")), base + 123456790)
        self.assertEqual(timestamp_ns("1970-01-01T09:00:00.000000001+09:00"), 1)
        self.assertEqual(event_bounds(iso(NOW)), (base, base + 1_000_000_000))
        self.assertEqual(event_bounds(nano(NOW, "123456789")), (base + 123456789, base + 123456790))
        for value in ("not a timestamp", "2026-10-12T12:00:00", nano(NOW, "1234567890")):
            with self.assertRaisesRegex(Rejected, "INVALID_TIMESTAMP"):
                timestamp_ns(value)

    def test_audio_event_before_completion_binding_needs_current_immutable_version(self):
        self.meeting["updatedAt"] = nano(NOW - 50, "123456789")
        original = event()
        self.assertEqual(self.inspector.plan(message(original), "delivery")["event"], original)
        self.s3.value["VersionId"] = "v2"  # Equal bytes/ETag are insufficient.
        self.rejected(message(original), "SOURCE_VERSION_CHANGED")
        self.s3.value["VersionId"] = "v1"
        missing = copy.deepcopy(original)
        del missing["detail"]["object"]["version-id"]
        self.rejected(message(missing), "AUDIO_VERSION_REQUIRED_AFTER_BINDING")
        self.s3.value["VersionId"] = missing["detail"]["object"]["version-id"] = "null"
        self.rejected(message(missing), "AUDIO_VERSION_REQUIRED_AFTER_BINDING")
        self.s3.value["VersionId"] = "v1"
        for status in ("done", "error", "summarizing"):
            self.meeting["status"] = status
            self.rejected(message(original), "AUDIO_NOT_REPLAYABLE")
        self.meeting["status"], self.meeting["audioKey"] = "transcribing", "audio/owner/meeting/rebound.wav"
        self.rejected(message(original), "AUDIO_BINDING_CHANGED")

    def test_audio_part_replay_rejects_completed_or_wrong_index_binding(self):
        key = "audio/owner/meeting/part_001_final.wav"
        self.meeting.update(audioKeys=["audio/owner/meeting/part_000_first.wav", key],
                            audioPartCount=2, audioPartsReadySet={0}, updatedAt=nano(NOW - 80, "123456789"))
        original = event(detail={"bucket": {"name": "assets"},
                                 "object": {"key": key, "etag": "etag", "version-id": "v1"}})
        self.inspector.plan(message(original), "delivery")
        self.meeting["audioPartsReadySet"].add(1)
        self.rejected(message(original), "AUDIO_PART_ALREADY_APPLIED")
        self.meeting["audioPartsReadySet"] = {0}
        self.meeting["audioKeys"].reverse()
        self.rejected(message(original), "INVALID_AUDIO_PART")

    def test_same_second_creation_is_allowed_but_next_second_or_precise_later_creation_is_not(self):
        self.image()
        self.meeting["createdAt"] = nano(NOW - 100, "100000001")
        att = self.table.items[("MEETING#meeting", "ATTACH#attachment")]
        att["createdAt"] = nano(NOW - 100, "234567891")
        self.inspector.plan(self.image_message(), "delivery")
        att["createdAt"] = nano(NOW - 99, "000000001")
        self.rejected(self.image_message(), "EVENT_PREDATES_ATTACHMENT")
        att["createdAt"] = nano(NOW - 100, "234567891")
        precise = self.image_message()
        body = json.loads(precise["Body"])
        body["time"] = nano(NOW - 100, "234567890")
        precise["Body"] = json.dumps(body)
        self.rejected(precise, "EVENT_PREDATES_ATTACHMENT")
        self.meeting["createdAt"] = iso(NOW - 99)
        self.rejected(self.image_message(), "EVENT_PREDATES_RESOURCE")

    def test_all_parts_uses_readiness_then_emission_order_not_truncated_event_second(self):
        self.meeting.update(audioPartCount=2, audioPartsReadySet={0, 1},
                            updatedAt=nano(NOW - 100, "123456789"),
                            allPartsEmittedAt=nano(NOW - 100, "234567891"))
        payload = message(event("ttobak.transcribe", "AllPartsTranscribed", {
            "meetingId": "meeting", "userId": "owner", "bucket": "assets", "partCount": 2}),
            function="summarize", rule="ttobak-all-parts-transcribed")
        self.inspector.plan(payload, "delivery")
        self.meeting["updatedAt"] = nano(NOW - 100, "234567892")
        self.rejected(payload, "MEETING_CHANGED_AFTER_EMISSION")
        self.meeting["updatedAt"] = nano(NOW - 100, "123456789")
        self.meeting["allPartsEmittedAt"] = nano(NOW - 99, "000000001")
        self.rejected(payload, "STALE_PARTS_EVENT")
        self.meeting["allPartsEmittedAt"] = nano(NOW - 100, "234567891")
        self.s3.value["LastModified"] = datetime.fromtimestamp(NOW - 99, timezone.utc)
        self.rejected(payload, "SOURCE_REPLACED_AFTER_EVENT")

    def test_unknown_events_and_old_messages_are_not_rewritten(self):
        self.rejected(message(event("other", "Other", {"meetingId": "meeting"})), "UNSUPPORTED_EVENT")
        payload = event()
        payload["time"] = iso(NOW - 8 * 86400)
        self.rejected(message(payload), "EVENT_OUTSIDE_RETENTION")

    def test_ambiguous_mutation_failure_is_not_retried(self):
        calls = []

        class Lambda:
            def invoke(self, **kwargs):
                calls.append(kwargs)
                raise TimeoutError("private unknown outcome")

        with self.assertRaises(TimeoutError):
            execute(self.inspector.plan(message(event()), "delivery"), Lambda())
        self.assertEqual(len(calls), 1)

    def test_cli_defaults_to_dry_run_and_rejects_execution_without_matching_confirmation(self):
        clients = []

        def client(name, **_):
            clients.append(name)
            if name == "sts":
                return SimpleNamespace(get_caller_identity=lambda: {"Account": ACCOUNT})
            if name == "s3":
                return self.s3
            self.fail("dry run or rejected execution created a mutation client")

        session = SimpleNamespace(client=client,
                                  resource=lambda *a, **k: SimpleNamespace(Table=lambda name: self.table))
        modules = {"boto3": SimpleNamespace(Session=lambda **kwargs: session),
                   "botocore.config": SimpleNamespace(Config=lambda **kwargs: kwargs)}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "one.json"
            path.write_text(json.dumps(message(event())))
            argv = ["replay", "--message-file", str(path), "--kind", "delivery", "--owner-id", "owner",
                    "--meeting-id", "meeting"]
            for extra, code in (([], None), (["--execute"], "WORKER_QUIESCENCE_REQUIRED"),
                                (["--execute", "--workers-quiesced", "--confirm-plan", "wrong"], "PLAN_CHANGED")):
                out, err = io.StringIO(), io.StringIO()
                with patch.dict("sys.modules", modules), patch("sys.argv", argv + extra), \
                        patch("replay_pipeline_failure.Inspector", return_value=self.inspector), \
                        redirect_stdout(out), redirect_stderr(err):
                    result = main()
                self.assertEqual(result, 0 if code is None else 1)
                response = json.loads(err.getvalue() if code else out.getvalue())
                self.assertEqual(response.get("code") if code else response["status"], code or "dry_run")
                self.assertNotIn("final.wav", out.getvalue() + err.getvalue())
                self.assertNotIn("Body", out.getvalue() + err.getvalue())
        self.assertNotIn("lambda", clients)


if __name__ == "__main__":
    unittest.main()
