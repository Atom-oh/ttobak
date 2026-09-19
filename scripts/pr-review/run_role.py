#!/usr/bin/env python3
"""Execute one prepared specialist review without changing its scope."""

from __future__ import annotations

import argparse
from collections import namedtuple
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time

from role_review import diagnostic_failure, issue_request, issued_request, load_plan, digest, Invalid, MAX_REQUEST_BYTES, MAX_OUTPUT_BYTES, output_bytes, text_file, strict_json, canonical, parse_response


DIRECTORY = Path(__file__).resolve().parent
MAX_CALL_TIMEOUT = 900
FAILURE = re.compile(
    r"Monthly request limit reached|MONTHLY_REQUEST_COUNT|UsageLimitReachedError|"
    r"ServiceQuotaExceededException|You have reached the limit for overages|"
    r"no agent with name|Falling back to user specified default|"
    r"Json supplied at .* is invalid|failed to set model|using tool:",
    re.IGNORECASE,
)
ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")
AGENT = {
    "name": "inline-review",
    "description": "Review inline data without tools, hooks or external resources.",
    "tools": [], "allowedTools": [], "mcpServers": {}, "resources": [],
    "hooks": {}, "useLegacyMcpJson": False,
}


def execute(command, cwd, environment, input_text, timeout):
    """Keep exit status and kill the whole process group on timeout."""
    try:
        process = subprocess.Popen(
            command, cwd=cwd, env=environment, stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            encoding="utf-8", errors="replace", start_new_session=True,
        )
    except OSError:
        return 127, "", "Review CLI unavailable."
    try:
        output, error = process.communicate(input_text, timeout=timeout)
        code = process.returncode
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        output, error = process.communicate()
        code, error = 124, error + "\nReview CLI timed out."
    try:
        output_bytes(output)
        output_bytes(error)
    except Invalid:
        return code or 1, "", "output_byte_limit"
    return code, output, error


def kiro_environment(cwd, source):
    environment = {
        key: source[key] for key in (
            "PATH", "LANG", "LC_ALL", "TMPDIR", "KIRO_API_KEY", "RUNNER_TRACKING_ID"
        )
        if key in source
    }
    environment["HOME"] = str(cwd)
    return environment


def install_agent(cwd):
    location = cwd / ".kiro" / "agents"
    location.mkdir(parents=True, exist_ok=True)
    (location / "inline-review.json").write_text(json.dumps(AGENT) + "\n")


def preflight(binary, model, cwd, environment, timeout):
    # Keep model selection and the empty-agent guard on the validated Kiro v1 path.
    install_agent(cwd)
    (cwd / "preflight-canary.txt").write_text(secrets.token_hex(24) + "\n")
    prompt = (
        "Kiro startup safety check. Read ./preflight-canary.txt using a file-reading "
        "tool and return its exact contents. If no file-reading tools are available, "
        "reply with exactly NO_TOOLS. Do not run any other tools."
    )
    code, output, error = execute(
        [binary, "chat", prompt, "--model", model, "--agent", "inline-review",
         "--no-interactive", "--wrap", "never", "--legacy-ui", "--agent-engine", "v1"],
        cwd, kiro_environment(cwd, environment), "", timeout,
    )
    reply = re.sub(r"(?m)^\s*> ?", "", ANSI.sub("", output)).strip()
    return code == 0 and reply == "NO_TOOLS" and not FAILURE.search(error), code, error


KiroStartup = namedtuple("KiroStartup", "plan_digest agent_digest binary checks")


def kiro_models(plan):
    return tuple((tag, role["model"]) for tag, role in sorted(plan["roles"].items())
                 if tag.startswith("kiro-") and role["required"])


def prepare_kiro_startup(work):
    plan = load_plan(work)
    binary = shutil.which("kiro-cli") or "kiro-cli"
    agent_digest = digest(AGENT)
    checks = []
    if plan["input_complete"] and kiro_models(plan):
        runtime = work / "runtime"
        runtime.mkdir(parents=True, exist_ok=True)
        timeout = bounded_setting("KIRO_PREFLIGHT_TIMEOUT", 60, 120)
        for tag, model in kiro_models(plan):
            with tempfile.TemporaryDirectory(prefix=f"{tag}-probe-", dir=runtime) as temporary:
                ok, code, error = preflight(binary, model, Path(temporary), dict(os.environ), timeout)
            checks.append((tag, model, bool(ok and code == 0 and not diagnostic_failure(error)), code, error))
    return KiroStartup(plan["plan_digest"], agent_digest, binary, tuple(checks))


def verify_kiro_startup(startup, work, plan, tag, nonce):
    # Only the trusted parent passes this in-memory object; no environment/file bypass.
    if (not isinstance(startup, KiroStartup)
            or startup.plan_digest != plan["plan_digest"]
            or startup.agent_digest != digest(AGENT)
            or tuple((row[0], row[1]) for row in startup.checks) != kiro_models(plan)
            or tag not in dict(kiro_models(plan))):
        raise Invalid("invalid_kiro_startup")
    current = load_plan(work)
    if current["plan_digest"] != startup.plan_digest:
        raise Invalid("stale_kiro_startup")
    receipt = issued_request(work, current, tag)
    if receipt["invocation_nonce"] != nonce:
        raise Invalid("stale_kiro_startup")


def bounded_setting(name, default, maximum):
    value = int(os.environ.get(name, default))
    if not 0 < value <= maximum:
        raise ValueError(f"{name} must be between 1 and {maximum}")
    return value


def scrub(text):
    """Reuse the repository's control and credential scrubbers before publication."""
    output_bytes(text)
    process = subprocess.run(
        ["bash", "-c", 'source "$1"; source "$2"; strip_ansi | scrub_secrets',
         "review-scrub", str(DIRECTORY / "lib.sh"), str(DIRECTORY / "role-controls.sh")],
        input=text, text=True, capture_output=True,
    )
    if process.returncode:
        raise RuntimeError("Review output scrubber failed")
    return process.stdout


def malformed_kiro_json(text):
    """Inspect syntax without treating successfully parsed review data as logs."""
    try:
        parse_response(scrub(text))
    except Invalid as error:
        return str(error) in ("malformed_json", "invalid_json_wrapper")
    return False


def run(work, tag, kiro_startup=None):
    plan = load_plan(work)
    role = plan["roles"][tag]
    if not plan["input_complete"]:
        print(f"{tag}: input incomplete; no provider call")
        return
    if not role["required"]:
        print(f"{tag}: NOT_APPLICABLE — {role['reason']}")
        return
    slot = work / "slot"
    slot.mkdir(exist_ok=True)
    runtime = work / "runtime"
    runtime.mkdir(exist_ok=True)
    attempts = bounded_setting("PANEL_RETRIES", 2, 3)
    timeout = bounded_setting("PANEL_TIMEOUT", 300, MAX_CALL_TIMEOUT)
    prompt = (work / "roles" / f"{tag}.txt").read_bytes().decode("utf-8")
    diff = (work / "roles" / f"{tag}.diff").read_bytes().decode("utf-8")
    start = time.monotonic()
    environment = dict(os.environ)
    # GitHub writes belong to the publisher. No review process needs this token.
    for name in ("GH_TOKEN", "GITHUB_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN"):
        environment.pop(name, None)
    output = ""
    error = ""
    code = 1
    nonce, framed_prompt, payload = issue_request(work, tag)
    with tempfile.TemporaryDirectory(prefix=f"{tag}-", dir=runtime) as temporary:
        cwd = Path(temporary)
        if tag.startswith("kiro-"):
            startup = kiro_startup if kiro_startup is not None else prepare_kiro_startup(work)
            verify_kiro_startup(startup, work, plan, tag, nonce)
            failed = [row for row in startup.checks if not row[2]]
            if failed:
                _, _, _, code, error = failed[0]
                error = error or "Kiro shared startup barrier failed."
                (slot / f"kiro-preflight-{tag}.flag").write_text(
                    "Kiro startup safety check failed; PR input withheld.\n"
                )
                code = code or 1
            else:
                install_agent(cwd)
                instruction = framed_prompt + "\n" + payload
                if len(instruction.encode()) >= MAX_REQUEST_BYTES:
                    code, error = 1, "Complete Kiro input exceeds argument limit."
                else:
                    command = [
                        startup.binary, "chat", instruction, "--model", role["model"],
                        "--agent", "inline-review", "--no-interactive", "--wrap", "never",
                        "--legacy-ui", "--agent-engine", "v1",
                    ]
                    for _ in range(attempts):
                        nonce, framed_prompt, payload = issue_request(work, tag)
                        verify_kiro_startup(startup, work, plan, tag, nonce)
                        command[2] = framed_prompt + "\n" + payload
                        code, output, error = execute(
                            command, cwd, kiro_environment(cwd, environment), "", timeout
                        )
                        if FAILURE.search(error) or diagnostic_failure(error):
                            code = code or 1
                            break
                        if code == 0 and output.strip():
                            if not malformed_kiro_json(output):
                                break
                            if FAILURE.search(output) or diagnostic_failure(output):
                                code = 1
                                error += ("\n" if error else "") + output
                                break
        else:
            environment.pop("KIRO_API_KEY", None)
            if tag == "codex":
                command = [
                    "codex", "exec", "--model", role["model"],
                    "-s", "read-only", "--skip-git-repo-check", "--json",
                    "--output-last-message", "", prompt,
                ]
                # Keep the trusted base checkout and its configured Bedrock provider.
                cwd = Path.cwd()
            elif tag == "claude-self":
                command = [
                    "claude", "-p", prompt, "--model", role["model"],
                    "--output-format", "json", "--json-schema", canonical(claude_schema()),
                    "--strict-mcp-config", "--tools", "",
                ]
            else:
                raise ValueError("Unknown specialist")
            # Claude's structured response may need more than one nominal slice.
            # Share the existing total budget; retries cannot reset its deadline.
            deadline = time.monotonic() + attempts * timeout if tag == "claude-self" else None
            for _ in range(attempts):
                nonce, framed_prompt, payload = issue_request(work, tag)
                if tag == "codex":
                    # The role's mode-0700 temporary directory is independent
                    # of the trusted base cwd. Each attempt gets a fresh file.
                    final_output = Path(temporary) / f"codex-final-{nonce}.txt"
                    final_output.unlink(missing_ok=True)
                    command[-2] = str(final_output)
                    command[-1] = "-"
                    delivered = framed_prompt + "\n" + payload
                else:
                    command[2] = framed_prompt
                    delivered = payload
                    schema = command[command.index("--json-schema") + 1]
                    if len((framed_prompt + payload + schema).encode()) >= MAX_REQUEST_BYTES:
                        code, output, error = 1, "", "Complete Claude request exceeds input limit."
                        break
                call_timeout = timeout
                if deadline is not None:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        code, output, error = 124, "", "Review CLI budget exhausted."
                        break
                    call_timeout = min(MAX_CALL_TIMEOUT, remaining)
                code, output, error = execute(command, cwd, environment, delivered, call_timeout)
                if diagnostic_failure(error):
                    code = code or 1
                    break
                if tag == "codex":
                    output, event_error, complete = codex_response(output, final_output)
                    if diagnostic_failure(event_error) == "output_byte_limit":
                        code, output, error = code or 1, "", "output_byte_limit"
                        break
                    if event_error:
                        error = error + ("\n" if error else "") + event_error
                    if not complete:
                        code = code or 1
                    try:
                        output_bytes(output)
                        output_bytes(error)
                    except Invalid:
                        code, output, error = code or 1, "", "output_byte_limit"
                else:
                    review, envelope_error, complete = claude_response(output, role["model"], code)
                    if envelope_error == "output_byte_limit":
                        code, output, error = code or 1, "", "output_byte_limit"
                        break
                    if diagnostic_failure(envelope_error) or (code == 0 and not complete):
                        code, output = code or 1, ""
                        error += ("\n" if error else "") + envelope_error
                        try:
                            output_bytes(error)
                        except Invalid:
                            error = "output_byte_limit"
                        # Terminal native diagnostics block even after nonzero exit.
                        break
                    if code == 0:
                        output = review
                if diagnostic_failure(error):
                    code = code or 1
                    break
                if code == 0 and output.strip():
                    break
    # Only scrubbed artifacts enter slot/. Runtime raw output is never uploaded.
    output_path = runtime / f"{tag}.txt"
    error_path = runtime / f"{tag}.err"
    output_path.write_text(scrub(output))
    error_path.write_text(scrub(error))
    result = subprocess.run([
        sys.executable, str(DIRECTORY / "role_review.py"), "record",
        "--work", str(work), "--tag", tag, "--output", str(output_path),
        "--stderr", str(error_path), "--exit-code", str(code),
        "--nonce", nonce,
    ])
    if result.returncode not in (0, 2):
        raise RuntimeError("Specialist result recording failed")
    (slot / f"{tag}-timing.json").write_text(json.dumps({
        "tag": tag, "elapsed_seconds": round(time.monotonic() - start, 3),
        "exit_code": code, "configured_model": role["model"],
    }, sort_keys=True) + "\n")
    print(f"{tag}: finished in {time.monotonic() - start:.1f}s (exit {code})")


def run_all(work):
    plan = load_plan(work)
    with ThreadPoolExecutor(max_workers=4) as pool:
        pending = {pool.submit(run, work, tag): tag for tag, role in plan["roles"].items()
                   if role["required"] and not tag.startswith("kiro-")}
        try:
            startup = prepare_kiro_startup(work)
        except Exception:
            (work / "slot" / "kiro-preflight.flag").write_text("Kiro startup did not complete.\n")
        else:
            for tag, _ in kiro_models(plan):
                pending[pool.submit(run, work, tag, startup)] = tag
        for future, tag in pending.items():
            try:
                future.result()
            except Exception:
                (work / "slot" / f"{tag}-execution.flag").write_text("Role execution failed.\n")
                print(f"{tag}: execution failed; required coverage blocked")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", required=True, type=Path)
    target = parser.add_mutually_exclusive_group(required=True)
    target.add_argument("--tag")
    target.add_argument("--all", action="store_true")
    arguments = parser.parse_args()
    if arguments.all:
        run_all(arguments.work.resolve())
    else:
        run(arguments.work.resolve(), arguments.tag)



def claude_schema():
    """Request structure; record still verifies exact identity and full coverage."""
    text = {"type": "string", "minLength": 1}
    prose = {
        **text,
        "pattern": "^[^`]*$",
        "description": (
            "Use English prose and unquoted symbol/path references, without backticks. "
            "Describe assignments and calls in prose instead of inline code snippets. "
            "If an example is necessary, use a closed top-level tilde fence with "
            "delimiters on their own lines. Existing review validation still applies."
        ),
    }

    def object_schema(properties):
        return {"type": "object", "properties": properties,
                "required": list(properties), "additionalProperties": False}

    return object_schema({
        "head_sha": text, "role": text, "scope_complete": {"type": "boolean"},
        "reviewed_paths": {"type": "array", "items": text},
        "checks": {"type": "array", "items": object_schema({"path": text, "evidence": prose})},
        "findings": {"type": "array", "items": object_schema({
            "severity": {"type": "string", "enum": ["CRITICAL", "MAJOR", "MINOR", "INFO"]},
            "path": text, "condition": prose, "evidence": prose,
        })},
        "uncertainties": {"type": "array", "items": prose},
    })


def claude_response(raw, expected_model=None, exit_code=0):
    """Only a successful CLI structured_output is eligible for review validation."""
    try:
        output_bytes(raw)
        envelope = strict_json(raw)
        if not isinstance(envelope, dict) or envelope.get("type") != "result":
            return "", "Claude structured-output envelope is missing or unsuccessful.", False
        usage = envelope.get("modelUsage")
        # Preserve reported mismatches before any generic failure can permit retry.
        # Profile/model aliases are reported metadata, not proof of provider weights.
        if expected_model and "modelUsage" in envelope:
            if not isinstance(usage, dict):
                return "", "Error: INVALID_MODEL_ID", False
            names = {expected_model, expected_model.removeprefix("global.anthropic.")}
            if usage and not names.intersection(usage):
                return "", "Error: INVALID_MODEL_ID", False
        # These are outer CLI diagnostics; never scan the review's evidence as logs.
        messages = []
        invalid_metadata = False
        fields = ["errors", "warnings"]
        if (exit_code != 0 or envelope.get("is_error") is not False
                or envelope.get("subtype") != "success"):
            fields.append("result")
        for field in fields:
            value = envelope.get(field, [])
            if isinstance(value, str):
                messages.append(value)
            elif isinstance(value, list):
                messages.extend(item for item in value if isinstance(item, str))
                invalid_metadata |= any(not isinstance(item, str) for item in value)
            else:
                invalid_metadata = True
        failures = {
            "model_selection_diagnostic": "Error: INVALID_MODEL_ID",
            "model_fallback_diagnostic": "Falling back to another model",
            "quota_diagnostic": "quota exceeded",
            "agent_preflight_diagnostic": "no agent with name inline-review found",
            "output_byte_limit": "output_byte_limit",
        }
        for message in messages:
            failure = diagnostic_failure(message)
            if failure:
                return "", failures[failure], False
        if invalid_metadata:
            return "", "Claude structured-output diagnostic metadata is invalid.", False
        if envelope.get("errors"):
            return "", "Claude structured-output envelope reports errors.", False
        if (envelope.get("subtype") != "success"
                or envelope.get("is_error") is not False
                or not isinstance(envelope.get("structured_output"), dict)):
            return "", "Claude structured-output envelope is missing or unsuccessful.", False
        if expected_model and "modelUsage" in envelope:
            if not usage:
                return "", "Error: INVALID_MODEL_ID", False
        output = canonical(envelope["structured_output"])
        # Keep controls escaped until sanitization operates on individual strings.
        controls = [*range(0x20), *range(0x7F, 0xA0), 0x2028, 0x2029]
        output = output.translate({code: f"\\u{code:04x}" for code in controls})
        output_bytes(output)
    except Invalid as exc:
        if str(exc) == "output_byte_limit":
            return "", "output_byte_limit", False
        return "", "Claude structured-output envelope is invalid JSON.", False
    except (UnicodeError, RecursionError):
        return "", "Claude structured-output envelope is invalid JSON.", False
    return output, "", True


def codex_response(raw, final_path):
    """Validate all events, then read the CLI-designated final reply unchanged."""
    overflow, final_error, output = False, None, ""
    try:
        output_bytes(raw)
    except Invalid:
        overflow = True
    try:
        if final_path.is_symlink() or not final_path.is_file():
            raise OSError()
        output = text_file(final_path, MAX_OUTPUT_BYTES)
    except Invalid as exc:
        overflow = overflow or str(exc) == "output_byte_limit"
        final_error = str(exc)
    except OSError:
        final_error = "Codex final reply file is missing or invalid."
    if overflow:
        return "", "output_byte_limit", False
    started = completed = failed = False
    has_message = False
    diagnostics = []
    def diagnostic(text):
        line = " ".join(text.splitlines())
        if line.lower().startswith("model rerouted:"):
            line = "Falling back to another model: " + line
        diagnostics.append(line)

    lines = raw.split("\n")
    if lines[-1] == "":
        lines.pop()
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            failed = True
            continue
        if not isinstance(event, dict) or not isinstance(event.get("type"), str):
            failed = True
            continue
        kind = event["type"]
        if completed:
            failed = True
        if kind in ("error", "turn.failed"):
            # Native "error" includes in-turn reconnect notices. A completed
            # turn may recover; the caller still rejects terminal diagnostics.
            if kind == "turn.failed":
                failed = True
            error = event.get("error", event)
            text = error.get("message") if isinstance(error, dict) else None
            if isinstance(text, str):
                diagnostic(text)
            else:
                failed = True
            continue
        if kind == "turn.started":
            if started:
                failed = True
            started = True
        elif kind == "turn.completed":
            if not started:
                failed = True
            completed = True
        elif kind == "item.completed":
            item = event.get("item")
            if not started or not isinstance(item, dict):
                failed = True
            elif item.get("type") == "error":
                text = item.get("message")
                if isinstance(text, str):
                    diagnostic(text)
                else:
                    failed = True
            elif item.get("type") == "agent_message":
                text = item.get("text")
                if not isinstance(text, str):
                    failed = True
                else:
                    # Progress items are not the final response. Codex owns
                    # final-message selection; never search for parsable JSON.
                    has_message = True
    if final_error:
        diagnostics.append(final_error)
    if failed or not completed or not has_message:
        diagnostics.append("Codex event stream did not complete with agent output.")
        return "", "\n".join(diagnostics), False
    if final_error:
        return "", "\n".join(diagnostics), False
    return output, "\n".join(diagnostics), True

if __name__ == "__main__":
    main()
