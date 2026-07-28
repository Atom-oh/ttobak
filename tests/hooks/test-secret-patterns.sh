#!/usr/bin/env bash
# Test secret scanning patterns

echo "# Secret pattern tests"

# True positives - should be detected
while IFS= read -r pattern; do
  [ -z "$pattern" ] && continue
  [[ "$pattern" == \#* ]] && continue
  TOTAL=$((TOTAL + 1))
  if echo "$pattern" | grep -qP 'AKIA[0-9A-Z]{16}|BEGIN.*PRIVATE KEY|(password|secret|token|api_key)\s*[=:]\s*["\x27][^"\x27]{8,}'; then
    echo "ok $TOTAL - True positive detected: ${pattern:0:30}..."
    PASS=$((PASS + 1))
  else
    echo "not ok $TOTAL - True positive missed: ${pattern:0:30}..."
    FAIL=$((FAIL + 1))
  fi
done < tests/fixtures/secret-samples.txt

# False positives - should NOT be detected
while IFS= read -r pattern; do
  [ -z "$pattern" ] && continue
  [[ "$pattern" == \#* ]] && continue
  TOTAL=$((TOTAL + 1))
  if echo "$pattern" | grep -qP 'AKIA[0-9A-Z]{16}|BEGIN.*PRIVATE KEY|(password|secret|token|api_key)\s*[=:]\s*["\x27][^"\x27]{8,}'; then
    echo "not ok $TOTAL - False positive triggered: ${pattern:0:30}..."
    FAIL=$((FAIL + 1))
  else
    echo "ok $TOTAL - False positive correctly ignored: ${pattern:0:30}..."
    PASS=$((PASS + 1))
  fi
done < tests/fixtures/false-positives.txt
