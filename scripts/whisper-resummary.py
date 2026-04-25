#!/usr/bin/env python3
"""
Whisper Re-Transcription + Bedrock Re-Summary for existing Ttobak meetings.
Downloads audio from S3, transcribes with faster-whisper (CPU, large-v3),
updates DynamoDB transcriptB, and triggers Bedrock re-summarization.
"""
import os
import sys
import json
import time
import tempfile
import boto3
from datetime import datetime

# Config
BUCKET = "ttobak-assets-180294183052"
TABLE = "ttobak-main"
REGION = "ap-northeast-2"
WHISPER_MODEL = "large-v3"
BEDROCK_MODEL = "anthropic.claude-sonnet-4-6-20250514"

s3 = boto3.client("s3", region_name=REGION)
ddb = boto3.resource("dynamodb", region_name=REGION)
table = ddb.Table(TABLE)
bedrock = boto3.client("bedrock-runtime", region_name=REGION)


def list_meetings():
    """List meetings with audio files from S3."""
    resp = s3.list_objects_v2(Bucket=BUCKET, Prefix="audio/", Delimiter="")
    files = []
    for obj in resp.get("Contents", []):
        key = obj["Key"]
        if "recording_progress" in key or "checkpoint" in key:
            continue
        if obj["Size"] < 10000:  # skip tiny files
            continue
        parts = key.split("/")
        if len(parts) >= 4:
            meeting_id = parts[2]
            files.append({
                "key": key,
                "meeting_id": meeting_id,
                "size_mb": round(obj["Size"] / 1024 / 1024, 1),
                "filename": parts[-1],
            })
    return files


def get_meeting(meeting_id):
    """Get meeting record from DynamoDB using GSI3."""
    ddb_client = boto3.client("dynamodb", region_name=REGION)
    resp = ddb_client.query(
        TableName=TABLE,
        IndexName="GSI3",
        KeyConditionExpression="meetingId = :mid",
        FilterExpression="entityType = :et",
        ExpressionAttributeValues={
            ":mid": {"S": meeting_id},
            ":et": {"S": "MEETING"},
        },
    )
    for item in resp.get("Items", []):
        if item.get("entityType", {}).get("S") == "MEETING":
            return {
                "PK": item["PK"]["S"],
                "SK": item["SK"]["S"],
                "meetingId": item["meetingId"]["S"],
                "title": item.get("title", {}).get("S", ""),
                "status": item.get("status", {}).get("S", ""),
                "userId": item["userId"]["S"],
            }
    return None


def transcribe_with_whisper(audio_path):
    """Transcribe audio file with faster-whisper."""
    from faster_whisper import WhisperModel

    print(f"  Loading Whisper {WHISPER_MODEL} (CPU, int8)...")
    model = WhisperModel(WHISPER_MODEL, device="cpu", compute_type="int8")

    print(f"  Transcribing {audio_path}...")
    start = time.time()
    segments, info = model.transcribe(
        audio_path,
        beam_size=5,
        language=None,  # auto-detect
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
    )

    transcript_parts = []
    for seg in segments:
        line = f"[{seg.start:.1f}s-{seg.end:.1f}s] {seg.text.strip()}"
        transcript_parts.append(line)
        if len(transcript_parts) % 20 == 0:
            print(f"    ...{len(transcript_parts)} segments processed")

    elapsed = time.time() - start
    transcript = "\n".join(transcript_parts)
    print(f"  Whisper done: {len(transcript_parts)} segments, {elapsed:.0f}s, detected lang: {info.language}")
    return transcript, info.language


def summarize_with_bedrock(transcript, title):
    """Generate meeting summary using Bedrock Claude."""
    print(f"  Summarizing with Bedrock ({BEDROCK_MODEL})...")

    system = """You are an expert meeting assistant. Create comprehensive, well-structured meeting notes in Markdown.

Your output MUST follow this exact structure:

# 회의록

## 참석자
- 화자별 식별 및 주요 역할 추정

## 개요
- 회의 핵심 요약 (3-5문장)

## 주요 논의 사항
- 논의된 핵심 토픽 (상세하게)

## 결정 사항
- 합의된 결정들

## 액션 아이템
- [ ] 담당자: 할 일 내용

Format in Korean. Use bullet points and checkboxes."""

    user_prompt = f"회의 제목: {title}\n\n다음 회의 녹취록을 바탕으로 회의록을 작성해주세요:\n\n{transcript[:80000]}"

    body = json.dumps({
        "anthropic_version": "bedrock-2023-05-31",
        "max_tokens": 4096,
        "system": system,
        "messages": [{"role": "user", "content": user_prompt}],
    })

    resp = bedrock.invoke_model(modelId=BEDROCK_MODEL, body=body)
    result = json.loads(resp["body"].read())
    content = result["content"][0]["text"]
    print(f"  Summary generated: {len(content)} chars")
    return content


def update_meeting(meeting, transcript, summary):
    """Update meeting in DynamoDB with Whisper transcript and new summary."""
    now = datetime.utcnow().isoformat() + "Z"
    table.update_item(
        Key={"PK": meeting["PK"], "SK": meeting["SK"]},
        UpdateExpression="SET transcriptB = :tb, content = :c, #st = :s, updatedAt = :u",
        ExpressionAttributeNames={"#st": "status"},
        ExpressionAttributeValues={
            ":tb": transcript,
            ":c": summary,
            ":s": "done",
            ":u": now,
        },
    )
    print(f"  DynamoDB updated: transcriptB + content + status=done")


def process_meeting(audio_info):
    """Full pipeline: download → transcribe → summarize → update."""
    meeting_id = audio_info["meeting_id"]
    print(f"\n{'='*60}")
    print(f"Processing: {audio_info['filename']} ({audio_info['size_mb']}MB)")
    print(f"Meeting ID: {meeting_id}")

    # Get meeting record
    meeting = get_meeting(meeting_id)
    if not meeting:
        print(f"  SKIP: Meeting not found in DynamoDB")
        return False
    print(f"  Title: {meeting['title']}")
    print(f"  Status: {meeting['status']}")

    # Download audio
    with tempfile.NamedTemporaryFile(suffix=os.path.splitext(audio_info["filename"])[1], delete=False) as tmp:
        tmp_path = tmp.name
        print(f"  Downloading from S3: {audio_info['key']}...")
        s3.download_file(BUCKET, audio_info["key"], tmp_path)
        print(f"  Downloaded to {tmp_path}")

    try:
        # Transcribe
        transcript, lang = transcribe_with_whisper(tmp_path)

        # Summarize
        summary = summarize_with_bedrock(transcript, meeting["title"])

        # Update DynamoDB
        update_meeting(meeting, transcript, summary)

        print(f"  DONE: {meeting_id}")
        return True
    finally:
        os.unlink(tmp_path)


def main():
    print("Whisper Re-Transcription + Bedrock Re-Summary")
    print(f"Model: {WHISPER_MODEL} (CPU, int8)")
    print(f"Bedrock: {BEDROCK_MODEL}")
    print()

    files = list_meetings()
    # Sort by size descending, skip tiny recordings
    files = [f for f in files if f["size_mb"] > 1]
    files.sort(key=lambda x: x["size_mb"], reverse=True)

    print(f"Found {len(files)} meetings with audio:")
    for f in files:
        print(f"  {f['meeting_id'][:8]}... {f['filename']:40s} {f['size_mb']:>8.1f}MB")

    # Process specific meeting or all
    if len(sys.argv) > 1:
        target_id = sys.argv[1]
        files = [f for f in files if f["meeting_id"] == target_id]
        if not files:
            print(f"Meeting {target_id} not found")
            sys.exit(1)

    total = len(files)
    success = 0
    for i, f in enumerate(files):
        print(f"\n[{i+1}/{total}]")
        try:
            if process_meeting(f):
                success += 1
        except Exception as e:
            print(f"  ERROR: {e}")

    print(f"\n{'='*60}")
    print(f"Complete: {success}/{total} meetings processed")


if __name__ == "__main__":
    main()
