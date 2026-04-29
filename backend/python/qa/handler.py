import hashlib
import json
import os
import base64
import logging
import time
import boto3

from aws_docs import search_aws_docs
from prompts import get_system_prompt, DETECT_QUESTIONS_PROMPT
from tools import TOOL_DEFINITIONS, execute_tool

logger = logging.getLogger()
logger.setLevel(logging.INFO)

# Environment variables
TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_ID = os.environ.get('KB_ID', 'XGFBOMVSS8')
BEDROCK_MODEL_ID = os.environ.get('BEDROCK_MODEL_ID', 'global.anthropic.claude-sonnet-4-6')
DETECT_MODEL_ID = os.environ.get('DETECT_MODEL_ID', 'qwen.qwen3-32b-v1:0')

MAX_TOOL_ROUNDS = int(os.environ.get('MAX_TOOL_ROUNDS', '3'))
KB_CACHE_TTL_SECONDS = int(os.environ.get('KB_CACHE_TTL_SECONDS', '600'))

# AWS clients
bedrock_agent_runtime = boto3.client('bedrock-agent-runtime')
bedrock_runtime = boto3.client('bedrock-runtime')
s3_client = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
BUCKET_NAME = os.environ.get('BUCKET_NAME', 'ttobak-assets')
ORIGIN_VERIFY_SECRET = os.environ.get('ORIGIN_VERIFY_SECRET', '')
RESEARCH_SFN_ARN = os.environ.get('RESEARCH_SFN_ARN', '')


def create_research_from_chat(user_id, topic, mode):
    """Create a research job from the chat assistant. Mirrors Go CreateResearch logic."""
    import secrets
    from datetime import datetime, timezone

    research_id = secrets.token_hex(16)
    now = datetime.now(timezone.utc).isoformat()
    s3_key = f"shared/research/{research_id}.md"

    try:
        ddb_client = boto3.client('dynamodb')
        ddb_client.transact_write_items(TransactItems=[
            {"Put": {"TableName": TABLE_NAME, "Item": {
                "PK": {"S": f"RESEARCH#{research_id}"}, "SK": {"S": "CONFIG"},
                "entityType": {"S": "RESEARCH"},
                "researchId": {"S": research_id}, "userId": {"S": user_id},
                "topic": {"S": topic}, "mode": {"S": mode},
                "status": {"S": "planning"}, "createdAt": {"S": now}, "s3Key": {"S": s3_key},
            }}},
            {"Put": {"TableName": TABLE_NAME, "Item": {
                "PK": {"S": f"USER#{user_id}"}, "SK": {"S": f"RESEARCH#{research_id}"},
                "entityType": {"S": "RESEARCH_INDEX"}, "researchId": {"S": research_id},
            }}},
        ])
    except Exception as e:
        logger.error(f"Failed to create research in DynamoDB: {e}")
        return {"error": str(e)}

    if RESEARCH_SFN_ARN:
        try:
            sfn_client = boto3.client('stepfunctions')
            sfn_input = json.dumps({
                "researchId": research_id, "userId": user_id,
                "topic": topic, "mode": "plan", "qualityMode": mode, "s3Key": s3_key,
            })
            exec_name = f"research-{research_id[:8]}-plan-{secrets.token_hex(4)}"
            sfn_client.start_execution(
                stateMachineArn=RESEARCH_SFN_ARN, name=exec_name, input=sfn_input,
            )
        except Exception as e:
            logger.error(f"Failed to start research SFN: {e}")
            table.update_item(
                Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
                UpdateExpression="SET #s = :s, errorMessage = :e",
                ExpressionAttributeNames={"#s": "status"},
                ExpressionAttributeValues={":s": "error", ":e": f"Failed to start: {e}"},
            )
            return {"error": str(e)}

    return {"researchId": research_id}


def resolve_s3_ref(value):
    """Resolve s3:// reference to actual content. Returns original value if not an S3 ref."""
    if not isinstance(value, str) or not value.startswith('s3://'):
        return value
    try:
        # Parse s3://bucket/key
        path = value[5:]  # strip "s3://"
        bucket, key = path.split('/', 1)
        obj = s3_client.get_object(Bucket=bucket, Key=key)
        return obj['Body'].read().decode('utf-8')
    except Exception as e:
        logger.warning(f'Failed to resolve S3 reference {value[:60]}: {e}')
        return ''


def lambda_handler(event, context):
    """Main Lambda handler for API Gateway HTTP API v2.0 payload.

    Also handles async streaming invocations from the WebSocket Lambda
    (event shape: {"streamMode": "ask_live", "connectionId", "endpoint", ...}).
    """
    if event.get('streamMode') == 'ask_live':
        return handle_ask_stream(event)

    # Block direct API Gateway access — only allow requests through CloudFront
    if ORIGIN_VERIFY_SECRET:
        headers = event.get('headers', {})
        if headers.get('x-origin-verify', '') != ORIGIN_VERIFY_SECRET:
            return response(403, {'error': {'code': 'FORBIDDEN', 'message': 'direct access not allowed'}})

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

    # Extract userId from JWT (Authorization header) — required for all endpoints
    user_id = extract_user_id(event)
    if not user_id:
        return response(401, {'error': {'code': 'UNAUTHORIZED', 'message': 'Authentication required'}})

    # Route handling
    if path == '/api/qa/ask':
        question = body.get('question', '').strip()
        if not question:
            return response(400, {'error': {'code': 'BAD_REQUEST', 'message': 'question is required'}})
        return handle_ask(question, body.get('context'), body.get('meetingId'), body.get('sessionId'), user_id)
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


def _kb_cache_key(question, number_of_results, user_id=None):
    """Build a deterministic cache key for a KB query."""
    normalized = ' '.join(question.lower().split())
    raw = f"{user_id or ''}|{normalized}|{number_of_results}"
    digest = hashlib.sha256(raw.encode('utf-8')).hexdigest()
    return f"CACHE#KB#{digest}"


def _kb_cache_get(question, number_of_results, user_id=None):
    """Look up a cached KB retrieve() response. Returns list or None."""
    if KB_CACHE_TTL_SECONDS <= 0:
        return None
    try:
        result = table.get_item(Key={"PK": _kb_cache_key(question, number_of_results, user_id), "SK": "V1"})
        item = result.get("Item")
        if not item:
            return None
        if int(item.get("TTL", 0)) < int(time.time()):
            return None
        return json.loads(item["results"])
    except Exception as e:
        logger.warning(f"KB cache read failed: {e}")
        return None


def _kb_cache_put(question, number_of_results, results, user_id=None):
    """Store KB retrieve() response with TTL."""
    if KB_CACHE_TTL_SECONDS <= 0:
        return
    try:
        table.put_item(Item={
            "PK": _kb_cache_key(question, number_of_results, user_id),
            "SK": "V1",
            "results": json.dumps(results, ensure_ascii=False),
            "TTL": int(time.time()) + KB_CACHE_TTL_SECONDS,
        })
    except Exception as e:
        logger.warning(f"KB cache write failed: {e}")


# Cache shared-meeting lookups per user (warm for Lambda lifetime)
_shared_meetings_cache = {}


def _list_shared_meetings(user_id):
    """Query DynamoDB for meetings shared with this user.

    Returns list of {'meetingId': ..., 'ownerId': ...}.
    Results are cached in module-level dict keyed by user_id.
    """
    if user_id in _shared_meetings_cache:
        return _shared_meetings_cache[user_id]
    try:
        from boto3.dynamodb.conditions import Key
        resp = table.query(
            KeyConditionExpression=Key('PK').eq(f'USER#{user_id}') & Key('SK').begins_with('SHARED#'),
            ProjectionExpression='meetingId, ownerId',
        )
        items = [
            {'meetingId': item['meetingId'], 'ownerId': item['ownerId']}
            for item in resp.get('Items', [])
            if item.get('meetingId') and item.get('ownerId')
        ]
        _shared_meetings_cache[user_id] = items
        return items
    except Exception as e:
        logger.warning(f"Failed to list shared meetings for {user_id}: {e}")
        _shared_meetings_cache[user_id] = []
        return []


def list_meetings_for_user(user_id, date_from=None, date_to=None, tag=None, keyword=None, limit=None):
    """List meetings for a user (own + shared), with optional filters.

    Returns list of dicts: {meetingId, title, date, tags, status, isShared, sharedBy?}
    """
    from boto3.dynamodb.conditions import Key

    limit = limit or 20
    projection = 'meetingId, title, createdAt, tags, #s'
    expr_names = {'#s': 'status'}
    meetings = []

    # 1. Own meetings
    try:
        resp = table.query(
            KeyConditionExpression=Key('PK').eq(f'USER#{user_id}') & Key('SK').begins_with('MEETING#'),
            ProjectionExpression=projection,
            ExpressionAttributeNames=expr_names,
        )
        for item in resp.get('Items', []):
            meetings.append({
                'meetingId': item.get('meetingId', ''),
                'title': item.get('title', ''),
                'date': item.get('createdAt', ''),
                'tags': item.get('tags', []),
                'status': item.get('status', ''),
                'isShared': False,
            })
    except Exception as e:
        logger.warning(f"Failed to query own meetings for {user_id}: {e}")

    # 2. Shared meetings
    try:
        shared = _list_shared_meetings(user_id)
        for s in shared:
            try:
                resp = table.get_item(
                    Key={'PK': f"USER#{s['ownerId']}", 'SK': f"MEETING#{s['meetingId']}"},
                    ProjectionExpression=projection,
                    ExpressionAttributeNames=expr_names,
                )
                item = resp.get('Item')
                if item:
                    meetings.append({
                        'meetingId': item.get('meetingId', ''),
                        'title': item.get('title', ''),
                        'date': item.get('createdAt', ''),
                        'tags': item.get('tags', []),
                        'status': item.get('status', ''),
                        'isShared': True,
                        'sharedBy': s['ownerId'],
                    })
            except Exception as e:
                logger.warning(f"Failed to get shared meeting {s['meetingId']}: {e}")
    except Exception as e:
        logger.warning(f"Failed to list shared meetings for {user_id}: {e}")

    # 3. Apply client-side filters
    if date_from:
        meetings = [m for m in meetings if m['date'] >= date_from]
    if date_to:
        # Include the entire end date (compare with date_to + 'Z' to include full day)
        meetings = [m for m in meetings if m['date'] <= date_to + 'T23:59:59Z']
    if tag:
        tag_lower = tag.lower()
        meetings = [m for m in meetings if any(tag_lower in t.lower() for t in m.get('tags', []))]
    if keyword:
        kw_lower = keyword.lower()
        meetings = [m for m in meetings if kw_lower in (m.get('title') or '').lower()]

    # 4. Sort by date descending, limit
    meetings.sort(key=lambda m: m.get('date', ''), reverse=True)
    return meetings[:limit]


def retrieve_from_kb(question, number_of_results=5, user_id=None):
    """Retrieve relevant documents from Bedrock Knowledge Base, with short-lived DynamoDB cache."""
    capped = min(number_of_results, 10)

    cached = _kb_cache_get(question, capped, user_id)
    if cached is not None:
        logger.info(f"KB cache hit: query={question[:60]!r} n={capped}")
        return cached

    try:
        retrieval_config = {
            'vectorSearchConfiguration': {
                'numberOfResults': capped,
            }
        }
        # Filter: user's personal KB + user's meeting docs + shared crawler docs + shared meetings
        if user_id:
            filters = [
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'kb/{user_id}/'}},
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': f'meetings/{user_id}/'}},
                {'stringContains': {'key': 'x-amz-bedrock-kb-source-uri', 'value': 'shared/'}},
            ]
            # Include documents from meetings shared with this user
            for shared in _list_shared_meetings(user_id):
                filters.append({
                    'stringContains': {
                        'key': 'x-amz-bedrock-kb-source-uri',
                        'value': f"meetings/{shared['ownerId']}/{shared['meetingId']}",
                    }
                })
            retrieval_config['vectorSearchConfiguration']['filter'] = {'orAll': filters}
        resp = bedrock_agent_runtime.retrieve(
            knowledgeBaseId=KB_ID,
            retrievalQuery={'text': question},
            retrievalConfiguration=retrieval_config
        )
        results = []
        for item in resp.get('retrievalResults', []):
            score = item.get('score', 0)
            if score >= 0.5:
                text = item.get('content', {}).get('text', '')
                uri = item.get('location', {}).get('s3Location', {}).get('uri', '')
                if text:
                    results.append({'text': text, 'uri': uri, 'score': score})
        _kb_cache_put(question, capped, results, user_id)
        return results
    except Exception as e:
        logger.warning(f'KB retrieve failed: {e}')
        return []


def load_session(session_id, user_id=None):
    """Load conversation history from DynamoDB."""
    if not session_id:
        return []
    # Scope session key to user to prevent cross-user session access
    pk = f"SESSION#{user_id}#{session_id}" if user_id else f"SESSION#{session_id}"
    try:
        result = table.get_item(Key={"PK": pk, "SK": "MESSAGES"})
        item = result.get("Item")
        if item:
            return json.loads(item.get("messages", "[]"))
        return []
    except Exception as e:
        logger.warning(f"Failed to load session {session_id}: {e}")
        return []


def save_session(session_id, messages, user_id=None):
    """Save conversation history to DynamoDB with 7-day TTL."""
    if not session_id:
        return
    pk = f"SESSION#{user_id}#{session_id}" if user_id else f"SESSION#{session_id}"
    try:
        table.put_item(Item={
            "PK": pk,
            "SK": "MESSAGES",
            "messages": json.dumps(messages, ensure_ascii=False),
            "TTL": int(time.time()) + 604800,  # 7 days
        })
    except Exception as e:
        logger.warning(f"Failed to save session {session_id}: {e}")

    # Create/update CHAT_SESSION metadata for chat- prefixed sessions
    if user_id and session_id.startswith('chat-'):
        try:
            from datetime import datetime, timezone
            now = datetime.now(timezone.utc).isoformat()

            # Extract first user question text
            first_question = None
            msg_count = 0
            for msg in messages:
                role = msg.get('role', '')
                if role == 'user':
                    content = msg.get('content', [])
                    # Skip tool result messages
                    if isinstance(content, list) and content and isinstance(content[0], dict):
                        if 'toolResult' in content[0]:
                            continue
                        if first_question is None:
                            first_question = content[0].get('text', '')[:50]
                    msg_count += 1

            table.put_item(Item={
                "PK": f"USER#{user_id}",
                "SK": f"CHAT_SESSION#{session_id}",
                "sessionId": session_id,
                "title": first_question or '새 대화',
                "createdAt": now,
                "lastMessageAt": now,
                "messageCount": msg_count,
                "entityType": "CHAT_SESSION",
                "TTL": int(time.time()) + 2592000,  # 30 days
            })
        except Exception as e:
            logger.warning(f"Failed to save chat session metadata {session_id}: {e}")


def agentic_converse(messages, transcript=None, session_id=None, user_id=None):
    """Agentic tool-use loop: model decides what tools to call."""
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": lambda q, n=5: retrieve_from_kb(q, n, user_id=user_id),
        "list_meetings": list_meetings_for_user,
        "load_meeting_context": load_meeting_context,
        "create_research": lambda uid, topic, mode: create_research_from_chat(uid, topic, mode),
        "user_id": user_id,
    }
    tools_used = []
    sources = []

    # Build system messages: base prompt + optional meeting context
    system_messages = [{"text": get_system_prompt()}]
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
    save_session(session_id, messages, user_id=user_id)

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


def handle_ask(question, context=None, meeting_id=None, session_id=None, user_id=None):
    """Handle POST /api/qa/ask — agentic Q&A with tool-use loop."""
    try:
        # Load existing conversation or start new
        messages = load_session(session_id, user_id=user_id)

        # User message is just the question — context is in system prompt
        messages.append({"role": "user", "content": [{"text": question}]})

        answer, tools_used, sources = agentic_converse(
            messages,
            transcript=context,
            session_id=session_id,
            user_id=user_id,
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


def load_meeting_context(user_id, meeting_id):
    """Load meeting transcript from DynamoDB + S3 for QA context.

    Returns (transcript_string, error_dict_or_None).
    On success error_dict is None; on failure transcript is None.
    """
    try:
        result = table.get_item(
            Key={'PK': f'USER#{user_id}', 'SK': f'MEETING#{meeting_id}'}
        )
        item = result.get('Item')
        if not item:
            return None, {'code': 'NOT_FOUND', 'message': 'Meeting not found', 'status': 404}
        parts = []
        if item.get('title'):
            parts.append(f"제목: {item['title']}")
        if item.get('content'):
            parts.append(f"내용:\n{resolve_s3_ref(item['content'])}")
        if item.get('transcriptA'):
            parts.append(f"트랜스크립트:\n{resolve_s3_ref(item['transcriptA'])}")
        return '\n\n'.join(parts), None
    except Exception as e:
        logger.error(f'Failed to fetch meeting: {e}')
        return None, {'code': 'INTERNAL_ERROR', 'message': 'Failed to fetch meeting', 'status': 500}


def handle_meeting_ask(question, meeting_id, user_id, session_id=None):
    """Handle POST /api/qa/meeting/{meetingId} — meeting-context agentic Q&A."""
    if not user_id:
        return response(401, {'error': {'code': 'UNAUTHORIZED', 'message': 'Authentication required'}})

    transcript, err = load_meeting_context(user_id, meeting_id)
    if err:
        return response(err['status'], {'error': {'code': err['code'], 'message': err['message']}})

    try:
        messages = load_session(session_id, user_id=user_id)

        user_content = f"[미팅 '{meeting_id}'의 트랜스크립트가 있습니다. search_transcript 도구로 검색할 수 있습니다.]\n\n{question}"
        messages.append({"role": "user", "content": [{"text": user_content}]})

        answer, tools_used, sources = agentic_converse(
            messages,
            transcript=transcript,
            session_id=session_id,
            user_id=user_id,
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


# ---------------------------------------------------------------------------
# Streaming (WebSocket) path — invoked async from the Go websocket Lambda.
# Streams answer tokens back over WebSocket via PostToConnection.
# ---------------------------------------------------------------------------


def _apigw_client(endpoint):
    """Build an ApiGatewayManagementApi client bound to a WebSocket endpoint."""
    return boto3.client('apigatewaymanagementapi', endpoint_url=endpoint)


def _post_ws(apigw, connection_id, payload):
    """Post a JSON message to a WebSocket connection. Returns False if gone."""
    try:
        apigw.post_to_connection(
            ConnectionId=connection_id,
            Data=json.dumps(payload, ensure_ascii=False).encode('utf-8'),
        )
        return True
    except apigw.exceptions.GoneException:
        logger.info(f"WebSocket {connection_id} is gone; aborting stream")
        return False
    except Exception as e:
        logger.warning(f"post_to_connection failed: {e}")
        return False


def handle_ask_stream(event):
    """Handle a streaming ask invocation from the WebSocket Lambda.

    Expected event:
      {
        "streamMode": "ask_live",
        "connectionId": "...",
        "endpoint": "https://<api>.execute-api.<region>.amazonaws.com/<stage>",
        "question": "...",
        "context": "... optional transcript ...",
        "meetingId": "... optional ...",
        "sessionId": "... optional ...",
        "userId": "... from WebSocket authorizer ...",
      }
    """
    connection_id = event.get('connectionId')
    endpoint = event.get('endpoint')
    question = (event.get('question') or '').strip()
    transcript = event.get('context')
    session_id = event.get('sessionId')
    user_id = event.get('userId')

    if not connection_id or not endpoint or not question:
        logger.warning("ask_live invocation missing required fields")
        return {'status': 'bad_request'}

    apigw = _apigw_client(endpoint)

    if not _post_ws(apigw, connection_id, {
        'type': 'answer_start',
        'sessionId': session_id,
    }):
        return {'status': 'gone'}

    try:
        messages = load_session(session_id, user_id=user_id)
        user_content = question
        if transcript:
            user_content = f"[현재 미팅 트랜스크립트가 있습니다. search_transcript 도구로 검색할 수 있습니다.]\n\n{question}"
        messages.append({"role": "user", "content": [{"text": user_content}]})

        answer, tools_used, sources = agentic_converse_stream(
            messages,
            transcript=transcript,
            session_id=session_id,
            user_id=user_id,
            apigw=apigw,
            connection_id=connection_id,
        )

        _post_ws(apigw, connection_id, {
            'type': 'answer_complete',
            'sessionId': session_id,
            'answer': answer,
            'sources': sources,
            'toolsUsed': list(set(tools_used)),
            'usedKB': 'search_knowledge_base' in tools_used,
            'usedDocs': 'search_aws_docs' in tools_used,
        })
    except Exception as e:
        logger.error(f"handle_ask_stream failed: {e}", exc_info=True)
        _post_ws(apigw, connection_id, {
            'type': 'answer_error',
            'sessionId': session_id,
            'error': '답변 생성 중 오류가 발생했습니다.',
        })
        return {'status': 'error'}

    return {'status': 'ok'}


def agentic_converse_stream(messages, transcript, session_id, user_id, apigw, connection_id):
    """Agentic tool-use loop using ConverseStream. Streams text deltas to the WebSocket."""
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": lambda q, n=5: retrieve_from_kb(q, n, user_id=user_id),
        "list_meetings": list_meetings_for_user,
        "load_meeting_context": load_meeting_context,
        "create_research": lambda uid, topic, mode: create_research_from_chat(uid, topic, mode),
        "user_id": user_id,
    }
    tools_used = []
    sources = []
    final_answer_parts = []

    for _ in range(MAX_TOOL_ROUNDS):
        try:
            stream_resp = bedrock_runtime.converse_stream(
                modelId=BEDROCK_MODEL_ID,
                system=[{"text": get_system_prompt()}],
                messages=messages,
                toolConfig={"tools": TOOL_DEFINITIONS},
                inferenceConfig={"maxTokens": 4096, "temperature": 0.3},
            )
        except Exception as e:
            logger.error(f"Bedrock converse_stream failed: {e}", exc_info=True)
            _post_ws(apigw, connection_id, {
                'type': 'answer_delta',
                'sessionId': session_id,
                'text': '\n(응답 생성 중 오류)',
            })
            break

        assembled_content = []
        current_block = None
        stop_reason = None
        round_text = ''

        for ev in stream_resp.get('stream', []):
            if 'messageStart' in ev:
                continue
            if 'contentBlockStart' in ev:
                start = ev['contentBlockStart'].get('start', {})
                if 'toolUse' in start:
                    current_block = {
                        'toolUse': {
                            'toolUseId': start['toolUse']['toolUseId'],
                            'name': start['toolUse']['name'],
                            'input': '',
                        }
                    }
                else:
                    current_block = {'text': ''}
                continue
            if 'contentBlockDelta' in ev:
                delta = ev['contentBlockDelta'].get('delta', {})
                if 'text' in delta and current_block is not None and 'text' in current_block:
                    current_block['text'] += delta['text']
                    round_text += delta['text']
                    _post_ws(apigw, connection_id, {
                        'type': 'answer_delta',
                        'sessionId': session_id,
                        'text': delta['text'],
                    })
                elif 'toolUse' in delta and current_block is not None and 'toolUse' in current_block:
                    current_block['toolUse']['input'] += delta['toolUse'].get('input', '')
                continue
            if 'contentBlockStop' in ev:
                if current_block is not None:
                    if 'toolUse' in current_block:
                        raw_input = current_block['toolUse']['input']
                        try:
                            current_block['toolUse']['input'] = json.loads(raw_input) if raw_input else {}
                        except json.JSONDecodeError:
                            logger.warning(f"Tool input JSON parse failed: {raw_input!r}")
                            current_block['toolUse']['input'] = {}
                    assembled_content.append(current_block)
                    current_block = None
                continue
            if 'messageStop' in ev:
                stop_reason = ev['messageStop'].get('stopReason')
                continue
            if 'metadata' in ev:
                continue

        messages.append({"role": "assistant", "content": assembled_content})
        if round_text:
            final_answer_parts.append(round_text)

        if stop_reason == 'end_turn' or stop_reason is None:
            break

        if stop_reason == 'tool_use':
            tool_results = []
            for block in assembled_content:
                if 'toolUse' not in block:
                    continue
                tool = block['toolUse']
                logger.info(f"Tool call (stream): {tool['name']}")
                try:
                    result, result_sources = execute_tool(tool['name'], tool['input'], context)
                except Exception as e:
                    logger.warning(f"Tool execution failed ({tool['name']}): {e}")
                    result = f"도구 실행 중 오류가 발생했습니다: {tool['name']}"
                    result_sources = []
                tools_used.append(tool['name'])
                sources.extend(result_sources)
                tool_results.append({
                    'toolResult': {
                        'toolUseId': tool['toolUseId'],
                        'content': [{'text': result}],
                    }
                })
            messages.append({'role': 'user', 'content': tool_results})

    save_session(session_id, messages, user_id=user_id)

    seen = set()
    unique_sources = []
    for s in sources:
        if s and s not in seen:
            seen.add(s)
            unique_sources.append(s)

    return '\n'.join(final_answer_parts), tools_used, unique_sources
