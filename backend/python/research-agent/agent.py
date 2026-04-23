"""Ttobak Deep Research Agent — AgentCore Runtime.

Performs multi-source web research and generates citation-backed reports.
Deployed to AgentCore Runtime with Strands Agents SDK.
"""

import os
import json
import logging
from bedrock_agentcore.runtime import BedrockAgentCoreApp
from strands import Agent
from strands.models.bedrock import BedrockModel
from tools import web_search, fetch_page, save_report

logger = logging.getLogger(__name__)
logging.basicConfig(level=logging.INFO)

app = BedrockAgentCoreApp()

SYSTEM_PROMPT = """You are a Deep Research Agent for Ttobak, an AI meeting assistant for AWS Solutions Architects.

Your task is to perform comprehensive multi-source research on a given topic and produce a structured, citation-backed report in Korean.

## Research Pipeline

Follow these phases in order:

### Phase 1: SCOPE
- Analyze the topic carefully
- Identify 3-5 research angles
- Generate 5-10 search queries (mix Korean and English for broader coverage)

### Phase 2: PLAN
- Design report structure with 4-6 main sections
- Define what evidence each section needs
- Prioritize sections by importance to an AWS SA

### Phase 3: RETRIEVE
- Use fetch_page tool to gather information from multiple web sources
- For each search query, fetch the most relevant pages
- Collect 8-12 sources for standard mode, 5-8 for quick mode
- Extract key findings with source URLs
- Focus on: official docs, case studies, technical blogs, news

### Phase 4: TRIANGULATE
- Cross-verify claims across multiple sources
- Flag any contradictions between sources
- Note source credibility (official docs > blogs > forums)

### Phase 5: SYNTHESIZE
- Draft each section (600-2000 words each)
- Ensure every major claim cites at least 2 sources
- Write in Korean with technical terms in English where appropriate
- Use markdown formatting: headings, tables, bullet points, blockquotes

### Phase 6: CRITIQUE (standard/deep mode only)
- Self-review the draft
- Check for gaps, unsupported claims, or bias
- If critical gaps found, fetch additional sources and revise

### Phase 7: REFINE (deep mode only)
- Polish prose and flow
- Ensure executive summary captures all key insights
- Verify all citations are valid and URLs are included

### Phase 8: PACKAGE
- Generate final markdown report with this structure:
  - # Title
  - ## Executive Summary (200-400 words)
  - ## 1. Section Title (each 600-2000 words)
  - ...
  - ## Synthesis & Implications
  - ## References (numbered list with URLs)
- Call save_report tool with the complete report content

## Output Requirements
- Write in Korean (기술 용어는 영어 병기)
- Include source URLs for all major claims
- Executive summary: 200-400 words
- Each finding section: 600-2,000 words
- Format as clean markdown with tables where data comparison is needed

## Mode Behavior
The user message will specify a mode:
- quick: Phases 1, 3, 8 only. 5+ sources. Shorter sections.
- standard: Phases 1-6, 8. 8-12 sources. Full sections.
- deep: All 8 phases. 12-20 sources. Maximum depth.

## Tool Usage
- web_search: Search Google News RSS for Korean queries. Returns article titles + URLs.
- fetch_page: Fetch and extract text from a URL. Use for detailed content.
- save_report: Call ONCE at the very end with the complete markdown report.

IMPORTANT: You MUST call save_report at the end with the complete report. The researchId will be provided in the user message."""

REGION = os.environ.get("AWS_REGION", "ap-northeast-2")

MODEL_BY_MODE = {
    "quick": "global.anthropic.claude-sonnet-4-6",
    "standard": "global.anthropic.claude-sonnet-4-6",
    "deep": "us.anthropic.claude-opus-4-7",
}


def _create_agent(mode: str) -> Agent:
    model_id = MODEL_BY_MODE.get(mode, MODEL_BY_MODE["standard"])
    logger.info(f"Using model {model_id} for {mode} mode")
    model = BedrockModel(model_id=model_id, region_name=REGION)
    return Agent(
        model=model,
        system_prompt=SYSTEM_PROMPT,
        tools=[web_search, fetch_page, save_report],
    )


@app.entrypoint
def handle(payload):
    """AgentCore Runtime entrypoint."""
    topic = payload.get("topic", "")
    mode = payload.get("mode", "standard")
    research_id = payload.get("researchId", "")

    if not topic or not research_id:
        return {"error": "topic and researchId are required"}

    prompt = (
        f"Research ID: {research_id}\n"
        f"Mode: {mode}\n"
        f"Topic: {topic}\n\n"
        f"Please conduct the research following the {mode} mode pipeline. "
        f"Call save_report with researchId='{research_id}' when the report is complete."
    )

    logger.info(f"Starting research: id={research_id}, mode={mode}, topic={topic[:80]}")

    try:
        agent = _create_agent(mode)
        result = agent(prompt)
        return {"status": "completed", "researchId": research_id}
    except Exception as e:
        logger.error(f"Research failed: {e}", exc_info=True)
        # Update DynamoDB with error status
        try:
            import boto3
            table = boto3.resource("dynamodb").Table(os.environ.get("TABLE_NAME", "ttobak-main"))
            table.update_item(
                Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
                UpdateExpression="SET #s = :s, errorMessage = :e",
                ExpressionAttributeNames={"#s": "status"},
                ExpressionAttributeValues={":s": "error", ":e": str(e)[:500]},
            )
        except Exception:
            pass
        return {"error": str(e), "researchId": research_id}
