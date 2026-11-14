"""Web tools: AgentCore Gateway web search and bounded public page fetch.

Results are untrusted text; prompts that include them must fence them as data.
Neither function touches the run ledger (callers wrap them with budgets).
"""

import hashlib
import hmac
import json
import logging
import os
from urllib.request import HTTPRedirectHandler, Request, build_opener

import botocore.session
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

import fetching

logger = logging.getLogger("customer-update-agent")

MAX_SEARCH_RESULTS = 5
MAX_SNIPPET = 500
MAX_TITLE = 300
MAX_URL = 2048
MAX_PAGE_TEXT = 8000
FETCH_TIMEOUT_SECONDS = 10
_SEARCH_TOOL = "ttobak-web-search-tool___WebSearch"


class ToolError(Exception):
    def __init__(self, code, reason=""):
        super().__init__(code)
        self.code = code
        self.reason = reason


# SigV4 MCP web-search transport: the fourth copy. The others are
# backend/python/crawler/news_crawler.py, backend/python/research-agent/tools.py
# and backend/python/qa/web_search.py (separate deploy artifacts). Keep all
# four in sync.

class _NoRedirectHandler(HTTPRedirectHandler):
    """Refuse redirects: urlopen would re-send the SigV4 Authorization header
    to the redirect target. The managed gateway never legitimately redirects."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError(f"gateway redirect refused (HTTP {code})")


_no_redirect_opener = build_opener(_NoRedirectHandler())


def _extract_sse_json(text):
    """Return the JSON-RPC response from a plain JSON or SSE body. Multi-line
    data fields are newline-joined per frame; the frame carrying "result" or
    "error" wins, else the last frame."""
    if text.lstrip().startswith("{"):
        return text
    frames = []
    current = []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("data:"):
            current.append(stripped[len("data:"):].strip())
        elif current:
            frames.append("\n".join(current))
            current = []
    if current:
        frames.append("\n".join(current))
    for frame in frames:
        try:
            parsed = json.loads(frame)
        except json.JSONDecodeError:
            continue
        if isinstance(parsed, dict) and ("result" in parsed or "error" in parsed):
            return frame
    return frames[-1] if frames else text


def _gateway():
    url = os.environ.get("WEB_SEARCH_GATEWAY_URL", "")
    region = os.environ.get("WEB_SEARCH_GATEWAY_REGION", "us-east-1")
    return url, region


def _sigv4_post(body_json):
    """POST body_json to the gateway MCP endpoint, SigV4-signed; https only."""
    url, region = _gateway()
    if not url:
        raise RuntimeError("WEB_SEARCH_GATEWAY_URL is not set")
    if not url.startswith("https://"):
        raise RuntimeError("WEB_SEARCH_GATEWAY_URL must be https")
    credentials = botocore.session.get_session().get_credentials()
    if credentials is None:
        raise RuntimeError("No AWS credentials available for SigV4 signing")
    request = AWSRequest(method="POST", url=url, data=body_json, headers={
        "Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    SigV4Auth(credentials, "bedrock-agentcore", region).add_auth(request)
    prepared = request.prepare()
    body = prepared.body.encode("utf-8") if isinstance(prepared.body, str) else prepared.body
    req = Request(prepared.url, data=body, headers=dict(prepared.headers), method="POST")
    with _no_redirect_opener.open(req, timeout=FETCH_TIMEOUT_SECONDS) as resp:
        return _extract_sse_json(resp.read(2_000_000).decode("utf-8", errors="replace"))


# Per-process key: an unkeyed hash of a short query is dictionary-attackable
# from logs. Same scheme as backend/python/qa/web_search.py's _query_ref.
_LOG_HMAC_KEY = os.urandom(16)


def _query_ref(query):
    digest = hmac.new(_LOG_HMAC_KEY, query.encode("utf-8"), hashlib.sha256).hexdigest()[:12]
    return f"q#{digest}/len={len(query)}"


def _one_line(value, limit):
    return " ".join(value.split())[:limit] if isinstance(value, str) else ""


def _safe_url(url):
    return (isinstance(url, str) and url.startswith("https://") and len(url) <= MAX_URL
            and not any(c.isspace() or ord(c) < 32 or ord(c) == 127 for c in url))


def _parse_results(raw, limit):
    parsed = json.loads(raw)
    if not isinstance(parsed, dict) or "error" in parsed:
        raise ValueError("gateway error")
    result = parsed.get("result", parsed)
    if not isinstance(result, dict) or result.get("isError"):
        raise ValueError("tool error")
    block = next((b for b in result.get("content") or []
                  if isinstance(b, dict) and b.get("type") == "text" and "text" in b), None)
    if block is None:
        raise ValueError("no text block")
    inner = json.loads(block["text"])
    out = []
    for r in inner.get("results") or []:
        if isinstance(r, dict) and _safe_url(r.get("url")):
            out.append({"title": _one_line(r.get("title"), MAX_TITLE), "url": r["url"],
                        "snippet": _one_line(r.get("text"), MAX_SNIPPET)})
        if len(out) == limit:
            break
    return out


def _clamp_results(value):
    """Model-supplied: anything that is not a finite number becomes the default."""
    try:
        limit = int(value)
    except (TypeError, ValueError, OverflowError):
        return MAX_SEARCH_RESULTS
    return max(1, min(limit, MAX_SEARCH_RESULTS))


def web_search(query, max_results=MAX_SEARCH_RESULTS):
    """Return up to five {title, url, snippet} https results; raise ToolError on failure."""
    query = query[:200] if isinstance(query, str) else ""
    limit = _clamp_results(max_results)
    qref = _query_ref(query)
    url, _ = _gateway()
    if not url.startswith("https://"):
        logger.error("web search unavailable %s", qref)
        raise ToolError("search_unavailable")
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
        "name": _SEARCH_TOOL, "arguments": {"query": query, "maxResults": limit}}},
        ensure_ascii=False)
    try:
        results = _parse_results(_sigv4_post(body), limit)
    except Exception as error:
        logger.warning("web search failed %s type=%s", qref, type(error).__name__)
        raise ToolError("search_failed") from None
    logger.info("web search %s results=%d", qref, len(results))
    return results


def fetch_public(url):
    """Fetch a public https page through article_fetcher; text capped at 8,000 chars."""
    af = fetching.module()
    try:
        article = af.fetch_article(url, https_only=True)
    except af.FetchRejected as error:
        raise ToolError("fetch_rejected", getattr(error, "reason", "")) from None
    title = (article.title or "").strip()
    text = f"{title}\n\n{article.body}" if title else article.body
    return text[:MAX_PAGE_TEXT]
