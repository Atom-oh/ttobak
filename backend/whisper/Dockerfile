FROM nvidia/cuda:12.9.1-cudnn-runtime-ubuntu24.04

# libpython3.12: pyannote.audio 4.x can decode audio via torchcodec, whose
# FFmpeg-6 variant links against libpython3.12.so.1.0 -- Ubuntu's `python3`
# package ships only the interpreter, not the shared library. transcribe.py
# bypasses torchcodec by preloading the waveform (_load_wav_waveform), so
# this package is belt-and-suspenders, same as Dockerfile.whisperx. On noble
# the library ships as the t64 transition package (Provides: libpython3.12);
# try the real name first, fall back for either packaging state.
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg && \
    (apt-get install -y --no-install-recommends libpython3.12t64 || \
     apt-get install -y --no-install-recommends libpython3.12) && \
    rm -rf /var/lib/apt/lists/*

# ASR stack pinned EXACTLY to the versions the proven 2026-08-28 production
# image resolved (read out of the live image via `importlib.metadata`,
# ttobak-whisper:6e3a1161): the Phase-2 cross-validation found the whisperx
# bench image silently dropped a contiguous ~5-minute block of one 64-min
# meeting that THIS stack transcribes fully. Both images were then
# inspected: faster-whisper (1.2.1), av (18.1.0) and onnxruntime (1.29.0)
# are IDENTICAL across them -- the dropout differentiator is in what
# differs, ctranslate2 above all (4.8.1 here vs 4.8.2 there). So the
# diarization upgrade below must not move a single package on
# faster-whisper's actual ASR path: ctranslate2 (inference), onnxruntime
# (its VAD), av (its audio decode), tokenizers/huggingface-hub.
#
# torch/torchaudio 2.5.1->2.8.0 (cu128 wheels; the cuda:12.9.1 base's
# driver covers them) is REQUIRED by the pyannote bump and is NOT an
# ASR-path move: faster-whisper does not use torch anywhere (ct2 +
# onnxruntime + av cover inference/VAD/decode), so torch only feeds
# pyannote -- the same torch 2.8/pyannote 4.0.7 pairing the bench image
# validated. pyannote.audio is the ONLY intentional behavior change:
# 3.4.0 -> 4.0.7 community-1 (phantom-speaker fix confirmed on meetings
# a435a3dc/3804e0f5 against operator-provided ground truth). Its
# torchcodec dependency is decode-path only and bypassed by
# transcribe.py's waveform preload.
RUN pip3 install --no-cache-dir --break-system-packages \
    torch==2.8.0 torchaudio==2.8.0 --index-url https://download.pytorch.org/whl/cu128 && \
    pip3 install --no-cache-dir --break-system-packages \
    faster-whisper==1.2.1 ctranslate2==4.8.1 onnxruntime==1.29.0 \
    av==18.1.0 tokenizers==0.23.1 huggingface-hub==1.29.0 \
    boto3 "pyannote.audio==4.0.7" pyyaml

# Whisper + diarization models are loaded from S3 at runtime (~30s same-region
# transfer each). Keeps the image itself free of model weights and allows
# model updates without a rebuild.
COPY transcribe.py /app/transcribe.py
WORKDIR /app

ENTRYPOINT ["python3", "transcribe.py"]
