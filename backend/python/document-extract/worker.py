"""Resource-limited parser child. No AWS or network integration."""
import argparse
import json
import math
import os
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import sys
import time

# -I excludes the script directory: add only this trusted installed module path.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from contract import Limits, MESSAGES, ParseFailure, encoded, failure


def _kill(process):
    # Descendants may retain pipes even after the process-group leader exits.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def _bounded_child(command, data, limits):
    """Nonblocking IO bounds both output streams, including a broken child."""
    process = subprocess.Popen(
        command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        close_fds=True, start_new_session=True,
        env={"PATH": os.defpath, "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"},
    )
    selector = selectors.DefaultSelector()
    stdout, stderr = bytearray(), bytearray()
    offset, deadline = 0, time.monotonic() + limits.wall_seconds
    for stream, event in ((process.stdin, selectors.EVENT_WRITE),
                          (process.stdout, selectors.EVENT_READ), (process.stderr, selectors.EVENT_READ)):
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, event)
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ParseFailure("TIMEOUT")
            for key, _ in selector.select(min(remaining, 0.05)):
                stream = key.fileobj
                if stream is process.stdin:
                    try:
                        if offset < len(data):
                            offset += os.write(stream.fileno(), memoryview(data)[offset:offset + 65536])
                        if offset == len(data):
                            selector.unregister(stream)
                            stream.close()
                    except BrokenPipeError:
                        selector.unregister(stream)
                        stream.close()
                else:
                    chunk = os.read(stream.fileno(), 65536)
                    if not chunk:
                        selector.unregister(stream)
                        stream.close()
                        continue
                    target, cap = (stdout, limits.max_result_bytes) if stream is process.stdout else (stderr, limits.max_stderr_bytes)
                    if len(target) + len(chunk) > cap:
                        raise ParseFailure("WORKER_OUTPUT_LIMIT")
                    target.extend(chunk)
        try:
            code = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            raise ParseFailure("TIMEOUT") from None
        if code in (-signal.SIGKILL, -signal.SIGXCPU):
            raise ParseFailure("RESOURCE_LIMIT")
        if code != 0:
            raise ParseFailure("WORKER_FAILED")
        return bytes(stdout)
    finally:
        _kill(process)
        selector.close()
        for stream in (process.stdin, process.stdout, process.stderr):
            if not stream.closed:
                stream.close()


def run_parser(data, fmt, limits=None):
    """Trusted parent entry for already-downloaded bytes; no path or AWS access."""
    limits = limits or Limits()
    try:
        limits.validate()
        if fmt not in ("pdf", "pptx", "docx", "md"):
            raise ParseFailure("UNSUPPORTED_FORMAT")
        if not isinstance(data, bytes):
            raise ParseFailure("INVALID_OPTIONS")
        if len(data) > limits.max_input_bytes:
            raise ParseFailure("LIMIT_EXCEEDED")
        if os.name != "posix":
            raise ParseFailure("ISOLATION_UNAVAILABLE")
        body = _bounded_child(
            [sys.executable, "-I", "-B", str(Path(__file__).resolve()), "--child", fmt,
             json.dumps(limits.to_dict(), separators=(",", ":"))], data, limits,
        )
        result = json.loads(body.decode("utf-8", "strict"))
        if not valid_result(result, fmt, limits):
            raise ParseFailure("WORKER_FAILED")
        return result
    except ParseFailure as error:
        return failure(fmt, error.code)
    except (ValueError, UnicodeError, OSError):
        return failure(fmt, "WORKER_FAILED")


def extract_file(path, fmt=None, limits=None):
    limits = limits or Limits()
    fmt = fmt or Path(path).suffix.lower().lstrip(".")
    try:
        limits.validate()
        if fmt not in ("pdf", "pptx", "docx", "md"):
            raise ParseFailure("UNSUPPORTED_FORMAT")
        if not hasattr(os, "O_NOFOLLOW"):
            raise ParseFailure("ISOLATION_UNAVAILABLE")
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode):
                raise ParseFailure("INPUT_UNAVAILABLE")
            if info.st_size > limits.max_input_bytes:
                raise ParseFailure("LIMIT_EXCEEDED")
            data = stream.read(limits.max_input_bytes + 1)
        return run_parser(data, fmt, limits)
    except ParseFailure as error:
        return failure(fmt, error.code)
    except (OSError, ValueError):
        return failure(fmt, "INPUT_UNAVAILABLE")


def valid_result(result, fmt, limits):
    if (not isinstance(result, dict) or result.get("schemaVersion") != 1 or result.get("format") != fmt or
            result.get("status") not in ("succeeded", "partial", "failed") or
            type(result.get("complete")) is not bool or not isinstance(result.get("units"), list) or
            not isinstance(result.get("warnings"), list) or len(result["warnings"]) > 400 or
            len(result["units"]) > limits.max_units or len(encoded(result)) > limits.max_result_bytes):
        return False
    size = 0
    for unit in result["units"]:
        if (not isinstance(unit, dict) or not isinstance(unit.get("text"), str) or not unit["text"].strip() or
                not isinstance(unit.get("location"), dict)):
            return False
        size += len(unit["text"].encode("utf-8", "strict"))
    if size > limits.max_text_bytes:
        return False
    metrics = result.get("metrics")
    if (not isinstance(metrics, dict) or type(metrics.get("units")) is not int or type(metrics.get("textBytes")) is not int or
            metrics["units"] != len(result["units"]) or metrics["textBytes"] != size):
        return False
    if result["status"] == "failed":
        error = result.get("error")
        return (not result["complete"] and not result["units"] and result.get("scope") == "none" and
                isinstance(error, dict) and error.get("code") in MESSAGES and
                error.get("message") == MESSAGES[error["code"]])
    scope = {"pdf": "embedded_pdf_text", "md": "markdown_source", "docx": "document_body", "pptx": "slide_body"}
    return (bool(result["units"]) and result.get("error") is None and result.get("scope") == scope[fmt] and
            result["complete"] == (result["status"] == "succeeded") and
            bool(result["warnings"]) == (result["status"] == "partial"))


def _audit(event, args):
    if event.startswith(("socket.", "subprocess.", "os.exec", "os.spawn", "os.posix_spawn", "os.fork")) or event == "os.system":
        raise PermissionError("Parser network and subprocess operations are disabled")


def _apply_limits(limits):
    import resource
    # Apply in the child, never preexec_fn (unsafe in a threaded future parent).
    resource.setrlimit(resource.RLIMIT_AS, (limits.memory_bytes, limits.memory_bytes))
    cpu = math.ceil(limits.cpu_seconds)
    resource.setrlimit(resource.RLIMIT_CPU, (cpu, cpu + 1))
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_FSIZE, (limits.max_result_bytes, limits.max_result_bytes))


def _child(fmt, config):
    memory_failure = encoded(failure(fmt, "RESOURCE_LIMIT"))
    try:
        if len(config) > 4096:
            raise ParseFailure("INVALID_OPTIONS")
        limits = Limits(**json.loads(config)).validate()
        try:
            _apply_limits(limits)
        except (ImportError, AttributeError, ValueError, OSError):
            raise ParseFailure("ISOLATION_UNAVAILABLE") from None
        sys.addaudithook(_audit)
        from parsers import extract_bytes
        data = bytearray()
        while True:
            chunk = sys.stdin.buffer.read(min(65536, limits.max_input_bytes + 1 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
            if len(data) > limits.max_input_bytes:
                raise ParseFailure("LIMIT_EXCEEDED")
        result = extract_bytes(bytes(data), fmt, limits)
        del data
        output = encoded(result)
        if len(output) > limits.max_result_bytes:
            output = encoded(failure(fmt, "WORKER_OUTPUT_LIMIT"))
    except MemoryError:
        output = memory_failure
    except ParseFailure as error:
        output = encoded(failure(fmt, error.code))
    except Exception:
        output = encoded(failure(fmt, "WORKER_FAILED"))
    sys.stdout.buffer.write(output)
    sys.stdout.buffer.flush()


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "--child":
        _child(sys.argv[2], sys.argv[3])
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("file")
    parser.add_argument("--format", choices=["pdf", "pptx", "docx", "md"])
    args = parser.parse_args()
    result = extract_file(args.file, args.format)
    sys.stdout.buffer.write(encoded(result))
    raise SystemExit(0 if result["status"] == "succeeded" else 2)


if __name__ == "__main__":
    main()
