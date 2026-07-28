FROM nvidia/cuda:12.9.1-cudnn-runtime-ubuntu24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg && \
    rm -rf /var/lib/apt/lists/*

# torch/torchaudio pinned to a cu124 wheel -- the cuda:12.9.1 base image's
# driver is backward-compatible with cu124-built wheels. Bump together if
# pyannote.audio's torch version floor changes.
RUN pip3 install --no-cache-dir --break-system-packages \
    torch==2.5.1 torchaudio==2.5.1 --index-url https://download.pytorch.org/whl/cu124 && \
    pip3 install --no-cache-dir --break-system-packages \
    faster-whisper boto3 "pyannote.audio>=3.1,<4" pyyaml

# Whisper + diarization models are loaded from S3 at runtime (~30s same-region
# transfer each). Keeps the image itself free of model weights and allows
# model updates without a rebuild.
COPY transcribe.py /app/transcribe.py
WORKDIR /app

ENTRYPOINT ["python3", "transcribe.py"]
