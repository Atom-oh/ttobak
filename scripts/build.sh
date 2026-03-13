#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Usage: ./build.sh [target...]
# Targets: backend, frontend, all (default: all)

targets=("${@:-all}")

build_backend() {
  echo "==> Building Go Lambda functions (ARM64)..."
  cd "$ROOT/backend"
  for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/websocket cmd/kb; do
    echo "  - $dir"
    GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o "$dir/bootstrap" "./$dir"
  done
  echo "  Go build complete"
}

build_frontend() {
  echo "==> Building frontend..."
  cd "$ROOT/frontend"
  npm run build
  echo "  Frontend build complete: frontend/out/"
}

for target in "${targets[@]}"; do
  case "$target" in
    backend)  build_backend ;;
    frontend) build_frontend ;;
    all)      build_backend; build_frontend ;;
    *)        echo "Unknown target: $target (use: backend, frontend, all)"; exit 1 ;;
  esac
done

echo "==> Build done"
