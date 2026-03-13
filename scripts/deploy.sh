#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

BUCKET="ttobak-site-180294183052-ap-northeast-2"
DIST_ID="E3BPV9VFNI1H2S"

# Usage: ./deploy.sh [target...]
# Targets: infra, frontend, all (default: all)
# Options: --build  Run build before deploying

targets=()
do_build=false

for arg in "$@"; do
  case "$arg" in
    --build) do_build=true ;;
    *)       targets+=("$arg") ;;
  esac
done

if [ ${#targets[@]} -eq 0 ]; then
  targets=("all")
fi

deploy_infra() {
  echo "==> Deploying CDK stacks..."
  cd "$ROOT/infra"
  npx cdk deploy --all --require-approval never
  echo "  CDK deploy complete"
}

deploy_frontend() {
  echo "==> Deploying frontend to S3 + CloudFront..."
  cd "$ROOT/frontend"
  aws s3 sync out/ "s3://${BUCKET}/" --delete
  aws cloudfront create-invalidation --distribution-id "$DIST_ID" --paths "/*" --output text
  echo "  Frontend deploy complete"
}

if $do_build; then
  "$ROOT/scripts/build.sh" all
fi

for target in "${targets[@]}"; do
  case "$target" in
    infra)    deploy_infra ;;
    frontend) deploy_frontend ;;
    all)      deploy_infra; deploy_frontend ;;
    *)        echo "Unknown target: $target (use: infra, frontend, all)"; exit 1 ;;
  esac
done

echo "==> Deploy done"
