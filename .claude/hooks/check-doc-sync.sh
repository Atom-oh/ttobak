#!/usr/bin/env bash
# PostToolUse hook — checks if documentation needs updating after code changes
set -euo pipefail

INPUT=$(cat)

# Extract file path from tool input
FILE_PATH=$(echo "$INPUT" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    print(data.get('file_path', ''))
except:
    pass
" 2>/dev/null || true)

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# Check which documentation might need updating based on changed file
case "$FILE_PATH" in
  */backend/internal/handler/*)
    echo "NOTE: API handler changed. Consider updating docs/API-SPEC.md if endpoints were added/modified."
    ;;
  */infra/lib/*)
    echo "NOTE: Infrastructure changed. Consider updating docs/INFRA-SPEC.md."
    ;;
  */frontend/src/components/*)
    echo "NOTE: UI component changed. Consider updating docs/DESIGN-SPEC.md if design patterns changed."
    ;;
  */backend/cmd/*/main.go)
    echo "NOTE: Lambda entry point changed. Consider updating CLAUDE.md Architecture section."
    ;;
esac

exit 0
