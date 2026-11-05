"""Opus Converse adapter with a local, conservative input-token bound.

The runtime CountTokens API does not support this model's CRIS profile, so the
bound is computed locally from UTF-8 text bytes plus a fixed framing allowance,
assuming each model token covers at least one byte. Service-reported usage
remains the authoritative charge and the post-call cap still applies.
Deployment supplies a bounded, no-retry runtime client; no other endpoint is used.
"""

from news_enrichment import FINAL_MODEL_ID, MAX_REQUEST_BYTES, MAX_TOKENS, QualityRejected

# Role, system and message delimiters added by the service-side prompt format.
FRAMING_TOKENS = 512


def _text_bytes(blocks):
    if not isinstance(blocks, list) or not 1 <= len(blocks) <= 16:
        raise QualityRejected("INVALID_COUNT_REQUEST")
    total = 0
    for block in blocks:
        if not isinstance(block, dict) or set(block) != {"text"} or not isinstance(block["text"], str):
            raise QualityRejected("INVALID_COUNT_REQUEST")
        try:
            total += len(block["text"].encode("utf-8"))
        except UnicodeError:
            raise QualityRejected("INVALID_COUNT_REQUEST") from None
    return total


def input_token_bound(request):
    """Upper-bound only this worker's text-only, non-tool Converse request."""
    if (set(request) != {"modelId", "input"} or request["modelId"] != FINAL_MODEL_ID
            or not isinstance(request["input"], dict)
            or set(request["input"]) != {"converse"}):
        raise QualityRejected("INVALID_COUNT_REQUEST")
    value = request["input"]["converse"]
    if (not isinstance(value, dict)
            or set(value) - {"messages", "system", "additionalModelRequestFields"}
            or value.get("additionalModelRequestFields") != {"thinking": {"type": "disabled"}}
            or not isinstance(value.get("messages"), list) or not 1 <= len(value["messages"]) <= 3):
        raise QualityRejected("INVALID_COUNT_REQUEST")
    total = FRAMING_TOKENS
    for message in value["messages"]:
        if (not isinstance(message, dict) or set(message) != {"role", "content"}
                or message["role"] not in ("user", "assistant")):
            raise QualityRejected("INVALID_COUNT_REQUEST")
        total += _text_bytes(message["content"])
    if "system" in value:
        total += _text_bytes(value["system"])
    if total > MAX_REQUEST_BYTES + 4096 + FRAMING_TOKENS:
        raise QualityRejected("INVALID_COUNT_REQUEST")
    return total


class NewsModel:
    def __init__(self, runtime):
        self.runtime = runtime

    def input_token_bound(self, **request):
        return {"inputTokens": input_token_bound(request)}

    def converse(self, **request):
        if (request.get("modelId") != FINAL_MODEL_ID
                or request.get("inferenceConfig") != {"maxTokens": MAX_TOKENS}
                or set(request) != {"modelId", "system", "messages", "inferenceConfig",
                                    "additionalModelRequestFields"}):
            raise QualityRejected("MODEL_BUDGET")
        return self.runtime.converse(**request)
