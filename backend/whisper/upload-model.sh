#!/bin/bash
set -e

BUCKET="${1:-ttobak-assets-180294183052}"
REGION="${2:-ap-northeast-2}"
MODEL_NAME="Systran/faster-whisper-large-v3"
S3_KEY="models/faster-whisper-large-v3.tar.gz"

echo "Downloading ${MODEL_NAME} from HuggingFace..."
pip3 install -q huggingface_hub

MODEL_DIR=$(python3 -c "
from huggingface_hub import snapshot_download
path = snapshot_download('${MODEL_NAME}')
print(path)
")

echo "Model at: ${MODEL_DIR}"
echo "Compressing..."
tar -czf /tmp/model.tar.gz -C "$(dirname "$MODEL_DIR")" "$(basename "$MODEL_DIR")"
SIZE=$(du -sh /tmp/model.tar.gz | cut -f1)
echo "Archive: ${SIZE}"

echo "Uploading to s3://${BUCKET}/${S3_KEY}"
aws s3 cp /tmp/model.tar.gz "s3://${BUCKET}/${S3_KEY}" --region "${REGION}"
rm /tmp/model.tar.gz

echo "Done. Model available at s3://${BUCKET}/${S3_KEY}"
