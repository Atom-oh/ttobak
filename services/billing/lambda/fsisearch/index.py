"""Web search proxy for the analysis worker (no VPC, no inbound; invoked by the worker through the Lambda endpoint).

The worker runs in isolated subnets with no internet route. This function calls the TTOBAK AgentCore gateway's web
search tool (us-east-1, IAM inbound auth) with SigV4 and returns https results only.

Event:  {"queries": ["...", ...]}  (at most MAX_QUERIES strings of at most MAX_QUERY chars; the worker builds generic
        queries and refuses any that name the customer, analysis/scripts/agents.py safe_query)
Result: {"results": [{"query": i, "items": [{"title", "url", "date", "text"}]}], "errors": [i, ...]}
Queries are never logged: only their count and per-query error codes.
"""
import json, os, re
from urllib.request import HTTPRedirectHandler, Request, build_opener

import botocore.session
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

GATEWAY_URL = os.environ.get("GATEWAY_URL", "")
GATEWAY_REGION = os.environ.get("GATEWAY_REGION", "us-east-1")
TOOL = os.environ.get("TOOL_NAME", "")  # "{targetName}___{configurationName}" on the gateway
MAX_QUERIES = 8
MAX_QUERY = 200
MAX_RESULTS = 5
TIMEOUT_S = 15
MAX_BODY = 2_000_000
TEXT = 500
# ISO dates only: the connector's publishedDate is often a crawl time ("05:26PM, Thursday, October 15 2026, PDT"), not
# when the page was published, so anything else is dropped rather than shown as a publication date
ISO_DATE = re.compile(r"\d{4}-\d{2}-\d{2}")


class _NoRedirect(HTTPRedirectHandler):
    # a redirect would re-send the SigV4 Authorization header to the new host
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError(f"redirect refused ({code})")


_open = build_opener(_NoRedirect()).open


def _sse_json(text):
    """The JSON-RPC frame with result/error from an SSE body; plain JSON passes through."""
    if not text.lstrip().startswith(("data:", "event:", ":")):
        return json.loads(text)
    frames, cur = [], []
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("data:"):
            cur.append(s[5:].strip())
        elif cur:
            frames.append("\n".join(cur))
            cur = []
    if cur:
        frames.append("\n".join(cur))
    parsed = []
    for f in frames:
        try:
            parsed.append(json.loads(f))
        except ValueError:
            continue
    for p in parsed:
        if isinstance(p, dict) and ("result" in p or "error" in p):
            return p
    raise ValueError("no result frame")


def _post(body, creds):
    req = AWSRequest(method="POST", url=GATEWAY_URL, data=body,
                     headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    SigV4Auth(creds, "bedrock-agentcore", GATEWAY_REGION).add_auth(req)
    p = req.prepare()
    data = p.body.encode("utf-8") if isinstance(p.body, str) else p.body
    with _open(Request(p.url, data=data, headers=dict(p.headers), method="POST"), timeout=TIMEOUT_S) as r:
        return _sse_json(r.read(MAX_BODY).decode("utf-8", errors="replace"))


def _items(frame):
    """https results of one tools/call answer; raises on a gateway or tool error."""
    if "error" in frame:
        raise RuntimeError("gateway error")
    res = frame.get("result")
    if not isinstance(res, dict) or res.get("isError"):
        raise RuntimeError("tool error")
    block = next((b for b in res.get("content") or [] if isinstance(b, dict) and b.get("type") == "text"), None)
    if block is None:
        raise RuntimeError("no text content")
    out = []
    for r in (json.loads(block["text"]).get("results") or [])[:MAX_RESULTS]:
        if not isinstance(r, dict):
            continue
        url, title = r.get("url"), r.get("title")
        if not (isinstance(url, str) and url.startswith("https://") and len(url) <= 2000 and isinstance(title, str) and title.strip()):
            continue
        date = r.get("publishedDate")
        out.append({"title": " ".join(title.split())[:300], "url": url,
                    "date": date[:10] if isinstance(date, str) and ISO_DATE.match(date) else "",
                    "text": " ".join(str(r.get("text") or "").split())[:TEXT]})
    return out


def search(queries, post):
    results, errors = [], []
    for i, q in enumerate(queries):
        body = json.dumps({"jsonrpc": "2.0", "id": i + 1, "method": "tools/call",
                           "params": {"name": TOOL, "arguments": {"query": q, "maxResults": MAX_RESULTS}}})
        try:
            results.append({"query": i, "items": _items(post(body))})
        except Exception as ex:  # one failed query does not sink the others
            print(json.dumps({"event": "query_failed", "query": i, "error": type(ex).__name__}))
            errors.append(i)
    return {"results": results, "errors": errors}


def handler(event, context=None):
    qs = event.get("queries") if isinstance(event, dict) else None
    if not (isinstance(qs, list) and 0 < len(qs) <= MAX_QUERIES
            and all(isinstance(q, str) and 0 < len(q.strip()) <= MAX_QUERY for q in qs)):
        raise ValueError("queries: 1-%d strings of at most %d chars" % (MAX_QUERIES, MAX_QUERY))
    if not (GATEWAY_URL.startswith("https://") and TOOL):
        raise RuntimeError("GATEWAY_URL (https) and TOOL_NAME must be set")
    creds = botocore.session.get_session().get_credentials()
    out = search([q.strip() for q in qs], lambda b: _post(b, creds))
    print(json.dumps({"event": "search", "queries": len(qs), "errors": len(out["errors"])}))
    return out
