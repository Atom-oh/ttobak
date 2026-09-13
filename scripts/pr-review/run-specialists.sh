#!/usr/bin/env bash
# The trusted parent releases Kiro roles only after shared startup checks pass.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
WORK="${3:?Expected diff, lenses directory and work directory}"
. "$DIR/lib.sh"
ensure_slots "$WORK"
python3 "$DIR/prepare_roles.py" --work "$WORK" --prepared-diff "$1"
python3 "$DIR/run_role.py" --work "$WORK" --all
status=0
python3 "$DIR/role_review.py" aggregate --work "$WORK" || status=$?
# Exit 2 is a recorded coverage failure: let synthesis publish its FAIL report.
[ "$status" -eq 0 ] || [ "$status" -eq 2 ]
