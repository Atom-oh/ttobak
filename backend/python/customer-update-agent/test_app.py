import json
import logging
import os
import re
import threading
import types
import unittest
import warnings
from unittest import mock

with warnings.catch_warnings():
    warnings.simplefilter("ignore")
    from fastapi.testclient import TestClient

import app
import orchestrator
import store

# app.py gives this logger a stderr handler for the container; keep tests quiet.
logging.getLogger("customer-update-agent").handlers = [logging.NullHandler()]
logging.getLogger("httpx").setLevel(logging.WARNING)

CONTRACT = os.path.join(os.path.dirname(os.path.abspath(__file__)), os.pardir, os.pardir,
                        os.pardir, "contracts", "customer-updates", "run-v1.json")
with open(CONTRACT, encoding="utf-8") as f:
    EXAMPLE = json.load(f)["example"]
ENV = {"TABLE_NAME": "ttobak-main", "KMS_KEY_ID": "key-id"}


class AppTest(unittest.TestCase):
    def setUp(self):
        self.client = TestClient(app.app)
        self.release = threading.Event()
        self.started = threading.Event()
        self.runs = []
        env = mock.patch.dict(os.environ, ENV)
        env.start()
        self.addCleanup(env.stop)
        self.claims = []
        self.claim_error = None
        self.deps = types.SimpleNamespace(results=types.SimpleNamespace(claim=self._claim))
        run = mock.patch.object(orchestrator, "run", side_effect=self._blocking_run)
        deps = mock.patch.object(orchestrator, "build_deps", return_value=self.deps)
        run.start()
        deps.start()
        self.addCleanup(run.stop)
        self.addCleanup(deps.stop)
        self.addCleanup(self._finish)

    def _claim(self):
        self.claims.append(threading.current_thread().name)
        if self.claim_error:
            raise self.claim_error

    def _blocking_run(self, run_input, deps):
        self.runs.append((run_input.run_id, deps))
        self.started.set()
        self.release.wait(5)

    def _finish(self):
        self.release.set()
        thread = app._run_thread
        if thread is not None:
            thread.join(5)
        app._run_thread = None

    def post(self, body):
        return self.client.post("/invocations", content=body,
                                headers={"Content-Type": "application/json"})

    def test_invalid_payload_400_and_no_thread(self):
        bad = dict(EXAMPLE, version=2)
        resp = self.post(json.dumps(bad))
        self.assertEqual(resp.status_code, 400)
        self.assertEqual(resp.json(), {"error": {"code": "version",
                                                 "message": "invalid run payload"}})
        self.assertIsNone(app._run_thread)
        self.assertEqual(self.runs, [])

    def test_malformed_json_400(self):
        resp = self.post(b"{not json")
        self.assertEqual(resp.status_code, 400)
        self.assertEqual(resp.json()["error"]["message"], "invalid run payload")
        self.assertIsNone(app._run_thread)

    def test_oversized_body_413(self):
        resp = self.post(b" " * (app.MAX_BODY_BYTES + 1))
        self.assertEqual(resp.status_code, 413)
        self.assertEqual(resp.json()["error"]["code"], "TOO_LARGE")
        self.assertIsNone(app._run_thread)

    def test_missing_configuration_503(self):
        for name in ("KMS_KEY_ID", "TABLE_NAME"):
            with self.subTest(name=name), mock.patch.dict(os.environ, {name: ""}):
                resp = self.post(json.dumps(EXAMPLE))
                self.assertEqual(resp.status_code, 503)
                self.assertEqual(resp.json()["error"]["code"], "UNAVAILABLE")
                self.assertIsNone(app._run_thread)

    def test_accepts_and_runs_in_background(self):
        resp = self.post(json.dumps(EXAMPLE))
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"accepted": True, "runId": EXAMPLE["runId"]})
        self.assertTrue(self.started.wait(5))
        self.assertEqual(self.runs, [(EXAMPLE["runId"], self.deps)])
        self.assertTrue(app._run_thread.daemon)
        # Claimed synchronously in the request, not in the run thread.
        self.assertEqual(len(self.claims), 1)
        self.assertNotEqual(self.claims[0], "customer-update-run")

    def test_reused_run_id_409_and_no_thread(self):
        self.claim_error = store.RunExists()
        resp = self.post(json.dumps(EXAMPLE))
        self.assertEqual(resp.status_code, 409)
        self.assertEqual(resp.json()["error"]["code"], "RUN_EXISTS")
        self.assertIsNone(app._run_thread)
        self.assertEqual(self.runs, [])

    def test_claim_store_error_503_and_no_thread(self):
        self.claim_error = RuntimeError("dynamodb down 한빛리테일")
        with self.assertLogs("customer-update-agent", level="ERROR") as logs:
            resp = self.post(json.dumps(EXAMPLE))
        self.assertEqual(resp.status_code, 503)
        self.assertEqual(resp.json()["error"]["code"], "UNAVAILABLE")
        self.assertIsNone(app._run_thread)
        self.assertIn("RuntimeError", "\n".join(logs.output))
        self.assertNotIn("한빛리테일", "\n".join(logs.output))

    def test_second_run_while_busy_409(self):
        self.assertEqual(self.post(json.dumps(EXAMPLE)).status_code, 200)
        self.assertTrue(self.started.wait(5))
        resp = self.post(json.dumps(EXAMPLE))
        self.assertEqual(resp.status_code, 409)
        self.assertEqual(resp.json()["error"]["code"], "BUSY")
        self.assertEqual(len(self.runs), 1)

    def test_ping_healthy_busy_during_run(self):
        idle = self.client.get("/ping").json()
        self.assertEqual(idle["status"], "Healthy")
        self.assertIsInstance(idle["time_of_last_update"], int)
        self.post(json.dumps(EXAMPLE))
        self.assertTrue(self.started.wait(5))
        self.assertEqual(self.client.get("/ping").json()["status"], "HealthyBusy")
        self._finish()
        self.assertEqual(self.client.get("/ping").json()["status"], "Healthy")

    def test_run_exception_logged_by_class_name_only(self):
        secret = "한빛리테일 https://example.com/secret"

        def boom(run_input, deps):
            raise ValueError(secret)

        with mock.patch.object(orchestrator, "run", side_effect=boom), \
                self.assertLogs("customer-update-agent", level="ERROR") as logs:
            self.post(json.dumps(EXAMPLE))
            app._run_thread.join(5)
        joined = "\n".join(logs.output)
        self.assertIn("ValueError", joined)
        self.assertIn(EXAMPLE["runId"], joined)
        self.assertNotIn(secret, joined)
        self.assertTrue(all(r.exc_info is None for r in logs.records))

    def test_build_deps_failure_503(self):
        with mock.patch.object(orchestrator, "build_deps", side_effect=KeyError("TABLE_NAME")), \
                self.assertLogs("customer-update-agent", level="ERROR") as logs:
            resp = self.post(json.dumps(EXAMPLE))
        self.assertEqual(resp.status_code, 503)
        self.assertIn("KeyError", "\n".join(logs.output))
        self.assertIsNone(app._run_thread)
        self.assertEqual(self.runs, [])

    def test_thread_catches_base_exceptions_by_class_name(self):
        class Abort(BaseException):
            pass

        for error, reraised in ((Abort("secret"), False), (SystemExit("secret"), True)):
            with self.subTest(type(error).__name__), \
                    self.assertLogs("customer-update-agent", level="ERROR") as logs:
                with mock.patch.object(orchestrator, "run", side_effect=error):
                    if reraised:
                        with self.assertRaises(SystemExit):
                            app._run(app.contract.parse_run(EXAMPLE), self.deps)
                    else:
                        app._run(app.contract.parse_run(EXAMPLE), self.deps)
                joined = "\n".join(logs.output)
                self.assertIn(type(error).__name__, joined)
                self.assertNotIn("secret", joined)

    def test_library_logs_never_reach_stderr(self):
        root = logging.getLogger()
        self.assertTrue(root.handlers)  # so the last-resort stderr handler is never used
        for name in ("strands", "botocore", "boto3", "urllib3", "httpx", "uvicorn.access"):
            self.assertEqual(logging.getLogger(name).level, logging.WARNING, name)

    def test_payload_never_logged(self):
        with self.assertLogs("customer-update-agent", level="DEBUG") as logs:
            logging.getLogger("customer-update-agent").debug("marker")
            self.post(json.dumps(EXAMPLE))
            self.post(json.dumps(dict(EXAMPLE, version=2)))
        joined = "\n".join(logs.output)
        for needle in ("한빛리테일", "example.com", "bedrock-korea-launch"):
            self.assertNotIn(needle, joined)

    def test_only_two_routes(self):
        paths = {r.path for r in app.app.routes if getattr(r, "include_in_schema", False)}
        self.assertEqual(paths, {"/invocations", "/ping"})
        self.assertEqual(self.client.get("/invocations").status_code, 405)


class DockerfileTest(unittest.TestCase):
    """The image copies every runtime module and the fetcher, and no tests."""

    def test_copies_exactly_the_runtime_modules(self):
        here = os.path.dirname(os.path.abspath(__file__))
        with open(os.path.join(here, "Dockerfile"), encoding="utf-8") as f:
            text = f.read()
        copied = set(re.findall(r"customer-update-agent/([a-z_]+\.py)", text))
        modules = {n for n in os.listdir(here)
                   if n.endswith(".py") and not n.startswith("test_")}
        self.assertEqual(copied, modules)
        self.assertIn("crawler/article_fetcher.py crawler/news_pipeline.py", text)
        self.assertIn('CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]', text)


class ImportStateTest(unittest.TestCase):
    def test_nothing_run_related_at_import(self):
        import importlib
        fresh = importlib.reload(app)
        self.assertIsNone(fresh._last_activity)
        self.assertIsNone(fresh._run_thread)


if __name__ == "__main__":
    unittest.main()
