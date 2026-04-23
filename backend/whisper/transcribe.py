import json
import os
import sys
import time

import boto3
from faster_whisper import WhisperModel

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
BUCKET = os.environ["BUCKET_NAME"]
TABLE = os.environ["TABLE_NAME"]

s3 = boto3.client("s3", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)


def main():
    audio_key = os.environ["AUDIO_KEY"]
    meeting_id = os.environ["MEETING_ID"]
    user_id = os.environ["USER_ID"]

    ext = audio_key.rsplit(".", 1)[-1]
    local_path = f"/tmp/audio.{ext}"

    print(f"Downloading s3://{BUCKET}/{audio_key}")
    s3.download_file(BUCKET, audio_key, local_path)
    file_mb = os.path.getsize(local_path) / 1048576
    print(f"Audio: {file_mb:.1f} MB")

    print("Loading Whisper large-v3 (GPU float16)...")
    model = WhisperModel("large-v3", device="cuda", compute_type="float16")

    print("Transcribing...")
    start = time.time()
    segments, info = model.transcribe(
        local_path,
        language="ko",
        beam_size=5,
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
        word_timestamps=True,
    )

    all_segments = []
    for seg in segments:
        all_segments.append({
            "start": round(seg.start, 2),
            "end": round(seg.end, 2),
            "text": seg.text.strip(),
        })

    elapsed = time.time() - start
    transcript_text = " ".join(s["text"] for s in all_segments)
    print(f"Done: {len(transcript_text):,} chars in {elapsed:.1f}s")

    result = {
        "results": {
            "transcripts": [{"transcript": transcript_text}],
        },
        "status": "COMPLETED",
        "whisper_metadata": {
            "engine": "whisper-large-v3-gpu",
            "language": info.language,
            "language_probability": round(info.language_probability, 3),
            "duration_seconds": round(elapsed, 1),
            "segments": all_segments,
        },
    }

    output_key = f"transcripts/{meeting_id}.json"
    s3.put_object(
        Bucket=BUCKET,
        Key=output_key,
        Body=json.dumps(result, ensure_ascii=False, indent=2).encode("utf-8"),
        ContentType="application/json",
    )
    print(f"Uploaded s3://{BUCKET}/{output_key}")
    print("Transcript uploaded — EventBridge will trigger summarize Lambda")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        meeting_id = os.environ.get("MEETING_ID", "")
        user_id = os.environ.get("USER_ID", "")
        if meeting_id and user_id:
            try:
                dynamodb.Table(TABLE).update_item(
                    Key={"PK": f"USER#{user_id}", "SK": f"MEETING#{meeting_id}"},
                    UpdateExpression="SET #s = :s",
                    ExpressionAttributeNames={"#s": "status"},
                    ExpressionAttributeValues={":s": "error"},
                )
            except Exception:
                pass
        sys.exit(1)
