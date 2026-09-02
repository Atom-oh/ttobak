"""Fixed-ENTRYPOINT engine dispatcher for the whisperx bench image.

Why this exists (PR #175 round-2, L3 MAJOR): selecting the engine via an
ECS containerOverrides `command` requires splitting the Dockerfile's
ENTRYPOINT from its CMD -- but that split promotes the command override
into an arbitrary-code channel (`"command": ["-c", "<python>"]`), and this
task runs with networkMode host (EC2 IMDS reachable) and read access to
every user's original audio. Pinning the full ENTRYPOINT to this
dispatcher and selecting the engine via the ENGINE env var keeps the
selection on a surface operators could already override (environment)
without widening what a compromised RunTask caller can execute: only the
two allowlisted engine scripts can run, with no pass-through arguments.

Deliberately dependency-free (stdlib only, no boto3/env-derived clients)
so a bad ENGINE value fails before anything else initializes, and
import-safe for unit tests (all work happens under __main__ / main()).
"""
import os
import sys

# Engine names are operator-facing (runbook §3/§3c); script paths are the
# only thing this file will ever exec.
ENGINES = {
    "whisperx": "transcribe_whisperx.py",
    "fw_p4": "transcribe_fw_p4.py",
}
DEFAULT_ENGINE = "whisperx"


def resolve_engine(argv: list, env) -> str:
    """Returns the engine script to run, or raises ValueError with an
    operator-facing, safe-to-log message (no env values beyond the ENGINE
    knob itself are ever embedded)."""
    if len(argv) > 1:
        # A containerOverrides `command` lands here as argv. Refusing ALL
        # arguments (even ones naming an allowlisted script) keeps env the
        # single selection surface and the command override a hard no-op
        # channel.
        raise ValueError(
            "run_engine takes no arguments -- select the engine via the "
            "ENGINE env var ('whisperx' or 'fw_p4'), not a command override")
    raw = (env.get("ENGINE") or "").strip()
    name = raw.lower() or DEFAULT_ENGINE
    if name not in ENGINES:
        raise ValueError(
            f"ENGINE must be one of {sorted(ENGINES)}, got {raw!r}")
    return ENGINES[name]


def main() -> None:
    try:
        script = resolve_engine(sys.argv, os.environ)
    except ValueError as e:
        print(f"FATAL: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"run_engine: dispatching to {script}")
    script_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), script)
    # Replace the process rather than subprocess-wrapping it: the engine
    # keeps PID 1's signal handling (ECS stop -> SIGTERM reaches the
    # engine), and there's no parent left to leak file descriptors from.
    os.execv(sys.executable, [sys.executable, script_path])


if __name__ == "__main__":
    main()
