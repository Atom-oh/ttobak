#!/bin/bash
set -e

# Whisper GPU Benchmark on EC2 Spot g5.xlarge
# Downloads audio from S3, runs Whisper large-v3 on GPU, uploads results

REGION="ap-northeast-2"
BUCKET="ttobak-assets-180294183052"
RESULT_KEY="benchmark/whisper-gpu-results.json"

echo "=== Whisper GPU Benchmark ==="
echo "Instance: $(curl -s http://169.254.169.254/latest/meta-data/instance-type)"
echo "GPU: $(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null || echo 'checking...')"

# Install dependencies
pip3 install faster-whisper boto3 2>&1 | tail -3

# Download audio files
mkdir -p /tmp/audio /tmp/results
MEETINGS=(
  "08417f6d-a7a4-410f-86fa-2d33de73902c:하나은행_연구개발망"
  "84c56bfd-b9f9-40cb-ba88-85d2eddb1fc6:하나금융기술연구소"
  "6efb247f-d422-4f7c-aa96-e6f875e55190:하나금융기술연구소_Mobile"
)

for entry in "${MEETINGS[@]}"; do
  MID="${entry%%:*}"
  TITLE="${entry##*:}"

  # Find audio file
  AUDIO_KEY=$(aws s3 ls "s3://${BUCKET}/audio/84488d1c-70c1-7089-ca02-9a0005b3074b/${MID}/" --region $REGION | awk '{print $4}' | head -1)
  FULL_KEY="audio/84488d1c-70c1-7089-ca02-9a0005b3074b/${MID}/${AUDIO_KEY}"
  EXT="${AUDIO_KEY##*.}"

  echo ""
  echo "=== ${TITLE} (${MID:0:8}) ==="
  echo "Downloading: ${FULL_KEY}"
  aws s3 cp "s3://${BUCKET}/${FULL_KEY}" "/tmp/audio/${MID}.${EXT}" --region $REGION
  echo "Size: $(du -sh /tmp/audio/${MID}.${EXT} | cut -f1)"
done

# Run Whisper on GPU
python3 << 'PYTHON'
import json
import time
import os
from faster_whisper import WhisperModel

print("\nLoading Whisper large-v3 (GPU, float16)...")
model = WhisperModel("large-v3", device="cuda", compute_type="float16")

meetings = [
    ("08417f6d-a7a4-410f-86fa-2d33de73902c", "하나은행_연구개발망"),
    ("84c56bfd-b9f9-40cb-ba88-85d2eddb1fc6", "하나금융기술연구소"),
    ("6efb247f-d422-4f7c-aa96-e6f875e55190", "하나금융기술연구소_Mobile"),
]

results = []
for mid, title in meetings:
    # Find the audio file
    audio_path = None
    for ext in ["webm", "m4a", "mp4", "wav"]:
        path = f"/tmp/audio/{mid}.{ext}"
        if os.path.exists(path):
            audio_path = path
            break

    if not audio_path:
        print(f"\n  SKIP {title}: no audio file")
        continue

    file_size = os.path.getsize(audio_path) / (1024 * 1024)
    print(f"\n=== {title} ({file_size:.1f} MB) ===")

    start = time.time()
    segments, info = model.transcribe(
        audio_path,
        language="ko",
        beam_size=5,
        vad_filter=True,
        vad_parameters=dict(min_silence_duration_ms=500),
        word_timestamps=True,
    )

    texts = []
    for segment in segments:
        texts.append(segment.text.strip())

    elapsed = time.time() - start
    transcript = " ".join(texts)

    print(f"  Time: {elapsed:.1f}s ({elapsed/60:.1f}min)")
    print(f"  Chars: {len(transcript):,}")
    print(f"  Language: {info.language} ({info.language_probability:.0%})")
    print(f"  Speed ratio: {file_size:.0f}MB audio / {elapsed:.0f}s = {file_size/elapsed*60:.1f} MB/min")

    results.append({
        "meetingId": mid,
        "title": title,
        "transcript": transcript,
        "chars": len(transcript),
        "time_seconds": round(elapsed, 1),
        "language": info.language,
        "language_probability": round(info.language_probability, 3),
        "file_size_mb": round(file_size, 1),
    })

# Save results
with open("/tmp/results/whisper-gpu-results.json", "w") as f:
    json.dump(results, f, ensure_ascii=False, indent=2)

print(f"\n=== Summary ===")
for r in results:
    print(f"  {r['title']}: {r['chars']:,} chars in {r['time_seconds']}s")

PYTHON

# Upload results to S3
aws s3 cp /tmp/results/whisper-gpu-results.json "s3://${BUCKET}/${RESULT_KEY}" --region $REGION
echo ""
echo "Results uploaded to s3://${BUCKET}/${RESULT_KEY}"
echo "=== Benchmark Complete ==="
