"""STT A/B Benchmark: Transcribe vs Nova Sonic

Runs both STT engines on existing meeting audio files and evaluates
transcription quality using Claude Sonnet 4.6.

Usage:
    python scripts/stt-benchmark.py
"""

import json
import time
import boto3

# Config
REGION = "ap-northeast-2"
BUCKET = "ttobak-assets-180294183052"
TABLE = "ttobak-main"
BEDROCK_MODEL = "global.anthropic.claude-sonnet-4-6"

s3 = boto3.client("s3", region_name=REGION)
transcribe = boto3.client("transcribe", region_name=REGION)
bedrock = boto3.client("bedrock-runtime", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)

# Meetings to test (meetingId, title, audio S3 key)
MEETINGS = [
    ("08417f6d-a7a4-410f-86fa-2d33de73902c", "하나은행 연구개발망 미팅"),
    ("84c56bfd-b9f9-40cb-ba88-85d2eddb1fc6", "하나금융기술연구소"),
    ("6efb247f-d422-4f7c-aa96-e6f875e55190", "하나금융기술연구소(Mobile)"),
]


def get_existing_transcript(meeting_id):
    """Get existing Transcribe result from S3."""
    try:
        obj = s3.get_object(Bucket=BUCKET, Key=f"transcripts/{meeting_id}.json")
        data = json.loads(obj["Body"].read())
        return data["results"]["transcripts"][0]["transcript"]
    except Exception as e:
        print(f"  No existing transcript for {meeting_id}: {e}")
        return None


def run_nova_sonic(meeting_id):
    """Start Nova Sonic transcription job and wait for result."""
    # Find audio key
    resp = table.get_item(
        Key={"PK": f"USER#84488d1c-70c1-7089-ca02-9a0005b3074b", "SK": f"MEETING#{meeting_id}"},
        ProjectionExpression="audioKey",
    )
    audio_key = resp.get("Item", {}).get("audioKey", "")
    if not audio_key:
        # Find from S3
        prefix = f"audio/84488d1c-70c1-7089-ca02-9a0005b3074b/{meeting_id}/"
        objs = s3.list_objects_v2(Bucket=BUCKET, Prefix=prefix)
        if objs.get("Contents"):
            audio_key = objs["Contents"][0]["Key"]

    if not audio_key:
        print(f"  No audio found for {meeting_id}")
        return None

    # Determine media format
    ext = audio_key.rsplit(".", 1)[-1].lower()
    media_format = {"webm": "webm", "m4a": "mp4", "mp4": "mp4", "wav": "wav"}.get(ext, "webm")

    job_name = f"benchmark-nova-{meeting_id[:8]}-{int(time.time())}"
    print(f"  Starting Nova Sonic job: {job_name}")
    print(f"  Audio: s3://{BUCKET}/{audio_key}")

    try:
        transcribe.start_transcription_job(
            TranscriptionJobName=job_name,
            LanguageCode="ko-KR",
            MediaFormat=media_format,
            Media={"MediaFileUri": f"s3://{BUCKET}/{audio_key}"},
            OutputBucketName=BUCKET,
            OutputKey=f"benchmark/{job_name}.json",
            Settings={"ShowSpeakerLabels": True, "MaxSpeakerLabels": 10},
        )
    except Exception as e:
        print(f"  Failed to start Nova Sonic: {e}")
        return None

    # Wait for completion
    while True:
        resp = transcribe.get_transcription_job(TranscriptionJobName=job_name)
        status = resp["TranscriptionJob"]["TranscriptionJobStatus"]
        if status == "COMPLETED":
            break
        elif status == "FAILED":
            print(f"  Job failed: {resp['TranscriptionJob'].get('FailureReason')}")
            return None
        time.sleep(10)
        print(f"  Waiting... ({status})")

    # Get result
    obj = s3.get_object(Bucket=BUCKET, Key=f"benchmark/{job_name}.json")
    data = json.loads(obj["Body"].read())
    return data["results"]["transcripts"][0]["transcript"]


def evaluate_with_sonnet(meeting_title, transcript_a, transcript_b):
    """Use Sonnet to evaluate both transcriptions."""
    prompt = f"""두 개의 STT(음성-텍스트 변환) 결과를 비교 평가해주세요.

## 미팅: {meeting_title}

## Transcribe A (AWS Transcribe)
{transcript_a[:3000]}

## Transcribe B (동일 엔진, 재실행)
{transcript_b[:3000]}

## 평가 기준
각 항목을 1-10점으로 평가하고, 구체적인 예시를 들어주세요:

1. **문맥 연결성** (문맥이 끊기거나 비논리적인 부분이 있는지)
2. **완성도** (말이 잘리거나 누락된 부분이 있는지)
3. **단어 정확성** (이상한 단어가 들어오거나 오인식된 부분이 있는지)
4. **전문 용어** (AWS, IT 전문 용어가 정확하게 인식되었는지)
5. **화자 구분** (화자 전환이 자연스러운지)

## 출력 형식
| 평가 항목 | Transcribe A | Transcribe B | 승자 |
|-----------|-------------|-------------|------|
| 문맥 연결성 | X/10 | X/10 | A/B |
| 완성도 | X/10 | X/10 | A/B |
| 단어 정확성 | X/10 | X/10 | A/B |
| 전문 용어 | X/10 | X/10 | A/B |
| 화자 구분 | X/10 | X/10 | A/B |
| **종합** | X/10 | X/10 | A/B |

각 항목에 대해 구체적인 문제점과 예시를 2-3개씩 제시해주세요.
마지막에 종합 평가와 추천을 작성해주세요."""

    resp = bedrock.converse(
        modelId=BEDROCK_MODEL,
        messages=[{"role": "user", "content": [{"text": prompt}]}],
        inferenceConfig={"maxTokens": 4096, "temperature": 0.2},
    )
    return resp["output"]["message"]["content"][0]["text"]


def main():
    print("=" * 60)
    print("STT A/B Benchmark: Transcribe vs Re-Transcribe")
    print("=" * 60)
    print()

    results = []

    for meeting_id, title in MEETINGS:
        print(f"\n--- {title} ({meeting_id[:12]}) ---")

        # Get existing Transcribe result (A)
        print("  [A] Loading existing Transcribe result...")
        transcript_a = get_existing_transcript(meeting_id)
        if not transcript_a:
            print("  SKIP: No transcript A")
            continue
        print(f"  [A] {len(transcript_a)} chars")

        # Run second transcription (B) — same engine for now
        print("  [B] Running second Transcribe job...")
        transcript_b = run_nova_sonic(meeting_id)
        if not transcript_b:
            print("  SKIP: Transcribe B failed")
            continue
        print(f"  [B] {len(transcript_b)} chars")

        # Evaluate with Sonnet
        print("  [Eval] Evaluating with Sonnet 4.6...")
        evaluation = evaluate_with_sonnet(title, transcript_a, transcript_b)
        print(f"  [Eval] Done ({len(evaluation)} chars)")

        results.append({
            "meeting": title,
            "meetingId": meeting_id,
            "transcriptA_len": len(transcript_a),
            "transcriptB_len": len(transcript_b),
            "evaluation": evaluation,
        })

    # Save results
    output_path = "docs/stt-benchmark-results.md"
    with open(output_path, "w") as f:
        f.write("# STT A/B Benchmark Results\n\n")
        f.write(f"Date: {time.strftime('%Y-%m-%d %H:%M')}\n")
        f.write(f"Engine A: AWS Transcribe (existing)\n")
        f.write(f"Engine B: AWS Transcribe (re-run)\n")
        f.write(f"Evaluator: Claude Sonnet 4.6\n\n")

        for r in results:
            f.write(f"## {r['meeting']}\n\n")
            f.write(f"- Meeting ID: `{r['meetingId']}`\n")
            f.write(f"- Transcript A: {r['transcriptA_len']} chars\n")
            f.write(f"- Transcript B: {r['transcriptB_len']} chars\n\n")
            f.write(r["evaluation"])
            f.write("\n\n---\n\n")

    print(f"\n{'=' * 60}")
    print(f"Results saved to {output_path}")
    print(f"Tested {len(results)} meetings")


if __name__ == "__main__":
    main()
