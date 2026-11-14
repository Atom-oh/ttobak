"""AgentCore Runtime HTTP service for the customer-update agent.

POST /invocations validates a run-v1 payload, claims its single-use runId
(409 RUN_EXISTS on reuse) and starts one background run;
GET /ping reports HealthyBusy while that run is alive. Port 8080, ARM64.
Never log the payload or exception messages: both can carry customer text.
Logs carry runId, codes and exception class names only.
"""

import json
import logging
import os
import threading
import time
from typing import Optional

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

import contract
import orchestrator
import store

logger = logging.getLogger("customer-update-agent")
# Only this package's logger prints. Library records (whose messages and
# tracebacks can carry URLs, queries or payload text) reach only the root
# logger's NullHandler, so Python's last-resort stderr handler never prints
# them; their levels are WARNING so DEBUG/INFO records are not even built.
_root = logging.getLogger()
if not _root.handlers:
    _root.addHandler(logging.NullHandler())
for _name in ("strands", "botocore", "boto3", "urllib3", "httpx", "uvicorn.access"):
    logging.getLogger(_name).setLevel(logging.WARNING)
if not logger.handlers:
    _handler = logging.StreamHandler()
    _handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger.addHandler(_handler)
    logger.setLevel(logging.INFO)
    logger.propagate = False

MAX_BODY_BYTES = 1 << 20

app = FastAPI(title="TTOBAK Customer Update Agent", version="1.0.0",
              docs_url=None, redoc_url=None, openapi_url=None)

_lock = threading.Lock()
_run_thread: Optional[threading.Thread] = None
# Set by the first ping or the first run, never at import (AgentCore Runtime V2
# restores instances from a snapshot taken after import; see research-agent).
_last_activity: Optional[float] = None


def _error(status, code, message):
    return JSONResponse(status_code=status,
                        content={"error": {"code": code, "message": message}})


def _busy():
    return _run_thread is not None and _run_thread.is_alive()


def _reject_constant(_):
    raise ValueError("non-finite number")


def _run(run_input, deps):
    """Thread target. The orchestrator writes DONE itself only on success; this
    wrapper never writes rows and logs only the exception class."""
    global _last_activity
    try:
        orchestrator.run(run_input, deps)
    except BaseException as error:  # noqa: BLE001  the thread must not die noisily
        logger.error("run failed run=%s type=%s", run_input.run_id, type(error).__name__)
        if isinstance(error, (KeyboardInterrupt, SystemExit)):
            raise
    finally:
        _last_activity = time.time()


async def _read_bounded(request):
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_BODY_BYTES:
                return None
        except ValueError:
            return b""
    body = bytearray()
    async for chunk in request.stream():
        body.extend(chunk)
        if len(body) > MAX_BODY_BYTES:
            return None
    return bytes(body)


@app.post("/invocations")
async def invocations(request: Request):
    global _run_thread, _last_activity
    body = await _read_bounded(request)
    if body is None:
        return _error(413, "TOO_LARGE", "request body too large")
    try:
        payload = json.loads(body, parse_constant=_reject_constant)
    except (ValueError, UnicodeDecodeError, RecursionError):
        logger.info("invocation rejected code=json")
        return _error(400, "json", "invalid run payload")
    try:
        run_input = contract.parse_run(payload)
    except contract.ContractError as error:
        logger.info("invocation rejected code=%s", error.code)
        return _error(400, error.code, "invalid run payload")
    if not os.environ.get("KMS_KEY_ID") or not os.environ.get("TABLE_NAME"):
        logger.error("invocation refused run=%s code=UNAVAILABLE", run_input.run_id)
        return _error(503, "UNAVAILABLE", "runtime is not configured")
    with _lock:
        if _busy():
            logger.info("invocation refused run=%s code=BUSY", run_input.run_id)
            return _error(409, "BUSY", "a run is already in progress")
        # Claim the single-use runId before starting anything (no model call yet).
        try:
            deps = orchestrator.build_deps(run_input)
            deps.results.claim()
        except store.RunExists:
            logger.info("invocation refused run=%s code=RUN_EXISTS", run_input.run_id)
            return _error(409, "RUN_EXISTS", "this runId was already used")
        except Exception as error:  # noqa: BLE001  store or configuration failure
            logger.error("invocation refused run=%s code=UNAVAILABLE type=%s", run_input.run_id,
                         type(error).__name__)
            return _error(503, "UNAVAILABLE", "runtime is not available")
        thread = threading.Thread(target=_run, args=(run_input, deps), daemon=True,
                                  name="customer-update-run")
        _run_thread = thread
        _last_activity = time.time()
        thread.start()
    logger.info("invocation accepted run=%s", run_input.run_id)
    return {"accepted": True, "runId": run_input.run_id}


@app.get("/ping")
async def ping():
    global _last_activity
    if _last_activity is None:
        _last_activity = time.time()
    return {
        "status": "HealthyBusy" if _busy() else "Healthy",
        "time_of_last_update": int(_last_activity),
    }
