"""Ttobak Deep Research Agent — AgentCore Runtime (Container).

FastAPI server following official AgentCore container contract:
- POST /invocations — agent interaction
- GET /ping — health check
- Port 8080, ARM64

Research runs in background thread — AgentCore HTTP has ~5min timeout
but research takes 5-45min. save_report tool writes result to DynamoDB.
"""

import logging
import os
import threading
import time
from datetime import datetime

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import Dict, Any, Optional

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("research-agent")

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
TABLE_NAME = os.environ.get("TABLE_NAME", "ttobak-main")

MODEL_BY_MODE = {
    "quick": "global.anthropic.claude-sonnet-4-6",
    "standard": "global.anthropic.claude-sonnet-4-6",
    "deep": "global.anthropic.claude-opus-4-7",
}

SYSTEM_PROMPT = """You are a Deep Research Agent for Ttobak, an AI meeting assistant for AWS Solutions Architects.

Perform comprehensive multi-source research and produce a structured, citation-backed report in Korean.

## Pipeline
SCOPE → RETRIEVE → SYNTHESIZE → PACKAGE

## Tools
- web_search: Search Google News RSS
- fetch_page: Fetch and extract text from a URL
- save_report: Save the final report (MUST be called at the end)

## Output
- Korean with English technical terms
- Markdown with ## headings, tables, bullet points
- Executive summary 200-400 words, sections 600-2000 words
- Include source URLs

## Mode
- quick: 5+ sources
- standard: 8-12 sources
- deep: 12-20 sources, maximum depth

CRITICAL: You MUST call save_report at the end with the complete markdown report.
"""

app = FastAPI(title="Ttobak Research Agent", version="1.0.0")

_agents: dict = {}
_last_activity = time.time()


def _get_agent(mode: str):
    if mode in _agents:
        return _agents[mode]

    from strands import Agent
    from strands.models.bedrock import BedrockModel
    from tools import web_search, fetch_page, save_report

    model_id = MODEL_BY_MODE.get(mode, MODEL_BY_MODE["standard"])
    logger.info(f"Building agent mode={mode} model={model_id}")
    agent = Agent(
        model=BedrockModel(model_id=model_id, region_name=REGION),
        system_prompt=SYSTEM_PROMPT,
        tools=[web_search, fetch_page, save_report],
    )
    _agents[mode] = agent
    return agent


def _mark_error(research_id: str, msg: str) -> None:
    try:
        import boto3
        boto3.resource("dynamodb").Table(TABLE_NAME).update_item(
            Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
            UpdateExpression="SET #s = :s, errorMessage = :e",
            ExpressionAttributeNames={"#s": "status"},
            ExpressionAttributeValues={":s": "error", ":e": msg[:500]},
        )
    except Exception as e:
        logger.error(f"Failed to mark error: {e}")


def _run_research(topic: str, mode: str, research_id: str) -> None:
    global _last_activity
    _last_activity = time.time()
    prompt = (
        f"Research ID: {research_id}\n"
        f"Mode: {mode}\n"
        f"Topic: {topic}\n\n"
        f"Conduct research following the {mode} pipeline. "
        f"Call save_report with researchId='{research_id}' when the report is complete."
    )
    try:
        logger.info(f"[{research_id}] starting agent mode={mode}")
        _get_agent(mode)(prompt)
        _last_activity = time.time()
        logger.info(f"[{research_id}] agent finished")
    except Exception as e:
        logger.error(f"[{research_id}] agent failed: {e}", exc_info=True)
        _mark_error(research_id, str(e))
    finally:
        _last_activity = time.time()


class InvocationRequest(BaseModel):
    topic: str = ""
    mode: str = "standard"
    researchId: str = ""
    input: Optional[Dict[str, Any]] = None


@app.post("/invocations")
async def invoke(request: InvocationRequest):
    topic = request.topic or (request.input or {}).get("prompt", "")
    mode = request.mode
    research_id = request.researchId

    if not topic or not research_id:
        raise HTTPException(status_code=400, detail="topic and researchId required")

    logger.info(f"Received research id={research_id} mode={mode} topic={topic[:80]}")

    threading.Thread(
        target=_run_research,
        args=(topic, mode, research_id),
        daemon=True,
    ).start()

    return {"status": "running", "researchId": research_id}


@app.get("/ping")
async def ping():
    return {
        "status": "HealthyBusy" if threading.active_count() > 2 else "Healthy",
        "time_of_last_update": int(_last_activity),
    }


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8080)
