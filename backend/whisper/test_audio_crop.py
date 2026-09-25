import io
import json
import pathlib
import os
import uuid
from urllib.parse import urlparse
import struct
import subprocess
import tempfile
import threading
import unittest
import wave
from unittest import mock

from botocore.exceptions import ClientError
from botocore.response import StreamingBody

from audio_crop import _source_key, _now, run_crop, trim_audio
from datetime import datetime, timezone


def audio_bytes():
    output = io.BytesIO()
    with wave.open(output, "wb") as audio:
        audio.setnchannels(1)
        audio.setsampwidth(2)
        audio.setframerate(16000)
        audio.writeframes(b"".join(struct.pack("<h", sample) * 16000 for sample in [100, 200, 300, 400]))
    return output.getvalue()


class AudioCropTests(unittest.TestCase):
    def fixture(self):
        crop = {"state": "queued", "sourceMeetingId": "source", "sourceKey": "audio/owner/source/original.wav",
                "sourceETag": '"revision"', "startSeconds": 1, "endSeconds": 3}
        table = mock.Mock()
        table.get_item.side_effect = [
            {"Item": {"userId": "owner", "audioCrop": crop}},
            {"Item": {"userId": "owner", "meetingId": "source", "audioKey": crop["sourceKey"]}},
        ]
        source = audio_bytes()
        s3 = mock.Mock()
        s3.get_object.return_value = {"ContentLength": len(source), "Body": StreamingBody(io.BytesIO(source), len(source))}
        return table, s3, crop

    def test_revision_timestamps_match_go_rfc3339nano_precision(self):
        for micros, suffix in [(0, "00Z"), (100000, "00.1Z"), (120000, "00.12Z"), (123456, "00.123456Z")]:
            with mock.patch("audio_crop.datetime") as clock:
                clock.now.return_value = datetime(2026, 9, 25, 0, 0, 0, micros, tzinfo=timezone.utc)
                self.assertEqual(_now(), "2026-09-25T00:00:" + suffix)

    def test_real_crop_excludes_before_and_after_audio(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "source.wav"
            output = pathlib.Path(directory) / "crop.wav"
            source.write_bytes(audio_bytes())
            trim_audio(source, output, 1, 3)
            with wave.open(str(output), "rb") as audio:
                self.assertEqual(audio.getnframes(), 32000)
                self.assertEqual(audio.readframes(32000), struct.pack("<h", 200) * 16000 + struct.pack("<h", 300) * 16000)
            self.assertEqual(source.read_bytes(), audio_bytes())

    def test_range_past_end_fails_instead_of_silently_shortening(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "source.wav"
            source.write_bytes(audio_bytes())
            with self.assertRaises(ValueError):
                trim_audio(source, pathlib.Path(directory) / "crop.wav", 1, 9)

    def test_browser_webm_m4a_and_ogg_are_supported(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "source.wav"
            source.write_bytes(audio_bytes())
            for extension, codec in [("webm", "libopus"), ("m4a", "aac"), ("ogg", "libopus")]:
                encoded = pathlib.Path(directory) / f"source.{extension}"
                subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-i", str(source), "-c:a", codec, str(encoded)],
                               check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
                trim_audio(encoded, pathlib.Path(directory) / f"{extension}.wav", 1, 3)

    def test_playlist_demuxers_cannot_read_other_local_files(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "source.wav"
            source.write_bytes(audio_bytes())
            playlist = pathlib.Path(directory) / "playlist"
            playlist.write_text(f"ffconcat version 1.0\nfile '{source}'\n")
            with self.assertRaises(subprocess.CalledProcessError):
                trim_audio(playlist, pathlib.Path(directory) / "crop.wav", 0, 2)

    def test_only_cropped_bytes_are_transcribed_and_published(self):
        table, s3, crop = self.fixture()
        uploads = []
        def upload(**request):
            body = request["Body"]
            uploads.append((request["Key"], body.read() if hasattr(body, "read") else body))
        s3.put_object.side_effect = upload
        def transcribe(path):
            with wave.open(path, "rb") as audio:
                self.assertEqual(audio.getnframes(), 32000)
            return {"results": {"transcripts": [{"transcript": "selected speech"}]}}
        run_crop(s3, table, "bucket", "owner", "copy", transcribe)
        s3.get_object.assert_called_once_with(Bucket="bucket", Key=crop["sourceKey"], IfMatch='"revision"')
        self.assertEqual(len(uploads), 2)
        self.assertTrue(uploads[0][0].startswith("audio/owner/copy/crop_result_"))
        self.assertEqual(uploads[1][0], "transcripts/copy.json")
        self.assertEqual(json.loads(uploads[1][1])["results"]["transcripts"][0]["transcript"], "selected speech")
        publication = table.update_item.call_args_list[1].kwargs
        self.assertIn("audioCrop.runId = :run", publication["ConditionExpression"])
        self.assertIn("attribute_not_exists(audioKey)", publication["ConditionExpression"])

    def test_duplicate_delivery_does_not_read_audio_or_transcribe(self):
        table, s3, _ = self.fixture()
        table.update_item.side_effect = ClientError({"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem")
        transcribe = mock.Mock()
        run_crop(s3, table, "bucket", "owner", "copy", transcribe)
        s3.get_object.assert_not_called()
        transcribe.assert_not_called()

    def test_replaced_source_or_revoked_source_fails_before_bytes(self):
        for replacement in [None, {"userId": "other", "meetingId": "source", "audioKey": "audio/owner/source/original.wav"},
                            {"userId": "owner", "meetingId": "source", "audioKey": "audio/owner/source/new.wav"}]:
            table, s3, crop = self.fixture()
            table.get_item.side_effect = [{"Item": {"userId": "owner", "audioCrop": crop}}, {"Item": replacement}]
            with self.assertRaises(RuntimeError):
                run_crop(s3, table, "bucket", "owner", "copy", mock.Mock())
            s3.get_object.assert_not_called()
            s3.put_object.assert_not_called()

    def test_failed_publication_never_emits_transcript(self):
        table, s3, _ = self.fixture()
        table.update_item.side_effect = [None, ClientError({"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem"), None]
        with self.assertRaises(RuntimeError):
            run_crop(s3, table, "bucket", "owner", "copy", lambda path: {})
        self.assertEqual(s3.put_object.call_count, 1)
        self.assertNotIn("transcripts/", s3.put_object.call_args.kwargs["Key"])

    def test_long_transcription_refreshes_the_owned_claim(self):
        table, s3, _ = self.fixture()
        refreshed = threading.Event()
        def update(**request):
            if request["UpdateExpression"] == "SET updatedAt = :now":
                refreshed.set()
        table.update_item.side_effect = update
        def transcribe(path):
            self.assertTrue(refreshed.wait(1), "heartbeat must run during transcription")
            return {}
        with mock.patch("audio_crop.HEARTBEAT_SECONDS", 0.01):
            run_crop(s3, table, "bucket", "owner", "copy", transcribe)
        claim = table.update_item.call_args_list[0].kwargs["ExpressionAttributeValues"][":run"]
        beats = [call.kwargs for call in table.update_item.call_args_list if call.kwargs["UpdateExpression"] == "SET updatedAt = :now"]
        self.assertTrue(beats)
        self.assertTrue(all(beat["ExpressionAttributeValues"][":run"] == claim for beat in beats))
        self.assertEqual(s3.put_object.call_count, 2)

    def test_lost_heartbeat_claim_cannot_publish_a_transcript(self):
        table, s3, _ = self.fixture()
        rejected = threading.Event()
        def update(**request):
            if request["UpdateExpression"] == "SET updatedAt = :now":
                rejected.set()
                raise ClientError({"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem")
        table.update_item.side_effect = update
        def transcribe(path):
            self.assertTrue(rejected.wait(1))
            return {}
        with mock.patch("audio_crop.HEARTBEAT_SECONDS", 0.01), self.assertRaises(RuntimeError):
            run_crop(s3, table, "bucket", "owner", "copy", transcribe)
        self.assertFalse(any(call.kwargs["Key"].startswith("transcripts/") for call in s3.put_object.call_args_list))
        self.assertNotIn("#status =", table.update_item.call_args.kwargs["ConditionExpression"])

    def test_ambiguous_publication_retains_audio_but_definite_rejection_cleans_it(self):
        for code in ("InternalServerError", "ConditionalCheckFailedException"):
            table, s3, _ = self.fixture()
            table.update_item.side_effect = [None, ClientError({"Error": {"Code": code}}, "UpdateItem"), None]
            with self.assertRaises(RuntimeError):
                run_crop(s3, table, "bucket", "owner", "copy", lambda path: {})
            if code == "InternalServerError":
                s3.delete_object.assert_not_called()
            else:
                s3.delete_object.assert_called_once_with(Bucket="bucket", Key=s3.put_object.call_args.kwargs["Key"])

    def test_rejected_audio_cleanup_failure_is_reported(self):
        table, s3, _ = self.fixture()
        table.update_item.side_effect = [None, ClientError({"Error": {"Code": "ConditionalCheckFailedException"}}, "UpdateItem"), None]
        s3.delete_object.side_effect = RuntimeError("storage unavailable")
        with self.assertRaisesRegex(RuntimeError, "cleanup could not be confirmed"):
            run_crop(s3, table, "bucket", "owner", "copy", lambda path: {})

    def test_invalid_source_paths_are_rejected(self):
        for key in ["audio/other/source/file.wav", "audio/owner/other/file.wav", "audio/owner/source/../file.wav",
                    "audio/owner/source/%2e%2e.wav", "audio/owner/source/", "audio/owner/source/.."]:
            with self.assertRaises(ValueError):
                _source_key({"userId": "owner", "meetingId": "source", "audioKey": key}, "owner", "source")


@unittest.skipUnless(os.environ.get("DYNAMODB_LOCAL_ENDPOINT"), "requires a loopback DynamoDB Local endpoint")
class DynamoDBLocalCropTests(unittest.TestCase):
    def test_worker_publication_uses_valid_dynamodb_expressions(self):
        import boto3
        from botocore.config import Config
        endpoint = os.environ["DYNAMODB_LOCAL_ENDPOINT"]
        parsed = urlparse(endpoint)
        self.assertEqual(parsed.scheme, "http")
        self.assertIn(parsed.hostname, ("127.0.0.1", "localhost"))
        database = boto3.resource("dynamodb", endpoint_url=endpoint, region_name="us-west-2",
                                  aws_access_key_id="local", aws_secret_access_key="local", config=Config(retries={"total_max_attempts": 1}))
        table = database.create_table(
            TableName="crop-test-" + uuid.uuid4().hex,
            KeySchema=[{"AttributeName": "PK", "KeyType": "HASH"}, {"AttributeName": "SK", "KeyType": "RANGE"}],
            AttributeDefinitions=[{"AttributeName": name, "AttributeType": "S"} for name in ("PK", "SK")],
            BillingMode="PAY_PER_REQUEST",
        )
        try:
            table.wait_until_exists()
            _, s3, crop = AudioCropTests().fixture()
            source = {"PK": "USER#owner", "SK": "MEETING#source", "userId": "owner", "meetingId": "source",
                      "audioKey": crop["sourceKey"]}
            table.put_item(Item=source)
            key = {"PK": "USER#owner", "SK": "MEETING#copy"}
            table.put_item(Item={**key, "userId": "owner", "meetingId": "copy", "status": "transcribing", "audioCrop": crop})
            transcribe = mock.Mock(return_value={"results": {"transcripts": [{"transcript": "selected speech"}]}})
            run_crop(s3, table, "bucket", "owner", "copy", transcribe)
            saved = table.get_item(Key=key, ConsistentRead=True)["Item"]
            self.assertEqual(saved["audioCrop"]["state"], "done")
            self.assertEqual(saved["duration"], 2)
            self.assertTrue(saved["audioKey"].startswith("audio/owner/copy/crop_result_"))
            self.assertEqual(s3.put_object.call_args.kwargs["Key"], "transcripts/copy.json")
            run_crop(s3, table, "bucket", "owner", "copy", transcribe)
            transcribe.assert_called_once()
            self.assertEqual(table.get_item(Key={"PK": "USER#owner", "SK": "MEETING#source"}, ConsistentRead=True)["Item"], source)
            # Expiry/status changes must terminate the owned run and remove the
            # rejected WAV even though status is no longer transcribing.
            _, next_s3, _ = AudioCropTests().fixture()
            expired = {"PK": "USER#owner", "SK": "MEETING#expired"}
            table.put_item(Item={**expired, "userId": "owner", "meetingId": "expired", "status": "transcribing", "audioCrop": crop})
            def expire(path):
                table.update_item(Key=expired, UpdateExpression="SET #s=:s", ExpressionAttributeNames={"#s": "status"}, ExpressionAttributeValues={":s": "error"})
                return {}
            with self.assertRaises(RuntimeError):
                run_crop(next_s3, table, "bucket", "owner", "expired", expire)
            failed = table.get_item(Key=expired, ConsistentRead=True)["Item"]
            self.assertEqual(failed["audioCrop"]["state"], "failed")
            self.assertEqual(failed["status"], "error")
            self.assertNotIn("audioKey", failed)
            next_s3.delete_object.assert_called_once_with(Bucket="bucket", Key=next_s3.put_object.call_args.kwargs["Key"])
        finally:
            table.delete()
