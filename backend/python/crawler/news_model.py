"""Opus 5 Converse adapter with the supported Mantle token-count endpoint.

The runtime CountTokens API does not support this model's CRIS profile.
Deployment supplies refreshable credentials and a bounded, no-retry runtime
client. No API key, external proxy, configurable endpoint or redirect is used.
"""

import json
import time
from urllib.parse import urlsplit

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from article_fetcher import _open_response, _target

from news_enrichment import FINAL_MODEL_ID, MAX_REQUEST_BYTES, MAX_TOKENS, QualityRejected

MANTLE_REGION = "us-east-1"
MANTLE_URL = "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages/count_tokens"
MANTLE_MODEL = "anthropic.claude-opus-5"
MAX_COUNT_RESPONSE = 4096
COUNT_SECONDS = 20


class _CountResponse:
    def __init__(self, connection, response):
        self.connection, self.response = connection, response
        self.status = response.status

    def read(self, size, decode_content=False):
        headers = self.response.headers
        length = headers.get("Content-Length")
        transfer = (headers.get("Transfer-Encoding") or "").lower()
        if (len(headers.get_all("Content-Length", [])) > 1
                or len(headers.get_all("Transfer-Encoding", [])) > 1
                or len(headers.get_all("Content-Encoding", [])) > 1
                or (length is not None and transfer) or transfer not in ("", "chunked")
                or (headers.get("Content-Encoding") or "identity").lower() != "identity"):
            raise QualityRejected("TOKEN_COUNT_FAILED")
        if length is not None and (
                not length.isascii() or not length.isdigit() or len(length) > 10
                or int(length) > MAX_COUNT_RESPONSE):
            raise QualityRejected("TOKEN_COUNT_FAILED")
        value = self.response.read(size)
        if length is not None and len(value) != int(length):
            raise QualityRejected("TOKEN_COUNT_FAILED")
        return value

    def close(self):
        try:
            self.response.close()
        finally:
            self.connection.close()


class _CountTransport:
    def request(self, method, url, *, body, headers):
        if method != "POST" or url != MANTLE_URL:
            raise QualityRejected("INVALID_COUNT_REQUEST")
        deadline = time.monotonic() + COUNT_SECONDS
        parsed, answers = _target(MANTLE_URL, deadline)
        connection, response = _open_response(
            parsed, answers, deadline, method="POST", body=body, headers=headers)
        return _CountResponse(connection, response)


def _text_blocks(blocks):
    if not isinstance(blocks, list) or not 1 <= len(blocks) <= 16:
        raise QualityRejected("INVALID_COUNT_REQUEST")
    result = []
    for block in blocks:
        if not isinstance(block, dict) or set(block) != {"text"} or not isinstance(block["text"], str):
            raise QualityRejected("INVALID_COUNT_REQUEST")
        result.append({"type": "text", "text": block["text"]})
    return result


def count_body(request):
    """Convert only this worker's text-only, non-tool Converse request."""
    if (set(request) != {"modelId", "input"} or request["modelId"] != FINAL_MODEL_ID
            or FINAL_MODEL_ID != "global." + MANTLE_MODEL
            or not isinstance(request["input"], dict)
            or set(request["input"]) != {"converse"}):
        raise QualityRejected("INVALID_COUNT_REQUEST")
    value = request["input"]["converse"]
    if (not isinstance(value, dict)
            or set(value) - {"messages", "system", "additionalModelRequestFields"}
            or value.get("additionalModelRequestFields") != {"thinking": {"type": "disabled"}}
            or not isinstance(value.get("messages"), list) or not 1 <= len(value["messages"]) <= 3):
        raise QualityRejected("INVALID_COUNT_REQUEST")
    result = {"model": MANTLE_MODEL, "thinking": {"type": "disabled"}, "messages": []}
    for message in value["messages"]:
        if (not isinstance(message, dict) or set(message) != {"role", "content"}
                or message["role"] not in ("user", "assistant")):
            raise QualityRejected("INVALID_COUNT_REQUEST")
        result["messages"].append({"role": message["role"], "content": _text_blocks(message["content"])})
    if "system" in value:
        result["system"] = _text_blocks(value["system"])
    try:
        payload = json.dumps(result, ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError, UnicodeError):
        raise QualityRejected("INVALID_COUNT_REQUEST") from None
    if len(payload) > MAX_REQUEST_BYTES + 4096:
        raise QualityRejected("INVALID_COUNT_REQUEST")
    return payload


class NewsModel:
    def __init__(self, runtime, credentials, *, transport=None):
        self.runtime, self.credentials = runtime, credentials
        self.transport = transport or _CountTransport()

    def count_tokens(self, **request):
        payload = count_body(request)
        response = None
        try:
            credentials = self.credentials()
            if credentials is None:
                raise QualityRejected("TOKEN_COUNT_FAILED")
            signed = AWSRequest(
                method="POST", url=MANTLE_URL, data=payload,
                headers={"content-type": "application/json", "anthropic-version": "2023-06-01",
                         "accept-encoding": "identity", "host": urlsplit(MANTLE_URL).netloc})
            SigV4Auth(credentials.get_frozen_credentials(), "bedrock-mantle",
                      MANTLE_REGION).add_auth(signed)
            response = self.transport.request(
                "POST", MANTLE_URL, body=payload, headers=dict(signed.headers))
            if response.status != 200:
                raise QualityRejected("TOKEN_COUNT_FAILED")
            data = response.read(MAX_COUNT_RESPONSE + 1, decode_content=False)
            if len(data) > MAX_COUNT_RESPONSE:
                raise QualityRejected("TOKEN_COUNT_FAILED")
            value = json.loads(data.decode("utf-8")).get("input_tokens")
            if type(value) is not int or not 1 <= value <= 1000000:
                raise QualityRejected("TOKEN_COUNT_FAILED")
            return {"inputTokens": value}
        except QualityRejected:
            raise
        except Exception:
            # Never include signed headers, source text or raw service errors.
            raise QualityRejected("TOKEN_COUNT_FAILED") from None
        finally:
            if response is not None:
                response.close()

    def converse(self, **request):
        if (request.get("modelId") != FINAL_MODEL_ID
                or request.get("inferenceConfig") != {"maxTokens": MAX_TOKENS}
                or set(request) != {"modelId", "system", "messages", "inferenceConfig",
                                    "additionalModelRequestFields"}):
            raise QualityRejected("MODEL_BUDGET")
        return self.runtime.converse(**request)
