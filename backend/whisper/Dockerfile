FROM nvidia/cuda:12.4.1-runtime-ubuntu22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg && \
    rm -rf /var/lib/apt/lists/*

RUN pip3 install --no-cache-dir faster-whisper boto3

# Model is downloaded on first run (~3GB) and cached on ECS host volume.
# Baking into the image requires 6GB+ build memory which exceeds ARC runner limits.

COPY transcribe.py /app/transcribe.py
WORKDIR /app

ENTRYPOINT ["python3", "transcribe.py"]
