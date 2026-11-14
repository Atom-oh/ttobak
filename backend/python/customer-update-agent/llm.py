"""Model factory, strict JSON parsing and one-repair JSON questions.

Never log prompts or model output: both carry page text and customer context.
"""

import functools
import json
import logging
import os

logger = logging.getLogger("customer-update-agent")

LUNA_DEFAULT = "global.openai.gpt-6-luna"
WRITER_DEFAULT = "global.anthropic.claude-sonnet-5-5"
ROLES = frozenset({"orchestrator", "reader", "gate", "researcher", "writer", "reviewer"})
_WRITER_ROLES = frozenset({"researcher", "writer"})
# Same request field as backend/python/crawler/news_enrichment.py.
LUNA_REQUEST_FIELDS = {"reasoning": {"effort": "low"}}
MAX_RESPONSE_BYTES = 64_000
MAX_DEPTH = 32
DEFAULT_REGION = "ap-northeast-2"
# Output cap per model turn (BedrockModel max_tokens -> Converse inferenceConfig.maxTokens).
MAX_TOKENS = {"reader": 1200, "gate": 1500, "researcher": 2000, "writer": 1500,
              "reviewer": 600, "orchestrator": 1500}


class AgentLimit(Exception):
    """A tool wrapper hit a cap. Raised inside a tool built by
    build_stoppable_agent, it ends that agent's loop. `code` is a short stable string."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


class ModelOutputError(Exception):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


def _luna_model_id():
    return os.environ.get("LUNA_MODEL_ID") or LUNA_DEFAULT


def model_id_for(role):
    if role not in ROLES:
        raise ValueError("unknown role")
    luna = _luna_model_id()
    if role in _WRITER_ROLES:
        return os.environ.get("WRITER_MODEL_ID") or WRITER_DEFAULT
    if role == "orchestrator":
        return os.environ.get("ORCHESTRATOR_MODEL_ID") or luna
    return luna


def aws_region():
    """Region for every boto3 client this runtime builds."""
    return os.environ.get("AWS_REGION") or os.environ.get("AWS_DEFAULT_REGION") or DEFAULT_REGION


def _uses_luna_fields(role, model_id):
    """Luna reasoning fields go only to non-writer roles running the luna model."""
    return role not in _WRITER_ROLES and model_id == _luna_model_id()


def _bedrock_model(model_id, luna_fields, max_tokens):
    """All BedrockModel kwargs live here. Verified offline against
    strands-agents 1.65.0: format_request maps max_tokens to Converse
    inferenceConfig.maxTokens and additional_request_fields to
    additionalModelRequestFields."""
    from botocore.config import Config
    from strands.models.bedrock import BedrockModel

    kwargs = {
        "model_id": model_id,
        "max_tokens": max_tokens,
        "region_name": aws_region(),
        "boto_client_config": Config(read_timeout=120, connect_timeout=10,
                                     retries={"max_attempts": 3, "mode": "adaptive"}),
    }
    if luna_fields:
        kwargs["additional_request_fields"] = json.loads(json.dumps(LUNA_REQUEST_FIELDS))
    return BedrockModel(**kwargs)


def _sequential_executor():
    """strands.tools.executors.SequentialToolExecutor (verified against
    strands-agents 1.65); None on an older library without it. Tool callers
    that share run state also serialize in code, so this is defense in depth."""
    try:
        from strands.tools.executors import SequentialToolExecutor
    except ImportError:
        logger.warning("sequential tool executor unavailable")
        return None
    return SequentialToolExecutor()


def build_agent(role, system_prompt, tools=None):
    """Agents with tools run one model turn's tool calls one at a time."""
    from strands import Agent

    model_id = model_id_for(role)
    model = _bedrock_model(model_id, _uses_luna_fields(role, model_id), MAX_TOKENS[role])
    kwargs = {"model": model, "system_prompt": system_prompt,
              "tools": list(tools) if tools else [], "callback_handler": None}
    executor = _sequential_executor() if tools else None
    if executor is not None:
        kwargs["tool_executor"] = executor
    return Agent(**kwargs)


def build_stoppable_agent(role, system_prompt, fns):
    """An agent over plain tool functions; a function raising AgentLimit ends the loop.

    Verified offline against strands-agents 1.65.0 with a fake Model: an
    exception raised inside a tool becomes an error tool result and the loop
    keeps calling the model, while agent.cancel(after_current_tools=True) from
    inside a tool ends the invocation before the next model call (stop_reason
    "cancelled"). So the wrapper cancels and returns {"refused": code}."""
    from strands import tool

    holder = []

    def stoppable(fn):
        @functools.wraps(fn)
        def wrapped(*args, **kwargs):
            try:
                return fn(*args, **kwargs)
            except AgentLimit as limit:
                if holder:
                    holder[0].cancel(after_current_tools=True)
                logger.info("agent stopped role=%s code=%s", role, limit.code)
                return json.dumps({"refused": limit.code})
        return wrapped

    agent = build_agent(role, system_prompt, [tool(stoppable(fn)) for fn in fns])
    holder.append(agent)
    return agent


def _unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate field")
        value[key] = item
    return value


def _reject_constant(_):
    raise ValueError("nonfinite number")


def _check_depth(text):
    depth = 0
    quoted = escaped = False
    for char in text:
        if quoted:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in "[{":
            depth += 1
            if depth > MAX_DEPTH:
                raise ModelOutputError("depth")
        elif char in "]}":
            depth -= 1


def parse_json_object(text):
    if not isinstance(text, str) or len(text.encode("utf-8", "replace")) > MAX_RESPONSE_BYTES:
        raise ModelOutputError("size")
    text = text.strip()
    for fence in ("```json\n", "```\n"):
        if text.startswith(fence) and text.endswith("\n```"):
            text = text[len(fence):-4]
            break
    _check_depth(text)
    try:
        value = json.loads(text, object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except (RecursionError, ValueError):
        raise ModelOutputError("invalid_json") from None
    if not isinstance(value, dict):
        raise ModelOutputError("not_object")
    return value


def _checked(text, required):
    value = parse_json_object(text)
    if not set(required) <= value.keys():
        raise ModelOutputError("missing_keys")
    return value


def _repair_prompt(required):
    keys = ", ".join(json.dumps(k) for k in sorted(required))
    return ("Your previous reply was not a valid answer. Reply again with exactly one JSON "
            f"object containing the keys {keys}, and no other text.")


def ask_json(agent, prompt, required):
    """Ask once on a fresh conversation; on invalid output repair once, then raise.

    The repair turn relies on the conversation history and never repeats the
    prompt's untrusted block."""
    messages = getattr(agent, "messages", None)
    if isinstance(messages, list):
        messages.clear()
    try:
        return _checked(str(agent(prompt)), required)
    except ModelOutputError as error:
        logger.warning("model output invalid code=%s attempt=1", error.code)
    try:
        return _checked(str(agent(_repair_prompt(required))), required)
    except ModelOutputError as error:
        logger.warning("model output invalid code=%s attempt=2", error.code)
        raise
