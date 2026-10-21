"""Deadline-bound SigV4 MCP search for the publication workflow.

Unlike the legacy crawler adapter, this caller applies one deadline to DNS,
connect, response headers and body. It never logs query text or service payloads.
"""

import hashlib
import json
import logging
import re
import time
from urllib.parse import urlsplit

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

from article_fetcher import _open_response, _target

MAX_RESPONSE_BYTES = 2000000
SEARCH_SECONDS = 20
logger = logging.getLogger(__name__)


def _payload(text):
    if text.lstrip().startswith("{"):
        return json.loads(text)
    frames, lines = [], []
    for line in text.splitlines() + [""]:
        if line.startswith("data:"):
            lines.append(line[5:].lstrip())
        elif not line.strip() and lines:
            frames.append("\n".join(lines))
            lines = []
    for frame in frames:
        try:
            value = json.loads(frame)
        except ValueError:
            continue
        if isinstance(value, dict) and ("result" in value or "error" in value):
            return value
    raise ValueError("SEARCH_RESPONSE")


def _read_payload(response, deadline, length):
    content_type = (response.headers.get("Content-Type") or "").split(";", 1)[0].strip().lower()
    if content_type != "text/event-stream":
        raw = response.read(MAX_RESPONSE_BYTES + 1)
        if len(raw) > MAX_RESPONSE_BYTES or (length is not None and len(raw) != int(length)):
            raise ValueError("SEARCH_RESPONSE")
        return _payload(raw.decode("utf-8"))

    # read() fills its requested size or waits for EOF. A valid MCP event stream
    # may remain open after the response, so consume available chunks instead.
    total, line, data = 0, bytearray(), []
    after_cr = False
    while time.monotonic() < deadline:
        chunk = response.read1(min(65536, MAX_RESPONSE_BYTES + 1 - total))
        total += len(chunk)
        if (total > MAX_RESPONSE_BYTES or time.monotonic() >= deadline
                or (length is not None and total > int(length))):
            raise ValueError("SEARCH_RESPONSE")
        if not chunk:
            # SSE dispatch requires an empty line, not just an EOF.
            raise ValueError("SEARCH_RESPONSE")
        for byte in chunk:
            if byte == 10 and after_cr:
                after_cr = False
                continue
            after_cr = byte == 13
            if byte not in (10, 13):
                line.append(byte)
                continue
            if line.startswith(b"data:"):
                value = bytes(line[5:])
                data.append(value[1:] if value.startswith(b" ") else value)
            elif not line and data:
                frame, data = b"\n".join(data), []
                try:
                    value = json.loads(frame.decode("utf-8"))
                except (ValueError, UnicodeError):
                    value = None
                if (isinstance(value, dict) and type(value.get("id")) is int
                        and value["id"] == 1 and ("result" in value or "error" in value)):
                    return value
            line.clear()
    raise ValueError("SEARCH_DEADLINE")


class NewsSearch:
    def __init__(self, url, credentials, remaining):
        self.validate_url(url)
        self.url, self.credentials, self.remaining = url, credentials, remaining

    @staticmethod
    def validate_url(url):
        parsed = urlsplit(url)
        if (parsed.scheme != "https" or parsed.port is not None
                or parsed.username is not None or parsed.password is not None
                or parsed.path != "/mcp" or parsed.query or parsed.fragment
                or not re.fullmatch(
                    r"[a-z0-9-]+\.gateway\.bedrock-agentcore\.us-east-1\.amazonaws\.com",
                    parsed.hostname or "")):
            raise ValueError("INVALID_SEARCH_ENDPOINT")

    def __call__(self, query, maximum):
        if (not isinstance(query, str) or not 1 <= len(query) <= 200
                or type(maximum) is not int or not 1 <= maximum <= 25):
            return [], "INVALID_SEARCH_REQUEST"
        digest = hashlib.sha256(query.encode("utf-8")).hexdigest()[:16]
        if self.remaining() < SEARCH_SECONDS + 15:
            return [], "SEARCH_DEADLINE"
        response = connection = None
        try:
            body = json.dumps({
                "jsonrpc": "2.0", "id": 1, "method": "tools/call",
                "params": {"name": "ttobak-web-search-tool___WebSearch",
                           "arguments": {"query": query, "maxResults": maximum}},
            }, ensure_ascii=False).encode("utf-8")
            credentials = self.credentials()
            if credentials is None:
                raise ValueError("SEARCH_CREDENTIALS")
            request = AWSRequest(
                method="POST", url=self.url, data=body,
                headers={"content-type": "application/json", "accept-encoding": "identity",
                         "accept": "application/json, text/event-stream",
                         "host": urlsplit(self.url).netloc})
            SigV4Auth(credentials.get_frozen_credentials(), "bedrock-agentcore",
                      "us-east-1").add_auth(request)
            deadline = time.monotonic() + SEARCH_SECONDS
            parsed, addresses = _target(self.url, deadline)
            connection, response = _open_response(
                parsed, addresses, deadline, method="POST", body=body,
                headers=dict(request.headers))
            headers = response.headers
            length = headers.get("Content-Length")
            transfer = (headers.get("Transfer-Encoding") or "").lower()
            if (response.status != 200
                    or any(len(headers.get_all(name, [])) > 1 for name in (
                        "Content-Length", "Transfer-Encoding", "Content-Encoding"))
                    or (length is not None and transfer)
                    or transfer not in ("", "chunked")
                    or (headers.get("Content-Encoding") or "identity").lower() != "identity"):
                raise ValueError("SEARCH_RESPONSE")
            if length is not None and (
                    not length.isascii() or not length.isdigit() or len(length) > 10
                    or int(length) > MAX_RESPONSE_BYTES):
                raise ValueError("SEARCH_RESPONSE")
            parsed = _read_payload(response, deadline, length)
            if not isinstance(parsed, dict) or "error" in parsed:
                raise ValueError("SEARCH_RESPONSE")
            result = parsed.get("result", parsed)
            if not isinstance(result, dict) or result.get("isError"):
                raise ValueError("SEARCH_RESPONSE")
            content = result.get("content")
            if not isinstance(content, list):
                raise ValueError("SEARCH_RESPONSE")
            block = next((b for b in content if isinstance(b, dict) and b.get("type") == "text"), {})
            values = json.loads(block["text"])["results"]
            if not isinstance(values, list):
                raise ValueError("SEARCH_RESPONSE")
            return [v for v in values if isinstance(v, dict) and v.get("url")][:maximum], None
        except Exception:
            logger.warning("News search failed query_sha256=%s", digest)
            return [], "SEARCH_FAILED"
        finally:
            try:
                if response is not None:
                    response.close()
            finally:
                if connection is not None:
                    connection.close()
