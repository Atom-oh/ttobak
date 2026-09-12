#!/usr/bin/env bash
# Test runner with TAP-style output
set -euo pipefail

PASS=0
FAIL=0
TOTAL=0

assert_file_exists() {
  TOTAL=$((TOTAL + 1))
  if [ -f "$1" ]; then
    echo "ok $TOTAL - $2"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $2 (file not found: $1)"
    FAIL=$((FAIL + 1))
  fi
}

assert_dir_exists() {
  TOTAL=$((TOTAL + 1))
  if [ -d "$1" ]; then
    echo "ok $TOTAL - $2"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $2 (dir not found: $1)"
    FAIL=$((FAIL + 1))
  fi
}

assert_executable() {
  TOTAL=$((TOTAL + 1))
  if [ -x "$1" ]; then
    echo "ok $TOTAL - $2"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $2 (not executable: $1)"
    FAIL=$((FAIL + 1))
  fi
}

assert_contains() {
  TOTAL=$((TOTAL + 1))
  if grep -q "$2" "$1" 2>/dev/null; then
    echo "ok $TOTAL - $3"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $3 (pattern '$2' not in $1)"
    FAIL=$((FAIL + 1))
  fi
}

# assert_eq <label> <expected> <actual>
assert_eq() {
  TOTAL=$((TOTAL + 1))
  if [ "$2" = "$3" ]; then
    echo "ok $TOTAL - $1"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $1 (expected '$2', got '$3')"
    FAIL=$((FAIL + 1))
  fi
}

# assert_grep_match <label> <ERE> <string> — string (not file) matched with grep -E
assert_grep_match() {
  TOTAL=$((TOTAL + 1))
  if printf '%s\n' "$3" | grep -qE -- "$2"; then
    echo "ok $TOTAL - $1"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $1 (pattern '$2' not matched)"
    FAIL=$((FAIL + 1))
  fi
}

# assert_grep_no_match <label> <ERE> <string>
assert_grep_no_match() {
  TOTAL=$((TOTAL + 1))
  if printf '%s\n' "$3" | grep -qE -- "$2"; then
    echo "not ok $TOTAL - $1 (pattern '$2' unexpectedly matched)"
    FAIL=$((FAIL + 1))
  else
    echo "ok $TOTAL - $1"
    PASS=$((PASS + 1))
  fi
}

# assert_bash_syntax <label> <file>
assert_bash_syntax() {
  TOTAL=$((TOTAL + 1))
  if bash -n "$2" 2>/dev/null; then
    echo "ok $TOTAL - $1"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $1 (bash -n failed: $2)"
    FAIL=$((FAIL + 1))
  fi
}

# assert_json_valid <label> <file>
assert_json_valid() {
  TOTAL=$((TOTAL + 1))
  if python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$2" 2>/dev/null; then
    echo "ok $TOTAL - $1"
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - $1 (invalid JSON: $2)"
    FAIL=$((FAIL + 1))
  fi
}

# skip <label> <reason> — TAP "ok ... # SKIP" directive (counts toward TOTAL/PASS, never FAIL)
skip() {
  TOTAL=$((TOTAL + 1))
  PASS=$((PASS + 1))
  echo "ok $TOTAL - $1 # SKIP $2"
}

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

echo "TAP version 13"
echo "# Ttobak Project Structure Tests"

# Run sub-test suites
for test_file in tests/hooks/*.sh tests/structure/*.sh; do
  if [ -f "$test_file" ] && [ -x "$test_file" ]; then
    echo "# Running $test_file"
    source "$test_file"
  fi
done

echo ""
echo "1..$TOTAL"
echo "# pass $PASS"
echo "# fail $FAIL"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
