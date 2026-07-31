"""Web search for the QA Lambda via the AgentCore Gateway Web Search connector.

The gateway lives in us-east-1 only (AWS Web Search connector constraint), so
this Lambda (ap-northeast-2) calls it cross-region with SigV4, signing service
name 'bedrock-agentcore' — the same pattern as the news crawler.

The SigV4 POST + SSE-unwrap + MCP tools/call plumbing is duplicated in
backend/python/crawler/news_crawler.py and backend/python/research-agent/
tools.py (three separate deploy artifacts — a Lambda zip, another Lambda zip,
and an AgentCore container — not worth a shared package for one function).
Keep all copies in sync if this changes.
"""
import json
import logging
import os
from urllib.request import Request, urlopen

import botocore.session
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

logger = logging.getLogger(__name__)

WEB_SEARCH_GATEWAY_URL = os.environ.get('WEB_SEARCH_GATEWAY_URL', '')
WEB_SEARCH_GATEWAY_REGION = os.environ.get('WEB_SEARCH_GATEWAY_REGION', 'us-east-1')
# Live QA streams the answer to a waiting human — keep the search bounded so
# one slow upstream fetch can't eat the whole answer round.
FETCH_TIMEOUT_SECONDS = 15


def _extract_sse_json(text):
    """Unwrap an SSE response ("data: {...}") to the JSON-RPC result frame.

    Returns the frame containing 'result' or 'error'; falls back to the last
    frame, then the raw text (plain-JSON responses pass through json.loads
    unchanged upstream).
    """
    if not text.lstrip().startswith(('data:', 'event:', ':')):
        return text
    frames = []
    current_lines = []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith('data:'):
            current_lines.append(stripped[len('data:'):].strip())
        elif current_lines:
            frames.append('\n'.join(current_lines))
            current_lines = []
    if current_lines:
        frames.append('\n'.join(current_lines))
    for frame in frames:
        try:
            parsed = json.loads(frame)
        except json.JSONDecodeError:
            continue
        if isinstance(parsed, dict) and ('result' in parsed or 'error' in parsed):
            return frame
    return frames[-1] if frames else text


def _sigv4_post(body_json):
    """POST body_json to the Gateway MCP endpoint, SigV4-signed."""
    if not WEB_SEARCH_GATEWAY_URL:
        raise RuntimeError('WEB_SEARCH_GATEWAY_URL is not set')
    session = botocore.session.get_session()
    credentials = session.get_credentials()
    if credentials is None:
        raise RuntimeError('No AWS credentials available for SigV4 signing')
    request = AWSRequest(
        method='POST',
        url=WEB_SEARCH_GATEWAY_URL,
        data=body_json,
        headers={'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream'},
    )
    SigV4Auth(credentials, 'bedrock-agentcore', WEB_SEARCH_GATEWAY_REGION).add_auth(request)
    prepared = request.prepare()
    body = prepared.body.encode('utf-8') if isinstance(prepared.body, str) else prepared.body
    req = Request(prepared.url, data=body, headers=dict(prepared.headers), method='POST')
    with urlopen(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
        # Bounded read: a misbehaving upstream must not balloon Lambda memory.
        # 2MB is far above any real search response (a handful of snippets).
        return _extract_sse_json(resp.read(2_000_000).decode('utf-8', errors='replace'))


def _query_ref(query):
    """Opaque log reference for a search query. Queries derive from meeting
    conversation (customer names, project codenames, pricing) — never log
    them in plaintext to CloudWatch; a hash prefix + length is enough to
    correlate a log line with a specific request during debugging."""
    import hashlib
    digest = hashlib.sha256(query.encode('utf-8')).hexdigest()[:12]
    return f'q#{digest}/len={len(query)}'


def gateway_web_search(query, max_results=5):
    """Search via the AgentCore Gateway Web Search connector.

    Returns (results, error): results is [{"text", "url", "title",
    "publishedDate"}, ...] (possibly empty for a genuine zero-match search),
    error is None on success or a short reason string on any failure — the
    tool layer must surface a non-None error so an IAM/transport outage
    doesn't read as "관련 결과 없음" to the model.
    """
    if not WEB_SEARCH_GATEWAY_URL:
        return [], 'web search not configured'
    body = json.dumps({
        'jsonrpc': '2.0',
        'id': 1,
        'method': 'tools/call',
        'params': {
            # Gateway namespaces tool names as "{targetName}___{configurationName}"
            # (web-search-gateway-stack.ts: 'ttobak-web-search-tool' + 'WebSearch').
            'name': 'ttobak-web-search-tool___WebSearch',
            'arguments': {'query': query[:200], 'maxResults': max_results},
        },
    })
    qref = _query_ref(query)
    try:
        raw_response = _sigv4_post(body)
        parsed = json.loads(raw_response)
        if 'error' in parsed:
            logger.warning(f'Web search gateway JSON-RPC error for {qref}: {parsed["error"]}')
            return [], 'gateway error'
        result = parsed.get('result', parsed)
        if not isinstance(result, dict):
            logger.warning(f'Web search gateway returned non-object result for {qref}')
            return [], 'malformed gateway response'
        if result.get('isError'):
            logger.warning(f'Web search gateway returned isError for {qref}')
            return [], 'gateway error'
        content = result.get('content', [])
        text_block = next((b for b in content if b.get('type') == 'text' and 'text' in b), None)
        if text_block is None:
            logger.warning(f'Web search gateway returned no text content block for {qref}')
            return [], 'malformed gateway response'
        inner = json.loads(text_block['text'])
        results = inner.get('results', [])
        # http(s) scheme allowlist: search results are open-web data headed
        # for markdown links — drop javascript:/data:/file: style URLs here
        # rather than trusting every downstream renderer to.
        return [
            r for r in results
            if isinstance(r.get('url'), str) and r['url'].startswith(('https://', 'http://'))
        ][:max_results], None
    except Exception as e:
        # Full detail to CloudWatch only; the model-facing tool result gets a
        # generic reason (matching the crawler/research-agent policy of not
        # surfacing raw exception text).
        logger.warning(f'Web search gateway call failed for {qref}: {e}')
        return [], 'web search transport failed'


def format_web_results(results, error):
    """Format search results into the model-facing tool result string.

    Returns (text, source_urls). Search snippets are open-web content —
    the system prompt's untrusted-tool-output rule covers them, but keep the
    framing as data ("검색 결과") rather than instructions.
    """
    if error:
        return f'웹 검색을 수행하지 못했습니다 ({error}). 검색 없이 아는 범위에서 답하고, 불확실함을 명시하세요.', []
    if not results:
        return '웹에서 관련 결과를 찾지 못했습니다.', []
    lines = []
    sources = []
    for r in results:
        title = (r.get('title') or '').strip() or '(제목 없음)'
        # Escape markdown link metacharacters in the (untrusted) title so it
        # can't break out of the [title](url) structure — mirrors the Go
        # side's sanitizeMarkdownText for attachment names.
        for ch in ('\\', '[', ']', '(', ')', '`'):
            title = title.replace(ch, '\\' + ch)
        url = r.get('url', '')
        snippet = (r.get('text') or '').strip()[:500]
        published = (r.get('publishedDate') or '').strip()
        header = f'- [{title}]({url})' + (f' ({published[:10]})' if published else '')
        lines.append(f'{header}\n  {snippet}' if snippet else header)
        if url:
            sources.append(url)
    return '웹 검색 결과:\n' + '\n'.join(lines), sources
