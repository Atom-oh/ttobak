#!/bin/bash
# synthesize.sh 의 의장 timeout 정책 검사 — 기대값 assert, 어긋나면 non-zero.
#   ① timeout(rc=124) 로 죽으면 fallback 을 태우지 않는다 (같은 입력 = 같은 벽, 실측 근거는
#      synthesize.sh 주석 참조: ttobak #133 / omcs #138 에서 두 모델이 각각 600s 소진)
#   ② timeout 이 아닌 실패(빈 응답·VERDICT 형식 오류)에서는 fallback 을 태운다
#   ③ 정상 응답이면 어느 쪽도 태우지 않는다
# synthesize.sh 를 그대로 source 할 수 없어(본체 실행) 분기 조건만 동일 형태로 재현한다 —
# source of truth 는 synthesize.sh 이며, 조건식을 고치면 여기도 고쳐야 한다.
set -uo pipefail
FAILED=0
fail(){ echo "  [FAIL] $1" >&2; FAILED=1; }

decide(){ # $1=rc $2=valid(0|1) $3=primary $4=fallback -> "skip"|"fallback"|"none"
  local CHAIR_RC="$1" valid="$2" PRIMARY_MODEL="$3" FALLBACK_MODEL="$4" CHAIR_TIMED_OUT=0
  [ "$CHAIR_RC" = 124 ] && CHAIR_TIMED_OUT=1
  if [ "$valid" = 1 ]; then echo none; return; fi
  if [ "$CHAIR_TIMED_OUT" = 1 ]; then echo skip; return; fi
  if [ "$FALLBACK_MODEL" != "$PRIMARY_MODEL" ]; then echo fallback; return; fi
  echo none
}

t(){ # $1=desc $2=expected $3..=args
  local desc="$1" exp="$2"; shift 2
  local got; got="$(decide "$@")"
  printf "  %-46s -> %-9s (기대 %s)\n" "$desc" "$got" "$exp"
  [ "$got" = "$exp" ] || fail "$desc: 기대 $exp, 실제 $got"
}

echo "의장 timeout 정책"
t "timeout(124), invalid — fallback 낭비 방지"        skip     124 0 fable-5 opus-5
t "즉시 실패(rc=1), invalid — fallback 시도"          fallback 1   0 fable-5 opus-5
t "빈 응답(rc=0), invalid — fallback 시도"            fallback 0   0 fable-5 opus-5
t "정상 응답 — 아무것도 안 함"                         none     0   1 fable-5 opus-5
t "PRIMARY==FALLBACK, invalid — 동일 호출 반복 방지"   none     1   0 fable-5 fable-5
t "timeout + PRIMARY==FALLBACK — skip 우선"           skip     124 0 fable-5 fable-5

# timeout 캡이 실측 소요를 커버하는지: diff 줄당 ~1초(플릿 실측) 기준
CAP="$(grep -oE 'CHAIR_TIMEOUT:-[0-9]+' "$(dirname "$0")/synthesize.sh" | head -1 | sed -E 's/.*:-//')"
echo "CHAIR_TIMEOUT=${CAP}s -> 대략 ${CAP}줄 diff 까지 커버 (플릿 실측 ~1초/줄)"
[ "${CAP:-0}" -ge 1500 ] || fail "CHAIR_TIMEOUT=${CAP}s 는 이 repo 에서 실패한 1469줄 PR(#133)을 못 덮는다"

if [ "$FAILED" = 0 ]; then echo "PASS"; else echo "FAILED" >&2; exit 1; fi
