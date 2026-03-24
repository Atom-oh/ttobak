import json
import os
import base64
import logging
import time
import boto3

from aws_docs import search_aws_docs
from prompts import SYSTEM_PROMPT, DETECT_QUESTIONS_PROMPT
from tools import TOOL_DEFINITIONS, execute_tool

logger = logging.getLogger()
logger.setLevel(logging.INFO)

# Environment variables
TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_ID = os.environ.get('KB_ID', 'XGFBOMVSS8')
BEDROCK_MODEL_ID = os.environ.get('BEDROCK_MODEL_ID', 'global.anthropic.claude-opus-4-6-v1')
DETECT_MODEL_ID = os.environ.get('DETECT_MODEL_ID', 'global.anthropic.claude-haiku-4-5-20251001-v1:0')

MAX_TOOL_ROUNDS = 5

# AWS clients
bedrock_agent_runtime = boto3.client('bedrock-agent-runtime')
bedrock_runtime = boto3.client('bedrock-runtime')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)


def lambda_handler(event, context):
    """Main Lambda handler for API Gateway HTTP API v2.0 payload."""
    http_method = event.get('requestContext', {}).get('http', {}).get('method', '')
    path = event.get('rawPath', '')

    if http_method != 'POST':
        return response(405, {'error': {'code': 'BAD_REQUEST', 'message': 'Method not allowed'}})

    # Parse request body
    body = event.get('body', '{}')
    if event.get('isBase64Encoded'):
        body = base64.b64decode(body).decode('utf-8')
    try:
        body = json.loads(body)
    except json.JSONDecodeError:
        return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'Invalid JSON body'}})

    # Extract userId from JWT (Authorization header)
    user_id = extract_user_id(event)

    # Route handling
    if path == '/api/qa/ask':
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        return handle_ask(question, body.get('context'), body.get('meetingId'), body.get('sessionId'))
    elif path == '/api/qa/detect-questions':
        return handle_detect_questions(body)
    elif path.startswith('/api/qa/meeting/'):
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        meeting_id = path.split('/api/qa/meeting/')[1].split('/')[0]
        if not meeting_id:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'meetingId is required'}})
        return handle_meeting_ask(question, meeting_id, user_id, body.get('sessionId'))
    else:
        return response(404, {'error': {'code': 'NOT_FOUND', 'message': 'Route not found'}})


def extract_user_id(event):
    """Extract userId from JWT token in Authorization header."""
    headers = event.get('headers', {})
    auth = headers.get('authorization', '') or headers.get('Authorization', '')
    if not auth.startswith('Bearer '):
        return None
    token = auth[7:]
    try:
        # Decode JWT payload (no verification — Lambda@Edge already validated)
        payload = token.split('.')[1]
        # Add padding
        payload += '=' * (4 - len(payload) % 4)
        decoded = json.loads(base64.b64decode(payload))
        return decoded.get('sub') or decoded.get('cognito:username')
    except Exception:
        return None


def retrieve_from_kb(question, number_of_results=5):
    """Retrieve relevant documents from Bedrock Knowledge Base."""
    try:
        resp = bedrock_agent_runtime.retrieve(
            knowledgeBaseId=KB_ID,
            retrievalQuery={'text': question},
            retrievalConfiguration={
                'vectorSearchConfiguration': {
                    'numberOfResults': min(number_of_results, 10),
                }
            }
        )
        results = []
        for item in resp.get('retrievalResults', []):
            score = item.get('score', 0)
            if score >= 0.5:
                text = item.get('content', {}).get('text', '')
                uri = item.get('location', {}).get('s3Location', {}).get('uri', '')
                if text:
                    results.append({'text': text, 'uri': uri, 'score': score})
        return results
    except Exception as e:
        logger.warning(f'KB retrieve failed: {e}')
        return []


def load_session(session_id):
    """Load conversation history from DynamoDB."""
    if not session_id:
        return []
    try:
        result = table.get_item(Key={"PK": f"SESSION#{session_id}", "SK": "MESSAGES"})
        item = result.get("Item")
        if item:
            return json.loads(item.get("messages", "[]"))
        return []
    except Exception as e:
        logger.warning(f"Failed to load session {session_id}: {e}")
        return []


def save_session(session_id, messages):
    """Save conversation history to DynamoDB with 24h TTL."""
    if not session_id:
        return
    try:
        table.put_item(Item={
            "PK": f"SESSION#{session_id}",
            "SK": "MESSAGES",
            "messages": json.dumps(messages, ensure_ascii=False),
            "TTL": int(time.time()) + 86400,
        })
    except Exception as e:
        logger.warning(f"Failed to save session {session_id}: {e}")


def agentic_converse(messages, transcript=None, session_id=None):
    """Agentic tool-use loop: model decides what tools to call."""
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": retrieve_from_kb,
    }
    tools_used = []
    sources = []

    # Build system messages: base prompt + optional meeting context
    system_messages = [{"text": SYSTEM_PROMPT}]
    if transcript:
        truncated = transcript[-2000:] if len(transcript) > 2000 else transcript
        system_messages.append({"text": f"\n\n## 현재 미팅 대화 내용 (실시간)\n{truncated}\n\n위 대화 맥락에 기반하여 답변하세요. 미팅 내용과 관련없는 질문이라도 가능한 한 대화 맥락을 참조하세요."})

    for _ in range(MAX_TOOL_ROUNDS):
        try:
            resp = bedrock_runtime.converse(
                modelId=BEDROCK_MODEL_ID,
                system=system_messages,
                messages=messages,
                toolConfig={"tools": TOOL_DEFINITIONS},
                inferenceConfig={"maxTokens": 4096, "temperature": 0.3},
            )
        except Exception as e:
            logger.error(f"Bedrock converse failed: {e}", exc_info=True)
            return "죄송합니다. AI 응답 생성 중 오류가 발생했습니다. 잠시 후 다시 시도해주세요.", [], []

        output_message = resp["output"]["message"]
        messages.append(output_message)
        stop_reason = resp["stopReason"]

        if stop_reason == "end_turn":
            break

        if stop_reason == "tool_use":
            tool_results = []
            for block in output_message["content"]:
                if "toolUse" in block:
                    tool = block["toolUse"]
                    logger.info(f"Tool call: {tool['name']} input={json.dumps(tool['input'], ensure_ascii=False)}")
                    try:
                        result, result_sources = execute_tool(
                            tool["name"], tool["input"], context
                        )
                    except Exception as e:
                        logger.warning(f"Tool execution failed ({tool['name']}): {e}")
                        result = f"도구 실행 중 오류가 발생했습니다: {tool['name']}"
                        result_sources = []
                    tools_used.append(tool["name"])
                    sources.extend(result_sources)

                    tool_results.append({
                        "toolResult": {
                            "toolUseId": tool["toolUseId"],
                            "content": [{"text": result}]
                        }
                    })
            messages.append({"role": "user", "content": tool_results})

    # Extract final text answer
    answer = extract_text_answer(output_message)

    # Save conversation
    save_session(session_id, messages)

    # Deduplicate sources
    seen = set()
    unique_sources = []
    for s in sources:
        if s and s not in seen:
            seen.add(s)
            unique_sources.append(s)

    return answer, tools_used, unique_sources


def extract_text_answer(message):
    """Extract text content from a Bedrock Converse message."""
    parts = []
    for block in message.get("content", []):
        if "text" in block:
            parts.append(block["text"])
    return "\n".join(parts) if parts else ""


def handle_ask(question, context=None, meeting_id=None, session_id=None):
    """Handle POST /api/qa/ask — agentic Q&A with tool-use loop."""
    try:
        # Load existing conversation or start new
        messages = load_session(session_id)

        # User message is just the question — context is in system prompt
        messages.append({"role": "user", "content": [{"text": question}]})

        answer, tools_used, sources = agentic_converse(
            messages,
            transcript=context,
            session_id=session_id,
        )

        return response(200, {
            'answer': answer,
            'sources': sources,
            'usedKB': 'search_knowledge_base' in tools_used,
            'usedDocs': 'search_aws_docs' in tools_used,
            'toolsUsed': list(set(tools_used)),
        })
    except Exception as e:
        logger.error(f'handle_ask failed: {e}', exc_info=True)
        return response(500, {'error': {'code': 'INTERNAL_ERROR', 'message': 'Failed to generate answer'}})


def handle_meeting_ask(question, meeting_id, user_id, session_id=None):
    """Handle POST /api/qa/meeting/{meetingId} — meeting-context agentic Q&A."""
    # 1. Fetch meeting from DynamoDB
    transcript = None
    if user_id:
        try:
            result = table.get_item(
                Key={'PK': f'USER#{user_id}', 'SK': f'MEETING#{meeting_id}'}
            )
            item = result.get('Item')
            if item:
                parts = []
                if item.get('title'):
                    parts.append(f"제목: {item['title']}")
                if item.get('content'):
                    parts.append(f"내용:\n{item['content']}")
                if item.get('transcriptA'):
                    parts.append(f"트랜스크립트:\n{item['transcriptA']}")
                transcript = '\n\n'.join(parts)
            else:
                return response(404, {'error': {'code': 'NOT_FOUND', 'message': 'Meeting not found'}})
        except Exception as e:
            logger.error(f'Failed to fetch meeting: {e}')
            return response(500, {'error': {'code': 'INTERNAL_ERROR', 'message': 'Failed to fetch meeting'}})
    else:
        return response(401, {'error': {'code': 'UNAUTHORIZED', 'message': 'Authentication required'}})

    # 2. Agentic conversation
    try:
        messages = load_session(session_id)

        user_content = f"[미팅 '{meeting_id}'의 트랜스크립트가 있습니다. search_transcript 도구로 검색할 수 있습니다.]\n\n{question}"
        messages.append({"role": "user", "content": [{"text": user_content}]})

        answer, tools_used, sources = agentic_converse(
            messages,
            transcript=transcript,
            session_id=session_id,
        )

        return response(200, {
            'answer': answer,
            'sources': sources,
            'usedKB': 'search_knowledge_base' in tools_used,
            'usedDocs': 'search_aws_docs' in tools_used,
            'toolsUsed': list(set(tools_used)),
        })
    except Exception as e:
        logger.error(f'handle_meeting_ask failed: {e}', exc_info=True)
        return response(500, {'error': {'code': 'INTERNAL_ERROR', 'message': 'Failed to generate answer'}})


def handle_detect_questions(body):
    """Handle POST /api/qa/detect-questions — extract topic-aware questions from transcript."""
    transcript = body.get('transcript', '').strip()
    if not transcript:
        return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'transcript is required'}})

    summary = body.get('summary', '').strip()
    previous_questions = body.get('previousQuestions', [])

    # Build context: summary (for topic understanding) + transcript
    user_content = ''
    if summary:
        user_content += f'## 현재 미팅 요약\n{summary}\n\n'
    user_content += f'## 최근 대화 내용\n{transcript}'
    if previous_questions:
        user_content += '\n\n이미 제안된 질문:\n' + '\n'.join(f'- {q}' for q in previous_questions)

    try:
        resp = bedrock_runtime.converse(
            modelId=DETECT_MODEL_ID,
            system=[{'text': DETECT_QUESTIONS_PROMPT}],
            messages=[
                {'role': 'user', 'content': [{'text': user_content}]}
            ],
            inferenceConfig={
                'maxTokens': 512,
                'temperature': 0.2,
            }
        )

        answer = ''
        for block in resp.get('output', {}).get('message', {}).get('content', []):
            if 'text' in block:
                answer += block['text']

        # Parse JSON array from response
        questions = json.loads(answer.strip())
        if not isinstance(questions, list):
            questions = []
        questions = [q for q in questions if isinstance(q, str)][:5]
    except Exception as e:
        logger.warning(f'Question detection failed: {e}')
        questions = []

    return response(200, {'questions': questions})


def response(status_code, body):
    """Build API Gateway v2.0 response."""
    return {
        'statusCode': status_code,
        'headers': {
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*',
            'Access-Control-Allow-Headers': '*',
            'Access-Control-Allow-Methods': 'POST, OPTIONS',
        },
        'body': json.dumps(body, ensure_ascii=False),
    }
