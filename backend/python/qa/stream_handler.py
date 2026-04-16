"""Lambda response streaming handler for QA chatbot SSE.

Uses Bedrock converse_stream() for real-time token-by-token streaming.
Invoked via Lambda Function URL with RESPONSE_STREAM invoke mode.
"""

import json
import base64
import logging

from handler import (
    extract_user_id,
    load_meeting_context,
    load_session,
    save_session,
    retrieve_from_kb,
    bedrock_runtime,
    BEDROCK_MODEL_ID,
    MAX_TOOL_ROUNDS,
)
from prompts import SYSTEM_PROMPT
from tools import TOOL_DEFINITIONS, execute_tool

logger = logging.getLogger()
logger.setLevel(logging.INFO)


def write_sse(stream, event_type, data):
    """Write a single SSE event to the response stream."""
    payload = json.dumps({"type": event_type, **data}, ensure_ascii=False)
    stream.write(f"data: {payload}\n\n".encode("utf-8"))


def agentic_converse_stream(response_stream, messages, transcript=None, session_id=None, user_id=None):
    """Agentic tool-use loop with streaming final answer.

    Text deltas are streamed to the client in real-time via SSE.
    Tool-use rounds execute synchronously, then the loop continues.
    """
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": lambda q, n=5: retrieve_from_kb(q, n, user_id=user_id),
    }
    tools_used = []
    sources = []

    system_messages = [{"text": SYSTEM_PROMPT}]
    if transcript:
        truncated = transcript[-2000:] if len(transcript) > 2000 else transcript
        system_messages.append({
            "text": f"\n\n## 현재 미팅 대화 내용 (실시간)\n{truncated}\n\n위 대화 맥락에 기반하여 답변하세요. 미팅 내용과 관련없는 질문이라도 가능한 한 대화 맥락을 참조하세요."
        })

    for round_num in range(MAX_TOOL_ROUNDS):
        try:
            resp = bedrock_runtime.converse_stream(
                modelId=BEDROCK_MODEL_ID,
                system=system_messages,
                messages=messages,
                toolConfig={"tools": TOOL_DEFINITIONS},
                inferenceConfig={"maxTokens": 4096, "temperature": 0.3},
            )
        except Exception as e:
            logger.error(f"Bedrock converse_stream failed: {e}", exc_info=True)
            write_sse(response_stream, "error", {"text": "AI 응답 생성 중 오류가 발생했습니다."})
            return messages, tools_used, sources

        # Read the stream and collect blocks
        full_text = ""
        tool_parts = {}  # block_index -> {name, id, input}
        stop_reason = None

        for event in resp["stream"]:
            if "contentBlockStart" in event:
                idx = event["contentBlockStart"]["contentBlockIndex"]
                start = event["contentBlockStart"].get("start", {})
                if "toolUse" in start:
                    tool_parts[idx] = {
                        "name": start["toolUse"]["name"],
                        "id": start["toolUse"]["toolUseId"],
                        "input": "",
                    }
            elif "contentBlockDelta" in event:
                idx = event["contentBlockDelta"]["contentBlockIndex"]
                delta = event["contentBlockDelta"]["delta"]
                if "text" in delta:
                    text = delta["text"]
                    full_text += text
                    # Stream text chunk to client immediately
                    write_sse(response_stream, "chunk", {"text": text})
                elif "toolUse" in delta:
                    if idx in tool_parts:
                        tool_parts[idx]["input"] += delta["toolUse"]["input"]
            elif "messageStop" in event:
                stop_reason = event["messageStop"]["stopReason"]

        # Reconstruct assistant message for conversation history
        message_content = []
        if full_text:
            message_content.append({"text": full_text})
        for idx in sorted(tool_parts.keys()):
            tp = tool_parts[idx]
            message_content.append({
                "toolUse": {
                    "toolUseId": tp["id"],
                    "name": tp["name"],
                    "input": json.loads(tp["input"]),
                }
            })
        messages.append({"role": "assistant", "content": message_content})

        if stop_reason == "end_turn":
            break

        if stop_reason == "tool_use":
            tool_results = []
            for idx in sorted(tool_parts.keys()):
                tp = tool_parts[idx]
                tool_input = json.loads(tp["input"])
                logger.info(f"Tool call: {tp['name']} input={json.dumps(tool_input, ensure_ascii=False)}")
                try:
                    result, result_sources = execute_tool(tp["name"], tool_input, context)
                except Exception as e:
                    logger.warning(f"Tool execution failed ({tp['name']}): {e}")
                    result = f"도구 실행 중 오류가 발생했습니다: {tp['name']}"
                    result_sources = []
                tools_used.append(tp["name"])
                sources.extend(result_sources)
                tool_results.append({
                    "toolResult": {
                        "toolUseId": tp["id"],
                        "content": [{"text": result}],
                    }
                })
            messages.append({"role": "user", "content": tool_results})

    # Deduplicate sources
    seen = set()
    unique_sources = [s for s in sources if s and s not in seen and not seen.add(s)]

    return messages, tools_used, unique_sources


def handle_stream(response_stream, event):
    """Route and handle streaming QA requests."""
    path = event.get("rawPath", "")
    http_method = event.get("requestContext", {}).get("http", {}).get("method", "")

    # CORS preflight
    if http_method == "OPTIONS":
        write_sse(response_stream, "done", {})
        return

    if http_method != "POST":
        write_sse(response_stream, "error", {"text": "Method not allowed"})
        return

    # Parse body
    body = event.get("body", "{}")
    if event.get("isBase64Encoded"):
        body = base64.b64decode(body).decode("utf-8")
    try:
        body = json.loads(body)
    except json.JSONDecodeError:
        write_sse(response_stream, "error", {"text": "Invalid JSON body"})
        return

    user_id = extract_user_id(event)
    if not user_id:
        write_sse(response_stream, "error", {"text": "Authentication required"})
        return

    question = body.get("question", "").strip()
    if not question:
        write_sse(response_stream, "error", {"text": "question is required"})
        return

    session_id = body.get("sessionId")
    transcript = None
    meeting_id = None

    # Route: /api/qa/stream/meeting/{meetingId}
    if "/api/qa/stream/meeting/" in path:
        meeting_id = path.split("/api/qa/stream/meeting/")[1].split("/")[0]
        if not meeting_id:
            write_sse(response_stream, "error", {"text": "meetingId is required"})
            return
        transcript, err = load_meeting_context(user_id, meeting_id)
        if err:
            write_sse(response_stream, "error", {"text": err["message"]})
            return
    elif path == "/api/qa/stream/ask":
        transcript = body.get("context")
    else:
        write_sse(response_stream, "error", {"text": "Route not found"})
        return

    # Load session and build user message
    messages = load_session(session_id, user_id=user_id)
    if meeting_id:
        user_content = f"[미팅 '{meeting_id}'의 트랜스크립트가 있습니다. search_transcript 도구로 검색할 수 있습니다.]\n\n{question}"
    else:
        user_content = question
    messages.append({"role": "user", "content": [{"text": user_content}]})

    # Run agentic streaming conversation
    messages, tools_used, sources = agentic_converse_stream(
        response_stream,
        messages,
        transcript=transcript,
        session_id=session_id,
        user_id=user_id,
    )

    # Save session
    save_session(session_id, messages, user_id=user_id)

    # Send metadata
    write_sse(response_stream, "meta", {
        "sources": sources,
        "usedKB": "search_knowledge_base" in tools_used,
        "usedDocs": "search_aws_docs" in tools_used,
        "toolsUsed": list(set(tools_used)),
    })

    # Signal completion
    write_sse(response_stream, "done", {})


def lambda_handler(event, response_stream, context):
    """Lambda response streaming entry point for Function URL."""
    response_stream.content_type = "text/event-stream"
    try:
        handle_stream(response_stream, event)
    except Exception as e:
        logger.error(f"Stream handler error: {e}", exc_info=True)
        try:
            write_sse(response_stream, "error", {"text": "Internal server error"})
        except Exception:
            pass
    finally:
        response_stream.close()
