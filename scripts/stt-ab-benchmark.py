"""STT A/B Benchmark: Standard Transcribe (ko-KR) vs Multi-Language Transcribe

Runs both modes on existing meeting audio:
  A: Standard AWS Transcribe (LanguageCode=ko-KR, speaker labels)
  B: Multi-Language Transcribe (IdentifyMultipleLanguages, ko-KR + en-US)

Evaluates with Claude Sonnet 4.6 on 5 criteria.
"""

import json
import time
import boto3

REGION = "ap-northeast-2"
BUCKET = "ttobak-assets-180294183052"
TABLE = "ttobak-main"
BEDROCK_MODEL = "global.anthropic.claude-sonnet-4-6"

s3 = boto3.client("s3", region_name=REGION)
transcribe_client = boto3.client("transcribe", region_name=REGION)
bedrock = boto3.client("bedrock-runtime", region_name=REGION)
dynamodb = boto3.resource("dynamodb", region_name=REGION)
table = dynamodb.Table(TABLE)

MEETINGS = [
    ("08417f6d-a7a4-410f-86fa-2d33de73902c", "하나은행 연구개발망 미팅"),
    ("84c56bfd-b9f9-40cb-ba88-85d2eddb1fc6", "하나금융기술연구소"),
    ("6efb247f-d422-4f7c-aa96-e6f875e55190", "하나금융기술연구소(Mobile)"),
]


def get_audio_key(meeting_id):
    prefix = f"audio/84488d1c-70c1-7089-ca02-9a0005b3074b/{meeting_id}/"
    objs = s3.list_objects_v2(Bucket=BUCKET, Prefix=prefix)
    if objs.get("Contents"):
        return objs["Contents"][0]["Key"]
    return None


def get_media_format(key):
    ext = key.rsplit(".", 1)[-1].lower()
    return {"webm": "webm", "m4a": "mp4", "mp4": "mp4", "wav": "wav"}.get(ext, "webm")


def get_existing_transcript(meeting_id):
    try:
        obj = s3.get_object(Bucket=BUCKET, Key=f"transcripts/{meeting_id}.json")
        data = json.loads(obj["Body"].read())
        return data["results"]["transcripts"][0]["transcript"]
    except:
        return None


def run_transcribe_job(meeting_id, audio_key, mode="standard"):
    """Run a Transcribe job. mode='standard' (ko-KR) or 'multilang' (auto-detect)."""
    media_format = get_media_format(audio_key)
    job_name = f"bench-{mode}-{meeting_id[:8]}-{int(time.time())}"

    input_params = {
        "TranscriptionJobName": job_name,
        "MediaFormat": media_format,
        "Media": {"MediaFileUri": f"s3://{BUCKET}/{audio_key}"},
        "OutputBucketName": BUCKET,
        "OutputKey": f"benchmark/{job_name}.json",
        "Settings": {"ShowSpeakerLabels": True, "MaxSpeakerLabels": 10},
    }

    if mode == "multilang":
        input_params["IdentifyMultipleLanguages"] = True
        input_params["LanguageOptions"] = ["ko-KR", "en-US"]
    else:
        input_params["LanguageCode"] = "ko-KR"

    print(f"  Starting {mode} job: {job_name}")
    transcribe_client.start_transcription_job(**input_params)

    while True:
        resp = transcribe_client.get_transcription_job(TranscriptionJobName=job_name)
        status = resp["TranscriptionJob"]["TranscriptionJobStatus"]
        if status == "COMPLETED":
            break
        elif status == "FAILED":
            reason = resp["TranscriptionJob"].get("FailureReason", "unknown")
            print(f"  Job FAILED: {reason}")
            return None
        time.sleep(10)
        print(f"  {mode}: waiting... ({status})")

    obj = s3.get_object(Bucket=BUCKET, Key=f"benchmark/{job_name}.json")
    data = json.loads(obj["Body"].read())
    transcript = data["results"]["transcripts"][0]["transcript"]

    # Get detected languages for multilang mode
    lang_info = ""
    if mode == "multilang" and "language_identification" in data["results"]:
        langs = data["results"]["language_identification"]
        lang_info = ", ".join([f'{l["code"]}({l["score"]:.0%})' for l in langs[:3]])

    return transcript, lang_info


def evaluate_with_sonnet(title, transcript_a, transcript_b, lang_info_b=""):
    prompt = f"""두 개의 STT(음성-텍스트 변환) 결과를 비교 평가해주세요.
이 미팅은 한국어로 진행되었으며, AWS 관련 기술 용어가 포함되어 있습니다.

## 미팅: {title}

## Engine A: AWS Transcribe (한국어 고정 모드, LanguageCode=ko-KR)
{transcript_a[:4000]}
{"..." if len(transcript_a) > 4000 else ""}

## Engine B: AWS Transcribe (다국어 자동 감지 모드, IdentifyMultipleLanguages=ko-KR+en-US)
{f"감지된 언어: {lang_info_b}" if lang_info_b else ""}
{transcript_b[:4000]}
{"..." if len(transcript_b) > 4000 else ""}

## 평가 기준
각 항목을 1-10점으로 평가하고, 구체적인 예시를 들어주세요:

1. **문맥 연결성** — 문맥이 끊기거나 비논리적인 부분
2. **완성도** — 말이 잘리거나 누락된 부분
3. **단어 정확성** — 이상한 단어, 오인식
4. **전문 용어** — AWS/IT 전문 용어 정확도 (GPU, EKS, Lambda, S3, VPC 등)
5. **화자 구분** — 화자 전환 자연스러움

## 출력 형식

### 점수표
| 평가 항목 | Engine A (ko-KR 고정) | Engine B (다국어 감지) | 승자 |
|-----------|---------------------|---------------------|------|
| 문맥 연결성 | X/10 | X/10 | A/B/동점 |
| 완성도 | X/10 | X/10 | A/B/동점 |
| 단어 정확성 | X/10 | X/10 | A/B/동점 |
| 전문 용어 | X/10 | X/10 | A/B/동점 |
| 화자 구분 | X/10 | X/10 | A/B/동점 |
| **종합** | **X/10** | **X/10** | **A/B** |

### 상세 분석
각 항목별로 구체적 문제점과 예시 2-3개씩 제시해주세요.
특히 두 엔진 간의 **차이점**에 집중해주세요 — 한쪽만 틀린 부분, 한쪽이 더 잘한 부분.

### 종합 평가
어떤 모드가 한국어+영어 혼용 미팅에 더 적합한지 결론을 내려주세요."""

    resp = bedrock.converse(
        modelId=BEDROCK_MODEL,
        messages=[{"role": "user", "content": [{"text": prompt}]}],
        inferenceConfig={"maxTokens": 4096, "temperature": 0.2},
    )
    return resp["output"]["message"]["content"][0]["text"]


def main():
    print("=" * 70)
    print("STT A/B Benchmark: Standard (ko-KR) vs Multi-Language (ko-KR + en-US)")
    print("=" * 70)

    results = []

    for meeting_id, title in MEETINGS:
        print(f"\n{'='*50}")
        print(f"  {title} ({meeting_id[:12]})")
        print(f"{'='*50}")

        audio_key = get_audio_key(meeting_id)
        if not audio_key:
            print("  SKIP: No audio file")
            continue
        print(f"  Audio: {audio_key}")

        # Engine A: existing or new standard transcription
        print("\n  [A] Standard Transcribe (ko-KR)...")
        transcript_a = get_existing_transcript(meeting_id)
        if transcript_a:
            print(f"  [A] Using existing transcript: {len(transcript_a)} chars")
        else:
            result = run_transcribe_job(meeting_id, audio_key, "standard")
            if not result:
                print("  SKIP: Engine A failed")
                continue
            transcript_a, _ = result
            print(f"  [A] New transcript: {len(transcript_a)} chars")

        # Engine B: multi-language auto-detect
        print("\n  [B] Multi-Language Transcribe (ko-KR + en-US)...")
        result = run_transcribe_job(meeting_id, audio_key, "multilang")
        if not result:
            print("  SKIP: Engine B failed")
            continue
        transcript_b, lang_info = result
        print(f"  [B] Transcript: {len(transcript_b)} chars")
        if lang_info:
            print(f"  [B] Languages detected: {lang_info}")

        # Evaluate
        print("\n  [Eval] Evaluating with Sonnet 4.6...")
        evaluation = evaluate_with_sonnet(title, transcript_a, transcript_b, lang_info)
        print(f"  [Eval] Done ({len(evaluation)} chars)")

        results.append({
            "meeting": title,
            "meetingId": meeting_id,
            "a_len": len(transcript_a),
            "b_len": len(transcript_b),
            "lang_info": lang_info,
            "evaluation": evaluation,
        })

    # Save results
    output_path = "docs/stt-ab-benchmark-results.md"
    with open(output_path, "w") as f:
        f.write("# STT A/B Benchmark Results\n\n")
        f.write(f"Date: {time.strftime('%Y-%m-%d %H:%M')}\n\n")
        f.write("| Engine | Mode | Description |\n")
        f.write("|--------|------|-------------|\n")
        f.write("| **A** | Standard | `LanguageCode=ko-KR` (한국어 고정) |\n")
        f.write("| **B** | Multi-Language | `IdentifyMultipleLanguages=true` (ko-KR + en-US 자동 감지) |\n")
        f.write(f"\nEvaluator: Claude Sonnet 4.6\n\n")
        f.write(f"Meetings tested: {len(results)}\n\n---\n\n")

        for r in results:
            f.write(f"## {r['meeting']}\n\n")
            f.write(f"- Meeting ID: `{r['meetingId']}`\n")
            f.write(f"- Engine A: {r['a_len']:,} chars\n")
            f.write(f"- Engine B: {r['b_len']:,} chars\n")
            if r["lang_info"]:
                f.write(f"- Detected languages: {r['lang_info']}\n")
            f.write(f"\n{r['evaluation']}\n\n---\n\n")

    print(f"\n{'='*70}")
    print(f"Results saved to {output_path}")
    print(f"Tested {len(results)} meetings")


if __name__ == "__main__":
    main()
