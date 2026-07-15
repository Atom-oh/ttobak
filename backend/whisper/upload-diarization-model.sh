#!/bin/bash
set -e

BUCKET="${1:-ttobak-assets-180294183052}"
REGION="${2:-ap-northeast-2}"
PIPELINE_REPO="pyannote/speaker-diarization-3.1"
SEGMENTATION_REPO="pyannote/segmentation-3.0"
EMBEDDING_REPO="pyannote/wespeaker-voxceleb-resnet34-LM"
S3_KEY="models/pyannote-diarization-3.1.tar.gz"

if [ -z "${HF_TOKEN:-}" ]; then
  echo "ERROR: HF_TOKEN is not set. Accept the gated-model terms for all three repos" >&2
  echo "  (${PIPELINE_REPO}, ${SEGMENTATION_REPO}, ${EMBEDDING_REPO}) on huggingface.co" >&2
  echo "  with your account, then export HF_TOKEN=<your token> and re-run." >&2
  exit 1
fi

echo "Downloading ${PIPELINE_REPO}, ${SEGMENTATION_REPO}, ${EMBEDDING_REPO} from HuggingFace..."
pip3 install -q huggingface_hub pyyaml

STAGE_DIR=$(python3 -c "
import os
from huggingface_hub import snapshot_download

token = os.environ['HF_TOKEN']
pipeline_dir = snapshot_download('${PIPELINE_REPO}', token=token)
segmentation_dir = snapshot_download('${SEGMENTATION_REPO}', token=token)
embedding_dir = snapshot_download('${EMBEDDING_REPO}', token=token)

import shutil, tempfile
stage = tempfile.mkdtemp(prefix='pyannote-stage-')
shutil.copytree(pipeline_dir, os.path.join(stage, 'pipeline'), dirs_exist_ok=True)
shutil.copytree(segmentation_dir, os.path.join(stage, 'segmentation'), dirs_exist_ok=True)
shutil.copytree(embedding_dir, os.path.join(stage, 'embedding'), dirs_exist_ok=True)
print(stage)
")

echo "Staged at: ${STAGE_DIR}"
echo "Rewriting config.yaml to reference local paths (no runtime HF dependency)..."
# NOTE: pyannote's config.yaml schema/weight filenames can shift between
# releases. If this rewrite doesn't match what actually got downloaded,
# inspect ${STAGE_DIR}/pipeline/config.yaml and ${STAGE_DIR}/segmentation/,
# ${STAGE_DIR}/embedding/ by hand and adjust the keys below to match.
python3 -c "
import glob
import os
import yaml

config_path = '${STAGE_DIR}/pipeline/config.yaml'
with open(config_path) as f:
    config = yaml.safe_load(f)


def _find_weights(dir_path):
    for pattern in ('*.bin', '*.pt', '*.ckpt'):
        matches = glob.glob(os.path.join(dir_path, pattern))
        if matches:
            return os.path.basename(matches[0])
    raise RuntimeError(f'No weight file found in {dir_path}')


seg_weights = _find_weights('${STAGE_DIR}/segmentation')
emb_weights = _find_weights('${STAGE_DIR}/embedding')

params = config['pipeline']['params']
params['segmentation'] = f'../segmentation/{seg_weights}'
params['embedding'] = f'../embedding/{emb_weights}'

with open(config_path, 'w') as f:
    yaml.safe_dump(config, f)
print('Rewrote', config_path, '-> segmentation:', seg_weights, 'embedding:', emb_weights)
"

echo "Compressing..."
tar -czhf /tmp/pyannote-diarization-3.1.tar.gz -C "${STAGE_DIR}" .
SIZE=$(du -sh /tmp/pyannote-diarization-3.1.tar.gz | cut -f1)
echo "Archive: ${SIZE}"

echo "Uploading to s3://${BUCKET}/${S3_KEY}"
aws s3 cp /tmp/pyannote-diarization-3.1.tar.gz "s3://${BUCKET}/${S3_KEY}" --region "${REGION}"
rm /tmp/pyannote-diarization-3.1.tar.gz

echo "Done. Model available at s3://${BUCKET}/${S3_KEY}"
echo "Extracted layout: {root}/pipeline/config.yaml, {root}/segmentation/, {root}/embedding/"
