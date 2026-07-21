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
KB_ID = os.environ.get('KB_ID', 'BJJLVLFTOR')
BEDROCK_MODEL_ID = os.environ.get('BEDROCK_MODEL_ID', 'global.anthropic.claude-sonnet-5')
DETECT_MODEL_ID = os.environ.get('DETECT_MODEL_ID', 'qwen.qwen3-32b-v1:0')

MAX_TOOL_ROUNDS = int(os.environ.get('MAX_TOOL_ROUNDS', '3'))
# NOTE: retrieve_from_kb (below) always computes a live access signature via
# _list_shared_meetings BEFORE consulting this cache, and _kb_cache_get
# rejects a hit whose stored signature doesn't match the live one -- so a
# removed member re-asking a cached question does NOT get a stale answer,
# even within this TTL. The cache only saves the Bedrock retrieve() call
# for a caller whose access is unchanged.
KB_CACHE_TTL_SECONDS = int(os.environ.get('KB_CACHE_TTL_SECONDS', '600'))
# Bounds how long _list_shared_meetings_raw's Query results (the immutable
# meetingId/ownerId identity of each share -- NOT any authorization
# decision) are cached. Every authorization-relevant fact (share existence,
# origin, the meeting's sharedToAccount, live membership) is re-checked on
# every _list_shared_meetings call, uncached, so a removed member's very
# next QA request sees the revocation immediately regardless of this TTL.
SHARED_MEETINGS_CACHE_TTL_SECONDS = int(os.environ.get('SHARED_MEETINGS_CACHE_TTL_SECONDS', '300'))

# AWS clients
bedrock_agent_runtime = boto3.client('bedrock-agent-runtime')
bedrock_runtime = boto3.client('bedrock-runtime')
s3_client = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)
BUCKET_NAME = os.environ.get('BUCKET_NAME', 'ttobak-assets')
ORIGIN_VERIFY_SECRET = os.environ.get('ORIGIN_VERIFY_SECRET', '')
RESEARCH_SFN_ARN = os.environ.get('RESEARCH_SFN_ARN', '')
DAILY_RESEARCH_LIMIT = 5


def check_research_limit(user_id):
    """Check if user has exceeded daily research creation limit using an atomic counter."""
    from datetime import datetime, timezone
    today = datetime.now(timezone.utc).strftime('%Y-%m-%d')
    counter_pk = f"USER#{user_id}"
    counter_sk = f"RESEARCH_DAILY#{today}"
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
        if current > DAILY_RESEARCH_LIMIT:
            table.update_item(
                Key={"PK": counter_pk, "SK": counter_sk},
                UpdateExpression="SET #c = #c - :one",
                ExpressionAttributeNames={"#c": "count"},
                ExpressionAttributeValues={":one": 1},
            )
            return False
        return True
    except Exception as e:
        logger.warning(f"Failed to check research limit for {user_id}: {e}")
        return True


def create_research_from_chat(user_id, topic, mode):
    """Create a research job from the chat assistant.

    SCHEMA SYNC: Must match Go ResearchService.CreateResearch exactly:
    - PK: RESEARCH#{id}, SK: CONFIG, entityType: RESEARCH
    - PK: USER#{userId}, SK: RESEARCH#{id}, entityType: RESEARCH_INDEX
    - SFN input keys: researchId, userId, topic, mode, qualityMode, s3Key
    - s3Key format: shared/research/{id}.md
    Last synced with Go: 2026-04-29 (PR #59)

    TODO: Replace with internal Go API call to eliminate schema duplication.
    """
    import secrets
    from datetime import datetime, timezone

    if not topic or not topic.strip():
        return {"error": "topic is required"}
    topic = topic.strip()[:500]
    if mode not in ("quick", "standard", "deep"):
        mode = "standard"

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
        return {"error": "Failed to create research"}

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
            try:
                table.update_item(
                    Key={"PK": f"RESEARCH#{research_id}", "SK": "CONFIG"},
                    UpdateExpression="SET #s = :s, errorMessage = :e",
                    ExpressionAttributeNames={"#s": "status"},
                    ExpressionAttributeValues={":s": "error", ":e": "Research pipeline failed to start"},
                )
            except Exception as update_err:
                logger.error(f"Failed to update research status after SFN failure: {update_err}")
            return {"error": "Failed to start research pipeline"}

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


def _shared_access_signature(shared_meetings):
    """Hash of the exact meetingIds a LIVE _list_shared_meetings call granted
    access to, for tagging/validating a KB_CACHE entry.

    Stored alongside a KB cache entry at write time and re-derived (from a
    fresh _list_shared_meetings call) at read time: a mismatch means the
    caller's access has changed since the cached retrieval ran (a meeting
    revoked, un-shared, or newly granted), so the cached result -- built from
    the OLD filter -- must not be served even though KB_CACHE_TTL_SECONDS
    hasn't expired. Without this, a KB_CACHE hit would bypass
    _list_shared_meetings' live membership check entirely.
    """
    ids = sorted(f"{s['ownerId']}/{s['meetingId']}" for s in shared_meetings)
    return hashlib.sha256('|'.join(ids).encode('utf-8')).hexdigest()


def _kb_cache_get(question, number_of_results, user_id=None, access_signature=None):
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
        if item.get("accessSignature") != access_signature:
            return None  # caller's shared-meeting access changed since this was cached
        return json.loads(item["results"])
    except Exception as e:
        logger.warning(f"KB cache read failed: {e}")
        return None


def _kb_cache_put(question, number_of_results, results, user_id=None, access_signature=None):
    """Store KB retrieve() response with TTL, tagged with the access signature it was built under."""
    if KB_CACHE_TTL_SECONDS <= 0:
        return
    try:
        table.put_item(Item={
            "PK": _kb_cache_key(question, number_of_results, user_id),
            "SK": "V1",
            "results": json.dumps(results, ensure_ascii=False),
            "accessSignature": access_signature,
            "TTL": int(time.time()) + KB_CACHE_TTL_SECONDS,
        })
    except Exception as e:
        logger.warning(f"KB cache write failed: {e}")


# Cache shared-meeting lookups per user (warm for Lambda lifetime, bounded by
# SHARED_MEETINGS_CACHE_TTL_SECONDS -- see _is_account_member for why an
# account-origin share can't just be cached and trusted indefinitely).
_shared_meetings_cache = {}
_shared_meetings_cache_expiry = {}


def _is_account_member(account_id, user_id):
    """Check live AccountMember existence (ACCOUNT#{id}/MEMBER#{userId}).

    Deliberately uses ConsistentRead and NO cache of its own: it's only ever
    called from _list_shared_meetings, which already caches its own result
    for SHARED_MEETINGS_CACHE_TTL_SECONDS. A second, independent TTL here
    (as an earlier version of this function had) would let a membership
    check refreshed just before the outer cache's last expiry serve a stale
    positive for up to another full TTL after that -- compounding worst-case
    staleness to ~2x the documented bound instead of the single TTL
    SHARED_MEETINGS_CACHE_TTL_SECONDS actually promises.
    """
    try:
        result = table.get_item(
            Key={'PK': f'ACCOUNT#{account_id}', 'SK': f'MEMBER#{user_id}'},
            ConsistentRead=True,
        )
        return bool(result.get('Item'))
    except Exception as e:
        logger.warning(f"account membership check failed for account={account_id} user={user_id}: {e}")
        return False


def _list_shared_meetings_raw(user_id):
    """Query DynamoDB for the SET of meetings shared with this user --
    cached for SHARED_MEETINGS_CACHE_TTL_SECONDS, but ONLY the immutable
    identity of each share (meetingId, ownerId).

    Returns list of {'meetingId', 'ownerId'}. This only saves the DynamoDB
    Query that enumerates which Share rows exist; it deliberately does NOT
    cache origin/accountId/sharedToAccount -- those are mutable authorization
    inputs (a row can be deleted and recreated with a different origin; a
    meeting's sharedToAccount can flip) and caching them let a stale
    authorization decision survive within the TTL even though
    _is_account_member itself was checked fresh. _list_shared_meetings
    (below) re-fetches all of those live for every call.
    """
    now = time.time()
    if _shared_meetings_cache_expiry.get(user_id, 0) > now:
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
        _shared_meetings_cache_expiry[user_id] = now + SHARED_MEETINGS_CACHE_TTL_SECONDS
        return items
    except Exception as e:
        logger.warning(f"Failed to list shared meetings for {user_id}: {e}")
        _shared_meetings_cache[user_id] = []
        _shared_meetings_cache_expiry[user_id] = now + SHARED_MEETINGS_CACHE_TTL_SECONDS
        return []


def _list_shared_meetings(user_id):
    """Return currently-valid shared meetings for user_id: {'meetingId', 'ownerId'}.

    The SET of meetingId/ownerId pairs comes from the cached
    _list_shared_meetings_raw, but every authorization-relevant fact --
    whether the share is still present and what its current origin is, the
    meeting's current sharedToAccount, and live account membership -- is
    fetched fresh on every call, uncached. A direct share (origin !=
    'account') is included unconditionally once confirmed fresh; an
    account-origin share additionally requires current sharedToAccount and
    _is_account_member. This mirrors the Go backend's checkAccess/
    resolveSharedAccess, which re-verify at read time for the same reason
    (see backend/internal/service/meeting.go) -- revocation, un-sharing, or
    an origin change (e.g. a deleted direct share replaced by a new
    account-origin one on the same key) is visible on the very next call,
    not bounded by SHARED_MEETINGS_CACHE_TTL_SECONDS.
    """
    items = []
    for entry in _list_shared_meetings_raw(user_id):
        meeting_id, owner_id = entry['meetingId'], entry['ownerId']
        try:
            share_result = table.get_item(
                Key={'PK': f'USER#{user_id}', 'SK': f'SHARED#{meeting_id}'},
                ProjectionExpression='origin',
                ConsistentRead=True,
            )
        except Exception as e:
            logger.warning(f"share re-check failed for {user_id}/{meeting_id}: {e}")
            continue
        if 'Item' not in share_result:
            continue  # revoked since the raw list was cached
        share = share_result['Item']  # {} for a direct share with no other projected attrs -- NOT "missing"
        if share.get('origin') == 'account':
            try:
                meeting = table.get_item(
                    Key={'PK': f'USER#{owner_id}', 'SK': f'MEETING#{meeting_id}'},
                    ProjectionExpression='accountId, sharedToAccount',
                    ConsistentRead=True,
                ).get('Item')
            except Exception as e:
                logger.warning(f"meeting lookup failed for account-share check {meeting_id}: {e}")
                meeting = None
            meeting = meeting or {}
            account_id = meeting.get('accountId')
            if not account_id or not meeting.get('sharedToAccount') or not _is_account_member(account_id, user_id):
                continue
        items.append({'meetingId': meeting_id, 'ownerId': owner_id})
    return items


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

    # Computed unconditionally (not just on a cache miss) -- this call IS the
    # live membership/access check. Skipping it on a would-be cache hit is
    # exactly the bypass this signature exists to close: a cached result
    # built from an access set that has since changed (a meeting revoked)
    # must not be served just because the question/params match.
    shared = _list_shared_meetings(user_id) if user_id else []
    access_signature = _shared_access_signature(shared) if user_id else None

    cached = _kb_cache_get(question, capped, user_id, access_signature)
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
            for s in shared:
                filters.append({
                    'stringContains': {
                        'key': 'x-amz-bedrock-kb-source-uri',
                        'value': f"meetings/{s['ownerId']}/{s['meetingId']}",
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
        _kb_cache_put(question, capped, results, user_id, access_signature)
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
            messages = json.loads(item.get("messages", "[]"))
            # A failed/aborted round can persist history ending in a user-role
            # message, OR in an assistant message still holding an unresolved
            # toolUse block (MAX_TOOL_ROUNDS exhaustion leaves the round's
            # toolResult unsaved; the client_gone break can fire between the
            # toolUse append and its toolResult). Either shape breaks Bedrock's
            # role-alternation / tool-pairing validation and poisons EVERY
            # subsequent call in the session. Rewind past both at the single
            # load choke point, down to the last complete assistant(text) (or
            # fully-paired tool) boundary.
            while messages:
                last = messages[-1]
                role = last.get("role")
                if role == "user":
                    messages.pop()
                    continue
                if role == "assistant" and any(
                    isinstance(b, dict) and "toolUse" in b for b in last.get("content", [])
                ):
                    messages.pop()
                    continue
                break
            return messages
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


# ── Account-aware chat tools ───────────────────────────────────────────────
# These query the Account feature data directly via DynamoDB, scoped to the
# caller's OWN memberships (GSI1 keyed by USER#{userId}) — so resolving an
# account from the user's membership list IS the authorization gate; a user
# can never reach an account they aren't a member of.

def _query_all(**kwargs):
    """table.query following LastEvaluatedKey across all pages (no 1MB silent cap)."""
    items = []
    while True:
        resp = table.query(**kwargs)
        items.extend(resp.get('Items', []))
        lek = resp.get('LastEvaluatedKey')
        if not lek:
            break
        kwargs['ExclusiveStartKey'] = lek
    return items


def _user_account_metas(user_id):
    """Return [(meta_item, role)] for the user's accounts — one META read each."""
    from boto3.dynamodb.conditions import Key
    try:
        members = _query_all(
            IndexName='GSI1',
            KeyConditionExpression=Key('GSI1PK').eq(f'USER#{user_id}') & Key('GSI1SK').begins_with('ACCOUNT#'),
        )
    except Exception as e:
        logger.warning(f"account membership query failed: {e}")
        return []
    out = []
    for m in members:
        acc_id = m.get('accountId')
        if not acc_id:
            continue
        try:
            meta = table.get_item(Key={'PK': f'ACCOUNT#{acc_id}', 'SK': 'META'}).get('Item')
        except Exception as e:
            logger.warning(f"account META read failed for {acc_id}: {e}")
            continue
        if meta:
            out.append((meta, m.get('role', '')))
    return out


def list_accounts_for_user(user_id):
    """Return [{accountId, name, role}] for accounts the user is a member of."""
    return [
        {'accountId': meta.get('accountId', ''), 'name': meta.get('name', ''), 'role': role}
        for meta, role in _user_account_metas(user_id)
    ]


def _resolve_account(user_id, query):
    """Match query (accountId/name/alias, case-insensitive) against the user's own
    accounts. Returns the list of matching META items (0/1/many)."""
    q = (query or '').strip().lower()
    matches = []
    for meta, _role in _user_account_metas(user_id):
        names = [meta.get('name', '')] + list(meta.get('aliases') or [])
        if meta.get('accountId', '').lower() == q or any((n or '').strip().lower() == q for n in names):
            matches.append(meta)
    return matches


def _resolve_error(user_id, account_query, matches):
    if len(matches) == 0:
        names = [a['name'] for a in list_accounts_for_user(user_id)]
        return {'error': f"'{account_query}' 계정을 찾을 수 없습니다. 접근 가능한 계정: {', '.join(names) or '(없음)'}"}
    return {'error': f"'{account_query}'가 여러 계정과 매칭됩니다: {', '.join(m.get('name', '') for m in matches)}. 더 구체적으로 지정하세요."}


def _account_insights(acc_id, date_from=None, date_to=None, types=None):
    from boto3.dynamodb.conditions import Key
    items = _query_all(
        KeyConditionExpression=Key('PK').eq(f'ACCOUNT#{acc_id}') & Key('SK').begins_with('INSIGHT#'),
        ScanIndexForward=False,
    )
    type_set = set(types) if types else None
    out = []
    for it in items:
        occ = it.get('occurredAt', '') or ''
        if date_from and occ and occ < date_from:
            continue
        if date_to and occ and occ > date_to:
            continue
        if type_set and it.get('type') not in type_set:
            continue
        out.append({'type': it.get('type', ''), 'text': it.get('text', ''), 'occurredAt': occ,
                    'sourceType': it.get('sourceType', ''), 'entities': list(it.get('entities') or [])})
    return out


def get_account_insights_for_chat(user_id, account_query, date_from=None, date_to=None, types=None):
    """Return account insights filtered by period [from,to] (RFC3339) and types."""
    matches = _resolve_account(user_id, account_query)
    if len(matches) != 1:
        return _resolve_error(user_id, account_query, matches)
    acc = matches[0]
    try:
        insights = _account_insights(acc.get('accountId', ''), date_from, date_to, types)
    except Exception as e:
        logger.warning(f"account insights query failed: {e}")
        return {'error': '인사이트 조회 중 오류가 발생했습니다.'}
    return {'account': acc.get('name', ''), 'insights': insights}


def get_account_brief_for_chat(user_id, account_query):
    """Bundle: account meta + insights grouped by type + shared meeting titles."""
    from boto3.dynamodb.conditions import Key
    matches = _resolve_account(user_id, account_query)
    if len(matches) != 1:
        return _resolve_error(user_id, account_query, matches)
    acc = matches[0]
    acc_id = acc.get('accountId', '')
    try:
        insights = _account_insights(acc_id)
    except Exception as e:
        logger.warning(f"account brief insights failed: {e}")
        insights = []
    by_type = {}
    for ins in insights:
        by_type.setdefault(ins['type'], []).append(ins)
    meetings = []
    try:
        for r in _query_all(
            KeyConditionExpression=Key('PK').eq(f'ACCOUNT#{acc_id}') & Key('SK').begins_with('MEETINGREF#'),
            ScanIndexForward=False,
        ):
            meetings.append({'meetingId': r.get('meetingId', ''), 'title': r.get('title', ''), 'date': r.get('date', '')})
    except Exception as e:
        logger.warning(f"account meeting refs query failed: {e}")
    research = _account_research(acc_id)
    return {'account': acc.get('name', ''), 'industry': acc.get('industry', ''),
            'insightsByType': by_type, 'meetings': meetings, 'research': research}


def _account_research(acc_id):
    """Research reports linked to an account (RESEARCHREF# items -> full RESEARCH#{id}/CONFIG record)."""
    from boto3.dynamodb.conditions import Key
    out = []
    try:
        refs = _query_all(
            KeyConditionExpression=Key('PK').eq(f'ACCOUNT#{acc_id}') & Key('SK').begins_with('RESEARCHREF#'),
            ScanIndexForward=False,
        )
    except Exception as e:
        logger.warning(f"account research refs query failed: {e}")
        return out
    for ref in refs:
        research_id = ref.get('researchId', '')
        if not research_id:
            continue
        try:
            item = table.get_item(Key={'PK': f'RESEARCH#{research_id}', 'SK': 'CONFIG'}).get('Item')
        except Exception as e:
            logger.warning(f"research record read failed for {research_id}: {e}")
            continue
        if not item or item.get('trashedAt'):
            continue
        # Re-verify membership against the canonical accountIds rather than
        # trusting the RESEARCHREF# index alone: LinkAccount/UnlinkAccount
        # (backend/internal/service/research.go) best-effort the ref
        # write/delete and only log on failure, so a failed DeleteResearchRef
        # would otherwise leave a stale ref that keeps exposing this
        # research's summary in the Bedrock chat context after the owner
        # unlinked it -- fail-closed here instead of fail-open.
        if acc_id not in item.get('accountIds', []):
            continue
        out.append({'topic': item.get('topic', ''), 'summary': item.get('summary', ''), 'status': item.get('status', '')})
    return out


def agentic_converse(messages, transcript=None, session_id=None, user_id=None):
    """Agentic tool-use loop: model decides what tools to call."""
    context = {
        "transcript": transcript or "",
        "retrieve_from_kb": lambda q, n=5: retrieve_from_kb(q, n, user_id=user_id),
        "list_meetings": list_meetings_for_user,
        "load_meeting_context": load_meeting_context,
        "create_research": lambda uid, topic, mode: create_research_from_chat(uid, topic, mode),
        "check_research_limit": check_research_limit,
        "list_accounts": list_accounts_for_user,
        "get_account_insights": get_account_insights_for_chat,
        "get_account_brief": get_account_brief_for_chat,
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
                inferenceConfig={"maxTokens": 4096},
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
        # Shared meeting: look up ownerId from the share record
        if not item:
            for s in _list_shared_meetings(user_id):
                if s['meetingId'] == meeting_id:
                    result = table.get_item(
                        Key={'PK': f"USER#{s['ownerId']}", 'SK': f'MEETING#{meeting_id}'}
                    )
                    item = result.get('Item')
                    break
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
    """Post a JSON message to a WebSocket connection.

    Returns False only when the connection is confirmed gone (GoneException)
    -- callers treat False as "stop streaming, the client left". A transient
    error (throttling, a flaky post) does NOT mean the client is gone, so it
    must not be treated the same way or one blip silently truncates an
    otherwise-healthy multi-round streamed answer.
    """
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
        logger.warning(f"post_to_connection failed (treated as transient, not gone): {e}")
        return True


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
        "check_research_limit": check_research_limit,
        "list_accounts": list_accounts_for_user,
        "get_account_insights": get_account_insights_for_chat,
        "get_account_brief": get_account_brief_for_chat,
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
                inferenceConfig={"maxTokens": 4096},
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
        client_gone = False

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
                    if not _post_ws(apigw, connection_id, {
                        'type': 'answer_delta',
                        'sessionId': session_id,
                        'text': delta['text'],
                    }):
                        client_gone = True
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

        if client_gone:
            # WebSocket client is gone — stop burning Bedrock calls. The
            # assistant message was appended above so the saved session stays
            # role-consistent.
            break

        if stop_reason == 'end_turn' or stop_reason is None:
            break

        if stop_reason == 'tool_use':
            tool_results = []
            for block in assembled_content:
                if 'toolUse' not in block:
                    continue
                tool = block['toolUse']
                logger.info(f"Tool call (stream): {tool['name']}")
                # Tool execution (KB retrieve, research kickoff, etc.) sends no
                # answer_delta, so the client's stall watchdog would otherwise
                # go un-rearmed and time out a perfectly healthy long-running
                # tool round. This heartbeat rearms it without touching the
                # answer text. Its return value also doubles as the earliest
                # client-gone signal during a tool round (the alternative,
                # burning Bedrock/tool calls until the next answer_delta
                # notices, wastes a full round of work on a dead socket).
                if not _post_ws(apigw, connection_id, {
                    'type': 'tool_progress',
                    'sessionId': session_id,
                }):
                    client_gone = True
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
