"""ttobak-sim: the cost/sizing simulator's async worker (ADR-031).

Invoked fire-and-forget (InvocationType=Event) by the Go api Lambda's
SimService.CreateSimulation once a run has been recorded as "queued" via
PutSimRunIfNotRunning. This Lambda never sees the meeting transcript --
only the server-validated requirements/options JSON the invoke payload
carries -- so there is no code path here that could leak transcript text
into the Code Interpreter session or the codegen prompt.

Flow: check_sim_limit -> fetch_unit_prices -> start Code Interpreter
session -> writeFiles inputs -> codegen/executeCode loop (<=3 attempts) ->
readFiles outputs -> S3 put -> DynamoDB update -> stop session (finally).
"""
import json
import logging
import os
import time

import boto3

from codegen import (
    MAX_EXECUTE_ATTEMPTS,
    build_codegen_prompt,
    build_repair_prompt,
    classify_run_result,
    extract_code_from_response,
)
from pricing import fetch_unit_prices

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get("TABLE_NAME", "ttobak-main")
BUCKET_NAME = os.environ.get("BUCKET_NAME", "ttobak-assets")
BEDROCK_MODEL_ID = os.environ.get("BEDROCK_MODEL_ID", "global.anthropic.claude-sonnet-5")
CODE_INTERPRETER_ID = os.environ.get("CODE_INTERPRETER_ID", "ttobak_sim")
AWS_REGION = os.environ.get("AWS_REGION", "ap-northeast-2")

DAILY_SIM_LIMIT = int(os.environ.get("DAILY_SIM_LIMIT", "3"))
CI_SESSION_TIMEOUT_SECONDS = 600

dynamodb = boto3.resource("dynamodb", region_name=AWS_REGION)
table = dynamodb.Table(TABLE_NAME)


def check_sim_limit(user_id):
    """Atomic daily counter, same shape as qa/handler.py's
    check_research_limit but with its own SK/limit -- Code Interpreter
    session-minutes are the real per-run cost here."""
    from datetime import datetime, timezone

    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    counter_pk = f"USER#{user_id}"
    counter_sk = f"SIM_DAILY#{today}"
    try:
        resp = table.update_item(
            Key={"PK": counter_pk, "SK": counter_sk},
            UpdateExpression="SET #c = if_not_exists(#c, :zero) + :one, #ttl = :ttl",
            ExpressionAttributeNames={"#c": "count", "#ttl": "TTL"},
            ExpressionAttributeValues={
                ":zero": 0, ":one": 1,
                ":ttl": int(time.time()) + 172800,
            },
            ReturnValues="UPDATED_NEW",
        )
        current = int(resp.get("Attributes", {}).get("count", 1))
        if current > DAILY_SIM_LIMIT:
            table.update_item(
                Key={"PK": counter_pk, "SK": counter_sk},
                UpdateExpression="SET #c = #c - :one",
                ExpressionAttributeNames={"#c": "count"},
                ExpressionAttributeValues={":one": 1},
            )
            return False
        return True
    except Exception as e:  # noqa: BLE001
        logger.warning("failed to check sim limit for %s: %s", user_id, e)
        return True


def update_sim_run(meeting_id, sim_run_id, fields):
    """Patches the meeting's singleton SimRun item. Attribute names here
    must match model.SimRun's `dynamodbav` tags exactly (SCHEMA SYNC with
    backend/internal/model/sim.go) -- status, chartKeys, reportMarkdown,
    reportKey, codeKey, priceSnapshotKey, priceSnapshotAt, attempts,
    errorMessage.

    Conditioned on simRunId still matching this worker's own run -- without
    this, a zombied worker for an OLD run (superseded by a fresh claim on
    the same meeting, e.g. after a stuck-run timeout freed the slot) could
    write a late update straight onto the NEW run's row. A condition failure
    here means exactly that -- this worker's result is moot -- so it's
    swallowed, not raised; the Go repository's UpdateSimRunFieldsIfMatch
    implements the identical guard independently (cross-language, no shared
    code) and must be kept in sync.
    """
    from datetime import datetime, timezone

    from botocore.exceptions import ClientError

    fields = dict(fields)
    fields["updatedAt"] = datetime.now(timezone.utc).isoformat()
    names = {f"#{k}": k for k in fields}
    values = {f":{k}": v for k, v in fields.items()}
    values[":expectedSimRunId"] = sim_run_id
    update_expr = "SET " + ", ".join(f"#{k} = :{k}" for k in fields)
    try:
        table.update_item(
            Key={"PK": f"MEETING#{meeting_id}", "SK": "SIMRUN"},
            UpdateExpression=update_expr,
            ConditionExpression="simRunId = :expectedSimRunId",
            ExpressionAttributeNames=names,
            ExpressionAttributeValues=values,
        )
    except ClientError as e:
        if e.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            logger.warning(
                "update_sim_run: simRunId %s no longer matches meeting %s (superseded) -- dropping this update",
                sim_run_id, meeting_id,
            )
            return
        raise


def _invoke_codegen(bedrock_client, system_prompt, user_prompt):
    """Wraps the Converse API (per backend/python/CLAUDE.md convention:
    converse(), not invoke_model()) for a single codegen/repair turn."""
    resp = bedrock_client.converse(
        modelId=BEDROCK_MODEL_ID,
        system=[{"text": system_prompt}],
        messages=[{"role": "user", "content": [{"text": user_prompt}]}],
        inferenceConfig={"maxTokens": 4096, "temperature": 0},
    )
    return resp["output"]["message"]["content"][0]["text"]


def _ci_call(ci_client, session_id, name, arguments):
    resp = ci_client.invoke_code_interpreter(
        codeInterpreterIdentifier=CODE_INTERPRETER_ID,
        sessionId=session_id,
        name=name,
        arguments=arguments,
    )
    events = list(resp["stream"])
    return events[0]["result"] if events else {}


def run_codegen_loop(ci_client, bedrock_client, requirements, options, prices, session_id):
    """The codegen -> executeCode -> (retry|success) loop, bounded at
    MAX_EXECUTE_ATTEMPTS. Each repair round is fed only stderr + the missing
    output file list (build_repair_prompt) -- never the original prompt's
    full context again, and never a transcript (which this Lambda never has
    in the first place).

    Returns (code, listed_output_paths). Raises RuntimeError after the
    attempt budget is exhausted.
    """
    system_prompt, user_prompt = build_codegen_prompt(requirements, options, prices)
    last_stderr = ""
    last_missing = []
    code = None

    for attempt in range(1, MAX_EXECUTE_ATTEMPTS + 1):
        prompt = user_prompt if attempt == 1 else build_repair_prompt(last_stderr, last_missing)
        response_text = _invoke_codegen(bedrock_client, system_prompt, prompt)
        code = extract_code_from_response(response_text)

        exec_result = _ci_call(
            ci_client, session_id, "executeCode",
            {"language": "python", "code": code, "clearContext": True},
        )
        structured = exec_result.get("structuredContent", {})
        exit_code = structured.get("exitCode", 1)
        last_stderr = structured.get("stderr", "")

        listing = _ci_call(ci_client, session_id, "listFiles", {"directoryPath": "outputs"})
        listed_paths = [
            "outputs/" + c.get("name", "")
            for c in listing.get("content", [])
            if c.get("type") == "resource_link"
        ]

        verdict, missing = classify_run_result(exit_code, listed_paths)
        if verdict == "success":
            return code, listed_paths
        last_missing = missing
        logger.info("executeCode attempt %d/%d failed, missing=%s", attempt, MAX_EXECUTE_ATTEMPTS, missing)

    raise RuntimeError(f"code execution failed after {MAX_EXECUTE_ATTEMPTS} attempts: {last_stderr[-500:]}")


def _read_output_bytes(ci_client, session_id, path):
    result = _ci_call(ci_client, session_id, "readFiles", {"paths": [path]})
    for item in result.get("content", []):
        resource = item.get("resource", {})
        blob = resource.get("blob")
        if blob is not None:
            return blob if isinstance(blob, bytes) else bytes(blob)
        text = resource.get("text")
        if text is not None:
            return text.encode("utf-8")
    raise RuntimeError(f"readFiles returned no content for {path}")


def lambda_handler(event, context):
    sim_run_id = event["simRunId"]
    meeting_id = event["meetingId"]
    user_id = event["userId"]
    requirements = event.get("requirements", [])
    options = event.get("options", [])

    if not check_sim_limit(user_id):
        update_sim_run(meeting_id, sim_run_id, {
            "status": "error",
            "errorMessage": "일일 시뮬레이션 실행 횟수를 초과했습니다. 내일 다시 시도해주세요.",
        })
        return {"status": "error", "reason": "rate_limited"}

    ci_client = boto3.client("bedrock-agentcore", region_name=AWS_REGION)
    bedrock_client = boto3.client("bedrock-runtime", region_name=AWS_REGION)
    s3_client = boto3.client("s3")

    try:
        prices = fetch_unit_prices()
        update_sim_run(meeting_id, sim_run_id, {"status": "running"})

        session_id = ci_client.start_code_interpreter_session(
            codeInterpreterIdentifier=CODE_INTERPRETER_ID,
            name=f"sim-{sim_run_id}"[:100],
            sessionTimeoutSeconds=CI_SESSION_TIMEOUT_SECONDS,
        )["sessionId"]

        try:
            _ci_call(ci_client, session_id, "writeFiles", {"content": [
                {"path": "inputs/requirements.json", "text": json.dumps(requirements, ensure_ascii=False)},
                {"path": "inputs/options.json", "text": json.dumps(options, ensure_ascii=False)},
                {"path": "inputs/prices.json", "text": json.dumps(prices, ensure_ascii=False)},
            ]})

            code, listed_paths = run_codegen_loop(ci_client, bedrock_client, requirements, options, prices, session_id)

            report_bytes = _read_output_bytes(ci_client, session_id, "outputs/report.md")
            chart_bytes_list = []
            for path in sorted(p for p in listed_paths if p.startswith("outputs/chart_")):
                chart_bytes_list.append(_read_output_bytes(ci_client, session_id, path))
        finally:
            try:
                ci_client.stop_code_interpreter_session(
                    codeInterpreterIdentifier=CODE_INTERPRETER_ID, sessionId=session_id,
                )
            except Exception as e:  # noqa: BLE001 -- never let cleanup mask the real result
                logger.warning("failed to stop code interpreter session %s: %s", session_id, e)

        prefix = f"{user_id}/{meeting_id}/sim/{sim_run_id}"
        report_key = f"files/{prefix}/report.md"
        code_key = f"files/{prefix}/generated.py"
        price_key = f"files/{prefix}/prices.json"
        chart_keys = []
        for i, chart_bytes in enumerate(chart_bytes_list, start=1):
            key = f"images/{prefix}/chart_{i}.png"
            s3_client.put_object(Bucket=BUCKET_NAME, Key=key, Body=chart_bytes, ContentType="image/png")
            chart_keys.append(key)

        report_text = report_bytes.decode("utf-8", errors="replace")
        s3_client.put_object(Bucket=BUCKET_NAME, Key=report_key, Body=report_bytes, ContentType="text/markdown")
        s3_client.put_object(Bucket=BUCKET_NAME, Key=code_key, Body=code.encode("utf-8"), ContentType="text/x-python")
        s3_client.put_object(
            Bucket=BUCKET_NAME, Key=price_key,
            Body=json.dumps(prices, ensure_ascii=False).encode("utf-8"),
            ContentType="application/json",
        )

        update_sim_run(meeting_id, sim_run_id, {
            "status": "done",
            "chartKeys": chart_keys,
            "reportMarkdown": report_text[:100_000],
            "reportKey": report_key,
            "codeKey": code_key,
            "priceSnapshotKey": price_key,
            "priceSnapshotAt": prices.get("retrievedAt", ""),
        })
        return {"status": "done", "simRunId": sim_run_id}

    except Exception as e:  # noqa: BLE001 -- top-level: always leave the run in a terminal state
        logger.exception("simulation failed for meeting %s", meeting_id)
        update_sim_run(meeting_id, sim_run_id, {"status": "error", "errorMessage": str(e)[:2000]})
        return {"status": "error", "reason": str(e)[:200]}
