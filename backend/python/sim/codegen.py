"""Prompt building and output parsing for the cost/sizing simulator's
generated Python (ADR-033).

Injection boundary: the ONLY inputs to build_codegen_prompt are the
server-validated requirements/options (already passed through Go's
validateSimRequirements) and the server-fetched price snapshot. The raw
meeting transcript is never available to this Lambda at all -- extraction
happens in the Go api Lambda, and only its validated JSON output crosses
the wire to here. This module has no code path that could reach a
transcript even by mistake.
"""
import json
import re

MAX_EXECUTE_ATTEMPTS = 3  # initial + 2 repairs

# Defense-in-depth, not the trust boundary (that's the empty Code
# Interpreter execution role + SANDBOX network mode -- see ADR-033). This
# scan exists for log signal and an extra speed bump, and must never be
# treated as a reason to loosen the sandbox itself.
BANNED_IMPORT_RE = re.compile(
    r"^\s*(import\s+(boto3|socket|subprocess|requests|urllib)\b"
    r"|from\s+(boto3|socket|subprocess|requests|urllib)\b"
    r"|os\.system\s*\()",
    re.MULTILINE,
)

SYSTEM_PROMPT = """당신은 AWS 아키텍처 비용/사이징 비교를 위한 파이썬 코드를 작성합니다.

반드시 지켜야 할 계약:
- inputs/requirements.json 과 inputs/prices.json 만 읽으세요. 단가를 절대 하드코딩하지 마세요.
- outputs/report.md 와 outputs/chart_1.png ~ outputs/chart_N.png (N<=3) 를 작성하세요.
- 차트는 report.md 안에 ![설명](sim://chart_N) 형식으로 삽입하세요.
- matplotlib의 축/범례/제목 등 모든 텍스트는 영어로 작성하세요 (샌드박스에 한글 폰트가 없어 한글은 깨진 사각형으로 표시됩니다). report.md 본문의 설명은 한국어로 작성하세요.
- matplotlib.use("Agg") 를 사용하고 plt.show() 는 호출하지 마세요.
- 네트워크나 AWS API를 호출하지 마세요 (샌드박스에는 접근 권한이 없습니다). 순수 계산만 수행하세요.
- report.md 최상단에 입력 요구사항 표, 단가 스냅샷 시각(priceSnapshotAt), 그리고 "추정치 -- 고객 제시 전 검증 필요" 배너를 포함하세요.
- 오류 없이 종료 코드 0으로 끝나야 합니다.
"""


def build_codegen_prompt(requirements, options, prices):
    """Returns (system_prompt, user_prompt). requirements/options are the
    server-validated lists (list of dicts); prices is the snapshot dict from
    pricing.fetch_unit_prices."""
    user_prompt = (
        "다음 요구사항과 아키텍처 대안을 비교하는 파이썬 코드를 작성하세요.\n\n"
        f"요구사항 (JSON, inputs/requirements.json 과 동일):\n{json.dumps(requirements, ensure_ascii=False)}\n\n"
        f"비교할 아키텍처 대안 (JSON, inputs/requirements.json 과 별개로 inputs/options.json):\n"
        f"{json.dumps(options, ensure_ascii=False)}\n\n"
        f"단가 스냅샷 시각: {prices.get('retrievedAt', 'unknown')}\n"
        "단가는 inputs/prices.json 에서 읽으세요 (여기 프롬프트에 있는 값은 참고용 요약일 뿐입니다)."
    )
    return SYSTEM_PROMPT, user_prompt


def build_repair_prompt(stderr, missing_files):
    """The feedback loop for a failed executeCode round. Deliberately
    carries only the failure signal -- never re-includes the transcript
    (which was never available here anyway) or restates the full original
    prompt, keeping each repair round small."""
    parts = ["이전 코드 실행이 실패했습니다. 같은 계약(outputs/report.md, outputs/chart_N.png)을 지키며 코드를 수정하세요."]
    if stderr:
        parts.append(f"stderr (마지막 2000자):\n{stderr[-2000:]}")
    if missing_files:
        parts.append(f"누락된 출력 파일: {', '.join(missing_files)}")
    return "\n\n".join(parts)


def extract_code_from_response(text):
    """Pulls a single fenced ```python ... ``` (or bare ```) block out of a
    Bedrock text response. Raises ValueError if no fence is found or a
    banned import/call is present (see BANNED_IMPORT_RE's doc comment on
    what this scan is and isn't)."""
    if not text or not text.strip():
        raise ValueError("empty codegen response")

    match = re.search(r"```(?:python)?\n(.*?)```", text, re.DOTALL)
    if not match:
        raise ValueError("no fenced code block found in codegen response")
    code = match.group(1)

    if BANNED_IMPORT_RE.search(code):
        raise ValueError("generated code contains a banned import or call")

    return code


REQUIRED_OUTPUT_FILES = ("outputs/report.md",)


def classify_run_result(exit_code, listed_output_paths):
    """Decides whether an executeCode round succeeded, should be retried, or
    is a hard failure -- based on structural evidence (exit code + a real
    listFiles call), never on trusting a model-printed manifest.

    Returns "success" or ("retry", missing_files:list[str])."""
    missing = [p for p in REQUIRED_OUTPUT_FILES if p not in listed_output_paths]
    if exit_code != 0 or missing:
        return "retry", missing
    return "success", []
