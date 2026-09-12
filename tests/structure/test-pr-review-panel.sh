#!/usr/bin/env bash
# scripts/pr-review/run-panel.sh 의 Kiro 셀 fail-closed 계약을 핀한다(claude-code-usage-dashboard
# 저장소 PR #33 이식).
# kiro-cli 2.11.1 에서 `--trust-tools=`(빈 값)은 "무툴"이 아니라 무시되는 경고 한 줄로
# 퇴화했다(내장 툴 이름이 fs_read → read 등으로 바뀌며 cwd 안 read 가 기본 신뢰됨). 무툴은
# `tools: []` 에이전트를 `--agent` 로 지정해야만 성립하고(v2 엔진; `--v3` 는 이를 무시함),
# 월간 요청 한도(MONTHLY_REQUEST_COUNT) 소진은 rc=0+빈 stdout 으로 끝나 재시도만 태우므로
# 시그니처 감지가 있어야 원인이 코멘트/로그에 드러난다. 둘 다 조용히 되돌려지는 걸 막는다.
echo "# PR-review panel (Kiro no-tools agent / quota / agent-fallback) tests"

PANEL="scripts/pr-review/run-panel.sh"
SYNTH="scripts/pr-review/synthesize.sh"
AGENT="scripts/pr-review/agents/pr-review-notools.json"

assert_bash_syntax "run-panel.sh valid bash" "$PANEL"
assert_bash_syntax "synthesize.sh valid bash" "$SYNTH"
assert_file_exists "$AGENT" "kiro no-tools agent config present"
assert_json_valid "kiro no-tools agent config is valid JSON" "$AGENT"

AGENT_NAME=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["name"])' "$AGENT" 2>/dev/null || true)
assert_eq "agent .name is pr-review-notools" "pr-review-notools" "$AGENT_NAME"

AGENT_TOOLS=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(len(d.get("tools",["x"])), len(d.get("mcpServers",{"x":1})))' "$AGENT" 2>/dev/null || true)
assert_eq "agent declares tools: [] and no mcpServers" "0 0" "$AGENT_TOOLS"

PANEL_SRC=$(grep -v '^\s*#' "$PANEL")
assert_grep_match "run-panel.sh passes --agent \"\$KIRO_AGENT_NAME\" to kiro-cli chat" \
    'kiro-cli chat .*--agent "\$KIRO_AGENT_NAME"' "$(echo "$PANEL_SRC" | tr '\n' ' ')"
assert_grep_match "run-panel.sh copies the agent file into each cell cwd" \
    'cp "\$KIRO_AGENT_SRC" "\$CELL_CWD/\.kiro/agents/"' "$PANEL_SRC"
assert_grep_no_match "run-panel.sh no longer relies on --trust-tools= (ignored by kiro-cli 2.11.1)" \
    '\-{2}trust-tools' "$PANEL_SRC"
assert_grep_no_match "run-panel.sh does not use the --v3 engine (ignores tools: [])" \
    'kiro-cli -{2}v3|-{2}agent-engine' "$PANEL_SRC"

assert_grep_match "run-panel.sh detects the Kiro monthly quota signature (v2 stderr)" \
    'Monthly request limit reached' "$PANEL_SRC"
assert_grep_match "run-panel.sh detects the Kiro monthly quota signature (v3/JSON)" \
    'MONTHLY_REQUEST_COUNT' "$PANEL_SRC"
assert_grep_match "run-panel.sh writes kiro-quota.flag for synthesize.sh" \
    'kiro-quota\.flag' "$PANEL_SRC"
assert_grep_match "synthesize.sh renders the Kiro quota banner" \
    'kiro-quota\.flag' "$(grep -v '^\s*#' "$SYNTH")"
assert_grep_match "run-panel.sh detects the --agent fallback signature" \
    'no agent with name' "$PANEL_SRC"
assert_grep_match "synthesize.sh renders the agent-fallback banner" \
    'kiro-agent-fallback\.flag' "$(grep -v '^\s*#' "$SYNTH")"
assert_file_exists "docs/runbooks/pr-review-panel.md" "runbook for the panel failure modes exists"

# 동작 테스트: kiro-cli 스텁이 2.11.1 v2 엔진의 한도 소진 시그니처(rc=0, 빈 stdout, stderr
# 메시지)를 재현하면 재시도 없이 즉시 중단하고 quota 플래그를 남겨야 한다.
if command -v timeout >/dev/null 2>&1; then
    T_STUB=$(mktemp -d)
    wrap_kiro_stub() {
        {
            cat <<'PREFLIGHT_STUB'
#!/bin/bash
if [ "${1:-}" = "--version" ]; then
    echo "kiro-cli test"
    exit 0
fi
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    echo "NO_TOOLS"
    exit 0
fi
PREFLIGHT_STUB
            cat "$T_STUB/kiro-cli"
        } > "$T_STUB/kiro-cli.wrapped"
        mv "$T_STUB/kiro-cli.wrapped" "$T_STUB/kiro-cli"
        chmod +x "$T_STUB/kiro-cli"
    }
    cat > "$T_STUB/kiro-cli" <<'EOF'
#!/bin/bash
printf 'Monthly request limit reached\nThe limits reset on 10/01.\n' >&2
exit 0
EOF
    cat > "$T_STUB/codex" <<'EOF'
#!/bin/bash
cat > /dev/null; echo "no findings"
EOF
    chmod +x "$T_STUB/kiro-cli" "$T_STUB/codex"
    wrap_kiro_stub
    mkdir -p "$T_STUB/lenses" && echo "lens" > "$T_STUB/lenses/L2.txt"
    printf 'diff --git a/x b/x\n+x\n' > "$T_STUB/diff.txt"
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    assert_grep_no_match "quota exhaustion is not retried" '\[retry ' "$PANEL_OUT"
    assert_grep_match "quota exhaustion is reported as ::error:: with the reset date" \
        '::error::Kiro monthly request quota exhausted.*reset on 10/01' "$PANEL_OUT"
    assert_file_exists "$T_STUB/work/kiro-quota.flag" "quota exhaustion leaves kiro-quota.flag"
    assert_file_exists "$T_STUB/work/coverage-severe.flag" "quota exhaustion still forces coverage-severe (fail-closed kept)"

    # `--v3` 엔진 형태(rc=1, 메시지는 stdout, JSON 은 stderr) 도 stderr 만으로 잡혀야 한다.
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
echo "You've reached your monthly usage limit."
echo '[ERROR] [KRS] HTTP 400 body={"__type":"...ServiceQuotaExceededException","reason":"MONTHLY_REQUEST_COUNT"}' >&2
exit 1
EOF2
    wrap_kiro_stub
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    assert_grep_no_match "v3-style quota error is not retried" '\[retry ' "$PANEL_OUT"
    assert_grep_match "v3-style quota error is reported" '::error::Kiro monthly request quota exhausted' "$PANEL_OUT"
    KIRO_SLOT_BYTES=$(cat "$T_STUB"/work/slot/kiro-*.md 2>/dev/null | wc -c | tr -d ' ')
    assert_eq "v3-style quota stdout message is not counted as a response" "0" "$KIRO_SLOT_BYTES"

    # 에이전트 폴백: kiro-cli 2.11.1 은 --agent 를 못 찾으면 stderr 한 줄 + rc=0 으로 툴 있는
    # 기본 에이전트를 계속 실행한다. 응답이 있어도 폐기되고 severe 로 승격돼야 한다.
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
echo "Error: no agent with name pr-review-notools found. Falling back to user specified default" >&2
echo "> no findings"
exit 0
EOF2
    wrap_kiro_stub
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    assert_grep_match "agent fallback is reported as ::error::" '::error::kiro-cli ignored --agent pr-review-notools' "$PANEL_OUT"
    assert_grep_no_match "agent-fallback responses are not counted" 'Panel responded.*kiro-' "$PANEL_OUT"
    assert_file_exists "$T_STUB/work/kiro-agent-fallback.flag" "agent fallback leaves kiro-agent-fallback.flag"
    assert_file_exists "$T_STUB/work/coverage-severe.flag" "agent fallback forces coverage-severe"

    # 정상 응답 경로: 아무 플래그도 남지 않아야 한다(감지 로직의 오탐 가드).
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
echo "> no findings"
EOF2
    wrap_kiro_stub
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    assert_grep_match "healthy kiro cells are counted" 'Panel responded \(3 / 3 cells\)' "$PANEL_OUT"
    # run-all.sh 는 set -euo pipefail 로 source 하므로 매치 없는 ls 가 스위트를 죽인다 — find 로 센다.
    HEALTHY_FLAGS=$(find "$T_STUB/work" -maxdepth 1 -name '*.flag' | wc -l | tr -d ' ')
    assert_eq "healthy run leaves no flags" "0" "$HEALTHY_FLAGS"

    # Codex는 입력 diff를 stderr에도 출력한다. Kiro 오류 문자열을 인용하는 정상 리뷰가
    # Kiro 에이전트 폴백으로 폐기되면 안 된다(claude-code-usage-dashboard 저장소 PR #33의 실제 Codex 4셀 로그에서 재현).
    cat > "$T_STUB/codex" <<'EOF2'
#!/bin/bash
cat >&2
echo "no findings"
EOF2
    printf 'diff --git a/x b/x\n+Monthly request limit reached\n+no agent with name pr-review-notools found\n' > "$T_STUB/diff.txt"
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    assert_grep_match "Codex quoting Kiro errors remains a successful response" \
        'Panel responded \(3 / 3 cells\)' "$PANEL_OUT"
    QUOTED_FLAGS=$(find "$T_STUB/work" -maxdepth 1 -name '*.flag' | wc -l | tr -d ' ')
    assert_eq "quoted Kiro errors in Codex stderr leave no flags" "0" "$QUOTED_FLAGS"

    cat > "$T_STUB/codex" <<'EOF2'
#!/bin/bash
cat >/dev/null
printf 'attempt\n' >> "$0.attempts"
if [ "$(wc -l < "$0.attempts")" -eq 1 ]; then
    echo "Reviewed code quotes: Monthly request limit reached" >&2
    exit 1
fi
echo "no findings"
EOF2
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    CODEX_ATTEMPTS=$(wc -l < "$T_STUB/codex.attempts" | tr -d ' ')
    assert_eq "Codex retries its own transient failure despite a quoted Kiro quota" "2" "$CODEX_ATTEMPTS"
    assert_grep_match "Codex retry can restore full coverage" 'Panel responded \(3 / 3 cells\)' "$PANEL_OUT"
    RETRY_FLAGS=$(find "$T_STUB/work" -maxdepth 1 -name '*.flag' | wc -l | tr -d ' ')
    assert_eq "a recovered Codex retry leaves no Kiro failure flags" "0" "$RETRY_FLAGS"

    cat > "$T_STUB/codex" <<'EOF2'
#!/bin/bash
cat >/dev/null
echo "no findings"
EOF2
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
[ "${1:-}" = "--version" ] && { echo "kiro-cli test"; exit 0; }
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    printf 'preflight\n' >> "$0.events"
    cat >> "$0.preflight-input"
    [[ "$2" == *PR_DIFF_MARKER* ]] && touch "$0.diff-in-preflight"
    echo "> NO_TOOLS"
else
    printf 'review\n' >> "$0.events"
    echo "no findings"
fi
EOF2
    printf 'diff --git a/x b/x\n+PR_DIFF_MARKER\n' > "$T_STUB/diff.txt"
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    PREFLIGHT_ORDER=$(head -2 "$T_STUB/kiro-cli.events" | tr '\n' ' ')
    assert_eq "both model preflights finish before any Kiro review" "preflight preflight " "$PREFLIGHT_ORDER"
    PREFLIGHT_INPUT=$(cat "$T_STUB/kiro-cli.preflight-input" 2>/dev/null || echo "missing")
    assert_eq "preflight stdin contains no PR diff" "" "$PREFLIGHT_INPUT"
    PREFLIGHT_LEAKS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.diff-in-preflight' | wc -l | tr -d ' ')
    assert_eq "preflight prompt contains no PR diff" "0" "$PREFLIGHT_LEAKS"
    assert_grep_match "successful preflight preserves full coverage" 'Panel responded \(3 / 3 cells\)' "$PANEL_OUT"

    # A fallback may still print NO_TOOLS. It must be rejected before any PR input is sent.
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
[ "${1:-}" = "--version" ] && { echo "kiro-cli test"; exit 0; }
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    echo "Error: no agent with name pr-review-notools found. Falling back to user specified default" >&2
    echo "NO_TOOLS"
else
    touch "$0.review-started"
    echo "no findings"
fi
EOF2
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    PREFLIGHT_REVIEWS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.review-started' | wc -l | tr -d ' ')
    assert_eq "fallback during preflight prevents all Kiro reviews" "0" "$PREFLIGHT_REVIEWS"
    assert_file_exists "$T_STUB/work/kiro-preflight.flag" "failed preflight leaves a diagnostic flag"
    assert_file_exists "$T_STUB/work/coverage-severe.flag" "failed preflight forces coverage failure"
    rm -f "$T_STUB/kiro-cli.review-started"

    # The first model passing must not release its reviews before the other model is checked.
    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
[ "${1:-}" = "--version" ] && { echo "kiro-cli test"; exit 0; }
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    if [[ "$*" == *gpt-5.6-terra* ]]; then
        cat preflight-canary.txt
    else
        echo "NO_TOOLS"
    fi
else
    touch "$0.review-started"
    echo "no findings"
fi
EOF2
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    PREFLIGHT_REVIEWS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.review-started' | wc -l | tr -d ' ')
    assert_eq "a model reading the canary prevents every Kiro review" "0" "$PREFLIGHT_REVIEWS"
    assert_grep_match "Codex still reviews when Kiro preflight fails" 'Panel responded \(1 / 3 cells\)' "$PANEL_OUT"
    rm -f "$T_STUB/kiro-cli.review-started"

    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
[ "${1:-}" = "--version" ] && { echo "kiro-cli test"; exit 0; }
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    echo "NO_TOOLS"
    exit 1
fi
touch "$0.review-started"
echo "no findings"
EOF2
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    PREFLIGHT_REVIEWS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.review-started' | wc -l | tr -d ' ')
    assert_eq "NO_TOOLS with a failed command cannot release PR input" "0" "$PREFLIGHT_REVIEWS"

    cat > "$T_STUB/kiro-cli" <<'EOF2'
#!/bin/bash
[ "${1:-}" = "--version" ] && { echo "kiro-cli test"; exit 0; }
touch "$0.chat-invoked"
if [[ "${2:-}" == 'Kiro startup safety check.'* ]]; then
    echo "NO_TOOLS"
else
    echo "no findings"
fi
EOF2
    PANEL_OUT=$(PATH="$T_STUB:$PATH" PANEL_TIMEOUT=30 PANEL_RETRIES=3 \
        bash "$PANEL" "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1 || true)
    RECOVERED_FLAGS=$(find "$T_STUB/work" -maxdepth 1 -name '*.flag' | wc -l | tr -d ' ')
    assert_eq "a later healthy run clears the failed preflight flags" "0" "$RECOVERED_FLAGS"
    rm -f "$T_STUB/kiro-cli.chat-invoked"

    mkdir -p "$T_STUB/fixture/agents"
    cp "$PANEL" "$T_STUB/fixture/run-panel.sh"
    cp scripts/pr-review/lib.sh "$T_STUB/fixture/lib.sh"
    cat > "$T_STUB/fixture/agents/pr-review-notools.json" <<'EOF2'
{"name":"pr-review-notools","tools":[],"tools":["read"],"allowedTools":[],"mcpServers":{},"resources":[],"useLegacyMcpJson":false}
EOF2
    PANEL_RC=0
    PANEL_OUT=$(PATH="$T_STUB:$PATH" bash "$T_STUB/fixture/run-panel.sh" \
        "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1) || PANEL_RC=$?
    assert_eq "duplicate JSON keys are rejected before startup" "1" "$PANEL_RC"
    CHAT_CALLS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.chat-invoked' | wc -l | tr -d ' ')
    assert_eq "invalid agent configuration never reaches a model" "0" "$CHAT_CALLS"

    cat > "$T_STUB/cp" <<'EOF2'
#!/bin/bash
exit 1
EOF2
    chmod +x "$T_STUB/cp"
    PANEL_RC=0
    PANEL_OUT=$(PATH="$T_STUB:$PATH" bash "$PANEL" \
        "$T_STUB/diff.txt" "$T_STUB/lenses" "$T_STUB/work" 2>&1) || PANEL_RC=$?
    assert_eq "failed agent copy aborts before startup" "1" "$PANEL_RC"
    CHAT_CALLS=$(find "$T_STUB" -maxdepth 1 -name 'kiro-cli.chat-invoked' | wc -l | tr -d ' ')
    assert_eq "failed agent copy never reaches a model" "0" "$CHAT_CALLS"
    rm -rf "$T_STUB"
else
    skip "run-panel.sh quota stub behaviour" "timeout(1) not available"
fi
