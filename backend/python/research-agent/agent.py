"""Ttobak Deep Research Agent — AgentCore Runtime (Container).

Runs agent in background thread so AgentCore Runtime invoke returns immediately.
Deep research takes 5-45 min, but AgentCore HTTP response has ~5 min timeout.
save_report tool writes final result to S3 + DynamoDB when done.
"""

import logging
import os
import threading

from bedrock_agentcore.runtime import BedrockAgentCoreApp
from strands import Agent
from strands.models.bedrock import BedrockModel

from tools import fetch_page, save_report, web_search

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

app = BedrockAgentCoreApp()

_agents: dict[str, Agent] = {}


def _get_agent(mode: str) -> Agent:
    if mode in _agents:
        return _agents[mode]
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
        logger.error(f"Failed to mark error in DDB: {e}")


def _run_research(topic: str, mode: str, research_id: str) -> None:
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
        logger.info(f"[{research_id}] agent finished")
    except Exception as e:
        logger.error(f"[{research_id}] agent failed: {e}", exc_info=True)
        _mark_error(research_id, str(e))


@app.entrypoint
def handle(payload: dict) -> dict:
    topic = (payload or {}).get("topic", "")
    mode = (payload or {}).get("mode", "standard")
    research_id = (payload or {}).get("researchId", "")

    if not topic or not research_id:
        return {"error": "topic and researchId required"}

    logger.info(f"Received research id={research_id} mode={mode} topic={topic[:80]}")

    # Fire and forget — AgentCore Runtime HTTP response times out at ~5 min,
    # but research takes 5-45 min. save_report tool writes the result.
    threading.Thread(
        target=_run_research,
        args=(topic, mode, research_id),
        daemon=True,
    ).start()

    return {"status": "running", "researchId": research_id}


if __name__ == "__main__":
    app.run()
