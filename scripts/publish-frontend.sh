#!/usr/bin/env bash
set -euo pipefail

export_dir="${1:-frontend/out}"
: "${S3_BUCKET:?S3_BUCKET is required}"
: "${CF_DISTRIBUTION:?CF_DISTRIBUTION is required}"
[[ "$S3_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || { echo "Invalid site bucket" >&2; exit 2; }
[[ "$CF_DISTRIBUTION" =~ ^[A-Z0-9]+$ ]] || { echo "Invalid distribution ID" >&2; exit 2; }
[[ -f "$export_dir/index.html" && -d "$export_dir/_next/static" ]] || {
  echo "Complete frontend export is required" >&2; exit 2;
}
if [[ -n "$(find "$export_dir" -type l -print -quit)" ]]; then
  echo "Frontend export must not contain symlinks" >&2; exit 2;
fi

if [[ -n "$(find "$export_dir" -type f -name '__next.*.txt' -print -quit)" ]]; then
  # The infrastructure-owned marker follows router/distribution deployment.
  # Consume only this nonsecret readiness field; never print runtime config.
  aws s3 cp "s3://${S3_BUCKET}/config.json" - --only-show-errors |
    python3 -c 'import json,sys
try:
    ready=json.load(sys.stdin).get("staticRouterRevision")=="segmented-rsc-v1"
except (ValueError,AttributeError):
    ready=False
if not ready:
    sys.exit("Deploy the compatible frontend router before publishing segmented exports")'
fi

# Existing HTML/tabs can still request previous chunks. Retain every old key;
# garbage collection needs a separate, measured client-retention policy.
aws s3 sync "$export_dir/_next/static/" "s3://${S3_BUCKET}/_next/static/" \
  --cache-control "public,max-age=31536000,immutable" --only-show-errors --no-progress

# Runtime config is infrastructure-owned. Mutable public assets refresh first.
aws s3 sync "$export_dir/" "s3://${S3_BUCKET}/" \
  --exclude "_next/static/*" --exclude "*.html" --exclude "*.txt" --exclude "config.json" \
  --cache-control "no-cache,max-age=0,must-revalidate" --only-show-errors --no-progress

# cp forces upload and metadata replacement even when size/mtime are unchanged.
# Publish HTML and route payloads only after their referenced chunks are present.
aws s3 cp "$export_dir/" "s3://${S3_BUCKET}/" --recursive \
  --exclude "*" --include "*.html" --include "*.txt" --exclude "config.json" \
  --cache-control "no-cache,max-age=0,must-revalidate" --only-show-errors --no-progress

aws cloudfront create-invalidation --distribution-id "$CF_DISTRIBUTION" --paths "/*" \
  --query Invalidation.Id --output text
