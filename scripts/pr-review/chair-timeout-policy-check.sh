#!/bin/bash
# synthesize.sh 의 의장 timeout 정책 검사 — 기대값 assert, 어긋나면 non-zero.
#   ① 느린 실패(timeout kill 또는 CHAIR_FAST_FAIL_S 이상 소요)에서는 fallback 을 태우지 않는다
#      — 같은 입력·같은 캡이라 같은 벽이다. 실측: fallback 이 발동한 모든 실행이 실패로 끝났고
#      (ttobak 30364369995·30362329844, omcs 30350585601·30348322817) 매번 10분을 더 태웠다.
#   ② 빠른 실패(모델 불가용·인증 오류 계열)에서는 fallback 을 태운다 — 다른 모델이 답할 수 있다.
#   ③ 정상 응답이면 어느 쪽도 태우지 않는다
# synthesize.sh 를 그대로 source 할 수 없어(본체 실행) 분기 조건만 동일 형태로 재현한다 —
# source of truth 는 synthesize.sh 이며, 조건식을 고치면 여기도 고쳐야 한다.
set -uo pipefail
FAILED=0
fail(){ echo "  [FAIL] $1" >&2; FAILED=1; }

decide(){ # $1=rc $2=valid(0|1) $3=elapsed $4=primary $5=fallback -> "skip"|"fallback"|"none"
  local CHAIR_RC="$1" valid="$2" CHAIR_ELAPSED="$3" PRIMARY_MODEL="$4" FALLBACK_MODEL="$5"
  # 임계값은 synthesize.sh 의 기본값에서 읽어 고정한다($FF). 외부 env 를 상속하면 경계
  # 케이스가 실행 환경에 따라 달라져 비결정적이 된다(PR#139 리뷰 MINOR).
  local CHAIR_FAST_FAIL_S="$FF" CHAIR_TIMED_OUT=0 CHAIR_SLOW_FAIL=0
  [ "$CHAIR_RC" = 124 ] && CHAIR_TIMED_OUT=1
  { [ "$CHAIR_TIMED_OUT" = 1 ] || [ "$CHAIR_ELAPSED" -ge "$CHAIR_FAST_FAIL_S" ]; } && CHAIR_SLOW_FAIL=1
  if [ "$valid" = 1 ]; then echo none; return; fi
  if [ "$CHAIR_SLOW_FAIL" = 1 ]; then echo skip; return; fi
  if [ "$FALLBACK_MODEL" != "$PRIMARY_MODEL" ]; then echo fallback; return; fi
  echo none
}

t(){ # $1=desc $2=expected $3..=decide args
  local desc="$1" exp="$2"; shift 2
  local got; got="$(decide "$@")"
  printf "  %-50s -> %-9s (기대 %s)\n" "$desc" "$got" "$exp"
  [ "$got" = "$exp" ] || fail "$desc: 기대 $exp, 실제 $got"
}

CAP="$(grep -oE 'CHAIR_TIMEOUT:-[0-9]+' "$(dirname "$0")/synthesize.sh" | head -1 | sed -E 's/.*:-//')"
FF="$(grep -oE 'CHAIR_FAST_FAIL_S:-[0-9]+' "$(dirname "$0")/synthesize.sh" | head -1 | sed -E 's/.*:-//')"
[ -n "$CAP" ] || { echo "synthesize.sh 에서 CHAIR_TIMEOUT 기본값을 못 찾았다" >&2; exit 2; }
[ -n "$FF" ]  || { echo "synthesize.sh 에서 CHAIR_FAST_FAIL_S 기본값을 못 찾았다" >&2; exit 2; }

echo "의장 fallback 정책 (빠른 실패에만 fallback) — 기본값 CHAIR_TIMEOUT=${CAP}s, CHAIR_FAST_FAIL_S=${FF}s"
#                                                  기대       rc  valid elapsed primary  fallback
t "모델 불가용류 즉시 실패(3s)"                     fallback   1   0     3       fable-5 opus-5
t "인증 실패(30s)"                                  fallback   1   0     30      fable-5 opus-5
t "느린 실패(300s) — 예산 문제"                      skip       1   0     300     fable-5 opus-5
t "timeout kill(rc=124, 1500s)"                     skip       124 0     1500    fable-5 opus-5
t "경계값 정확히 ${FF}s — 느린 실패로 취급"           skip       1   0     "$FF"          fable-5 opus-5
t "경계값 $((FF-1))s — 빠른 실패"                    fallback   1   0     "$((FF-1))"    fable-5 opus-5
t "정상 응답 — 아무것도 안 함"                       none       0   1     240     fable-5 opus-5
t "PRIMARY==FALLBACK, 빠른 실패 — 동일 호출 방지"    none       1   0     5       fable-5 fable-5

# 빠른 실패선이 관측된 정상 체어의 하한(cc-on-bedrock 1m42s=102s... 실제 최단 102s)보다
# 커지면 정상 실행 직후의 실패까지 "느린 실패"로 오분류될 수 있다 — 넉넉히 아래에 둔다.
[ "$FF" -le 300 ] || fail "CHAIR_FAST_FAIL_S=${FF}s 는 너무 커서 정상 범위 실패를 예산 문제로 오분류한다"
# 체어 소요는 diff 줄수가 아니라 생성 리뷰 분량에 따르고(실측 17~42 B/s), 관측된 최장
# 정상 체어는 mra 의 7m43s(463s)다 — 캡은 그 3배 여유를 유지해야 정상 실행이 잘리지 않는다.
echo "CHAIR_TIMEOUT=${CAP}s (관측 최장 정상 체어 463s 의 $(( CAP * 10 / 463 ))/10 배)"
[ "${CAP:-0}" -ge 1389 ] || fail "CHAIR_TIMEOUT=${CAP}s 는 관측 최장 정상 체어(463s)의 3배 여유를 못 준다"

echo "rc 캡처 (timeout 판별의 전제)"
# 행동 검증: `|| true` 형태는 PIPESTATUS 를 잃고, if/else 형태는 보존한다.
rc_via_or_true(){ ( set -euo pipefail; f(){ exit 124; }; f | cat >/dev/null || true; echo "${PIPESTATUS[0]}" ); }
rc_via_if(){ ( set -euo pipefail; f(){ exit 124; }
  if f | cat >/dev/null; then echo 0; else echo "${PIPESTATUS[0]}"; fi ); }
got_or="$(rc_via_or_true)"; got_if="$(rc_via_if)"
printf "  %-50s -> %s\n" "|| true 뒤 PIPESTATUS (잘못된 형태)" "$got_or"
printf "  %-50s -> %s\n" "if/else PIPESTATUS (현재 형태)" "$got_if"
[ "$got_or" = 0 ]   || fail "전제가 바뀌었다: '|| true' 형태가 rc 를 보존한다($got_or) — 아래 구조 검사의 근거를 재확인할 것"
[ "$got_if" = 124 ] || fail "if/else 형태가 rc 를 잃는다($got_if) — timeout 판별이 동작하지 않는다"

# 구조 검증: synthesize.sh 가 실제로 그 형태를 쓰는지. 이게 없으면 위 행동 검증은
# 스크립트 밖 사실만 확인하고 회귀(다시 `|| true` 로 되돌리는 것)를 못 잡는다.
SYN="$(dirname "$0")/synthesize.sh"
# 주석은 제외한다 — run_chair 의 주석이 잘못된 형태('|| true')를 근거로 인용하고 있어서
# 주석까지 grep 하면 구조 검사가 그 인용에 걸려 오탐한다.
awk '/^run_chair\(\)/{f=1} f{print} f&&/^}/{exit}' "$SYN" | grep -vE '^\s*#' > "$(dirname "$0")/.run_chair.tmp"
grep -q 'CHAIR_RC="\${PIPESTATUS\[0\]}"' "$(dirname "$0")/.run_chair.tmp" \
  || fail "run_chair 에 PIPESTATUS 캡처가 없다"
grep -qE '^\s*if ANTHROPIC_MODEL=' "$(dirname "$0")/.run_chair.tmp" \
  || fail "run_chair 의 chair 파이프라인이 if 로 감싸져 있지 않다 — rc 가 유실된다"
grep -q '|| true' "$(dirname "$0")/.run_chair.tmp" \
  && fail "run_chair 에 '|| true' 가 있다 — PIPESTATUS 가 리셋돼 timeout 판별이 죽는다"
rm -f "$(dirname "$0")/.run_chair.tmp"
echo "  [ok] run_chair 가 if/else 로 rc 를 보존하는 형태"

if [ "$FAILED" = 0 ]; then echo "PASS"; else echo "FAILED" >&2; exit 1; fi
