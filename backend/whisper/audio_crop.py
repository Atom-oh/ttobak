import json
import os
import pathlib
import subprocess
import tempfile
import uuid
import wave
from datetime import datetime, timezone

from botocore.exceptions import ClientError

MAX_SOURCE_BYTES = 2 * 1024 * 1024 * 1024


def _now():
    value = datetime.now(timezone.utc).isoformat(timespec="microseconds").removesuffix("+00:00")
    return value.rstrip("0").rstrip(".") + "Z"


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
    if not crop or crop.get("state") != "queued" or meeting.get("userId") != user_id:
        return
    run_id = uuid.uuid4().hex
    try:
        table.update_item(
            Key=key,
            UpdateExpression="SET audioCrop.#state = :processing, audioCrop.runId = :run, #status = :transcribing, updatedAt = :now",
            ConditionExpression="audioCrop.#state = :queued AND userId = :user AND attribute_not_exists(audioKey)",
            ExpressionAttributeNames={"#state": "state", "#status": "status"},
            ExpressionAttributeValues={":processing": "processing", ":run": run_id, ":transcribing": "transcribing",
                                       ":now": _now(), ":queued": "queued", ":user": user_id},
        )
    except ClientError as error:
        if error.response["Error"]["Code"] == "ConditionalCheckFailedException":
            return
        raise

    try:
        source_id = crop["sourceMeetingId"]
        source = table.get_item(Key={"PK": f"USER#{user_id}", "SK": f"MEETING#{source_id}"}, ConsistentRead=True).get("Item")
        source_key = _source_key(source, user_id, source_id)
        if source_key != crop["sourceKey"] or not crop.get("sourceETag"):
            raise ValueError("source audio changed")
        start_seconds, end_seconds = int(crop["startSeconds"]), int(crop["endSeconds"])
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
            output_key = f"audio/{user_id}/{meeting_id}/crop_result_{run_id}.wav"
            with output_path.open("rb") as audio:
                s3.put_object(Bucket=bucket, Key=output_key, Body=audio, ContentType="audio/wav", IfNoneMatch="*")
            table.update_item(
                Key=key,
                UpdateExpression="SET audioKey = :audio, #duration = :duration, audioCrop.#state = :done, updatedAt = :now",
                ConditionExpression="audioCrop.runId = :run AND audioCrop.#state = :processing AND #status = :transcribing AND attribute_not_exists(audioKey)",
                ExpressionAttributeNames={"#state": "state", "#status": "status", "#duration": "duration"},
                ExpressionAttributeValues={":audio": output_key, ":duration": end_seconds - start_seconds,
                                           ":done": "done", ":now": _now(), ":run": run_id, ":processing": "processing",
                                           ":transcribing": "transcribing"},
            )
            s3.put_object(
                Bucket=bucket, Key=f"transcripts/{meeting_id}.json",
                Body=json.dumps(result, ensure_ascii=False).encode("utf-8"), ContentType="application/json",
            )
    except Exception:
        try:
            table.update_item(
                Key=key,
                UpdateExpression="SET audioCrop.#state = :failed, #status = :error, updatedAt = :now",
                ConditionExpression="audioCrop.runId = :run AND #status = :transcribing",
                ExpressionAttributeNames={"#state": "state", "#status": "status"},
                ExpressionAttributeValues={":failed": "failed", ":error": "error", ":now": _now(), ":run": run_id,
                                           ":transcribing": "transcribing"},
            )
        except Exception as update_error:
            print(f"Audio crop failure status unavailable: {type(update_error).__name__}")
        raise RuntimeError("Audio crop failed; original recording is unchanged") from None
