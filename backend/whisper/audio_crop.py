import json
import os
import pathlib
import subprocess
import tempfile
import threading
import uuid
import wave
from datetime import datetime, timezone

from botocore.exceptions import ClientError

MAX_SOURCE_BYTES = 2 * 1024 * 1024 * 1024
HEARTBEAT_SECONDS = 30


def _now():
    value = datetime.now(timezone.utc).isoformat(timespec="microseconds").removesuffix("+00:00")
    return value.rstrip("0").rstrip(".") + "Z"


class CropHeartbeat:
    def __init__(self, table, key, run_id):
        self.stop = threading.Event()
        self.failure = None
        def run():
            while not self.stop.wait(HEARTBEAT_SECONDS):
                try:
                    table.update_item(
                        Key=key, UpdateExpression="SET updatedAt = :now",
                        ConditionExpression="audioCrop.runId = :run AND audioCrop.#state = :processing AND #status = :transcribing",
                        ExpressionAttributeNames={"#state": "state", "#status": "status"},
                        ExpressionAttributeValues={":run": run_id, ":processing": "processing", ":transcribing": "transcribing", ":now": _now()},
                    )
                except Exception as error:
                    self.failure = error
                    return
        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()

    def close(self):
        self.stop.set()
        self.thread.join(timeout=25)
        if self.thread.is_alive() or self.failure:
            raise RuntimeError("Audio crop lost its processing claim")


def _source_key(source, user_id, meeting_id):
    if not source or source.get("userId") != user_id or source.get("meetingId") != meeting_id:
        raise ValueError("source meeting unavailable")
    keys = source.get("audioKeys") or [source.get("audioKey")]
    if len(keys) != 1 or not isinstance(keys[0], str) or source.get("audioPartCount", 0) > 1:
        raise ValueError("source must contain one recording")
    key = keys[0]
    prefix = f"audio/{user_id}/{meeting_id}/"
    if not key.startswith(prefix) or not key[len(prefix):] or any(character in key[len(prefix):] for character in "/\\%"):
        raise ValueError("invalid source audio key")
    if pathlib.PurePosixPath(key).as_posix() != key or key[len(prefix):] in (".", ".."):
        raise ValueError("invalid source audio key")
    return key


def trim_audio(source_path, output_path, start_seconds, end_seconds):
    if start_seconds < 0 or end_seconds <= start_seconds or end_seconds > 86400 or end_seconds - start_seconds > 21600:
        raise ValueError("invalid audio range")
    subprocess.run([
        "ffmpeg", "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe",
        "-format_whitelist", "wav,matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,mp3,ogg,flac,aac,caf",
        "-i", str(source_path), "-ss", str(start_seconds), "-t", str(end_seconds - start_seconds),
        "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", str(output_path),
    ], check=True, timeout=900, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    with wave.open(str(output_path), "rb") as audio:
        if audio.getnchannels() != 1 or audio.getsampwidth() != 2 or audio.getframerate() != 16000:
            raise ValueError("unexpected crop format")
        duration = audio.getnframes() / audio.getframerate()
        if abs(duration - (end_seconds - start_seconds)) > 0.25:
            raise ValueError("selected range exceeds the recorded audio")


def run_crop(s3, table, bucket, user_id, meeting_id, transcribe):
    key = {"PK": f"USER#{user_id}", "SK": f"MEETING#{meeting_id}"}
    meeting = table.get_item(Key=key, ConsistentRead=True).get("Item")
    crop = meeting.get("audioCrop") if meeting else None
    if not crop or crop.get("state") != "queued" or meeting.get("status") != "transcribing" or meeting.get("userId") != user_id:
        return
    run_id = uuid.uuid4().hex
    candidate_key = f"audio/{user_id}/{meeting_id}/crop_result_{run_id}.wav"
    try:
        table.update_item(
            Key=key,
            UpdateExpression="SET audioCrop.#state = :processing, audioCrop.runId = :run, audioCrop.resultKey = :result, #status = :transcribing, updatedAt = :now",
            ConditionExpression="audioCrop.#state = :queued AND #status = :transcribing AND userId = :user AND attribute_not_exists(audioKey)",
            ExpressionAttributeNames={"#state": "state", "#status": "status"},
            ExpressionAttributeValues={":processing": "processing", ":run": run_id, ":result": candidate_key, ":transcribing": "transcribing",
                                       ":now": _now(), ":queued": "queued", ":user": user_id},
        )
    except ClientError as error:
        if error.response["Error"]["Code"] == "ConditionalCheckFailedException":
            return
        raise

    heartbeat = None
    output_key = None
    publication_attempted = publication_rejected = published = False
    try:
        source_id = crop["sourceMeetingId"]
        source = table.get_item(Key={"PK": f"USER#{user_id}", "SK": f"MEETING#{source_id}"}, ConsistentRead=True).get("Item")
        source_key = _source_key(source, user_id, source_id)
        if source_key != crop["sourceKey"] or not crop.get("sourceETag"):
            raise ValueError("source audio changed")
        raw_start, raw_end = crop["startSeconds"], crop["endSeconds"]
        start_seconds, end_seconds = int(raw_start), int(raw_end)
        if any(isinstance(raw, bool) or raw != value for raw, value in ((raw_start, start_seconds), (raw_end, end_seconds))):
            raise ValueError("audio range must use integer seconds")
        if start_seconds < 0 or end_seconds <= start_seconds or end_seconds > 86400 or end_seconds - start_seconds > 21600:
            raise ValueError("invalid audio range")
        heartbeat = CropHeartbeat(table, key, run_id)
        with tempfile.TemporaryDirectory(prefix="ttobak-crop-") as directory:
            source_path = pathlib.Path(directory) / "source"
            output_path = pathlib.Path(directory) / "cropped.wav"
            response = s3.get_object(Bucket=bucket, Key=source_key, IfMatch=crop["sourceETag"])
            body = response["Body"]
            try:
                length = response.get("ContentLength", 0)
                if not 0 < length <= MAX_SOURCE_BYTES:
                    raise ValueError("source audio exceeds size limit")
                total = 0
                with source_path.open("wb") as output:
                    for chunk in body.iter_chunks(chunk_size=1024 * 1024):
                        total += len(chunk)
                        if total > length:
                            raise ValueError("source audio size changed")
                        output.write(chunk)
                if total != length:
                    raise ValueError("source download incomplete")
            finally:
                body.close()
            trim_audio(source_path, output_path, start_seconds, end_seconds)
            result = transcribe(str(output_path))
            if heartbeat.failure:
                raise RuntimeError("Audio crop lost its processing claim")
            output_key = candidate_key
            with output_path.open("rb") as audio:
                s3.put_object(Bucket=bucket, Key=output_key, Body=audio, ContentType="audio/wav", IfNoneMatch="*")
            # Only the heartbeat accesses the Table during media work. Join it
            # before publication so boto3 resource calls never overlap.
            heartbeat.close()
            heartbeat = None
            publication_attempted = True
            try:
                table.update_item(
                    Key=key,
                    UpdateExpression="SET audioKey = :audio, #duration = :duration, audioCrop.#state = :done, updatedAt = :now",
                    ConditionExpression="audioCrop.runId = :run AND audioCrop.#state = :processing AND #status = :transcribing AND attribute_not_exists(audioKey)",
                    ExpressionAttributeNames={"#state": "state", "#status": "status", "#duration": "duration"},
                    ExpressionAttributeValues={":audio": output_key, ":duration": end_seconds - start_seconds,
                                               ":done": "done", ":now": _now(), ":run": run_id, ":processing": "processing",
                                               ":transcribing": "transcribing"},
                )
            except ClientError as error:
                publication_rejected = error.response["Error"]["Code"] in (
                    "ConditionalCheckFailedException", "ValidationException", "ResourceNotFoundException", "AccessDeniedException")
                raise
            published = True
            s3.put_object(
                Bucket=bucket, Key=f"transcripts/{meeting_id}.json",
                Body=json.dumps(result, ensure_ascii=False).encode("utf-8"), ContentType="application/json",
            )
    except Exception:
        if heartbeat:
            try:
                heartbeat.close()
            except Exception:
                pass
        if published:
            # A timed-out PUT may already have emitted its S3 event. Keep the
            # bound run eligible for that event and reconcile missing output.
            raise RuntimeError("Cropped audio is bound; transcript publication is unconfirmed and retained for reconciliation") from None
        cleanup_failed = False
        if output_key and (not publication_attempted or publication_rejected):
            try:
                s3.delete_object(Bucket=bucket, Key=output_key)
            except Exception as cleanup_error:
                cleanup_failed = True
                print(f"Rejected crop cleanup failed: {type(cleanup_error).__name__}")
        try:
            if heartbeat and heartbeat.thread.is_alive():
                raise RuntimeError("heartbeat shutdown incomplete")
            # Expiry may already have set status=error; the owned processing run
            # must still become failed. Never overwrite an advanced summary.
            condition = "audioCrop.runId = :run AND audioCrop.#state = :owned AND attribute_not_exists(audioKey)"
            values = {":failed": "failed", ":error": "error", ":now": _now(), ":run": run_id,
                      ":owned": "processing"}
            values[":transcribing"] = "transcribing"
            condition += " AND (#status = :transcribing OR #status = :error)"
            table.update_item(
                Key=key, UpdateExpression="SET audioCrop.#state = :failed, #status = :error, updatedAt = :now",
                ConditionExpression=condition,
                ExpressionAttributeNames={"#state": "state", "#status": "status"},
                ExpressionAttributeValues=values,
            )
        except Exception as update_error:
            print(f"Audio crop failure status unavailable: {type(update_error).__name__}")
        suffix = "; temporary audio cleanup could not be confirmed" if cleanup_failed else ""
        raise RuntimeError("Audio crop failed; original recording is unchanged" + suffix) from None
