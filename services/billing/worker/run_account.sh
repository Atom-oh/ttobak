#!/usr/bin/env bash
# One uploaded COT zip -> one account's dashboard datasets, verified against its own COT360 page.
# Usage: [AS_OF=YYYY-MM-DD] run_account.sh <zip> <work_dir> <out_dir> <acct> [names.json]
#   zip: the upload, untrusted (stage_cot.py checks it and stages it under canonical names)
#   work_dir: new or empty dir, one per job (so no stale checks.json); out_dir: new or empty, may sit inside work_dir
#   acct: account id ^[A-Za-z0-9_-]{1,64}$; names.json: optional {acct: display name}
# stdout carries only "STEP <validate|extract|datasets|check> <running|done|failed|needs_review>" lines and
# {"reason": ...} lines for an invalid upload; script output goes to stderr.
# Exit: 0 ok, 1 check failed (needs review), 2 bad arguments, 3 invalid upload, 4 any other failure.
# Agents and the report run afterwards over <out_dir> (worker), not here.
set -euo pipefail
usage() { echo "usage: run_account.sh <zip> <work_dir> <out_dir> <acct> [names.json]" >&2; exit 2; }
[ $# -eq 4 ] || [ $# -eq 5 ] || usage
ACCT=$4
[[ "$ACCT" =~ ^[A-Za-z0-9_-]{1,64}$ ]] || { echo "invalid account id" >&2; exit 2; }
ZIP=$(realpath -m "$1"); WORK=$(realpath -m "$2"); OUT=$(realpath -m "$3")
S=$(cd "$(dirname "$0")/scripts" && pwd)
empty() { [ ! -e "$1" ] || { [ -d "$1" ] && [ -z "$(ls -A "$1")" ]; }; }
empty "$WORK" || { echo "work dir must be new or empty: $WORK" >&2; exit 2; }
empty "$OUT" || { echo "out dir must be new or empty: $OUT" >&2; exit 2; }
X="$WORK/extracted"; CSV="$WORK/csv"; DJ="$WORK/dashboard_data.json"
case "$OUT" in "$WORK"|"$X"|"$X"/*|"$CSV"|"$CSV"/*|"$DJ") echo "out dir collides with work files: $OUT" >&2; exit 2 ;; esac
# the COT run date (commitment expiry windows count from it; published as period.json as_of): today (UTC) by default
export AS_OF="${AS_OF:-$(date -u +%Y-%m-%d)}"
if [ $# -eq 5 ]; then export COT_NAMES=$(realpath -m "$5"); else unset COT_NAMES; fi
mkdir -p "$WORK" "$OUT"

CUR=""
step() { CUR=$1; echo "STEP $1 $2"; }
# any failure not handled below: report the running step as failed, never exit 0/1/3
trap '[ -n "$CUR" ] && echo "STEP $CUR failed"; exit 4' ERR
# a converter's exit 3 is an invalid upload (its reason is already on stdout)
invalid_or_fail() { local rc=$1; echo "STEP $CUR failed"; [ "$rc" -eq 3 ] && exit 3; exit 4; }

step validate running
rc=0; python3 -I "$S/stage_cot.py" "$ZIP" "$WORK" "$ACCT" || rc=$?
[ $rc -eq 0 ] || invalid_or_fail $rc
step validate done

step extract running
export COT_CSV_DIR="$CSV" COT_ENTITIES="$ACCT"
mkdir -p "$CSV/$ACCT/html"
# xlsx2csv: sheet stats on stderr, only an invalid-upload reason on stdout
rc=0; python3 -I "$S/xlsx2csv.py" "$X/$ACCT/COT_${ACCT}_Template.xlsx" "$CSV/$ACCT" Data "$ACCT" || rc=$?
[ $rc -eq 0 ] || invalid_or_fail $rc
python3 -I "$S/html2json.py" "$X/$ACCT/COT_${ACCT}_COT360.html" "$CSV/$ACCT/html" >&2
# the billing period every later step uses: the last 6 months in this account's service costs
# period.py crashing is a failure (4); running fine but finding no months is an invalid upload (3)
rc=0; COT_MONTHS=$(python3 -I "$S/period.py" "$ACCT") || rc=$?
[ $rc -eq 0 ] || invalid_or_fail 4
if [ -z "$COT_MONTHS" ]; then
  echo '{"reason": "COT 기간을 찾지 못했습니다"}'
  invalid_or_fail 3
fi
export COT_MONTHS
echo "  period $COT_MONTHS" >&2
step extract done

step datasets running
python3 -I "$S/build_data.py" "$X" "$DJ" >&2
python3 -I "$S/bedrock_tags.py" "$DJ" >&2
python3 -I "$S/export_datasets.py" "$DJ" "$OUT" >&2
python3 -I "$S/sessions.py" "$DJ" "$OUT" >&2
python3 -I "$S/tag_services.py" "$CSV" "$OUT" >&2
python3 -I "$S/accounts.py" "$CSV" "$OUT" >&2
python3 -I "$S/tag_signals.py" "$OUT" >&2
python3 -I "$S/ec2_types.py" "$CSV" "$OUT" >&2
python3 -I "$S/raw_export.py" "$CSV" "$OUT" >&2
python3 -I "$S/cot360_metrics.py" "$CSV" "$OUT" "$AS_OF" >&2
python3 -I "$S/insights.py" "$OUT" >&2
python3 -I "$S/briefing.py" "$OUT" >&2
step datasets done

step check running
rc=0; python3 -I "$S/cot360_check.py" "$OUT" "$X" >&2 || rc=$?
# checks.json is written only when every check ran; exit 1 without it is a crash, not a mismatch
if [ $rc -eq 0 ]; then step check done; exit 0; fi
if [ $rc -eq 1 ] && [ -f "$OUT/checks.json" ]; then step check needs_review; exit 1; fi
echo "STEP check failed"; exit 4
