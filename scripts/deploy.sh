#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

BUCKET="ttobak-site-180294183052-ap-northeast-2"
DIST_ID="E3BPV9VFNI1H2S"

# Usage: ./deploy.sh [target...]
# Target: frontend (default). Infrastructure uses reviewed, explicit stack deploys.
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
  targets=("frontend")
fi

deploy_infra() {
  echo "Use the reviewed infrastructure workflow or explicit --exclusively stack deployments." >&2
  return 2
}

deploy_frontend() {
  echo "==> Deploying frontend to S3 + CloudFront..."
  cd "$ROOT/frontend"
  S3_BUCKET="$BUCKET" CF_DISTRIBUTION="$DIST_ID" bash "$ROOT/scripts/publish-frontend.sh" out
  echo "  Frontend deploy complete"
}

if $do_build; then
  "$ROOT/scripts/build.sh" frontend
fi

for target in "${targets[@]}"; do
  case "$target" in
    infra)    deploy_infra ;;
    frontend) deploy_frontend ;;
    all)      deploy_infra ;;
    *)        echo "Unknown target: $target (use: frontend)"; exit 1 ;;
  esac
done

echo "==> Deploy done"
