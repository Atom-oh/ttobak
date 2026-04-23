"""STT 3-Way Benchmark: Transcribe (ko-KR) vs Transcribe (Multi-Lang) vs Whisper

Runs 3 STT engines on existing meeting audio and evaluates with Sonnet 4.6.
  A: AWS Transcribe (LanguageCode=ko-KR fixed)
  B: AWS Transcribe (IdentifyMultipleLanguages, ko-KR + en-US)
  C: Whisper large-v3 (faster-whisper, CPU)
"""

import json
import os
import time
import tempfile
import boto3

REGION = "ap-northeast-2"
BUCKET = "ttobak-assets-180294183052"
BEDROCK_MODEL = "global.anthropic.claude-sonnet-4-6"

s3 = boto3.client("s3", region_name=REGION)
transcribe_client = boto3.client("transcribe", region_name=REGION)
bedrock = boto3.client("bedrock-runtime", region_name=REGION)

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
    media_format = get_media_format(audio_key)
    job_name = f"bench3-{mode}-{meeting_id[:8]}-{int(time.time())}"

    params = {
        "TranscriptionJobName": job_name,
        "MediaFormat": media_format,
        "Media": {"MediaFileUri": f"s3://{BUCKET}/{audio_key}"},
        "OutputBucketName": BUCKET,
        "OutputKey": f"benchmark/{job_name}.json",
        "Settings": {"ShowSpeakerLabels": True, "MaxSpeakerLabels": 10},
    }

    if mode == "multilang":
        params["IdentifyMultipleLanguages"] = True
        params["LanguageOptions"] = ["ko-KR", "en-US"]
    else:
        params["LanguageCode"] = "ko-KR"

    print(f"    Starting {mode} job: {job_name}")
    transcribe_client.start_transcription_job(**params)

    start = time.time()
    while True:
        resp = transcribe_client.get_transcription_job(TranscriptionJobName=job_name)
        status = resp["TranscriptionJob"]["TranscriptionJobStatus"]
        if status == "COMPLETED":
            break
        elif status == "FAILED":
            print(f"    FAILED: {resp['TranscriptionJob'].get('FailureReason')}")
            return None, 0
        time.sleep(10)
    elapsed = time.time() - start

    obj = s3.get_object(Bucket=BUCKET, Key=f"benchmark/{job_name}.json")
    data = json.loads(obj["Body"].read())
    transcript = data["results"]["transcripts"][0]["transcript"]
    return transcript, elapsed


def run_whisper(audio_key):
    """Run faster-whisper on the audio file."""
    from faster_whisper import WhisperModel

    # Download audio from S3
    ext = audio_key.rsplit(".", 1)[-1]
    with tempfile.NamedTemporaryFile(suffix=f".{ext}", delete=False) as f:
        tmp_path = f.name
        print(f"    Downloading audio to {tmp_path}...")
        s3.download_file(BUCKET, audio_key, tmp_path)

    file_size_mb = os.path.getsize(tmp_path) / (1024 * 1024)
    print(f"    Audio file: {file_size_mb:.1f} MB")

    # Use large-v3 model for best quality, CPU mode
    print(f"    Loading Whisper large-v3 model (CPU, int8)...")
    model = WhisperModel("large-v3", device="cpu", compute_type="int8")

    print(f"    Transcribing...")
    start = time.time()
    segments, info = model.transcribe(
        tmp_path,
        language="ko",
        beam_size=5,
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
    )

    # Collect all segments
    texts = []
    for segment in segments:
        texts.append(segment.text.strip())

    elapsed = time.time() - start
    transcript = " ".join(texts)

    # Cleanup
    os.unlink(tmp_path)

    print(f"    Whisper done: {len(transcript)} chars in {elapsed:.0f}s")
    print(f"    Detected language: {info.language} ({info.language_probability:.0%})")

    return transcript, elapsed


def evaluate_3way(title, t_a, t_b, t_c, time_a, time_b, time_c):
    prompt = f"""세 가지 STT 엔진의 음성-텍스트 변환 결과를 비교 평가해주세요.
이 미팅은 한국어로 진행되었으며, AWS 관련 기술 용어(GPU, EKS, Lambda, VPC, S3 등)가 자주 등장합니다.

## 미팅: {title}

## Engine A: AWS Transcribe 표준 (한국어 고정, LanguageCode=ko-KR)
처리 시간: {time_a:.0f}초
{t_a[:3500]}
{"... (truncated)" if len(t_a) > 3500 else ""}

## Engine B: AWS Transcribe 다국어 (자동 감지, ko-KR + en-US)
처리 시간: {time_b:.0f}초
{t_b[:3500]}
{"... (truncated)" if len(t_b) > 3500 else ""}

## Engine C: OpenAI Whisper large-v3 (로컬 CPU)
처리 시간: {time_c:.0f}초
{t_c[:3500]}
{"... (truncated)" if len(t_c) > 3500 else ""}

## 평가 기준 (각 1-10점)
1. **문맥 연결성** — 문맥이 끊기거나 비논리적인 부분
2. **완성도** — 말이 잘리거나 누락된 부분
3. **단어 정확성** — 이상한 단어, 오인식 (특히 고유명사)
4. **전문 용어** — AWS/IT 전문 용어 (GPU, EKS, Lambda, S3, VPC, DynamoDB, Bedrock 등)
5. **화자 구분** — 화자 전환의 자연스러움

## 출력 형식

### 점수표
| 평가 항목 | A (Transcribe 표준) | B (Transcribe 다국어) | C (Whisper large-v3) | 승자 |
|-----------|--------------------|--------------------|--------------------|----|
| 문맥 연결성 | X/10 | X/10 | X/10 | A/B/C |
| 완성도 | X/10 | X/10 | X/10 | A/B/C |
| 단어 정확성 | X/10 | X/10 | X/10 | A/B/C |
| 전문 용어 | X/10 | X/10 | X/10 | A/B/C |
| 화자 구분 | X/10 | X/10 | X/10 | A/B/C |
| 처리 속도 | {time_a:.0f}s | {time_b:.0f}s | {time_c:.0f}s | - |
| **종합** | **X/10** | **X/10** | **X/10** | **A/B/C** |

### 상세 분석
각 항목별로 세 엔진 간의 차이점에 집중하세요. 같은 구간에서 엔진별로 다르게 인식한 예시를 3-5개 제시해주세요.

특히 영어 전문 용어(GPU, EKS, Lambda 등)가 섞인 한국어 문장에서 각 엔진의 처리 방식을 비교해주세요.

### 종합 추천
비용, 속도, 품질을 종합하여 한국어+영어 혼용 미팅에 가장 적합한 엔진을 추천해주세요."""

    resp = bedrock.converse(
        modelId=BEDROCK_MODEL,
        messages=[{"role": "user", "content": [{"text": prompt}]}],
        inferenceConfig={"maxTokens": 4096, "temperature": 0.2},
    )
    return resp["output"]["message"]["content"][0]["text"]


def main():
    print("=" * 70)
    print("STT 3-Way Benchmark")
    print("  A: AWS Transcribe (ko-KR)")
    print("  B: AWS Transcribe (Multi-Language)")
    print("  C: Whisper large-v3 (CPU)")
    print("=" * 70)

    results = []

    for meeting_id, title in MEETINGS:
        print(f"\n{'='*60}")
        print(f"  {title}")
        print(f"{'='*60}")

        audio_key = get_audio_key(meeting_id)
        if not audio_key:
            print("  SKIP: No audio")
            continue

        file_size = s3.head_object(Bucket=BUCKET, Key=audio_key)["ContentLength"] / (1024*1024)
        print(f"  Audio: {audio_key} ({file_size:.1f} MB)")

        # Engine A: existing standard transcript
        print("\n  [A] Transcribe Standard (ko-KR)...")
        t_a = get_existing_transcript(meeting_id)
        time_a = 0
        if t_a:
            print(f"  [A] Existing: {len(t_a):,} chars")
        else:
            t_a, time_a = run_transcribe_job(meeting_id, audio_key, "standard")
            if not t_a:
                print("  SKIP: A failed"); continue
            print(f"  [A] {len(t_a):,} chars in {time_a:.0f}s")

        # Engine B: multi-language
        print("\n  [B] Transcribe Multi-Language (ko-KR + en-US)...")
        t_b, time_b = run_transcribe_job(meeting_id, audio_key, "multilang")
        if not t_b:
            print("  SKIP: B failed"); continue
        print(f"  [B] {len(t_b):,} chars in {time_b:.0f}s")

        # Engine C: Whisper
        print("\n  [C] Whisper large-v3 (CPU)...")
        t_c, time_c = run_whisper(audio_key)
        if not t_c:
            print("  SKIP: C failed"); continue

        # Evaluate
        print("\n  [Eval] Evaluating with Sonnet 4.6...")
        evaluation = evaluate_3way(title, t_a, t_b, t_c, time_a, time_b, time_c)
        print(f"  [Eval] Done")

        results.append({
            "meeting": title,
            "id": meeting_id,
            "a_len": len(t_a), "b_len": len(t_b), "c_len": len(t_c),
            "a_time": time_a, "b_time": time_b, "c_time": time_c,
            "evaluation": evaluation,
        })

    # Save
    path = "docs/stt-3way-benchmark.md"
    with open(path, "w") as f:
        f.write("# STT 3-Way Benchmark Results\n\n")
        f.write(f"Date: {time.strftime('%Y-%m-%d %H:%M')}\n\n")
        f.write("| Engine | Mode | Model |\n")
        f.write("|--------|------|-------|\n")
        f.write("| **A** | AWS Transcribe | `LanguageCode=ko-KR` (한국어 고정) |\n")
        f.write("| **B** | AWS Transcribe | `IdentifyMultipleLanguages` (ko-KR + en-US) |\n")
        f.write("| **C** | Whisper | `large-v3` (faster-whisper, CPU, int8) |\n")
        f.write(f"\nEvaluator: Claude Sonnet 4.6\n")
        f.write(f"Meetings tested: {len(results)}\n\n---\n\n")

        for r in results:
            f.write(f"## {r['meeting']}\n\n")
            f.write(f"| Metric | A (Transcribe) | B (Multi-Lang) | C (Whisper) |\n")
            f.write(f"|--------|----------------|----------------|-------------|\n")
            f.write(f"| Chars | {r['a_len']:,} | {r['b_len']:,} | {r['c_len']:,} |\n")
            f.write(f"| Time | {r['a_time']:.0f}s | {r['b_time']:.0f}s | {r['c_time']:.0f}s |\n\n")
            f.write(r["evaluation"])
            f.write("\n\n---\n\n")

    print(f"\n{'='*70}")
    print(f"Results: {path}")
    print(f"Meetings: {len(results)}")


if __name__ == "__main__":
    main()
