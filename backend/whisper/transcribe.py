import json
import os
import sys
import time
import unicodedata

import boto3
from faster_whisper import WhisperModel

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
BUCKET = os.environ["BUCKET_NAME"]
TABLE = os.environ["TABLE_NAME"]
VOCAB_KEY = os.environ.get("VOCAB_KEY", "config/custom-vocabulary.txt")
MODEL_S3_KEY = os.environ.get("MODEL_S3_KEY", "models/faster-whisper-large-v3.tar.gz")
MODEL_LOCAL_DIR = "/tmp/whisper-model"

s3 = boto3.client("s3", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)


def _ensure_model() -> str:
    if os.path.exists(os.path.join(MODEL_LOCAL_DIR, "model.bin")):
        print("Model already cached locally")
        return MODEL_LOCAL_DIR

    print(f"Downloading model from s3://{BUCKET}/{MODEL_S3_KEY}")
    start = time.time()
    archive = "/tmp/model.tar.gz"
    s3.download_file(BUCKET, MODEL_S3_KEY, archive)
    os.makedirs(MODEL_LOCAL_DIR, exist_ok=True)

    import tarfile
    with tarfile.open(archive) as tar:
        tar.extractall(MODEL_LOCAL_DIR)
    os.remove(archive)
    elapsed = time.time() - start
    print(f"Model ready ({elapsed:.0f}s)")
    return MODEL_LOCAL_DIR


def _load_custom_vocab_prompt() -> str:
    try:
        resp = s3.get_object(Bucket=BUCKET, Key=VOCAB_KEY)
        lines = resp["Body"].read().decode("utf-8").strip().split("\n")
        terms = []
        for line in lines[1:]:
            cols = line.split("\t")
            display = cols[2].strip() if len(cols) >= 3 else cols[0].strip()
            if display:
                terms.append(display)
        prompt = " ".join(terms)
        print(f"Custom vocab loaded: {len(terms)} terms")
        return prompt
    except Exception as e:
        print(f"Custom vocab not available: {e}")
        return ""


def _resolve_s3_key(raw_key: str) -> str:
    """Try NFC/NFD variants to find the actual S3 key.

    S3 stores keys as uploaded bytes. Browser uploads use NFC (composed),
    macOS/iOS may use NFD (decomposed). Try the raw key first, then variants.
    """
    from botocore.exceptions import ClientError
    for candidate in (raw_key,
                      unicodedata.normalize("NFC", raw_key),
                      unicodedata.normalize("NFD", raw_key)):
        try:
            s3.head_object(Bucket=BUCKET, Key=candidate)
            if candidate != raw_key:
                print(f"S3 key resolved via normalization: {raw_key!r} → {candidate!r}")
            return candidate
        except ClientError:
            continue
    return raw_key


def main():
    audio_key = _resolve_s3_key(os.environ["AUDIO_KEY"])
    meeting_id = os.environ["MEETING_ID"]
    user_id = os.environ["USER_ID"]

    ext = audio_key.rsplit(".", 1)[-1]
    local_path = f"/tmp/audio.{ext}"

    print(f"Downloading s3://{BUCKET}/{audio_key}")
    s3.download_file(BUCKET, audio_key, local_path)
    file_mb = os.path.getsize(local_path) / 1048576
    print(f"Audio: {file_mb:.1f} MB")

    vocab_prompt = _load_custom_vocab_prompt()

    # Merge with INITIAL_PROMPT env var (user's custom dictionary from DynamoDB)
    env_prompt = os.environ.get("INITIAL_PROMPT", "").strip()
    if env_prompt:
        print(f"INITIAL_PROMPT from env: {len(env_prompt.split(','))} terms")
        if vocab_prompt:
            vocab_prompt = f"{vocab_prompt} {env_prompt}"
        else:
            vocab_prompt = env_prompt

    model_path = _ensure_model()
    print("Loading Whisper large-v3 (GPU float16)...")
    model = WhisperModel(model_path, device="cuda", compute_type="float16")

    print("Transcribing...")
    start = time.time()
    transcribe_kwargs = dict(
        language="ko",
        beam_size=5,
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
        word_timestamps=True,
    )
    if vocab_prompt:
        transcribe_kwargs["initial_prompt"] = vocab_prompt
    segments, info = model.transcribe(local_path, **transcribe_kwargs)

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
