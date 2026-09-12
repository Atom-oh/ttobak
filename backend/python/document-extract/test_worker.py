import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from dataclasses import replace
from unittest.mock import patch

from contract import Limits, ParseFailure
from fixtures import archive, docx_parts, pptx_parts, pdf
from worker import _bounded_child, _child_environment, extract_file, run_parser

CACHE = Path.home() / ".cache" / "ttobak-document-parser-tests"
MODULE = str(Path(__file__).resolve().parent)


class WorkerTests(unittest.TestCase):
    def test_child_loader_path_is_derived_from_the_interpreter_not_inherited(self):
        with patch.dict(os.environ, {
            "LD_LIBRARY_PATH": "/untrusted/path",
            "LD_PRELOAD": "/untrusted/library.so",
            "AWS_SECRET_ACCESS_KEY": "synthetic-only",
        }):
            env = _child_environment()
        self.assertEqual(env["LD_LIBRARY_PATH"], str(Path(sys.base_prefix) / "lib"))
        self.assertNotIn("LD_PRELOAD", env)
        self.assertNotIn("AWS_SECRET_ACCESS_KEY", env)

    def test_real_children_parse_all_formats(self):
        for data, fmt in [(b"# text", "md"), (pdf(), "pdf"), (archive(docx_parts()), "docx"), (archive(pptx_parts()), "pptx")]:
            with self.subTest(fmt=fmt):
                result = run_parser(data, fmt)
                self.assertEqual(result["status"], "succeeded", result)
                self.assertTrue(result["units"])

    def test_worker_wall_deadline_and_output_bounds(self):
        for script, limits, code in [
            ("import time; time.sleep(10)", replace(Limits(), wall_seconds=0.1), "TIMEOUT"),
            ("import os; os.write(1,b'x'*4096)", replace(Limits(), max_result_bytes=1024), "WORKER_OUTPUT_LIMIT"),
            ("import os; os.write(2,b'x'*4096)", replace(Limits(), max_stderr_bytes=1024), "WORKER_OUTPUT_LIMIT"),
        ]:
            with self.subTest(code=code):
                started = time.monotonic()
                with self.assertRaises(ParseFailure) as error:
                    _bounded_child([sys.executable, "-I", "-c", script], b"", limits)
                self.assertEqual(error.exception.code, code)
                self.assertLess(time.monotonic() - started, 3)

    def test_actual_cpu_and_address_space_limits(self):
        prefix = f"import sys;sys.path.insert(0,{MODULE!r});from worker import _apply_limits;from contract import Limits;"
        memory = prefix + "_apply_limits(Limits(memory_bytes=64*1024*1024));\ntry:\n x=bytearray(128*1024*1024)\nexcept MemoryError:\n print('memory bounded')\nelse:\n raise SystemExit(9)\n"
        self.assertEqual(_bounded_child([sys.executable, "-I", "-c", memory], b"", Limits()).strip(), b"memory bounded")
        cpu = prefix + "_apply_limits(Limits(cpu_seconds=1));\nwhile True: pass\n"
        with self.assertRaises(ParseFailure) as error:
            _bounded_child([sys.executable, "-I", "-c", cpu], b"", replace(Limits(), wall_seconds=4))
        self.assertEqual(error.exception.code, "RESOURCE_LIMIT")

    def test_child_guard_blocks_network_and_strips_credentials(self):
        script = f"""import sys,os,socket
sys.path.insert(0,{MODULE!r})
from worker import _audit
sys.addaudithook(_audit)
assert 'AWS_SECRET_ACCESS_KEY' not in os.environ
try: socket.socket()
except PermissionError: print('blocked')
else: raise SystemExit(9)
"""
        with patch.dict(os.environ, {"AWS_SECRET_ACCESS_KEY": "synthetic-only"}):
            self.assertEqual(_bounded_child([sys.executable, "-I", "-c", script], b"", Limits()).strip(), b"blocked")

    def test_actual_parser_memory_failure_is_explicit(self):
        # Force a new allocation beyond the allowance rather than depending on
        # how much free allocator space a particular Python build starts with.
        result = run_parser(b"x" * (16 * 1024 * 1024), "md", replace(Limits(), memory_bytes=8*1024*1024))
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["error"]["code"], "RESOURCE_LIMIT")
        result = run_parser(b"text", "md", replace(Limits(), wall_seconds=0.0001))
        self.assertEqual(result["error"]["code"], "TIMEOUT")

    def test_bad_child_result_never_becomes_empty_success(self):
        for output in [b"not JSON", json.dumps({"schemaVersion": 1, "format": "md", "status": "succeeded", "complete": True, "units": [], "warnings": []}).encode()]:
            with patch("worker._bounded_child", return_value=output):
                result = run_parser(b"text", "md")
            self.assertEqual(result["error"]["code"], "WORKER_FAILED")

    def test_regular_file_only_and_cli_json(self):
        CACHE.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="document-parser-test-", dir=CACHE) as directory:
            path = Path(directory) / "한글.md"
            path.write_text("실제 파일😀", encoding="utf-8")
            result = extract_file(path)
            self.assertEqual(result["units"][0]["text"], "실제 파일😀", result)
            link = Path(directory) / "link.md"
            link.symlink_to(path)
            self.assertEqual(extract_file(link)["error"]["code"], "INPUT_UNAVAILABLE")
            self.assertEqual(extract_file(directory, "md")["error"]["code"], "INPUT_UNAVAILABLE")
            import subprocess
            call = subprocess.run([sys.executable, str(Path(MODULE) / "worker.py"), str(path)], capture_output=True, timeout=5)
            self.assertEqual(call.returncode, 0, call.stderr)
            self.assertEqual(json.loads(call.stdout)["status"], "succeeded")


if __name__ == "__main__":
    unittest.main()
