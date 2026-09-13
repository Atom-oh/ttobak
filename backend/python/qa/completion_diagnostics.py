"""Fixed QA output ceiling and closed, content-free completion diagnostics."""
import json

QA_OUTPUT_TOKENS = 8192
MAX_COUNT = 10_000_000
STOP_REASONS = frozenset((
    "end_turn", "stop_sequence", "tool_use", "max_tokens", "guardrail_intervened",
    "content_filtered", "malformed_model_output", "malformed_tool_use", "model_context_window_exceeded",
))
FAILURES = frozenset((
    "request_failed", "iteration_failed", "missing_stop", "unsupported_stop", "open_block",
    "empty_answer", "tool_stop_without_tool", "tool_round_limit", "incomplete_response",
))
USAGE_FIELDS = ("inputTokens", "outputTokens", "totalTokens", "cacheReadInputTokens", "cacheWriteInputTokens")


def count(value):
    return value if type(value) is int and 0 <= value <= MAX_COUNT else None


def stop_category(value):
    if value is None:
        return "missing"
    return value if type(value) is str and value in STOP_REASONS else "other"


def object_value(value):
    return value if type(value) is dict else {}


def tool_count(content):
    if type(content) is not list:
        return 0
    return min(MAX_COUNT, sum(type(block) is dict and "toolUse" in block for block in content))


class CompletionDiagnostics:
    def __init__(self, mode, round_number):
        self.mode = mode if mode in ("converse", "stream") else "other"
        self.round = count(round_number)
        self.stop = "missing"
        self.usage = dict.fromkeys(USAGE_FIELDS)
        self.metadata_seen = False
        self.stop_seen = False
        self.events = self.starts = self.stops = self.text_characters = self.tools = self.unknown_events = 0

    def _usage(self, value):
        self.metadata_seen = True
        usage = object_value(value)
        for field in USAGE_FIELDS:
            self.usage[field] = count(usage.get(field))

    def response(self, response):
        value = object_value(response)
        self.stop = stop_category(value.get("stopReason"))
        self.stop_seen = "stopReason" in value
        if "usage" in value:
            self._usage(value["usage"])
        content = object_value(object_value(value.get("output")).get("message")).get("content", [])
        if type(content) is list:
            self.stops = min(MAX_COUNT, len(content))
            self.tools = tool_count(content)
            self.text_characters = min(MAX_COUNT, sum(
                len(block["text"]) for block in content
                if type(block) is dict and type(block.get("text")) is str))

    def event(self, event):
        self.events = min(MAX_COUNT, self.events + 1)
        value = object_value(event)
        if "contentBlockStart" in value:
            self.starts = min(MAX_COUNT, self.starts + 1)
        elif "contentBlockStop" in value:
            self.stops = min(MAX_COUNT, self.stops + 1)
        elif "contentBlockDelta" in value:
            text = object_value(object_value(value["contentBlockDelta"]).get("delta")).get("text")
            if type(text) is str:
                self.text_characters = min(MAX_COUNT, self.text_characters + len(text))
        elif "messageStop" in value:
            self.stop_seen = True
            self.stop = stop_category(object_value(value["messageStop"]).get("stopReason"))
        elif "metadata" in value:
            self._usage(object_value(value["metadata"]).get("usage"))
        elif "messageStart" not in value:
            self.unknown_events = min(MAX_COUNT, self.unknown_events + 1)

    def emit(self, logger, failure, *, open_block=None, content=None):
        block = object_value(open_block)
        kind = ("none" if open_block is None else "tool_use" if "toolUse" in block
                else "text" if "text" in block else "other")
        record = {
            "version": 1, "mode": self.mode, "round": self.round,
            "failure": failure if type(failure) is str and failure in FAILURES else "other",
            "maxOutputTokens": QA_OUTPUT_TOKENS, "stopReason": self.stop,
            "messageStopSeen": self.stop_seen, "metadataSeen": self.metadata_seen,
            "eventCount": self.events, "unknownEventCount": self.unknown_events,
            "blockStartCount": self.starts, "blockStopCount": self.stops,
            "textCharacters": self.text_characters, "openBlock": kind,
            "toolCount": self.tools if content is None else tool_count(content), **self.usage,
        }
        # Only fixed keys/enums, booleans, nulls and bounded integers reach JSON.
        # No raw response/event, input, content, identifier or exception is retained.
        logger.warning("QA completion diagnostic %s", json.dumps(record, separators=(",", ":")))
