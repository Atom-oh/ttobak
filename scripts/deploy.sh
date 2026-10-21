#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

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
  local deployment bucket distribution
  deployment=$(aws cloudformation describe-stacks \
    --stack-name TtobakFrontendStack --region "${AWS_REGION:-ap-northeast-2}" \
    --query 'Stacks[0].Outputs' --output json | python3 -c '
import json,re,sys
rows=json.load(sys.stdin)
values={row["OutputKey"]:row["OutputValue"] for row in rows}
bucket=values.get("SiteBucketName","")
distribution=values.get("DistributionId","")
if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]",bucket) or not re.fullmatch(r"[A-Z0-9]+",distribution):
    sys.exit("Frontend stack must expose valid SiteBucketName and DistributionId outputs")
print(bucket,distribution)')
  read -r bucket distribution <<< "$deployment"
  cd "$ROOT/frontend"
  S3_BUCKET="$bucket" CF_DISTRIBUTION="$distribution" bash "$ROOT/scripts/publish-frontend.sh" out
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
