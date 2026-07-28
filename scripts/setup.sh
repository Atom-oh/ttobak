#!/usr/bin/env bash
# Project setup script for new developers
set -euo pipefail

echo "=== Ttobak Project Setup ==="

# Check prerequisites
echo "Checking prerequisites..."

if ! /usr/local/go/bin/go version &>/dev/null; then
  echo "ERROR: Go not found at /usr/local/go/bin/go"
  exit 1
fi
echo "  Go: $(/usr/local/go/bin/go version)"

if ! node --version &>/dev/null; then
  echo "ERROR: Node.js not found"
  exit 1
fi
echo "  Node: $(node --version)"

if ! aws --version &>/dev/null; then
  echo "WARNING: AWS CLI not found (needed for deployment)"
fi

# Install frontend dependencies
echo ""
echo "Installing frontend dependencies..."
cd frontend && npm install && cd ..

# Install infra dependencies
echo ""
echo "Installing CDK dependencies..."
cd infra && npm install && cd ..

# Build backend
echo ""
echo "Building Go Lambda binaries..."
cd backend
for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  echo "  Building $dir..."
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o "$dir/bootstrap" "./$dir"
done
cd ..

# Build frontend
echo ""
echo "Building frontend..."
cd frontend && npm run build && cd ..

# Synth CDK
echo ""
echo "Synthesizing CDK stacks..."
cd infra && npx cdk synth --quiet && cd ..

# Install git hooks
echo ""
echo "Installing git hooks..."
bash scripts/install-hooks.sh

echo ""
echo "=== Setup Complete ==="
echo "Run 'cd frontend && npm run dev' to start the dev server"
