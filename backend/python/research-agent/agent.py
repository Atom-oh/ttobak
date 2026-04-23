"""Ttobak Deep Research Agent — AgentCore Runtime (Container).

stdlib http.server for instant port 8080 binding (health check).
Strands agent loads lazily on first invocation.
"""

import http.server
import json
import logging
import os
import sys

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("research-agent")

PORT = 8080
REGION = os.environ.get("AWS_REGION", "ap-northeast-2")
TABLE_NAME = os.environ.get("TABLE_NAME", "ttobak-main")

MODEL_BY_MODE = {
    "quick": "global.anthropic.claude-sonnet-4-6",
    "standard": "global.anthropic.claude-sonnet-4-6",
    "deep": "us.anthropic.claude-opus-4-7",
}

SYSTEM_PROMPT = """You are a Deep Research Agent for Ttobak, an AI meeting assistant for AWS Solutions Architects.

Perform comprehensive multi-source research and produce a structured, citation-backed report in Korean.

## Research Pipeline
Follow these phases: SCOPE → RETRIEVE → SYNTHESIZE → PACKAGE

## Tools
- web_search: Search Google News RSS for Korean/English queries
- fetch_page: Fetch and extract text from a URL
- save_report: Save the final report (MUST be called at the end)

## Output
- Korean with English technical terms
- Markdown with ## headings, tables, bullet points
- Executive summary 200-400 words, sections 600-2000 words each
- Include source URLs

## Mode
- quick: 5+ sources, shorter
- standard: 8-12 sources, full with critique
- deep: 12-20 sources, maximum depth

CRITICAL: You MUST call save_report at the end with the complete markdown report. If you do not, the research will fail."""

_agents = {}


def _get_agent(mode):
    if mode in _agents:
        return _agents[mode]

    logger.info(f"Building agent for mode={mode}...")
    from strands import Agent
    from strands.models.bedrock import BedrockModel
    from tools import web_search, fetch_page, save_report

    model_id = MODEL_BY_MODE.get(mode, MODEL_BY_MODE["standard"])
    model = BedrockModel(model_id=model_id, region_name=REGION)
    agent = Agent(
        model=model,
        system_prompt=SYSTEM_PROMPT,
        tools=[web_search, fetch_page, save_report],
    )
    _agents[mode] = agent
    logger.info(f"Agent ready: mode={mode}, model={model_id}")
    return agent


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"Healthy"}')

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body)
        except Exception:
            self._respond(400, {"error": "invalid JSON"})
            return

        topic = payload.get("topic", "")
        mode = payload.get("mode", "standard")
        research_id = payload.get("researchId", "")

        if not topic or not research_id:
            self._respond(400, {"error": "topic and researchId required"})
            return

        logger.info(f"Research: id={research_id} mode={mode} topic={topic[:80]}")

        try:
            agent = _get_agent(mode)
        except Exception as e:
            logger.error(f"Agent init failed: {e}", exc_info=True)
            self._update_error(research_id, f"Agent init: {e}")
            self._respond(500, {"error": f"init failed: {e}"})
            return

        prompt = (
            f"Research ID: {research_id}\n"
            f"Mode: {mode}\n"
            f"Topic: {topic}\n\n"
            f"Conduct research following the {mode} pipeline. "
            f"Call save_report with researchId='{research_id}' when the report is complete."
        )

        try:
            agent(prompt)
            self._respond(200, {"status": "completed", "researchId": research_id})
        except Exception as e:
            logger.error(f"Research failed: {e}", exc_info=True)
            self._update_error(research_id, str(e)[:500])
            self._respond(500, {"error": str(e)[:300], "researchId": research_id})

    def _respond(self, code, data):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(data).encode())

    def _update_error(self, research_id, msg):
        try:
            import boto3
            boto3.resource("dynamodb").Table(TABLE_NAME).update_item(
                Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
                UpdateExpression="SET #s = :s, errorMessage = :e",
                ExpressionAttributeNames={"#s": "status"},
                ExpressionAttributeValues={":s": "error", ":e": msg},
            )
        except Exception:
            pass

    def log_message(self, format, *args):
        if "/ping" not in str(args):
            logger.info(format % args)


if __name__ == "__main__":
    sys.stdout.write(f"Research agent starting pid={os.getpid()}\n")
    sys.stdout.flush()
    with http.server.HTTPServer(("0.0.0.0", PORT), Handler) as httpd:
        logger.info(f"Listening on {PORT}")
        httpd.serve_forever()
