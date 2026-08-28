#!/bin/bash
set -euo pipefail

BUCKET="${1:-ttobak-assets-180294183052}"
REGION="${2:-ap-northeast-2}"
# CONFIRMED ON HUGGINGFACE (2026-08-28): "pyannote/speaker-diarization-community-1"
# is the pyannote.audio 4.x flagship open-source speaker diarization pipeline
# (https://huggingface.co/pyannote/speaker-diarization-community-1 -- its own
# quick-start snippet uses this exact repo ID with Pipeline.from_pretrained,
# requires pyannote.audio>=4.0.0, released CC-BY-4.0, gated: must accept
# conditions on the HF page + pass a token).
#
# Unlike the 3.1-era pipeline (see upload-diarization-model.sh), this repo does
# NOT reference separate segmentation/embedding HF repos via "org/name" strings.
# Its config.yaml (https://huggingface.co/pyannote/speaker-diarization-community-1/blob/main/config.yaml)
# is self-contained: pipeline params point at "$model/segmentation",
# "$model/embedding", "$model/plda" -- local placeholders that pyannote.audio's
# loader resolves relative to the pipeline's own directory, not external repo
# IDs. The repo's file tree (.../tree/main) confirms segmentation/, embedding/,
# and plda/ subdirectories with checkpoints ship alongside config.yaml in this
# ONE repo -- there is no separate gated sub-model repo to accept terms for
# here (contrast the 3.1 script's three-repo gate). Because "$model/..." does
# not match the "org/name" pattern below, the generic rewrite loop is a no-op
# for this repo today; it's kept so a future pipeline revision that DOES
# reference external HF repos still gets staged/rewritten automatically.
PIPELINE_REPO="${PIPELINE_REPO:-pyannote/speaker-diarization-community-1}"
S3_KEY="models/whisperx-diarization-4.x.tar.gz"

if [ -z "${HF_TOKEN:-}" ]; then
  echo "ERROR: HF_TOKEN is not set. Accept the gated-model terms for" >&2
  echo "  ${PIPELINE_REPO} (and any sub-model repos its config references)" >&2
  echo "  on huggingface.co, then export HF_TOKEN=<your token> and re-run." >&2
  exit 1
fi

echo "Downloading ${PIPELINE_REPO} (pyannote.audio 4.x pipeline)..."
# Prefer a venv for operator hygiene; the --break-system-packages fallback
# below is only for PEP 668 "externally-managed" hosts (e.g. Ubuntu 24.04)
# where a plain pip3 install fails outright.
pip3 install -q huggingface_hub pyyaml 2>/dev/null || \
  pip3 install -q --break-system-packages huggingface_hub pyyaml
# HF_TOKEN is already in this shell's env (the runbook invokes this script as
# `HF_TOKEN=... ./upload-whisperx-diarization-model.sh`). This export only
# controls propagation into the child python3 process below -- it does not
# change whether/when the token entered the environment.
export PIPELINE_REPO HF_TOKEN

STAGE_DIR=$(python3 - <<'EOF'
import os, re, shutil, tempfile
import yaml
from huggingface_hub import snapshot_download

token = os.environ["HF_TOKEN"]
repo = os.environ["PIPELINE_REPO"]
stage = tempfile.mkdtemp(prefix="whisperx-diar-stage-")

pipeline_dir = snapshot_download(repo, token=token)
shutil.copytree(pipeline_dir, os.path.join(stage, "pipeline"), dirs_exist_ok=True)

config_path = os.path.join(stage, "pipeline", "config.yaml")
with open(config_path) as f:
    config = yaml.safe_load(f)

# Stage every HF repo the pipeline config references (segmentation/embedding
# checkpoints appear as "org/name" or "org/name@rev" strings) and rewrite the
# config to local paths so the container needs no runtime HF access.
def rewrite(node):
    if isinstance(node, dict):
        return {k: rewrite(v) for k, v in node.items()}
    if isinstance(node, list):
        return [rewrite(v) for v in node]
    if isinstance(node, str) and re.fullmatch(r"[\w.-]+/[\w.-]+(@[\w.-]+)?", node):
        ref = node.split("@")[0]
        local_name = ref.replace("/", "__")
        local_dir = os.path.join(stage, local_name)
        if not os.path.isdir(local_dir):
            print(f"  staging referenced repo: {ref}")
            src = snapshot_download(ref, token=token)
            shutil.copytree(src, local_dir, dirs_exist_ok=True)
        # container extracts the archive into DIARIZATION_LOCAL_DIR; config
        # lives at <root>/pipeline/config.yaml, referenced repos at <root>/<name>
        return os.path.join("..", local_name)
    return node

with open(config_path, "w") as f:
    yaml.safe_dump(rewrite(config), f, sort_keys=False)

print(stage)
EOF
)
STAGE_DIR=$(echo "$STAGE_DIR" | tail -1)

echo "Staged at: ${STAGE_DIR}"
echo "Compressing..."
tar -czf /tmp/whisperx-diarization.tar.gz -C "$STAGE_DIR" .
du -sh /tmp/whisperx-diarization.tar.gz

echo "Uploading to s3://${BUCKET}/${S3_KEY}"
aws s3 cp /tmp/whisperx-diarization.tar.gz "s3://${BUCKET}/${S3_KEY}" --region "${REGION}"
rm /tmp/whisperx-diarization.tar.gz
echo "Done."
