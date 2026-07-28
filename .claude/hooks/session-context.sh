#!/usr/bin/env bash
# SessionStart hook — loads project context
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "=== Ttobak Session Context ==="
echo "Branch: $(git -C "$PROJECT_ROOT" branch --show-current 2>/dev/null || echo 'unknown')"
echo "Last commit: $(git -C "$PROJECT_ROOT" log --oneline -1 2>/dev/null || echo 'none')"

# Show uncommitted changes summary
CHANGES=$(git -C "$PROJECT_ROOT" status --porcelain 2>/dev/null | wc -l)
echo "Uncommitted changes: $CHANGES files"

# Show if any Lambda binaries are stale
for dir in api transcribe summarize process-image kb; do
  BOOTSTRAP="$PROJECT_ROOT/backend/cmd/$dir/bootstrap"
  if [ -f "$BOOTSTRAP" ]; then
    BOOTSTRAP_AGE=$(( $(date +%s) - $(stat -c %Y "$BOOTSTRAP" 2>/dev/null || echo 0) ))
    if [ "$BOOTSTRAP_AGE" -gt 86400 ]; then
      echo "WARNING: backend/cmd/$dir/bootstrap is >24h old"
    fi
  fi
done

echo "=== End Context ==="
