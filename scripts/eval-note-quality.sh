#!/usr/bin/env bash
set -euo pipefail

mode="${1:-}"
output="${2:-}"
case "$mode" in requests|live|grade) ;; *)
  echo "Usage: $0 {requests|live|grade} /absolute/output-directory" >&2
  exit 2
esac
[[ "$output" = /* ]] || { echo "Output directory must be absolute" >&2; exit 2; }
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
go_binary="${TTOBAK_GO_BINARY:-/usr/local/go/bin/go}"
if [[ ! -x "$go_binary" ]]; then
  go_binary="$(command -v go || true)"
fi
[[ -n "$go_binary" && -x "$go_binary" ]] || { echo "Set TTOBAK_GO_BINARY to the Go executable" >&2; exit 2; }
export TTOBAK_NOTE_EVAL_MODE="$mode" TTOBAK_NOTE_EVAL_OUT="$output"
exec "$go_binary" -C "$repo_root/backend" test ./internal/service \
  -run '^TestNoteQualityEvaluation$' -count=1 -timeout=25m -v
