FROM nvidia/cuda:12.4.1-runtime-ubuntu22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg && \
    rm -rf /var/lib/apt/lists/*

RUN pip3 install --no-cache-dir faster-whisper boto3

# Pre-download model into image (~3GB compressed)
RUN python3 -c "from faster_whisper import WhisperModel; WhisperModel('large-v3', device='cpu', compute_type='int8')" && \
    echo "Model cached"

COPY transcribe.py /app/transcribe.py
WORKDIR /app

ENTRYPOINT ["python3", "transcribe.py"]
