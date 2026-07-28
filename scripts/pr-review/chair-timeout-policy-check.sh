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
  local CHAIR_FAST_FAIL_S="${CHAIR_FAST_FAIL_S:-120}" CHAIR_TIMED_OUT=0 CHAIR_SLOW_FAIL=0
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

echo "의장 fallback 정책 (빠른 실패에만 fallback)"
#                                                  기대       rc  valid elapsed primary  fallback
t "모델 불가용류 즉시 실패(3s)"                     fallback   1   0     3       fable-5 opus-5
t "인증 실패(30s)"                                  fallback   1   0     30      fable-5 opus-5
t "느린 실패(300s) — 예산 문제"                      skip       1   0     300     fable-5 opus-5
t "timeout kill(rc=124, 1500s)"                     skip       124 0     1500    fable-5 opus-5
t "경계값 정확히 120s — 느린 실패로 취급"            skip       1   0     120     fable-5 opus-5
t "경계값 119s — 빠른 실패"                          fallback   1   0     119     fable-5 opus-5
t "정상 응답 — 아무것도 안 함"                       none       0   1     240     fable-5 opus-5
t "PRIMARY==FALLBACK, 빠른 실패 — 동일 호출 방지"    none       1   0     5       fable-5 fable-5

CAP="$(grep -oE 'CHAIR_TIMEOUT:-[0-9]+' "$(dirname "$0")/synthesize.sh" | head -1 | sed -E 's/.*:-//')"
FF="$(grep -oE 'CHAIR_FAST_FAIL_S:-[0-9]+' "$(dirname "$0")/synthesize.sh" | head -1 | sed -E 's/.*:-//')"
[ -n "$CAP" ] || { echo "synthesize.sh 에서 CHAIR_TIMEOUT 기본값을 못 찾았다" >&2; exit 2; }
[ -n "$FF" ]  || { echo "synthesize.sh 에서 CHAIR_FAST_FAIL_S 기본값을 못 찾았다" >&2; exit 2; }
echo "기본값: CHAIR_TIMEOUT=${CAP}s, CHAIR_FAST_FAIL_S=${FF}s"
# 빠른 실패선이 관측된 정상 체어의 하한(cc-on-bedrock 1m42s=102s... 실제 최단 102s)보다
# 커지면 정상 실행 직후의 실패까지 "느린 실패"로 오분류될 수 있다 — 넉넉히 아래에 둔다.
[ "$FF" -le 300 ] || fail "CHAIR_FAST_FAIL_S=${FF}s 는 너무 커서 정상 범위 실패를 예산 문제로 오분류한다"
# 체어 소요는 diff 줄수가 아니라 생성 리뷰 분량에 따르고(실측 17~42 B/s), 관측된 최장
# 정상 체어는 mra 의 7m43s(463s)다 — 캡은 그 3배 여유를 유지해야 정상 실행이 잘리지 않는다.
echo "CHAIR_TIMEOUT=${CAP}s (관측 최장 정상 체어 463s 의 $(( CAP * 10 / 463 ))/10 배)"
[ "${CAP:-0}" -ge 1389 ] || fail "CHAIR_TIMEOUT=${CAP}s 는 관측 최장 정상 체어(463s)의 3배 여유를 못 준다"

if [ "$FAILED" = 0 ]; then echo "PASS"; else echo "FAILED" >&2; exit 1; fi
