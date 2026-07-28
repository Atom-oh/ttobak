#!/usr/bin/env bash
# PreToolUse hook — scans for secrets before writing files
# Reads the tool input from stdin (JSON with file_path and content/new_string)

set -euo pipefail

INPUT=$(cat)

# Extract the content being written
CONTENT=$(echo "$INPUT" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    print(data.get('content', '') or data.get('new_string', ''))
except:
    pass
" 2>/dev/null || true)

if [ -z "$CONTENT" ]; then
  exit 0
fi

VIOLATIONS=""

# AWS keys
if echo "$CONTENT" | grep -qP 'AKIA[0-9A-Z]{16}'; then
  VIOLATIONS="${VIOLATIONS}\n- AWS Access Key ID detected"
fi

# AWS secret keys
if echo "$CONTENT" | grep -qP '[0-9a-zA-Z/+]{40}' | grep -qP 'aws_secret|secret_access'; then
  VIOLATIONS="${VIOLATIONS}\n- AWS Secret Access Key pattern detected"
fi

# Generic secrets/tokens in assignments
if echo "$CONTENT" | grep -qPi '(password|secret|token|api_key|apikey)\s*[=:]\s*["\x27][^"\x27]{8,}'; then
  VIOLATIONS="${VIOLATIONS}\n- Hardcoded secret/password/token detected"
fi

# Private keys
if echo "$CONTENT" | grep -q 'BEGIN.*PRIVATE KEY'; then
  VIOLATIONS="${VIOLATIONS}\n- Private key detected"
fi

# .env file content
if echo "$INPUT" | python3 -c "
import sys, json
data = json.load(sys.stdin)
fp = data.get('file_path', '')
if fp.endswith('.env') or '/.env.' in fp:
    sys.exit(0)
sys.exit(1)
" 2>/dev/null; then
  VIOLATIONS="${VIOLATIONS}\n- Writing to .env file (secrets may be exposed)"
fi

if [ -n "$VIOLATIONS" ]; then
  echo "SECRET SCAN FAILED:"
  echo -e "$VIOLATIONS"
  echo ""
  echo "Remove secrets before committing. Use environment variables or AWS Secrets Manager."
  exit 1
fi

exit 0
