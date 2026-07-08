#!/usr/bin/env bash
# lens×모델 매트릭스 병렬 fan-out. 인자: <diff> <lenses_dir> <workdir>
# lenses_dir 안의 각 *.txt 가 lens 하나(파일명 stem = lens 태그, 예: L2/L3/L4/L5) —
# 그 lens 전용 리뷰 프롬프트(자체 완결형: "이 lens만 봐"). 각 lens × 각 모델이
# 독립 에이전트 셀 하나(design: oh-my-cloud-skills 원본 설계 문서 — 이 repo엔 없음, 그 repo의
# docs/superpowers/specs/2026-07-05-pr-review-hybrid-lens-design.md 참조).
# diff 전달 경로는 CLI 별로 다름: Codex 는 stdin(`< "$DIFF"` 직접 리다이렉트, 파일이라
# TTY 아님 → no-hang); Kiro 는 stdin 을 무시하므로 size-capped argv 텍스트로 직접 embed
# 한다(툴 미부여 — 아래 KIRO_DIFF_TEXT 주석 참조; fs_read 부여는 CRITICAL로 제거됨).
# timeout 백스톱 + 비대화형 플래그로 멈춤 방지. 셀이 비면 최대
# PANEL_RETRIES 회 재시도(gpt-5.5/bedrock-mantle 등 transient 흡수). 매 시도마다 재실행.
# 모든 셀(모델 수 × lens 수)이 병렬(&+wait) — 벽시계 ≈ 최슬로우 셀 하나, 순차합 아님.
set -uo pipefail
DIFF="$(realpath "$1" 2>/dev/null)" \
  || { echo "run-panel.sh: realpath failed to resolve diff path: $1" >&2; exit 1; }
LENSES_DIR="$2"; WORK="$3"
# precheck.sh 와 같은 원칙 — $WORK 가 비면 ensure_slots 의 `rm -rf "$1/slot"` 가
# `rm -rf /slot`(파일시스템 루트 하위) 이 되는 파괴적 경로가 생긴다. $LENSES_DIR 빈 값은
# 파괴적이진 않지만(글롭이 매치 없이 조용히 0셀로 끝남) 인자 오설정을 조용히 넘기지 않고
# 바로 잡아내는 게 디버깅에 낫다.
[ -n "$LENSES_DIR" ] || { echo "run-panel.sh: lenses_dir (\$2) must not be empty" >&2; exit 1; }
[ -n "$WORK" ] || { echo "run-panel.sh: workdir (\$3) must not be empty" >&2; exit 1; }
# $SLOT(="$WORK/slot")는 Kiro 셀에서 `cd "$KIRO_CWD"` 이후에도 그대로 참조된다 — 호출자가
# 상대경로 WORK를 주면 그 시점부터 깨진다. 현재 호출부(워크플로·테스트)는 전부 절대경로라
# 실 결함은 아니었지만, DIFF 처럼 코드가 직접 보장하도록 여기서 절대화한다(13차 리뷰 MINOR-1).
# mkdir/realpath 실패를 `set -e` 없이 조용히 넘기면 이후 전부 빈/잘못된 $WORK 로 계속
# 진행할 수 있다 — 8~9차에서 확립한 "파괴적 경로를 만들 수 있는 연산은 실패를 명시적으로
# 처리" 원칙과 일관되게 두 줄 다 fail-fast(15차 리뷰 MINOR-2).
mkdir -p "$WORK" || { echo "run-panel.sh: failed to create workdir: $WORK" >&2; exit 1; }
WORK="$(realpath "$WORK")" \
  || { echo "run-panel.sh: realpath failed to resolve workdir: $WORK" >&2; exit 1; }
DIR="$(cd "$(dirname "$0")" && pwd)"; . "$DIR/lib.sh"
ensure_slots "$WORK" || exit 1
SLOT="$WORK/slot"; RESP="$WORK/responded.txt"; : > "$RESP"
# 비-ephemeral 러너에서 $WORK 가 재사용되면 이전 실행이 남긴 severe 플래그가 그대로
# 살아남아, 이번엔 4모델 모두 정상 응답해도 synthesize.sh 가 강제 FAIL 하게 된다 —
# responded.txt/degraded-models.txt 처럼 매 실행 시작 시 리셋.
rm -f "$WORK/coverage-severe.flag"
T="${PANEL_TIMEOUT:-300}"
RETRIES="${PANEL_RETRIES:-3}"
KIRO_MODELS=("claude-opus-4.8:kiro-opus" "gpt-5.5:kiro-gpt" "glm-5:kiro-glm")

shopt -s nullglob
LENS_FILES=("$LENSES_DIR"/*.txt)
shopt -u nullglob
if [ "${#LENS_FILES[@]}" -eq 0 ]; then
  echo "run-panel.sh: no *.txt lens files found in $LENSES_DIR" >&2
  exit 1
fi

# 한 셀을 최대 $RETRIES 회 실행 — 슬롯이 비면 재시도(transient). 백그라운드로 호출.
#   try_panel <slot> <err> <cmd...>   (stdin=$DIFF, stdout=slot, stderr=err)
try_panel() {
  local slot="$1" err="$2"; shift 2
  local a rc=1
  for a in $(seq 1 "$RETRIES"); do
    "$@" > "$slot" 2>"$err" < "$DIFF"; rc=$?
    [ -s "$slot" ] && [ "$rc" -eq 0 ] && break
    [ "$a" -lt "$RETRIES" ] && echo "[retry $a/$RETRIES] $(basename "$slot" .md)" >&2
  done
  echo "$rc" > "$slot.rc"
}

# Kiro 셀은 이제 어떤 툴도 부여받지 않는다(`--trust-tools=`, 아래) — fs_read 부여를
# 제거했으므로 절대경로 read 를 유도하는 diff-injection 경로 자체가 없다(CRITICAL 수정).
# 격리 cwd/HOME(co-agent PR 게이트의 `_review_one`/`_sanitized_env`와 동일 패턴)은 이제
# 잔여 read 위험의 완화가 아니라 순수 defense-in-depth(캐시/세션 상태가 실행 간 전이되는
# 재현성 문제 예방, env 는 Kiro 자기 인증 최소 변수만) — 비-ephemeral 러너에서 $WORK 가
# 재사용돼도 매 실행 시작 시 리셋해 이전 실행 상태가 새 실행에 새지 않게 한다. 매트릭스
# 확장(4→16셀, kiro 셀 3→12개)으로 모든 kiro 셀이 동시(&) 실행되므로 셀마다 별도
# 서브디렉터리를 준다.
KIRO_CWD_BASE="$WORK/kiro-cwd"
[ -L "$KIRO_CWD_BASE" ] && { echo "run-panel.sh: \$KIRO_CWD_BASE is a symlink, refusing (TOCTOU guard)" >&2; exit 1; }
rm -rf "$KIRO_CWD_BASE"; mkdir -p "$KIRO_CWD_BASE"
kiro_env() {
  local cell_cwd="$1"; shift
  env -i PATH="$PATH" HOME="$cell_cwd" LANG="${LANG:-}" LC_ALL="${LC_ALL:-}" TMPDIR="${TMPDIR:-/tmp}" \
    ${KIRO_API_KEY:+KIRO_API_KEY="$KIRO_API_KEY"} "$@"
}

# Kiro 셀은 더 이상 fs_read 를 받지 않는다(diff 는 size-capped argv 텍스트로 직접 embed) --
# diff 는 untrusted PR 콘텐츠라, fs_read 를 신뢰하면 diff 내 프롬프트 인젝션이 절대경로
# read 를 유도할 수 있고 그 값이 체어 종합을 거쳐 공개 PR 코멘트로 노출될 수 있다(CRITICAL,
# claude-code-usage-dashboard PR #4 리뷰에서 발견 -- 동일 lens×model matrix 설계를 공유하는
# 모든 fleet repo에 동일 적용). `--trust-tools=` 로 툴을 아예 안 주면 이 경로가 구조적으로
# 막힌다. argv 임베드의 기존 우려(ARG_MAX, ps 노출)는 실질 트레이드오프가 아니다: 아래에서
# 커널 한도 아래로 캡핑하고, 이 diff 는 이미 public repo 의 PR diff 라 ps 노출이 새로운
# 기밀 노출이 아니다.
KIRO_DIFF_CAP="${KIRO_DIFF_CAP:-100000}"
KIRO_DIFF_TEXT="$(head -c "$KIRO_DIFF_CAP" "$DIFF")"
[ "$(wc -c < "$DIFF")" -gt "$KIRO_DIFF_CAP" ] \
  && KIRO_DIFF_TEXT+=$'\n[...TRUNCATED at '"$KIRO_DIFF_CAP"'B -- full diff not sent to Kiro...]'

for lens_file in "${LENS_FILES[@]}"; do
  lens="$(basename "$lens_file" .txt)"
  LENS_PROMPT="$(cat "$lens_file")"

  # Codex 셀 (Bedrock, config.toml). --skip-git-repo-check 필수. AWS_REGION 강제:
  # gpt-5.5(bedrock-mantle)는 In-Region(us-east-1) 만 지원 — 잡 region 무관하게 고정.
  # diff 는 stdin.
  if command -v codex >/dev/null 2>&1; then
    ( try_panel "$SLOT/codex-$lens.md" "$SLOT/codex-$lens.err" \
        env AWS_REGION="${CODEX_AWS_REGION:-us-east-1}" AWS_DEFAULT_REGION="${CODEX_AWS_REGION:-us-east-1}" \
        timeout "$T" codex exec -s read-only --skip-git-repo-check "$LENS_PROMPT" ) &
  else echo "[skip] codex/$lens (binary absent)" >&2; : > "$SLOT/codex-$lens.md"; fi

  # Kiro x3 셀 — model:tag 를 한 배열에서 파생(호출/집계 동기화). Kiro's non-interactive
  # `chat` reads ONLY the prompt arg -- it ignores stdin, so the diff must reach it via argv
  # (capped, embedded directly -- 위 KIRO_DIFF_TEXT/`--trust-tools=` 주석 참조).
  KIRO_INSTRUCTION="$LENS_PROMPT"$'\n\n'"Review ONLY the diff below; do not read or reference any other files:"$'\n\n'"$KIRO_DIFF_TEXT"
  for entry in "${KIRO_MODELS[@]}"; do
    m="${entry%%:*}"; tag="${entry##*:}"
    if command -v kiro-cli >/dev/null 2>&1; then
      CELL_CWD="$KIRO_CWD_BASE/$tag-$lens"; mkdir -p "$CELL_CWD"
      ( cd "$CELL_CWD" && try_panel "$SLOT/$tag-$lens.md" "$SLOT/$tag-$lens.err" \
          kiro_env "$CELL_CWD" timeout "$T" kiro-cli chat "$KIRO_INSTRUCTION" --model "$m" \
          --mode default --no-interactive --trust-tools= --wrap never ) &
    else echo "[skip] $tag/$lens (binary absent)" >&2; : > "$SLOT/$tag-$lens.md"; fi
  done
done

# NOTE: Antigravity(agy) 는 제거됨 — OAuth 인터랙티브 로그인 전용(API 키 인증 모드 없음)
# 이라 헤드리스 CI 에서 인증 불가. 패널 = Codex + Kiro x3 → Claude 의장.
wait

# 결과 집계 (KIRO_MODELS·LENS_FILES 와 동일 소스에서 태그 파생 → 하드코딩 불일치 방지)
for lens_file in "${LENS_FILES[@]}"; do
  lens="$(basename "$lens_file" .txt)"
  record_result "$SLOT/codex-$lens.md" "codex/$lens" "$RESP"
  for entry in "${KIRO_MODELS[@]}"; do
    tag="${entry##*:}"; record_result "$SLOT/$tag-$lens.md" "$tag/$lens" "$RESP"
  done
done
echo "Panel responded ($(wc -l < "$RESP") / $(( (${#KIRO_MODELS[@]} + 1) * ${#LENS_FILES[@]} )) cells): $(tr '\n' ' ' < "$RESP")"

# 커버리지 floor — 모델 하나(플래그 무효화/바이너리 부재/전면 인증 실패 등)가 lens 전부에서
# 응답 없으면, 매트릭스가 조용히 그 모델 없이 축소된 채 VERDICT: PASS 로 이어질 수 있다
# (예: kiro-cli 플래그(`--mode default --trust-tools=`)가 이 러너에서 무효거나 모델 ID 가
# 계정에 프로비저닝 안 되면 Kiro 12셀 전부 graceful skip → 실질 4셀짜리 리뷰인데 코멘트만
# 봐선 눈에 안 띌 수 있음). 모델별 row 가 완전히 비면 경고 + synthesize.sh 가 명시하도록 전달.
: > "$WORK/degraded-models.txt"
for model_tag in codex "${KIRO_MODELS[@]##*:}"; do
  # grep -c 는 매치가 0건이어도 "0"을 찍고 exit 1 한다(매치 없음 = grep 관점의 "실패") —
  # `|| echo 0` 폴백을 붙이면 그 "0" 뒤에 폴백의 "0"이 또 붙어 "0\n0"이 되는 회귀가
  # 실제로 있었다(test (f)에서 잡힘). $RESP 는 run-panel.sh 시작부에 항상 만들어지므로
  # "파일 없음" 폴백 자체가 불필요 — 그냥 grep 의 stdout 을 그대로 쓴다.
  # $RESP 가 예기치 않게 부재/비가독이면 grep 이 아무것도 못 찍어 row_count 가 빈 문자열이
  # 되고, `[ "" -eq 0 ]` 는 (set -e 없이) 조용히 false 로 삼켜져 degraded 경고 자체가
  # 빠진다 — 12차에서 잡은 responded.txt 부재 비대칭과 같은 부류(14차 리뷰 MINOR-1).
  row_count="$(grep -c "^${model_tag}/" "$RESP" 2>/dev/null)"
  if [ "${row_count:-0}" -eq 0 ]; then
    echo "::warning::model '$model_tag' produced zero responses across all ${#LENS_FILES[@]} lenses — coverage degraded" >&2
    echo "$model_tag" >> "$WORK/degraded-models.txt"
  fi
done

# 심각도 상향 — degraded 모델이 (전체-1)개 이상이면 살아남은 벤더가 최대 1개뿐이라, "매트릭스
# 자체가 lens당 교차확인"이라는 warn-only 의 전제(다른 모델이 여전히 같은 lens 를 본다)가
# 성립하지 않는다. 이 경우만 severe 로 승격해 synthesize.sh 가 VERDICT 를 강제 FAIL 하도록
# 신호를 남긴다(모델 1개 탈락은 여전히 warn-only 유지 — 간헐적 rate-limit 로도 흔하고, 남은
# 3개가 각 lens 를 여전히 교차확인하므로 이 PR 도입 시 설계한 대로 사람이 배너로만 인지해도
# 된다는 원 판단은 유효). 신규 kiro-cli 플래그가 처음 실전 투입되는 시점(3개 kiro 모델이
# 동시에 전멸하는 경우가 바로 이 기준을 정확히 친다)이 이 케이트가 노리는 실제 사례다.
# claude-code-usage-dashboard PR #4 리뷰(MAJOR)에서 발견: 옛 조건은 degraded 개수를
# TOTAL_MODELS-1 과 비교했을 뿐 벤더 축이 아니었다 -- codex 단독 탈락(모델 1개)은 남은
# 3개가 전부 kiro(벤더 1개)인데도 "1 >= 3"이 거짓이라 severe 가 안 걸렸다. 에러 메시지
# 자신의 "≤1 vendor" 주장과 반대로 동작하던 버그. codex 가 죽거나 kiro 가 전멸(둘 중
# 하나라도)하면 남는 벤더가 최대 1개이므로 그 자체로 severe.
CODEX_DEAD=0
grep -qx "codex" "$WORK/degraded-models.txt" 2>/dev/null && CODEX_DEAD=1
KIRO_TOTAL=${#KIRO_MODELS[@]}
KIRO_DEGRADED_COUNT=$(grep -c "^kiro-" "$WORK/degraded-models.txt" 2>/dev/null || echo 0)
KIRO_ALL_DEAD=0
[ "$KIRO_TOTAL" -gt 0 ] && [ "${KIRO_DEGRADED_COUNT:-0}" -ge "$KIRO_TOTAL" ] && KIRO_ALL_DEAD=1
if [ "$CODEX_DEAD" = 1 ] || [ "$KIRO_ALL_DEAD" = 1 ]; then
  echo "::error::coverage collapsed to ≤1 vendor (codex dead=$CODEX_DEAD, kiro fully dead=$KIRO_ALL_DEAD) — forcing VERDICT: FAIL, no cross-vendor check remains for any lens" >&2
  : > "$WORK/coverage-severe.flag"
fi

# skip 원인 노출: 빈 슬롯인데 stderr 가 있으면 stderr 의 끝(실제 에러)을 로그에 찍는다.
# public repo 라 이 Actions 로그는 누구나 읽을 수 있고, stderr(에러 메시지·스택트레이스)에
# 우연히 크리덴셜성 값이 섞여 나오는 경로가 원시로 찍으면 스크럽 없는 유출구가 된다.
# synthesize.sh 의 셀과 동일한 scrub_secrets() 를 통과시킨다.
for e in "$SLOT"/*.err; do
  [ -s "$e" ] || continue
  b="$(basename "$e" .err)"
  [ -s "$SLOT/$b.md" ] && continue   # 응답 성공이면 건너뜀
  echo "--- [$b] skipped; stderr (last 25 lines, scrubbed) ---" >&2
  tail -25 "$e" | scrub_secrets >&2
done
