import importlib.util
import json
import logging
import os
import sys
import types
import unittest
from unittest import mock

import llm

logging.getLogger("customer-update-agent").addHandler(logging.NullHandler())


class _FakeBedrockModel:
    def __init__(self, **kwargs):
        self.kwargs = kwargs


class _FakeAgent:
    def __init__(self, **kwargs):
        self.kwargs = kwargs


def fake_strands():
    """strands is container-only; stub the two classes llm.py constructs."""
    strands = types.ModuleType("strands")
    strands.Agent = _FakeAgent
    models = types.ModuleType("strands.models")
    bedrock = types.ModuleType("strands.models.bedrock")
    bedrock.BedrockModel = _FakeBedrockModel
    strands.models = models
    models.bedrock = bedrock
    return {"strands": strands, "strands.models": models, "strands.models.bedrock": bedrock}


class ParseJsonObjectTest(unittest.TestCase):
    def test_plain_object(self):
        self.assertEqual(llm.parse_json_object('{"a": 1}'), {"a": 1})

    def test_fenced_block(self):
        self.assertEqual(llm.parse_json_object('```json\n{"a": "b"}\n```'), {"a": "b"})
        self.assertEqual(llm.parse_json_object('```\n{"a": "b"}\n```'), {"a": "b"})

    def test_rejections(self):
        cases = {
            "duplicate": '{"a": 1, "a": 2}',
            "nan": '{"a": NaN}',
            "infinity": '{"a": Infinity}',
            "list": "[1, 2]",
            "string": '"x"',
            "invalid": "{not json}",
            "trailing": '{"a": 1} extra',
            "prose": 'Here it is: {"a": 1}',
            "deep": '{"a": ' + "[" * 40 + "]" * 40 + "}",
            "huge": '{"a": "' + "x" * (llm.MAX_RESPONSE_BYTES + 1) + '"}',
            "not_text": None,
        }
        for name, text in cases.items():
            with self.subTest(name):
                with self.assertRaises(llm.ModelOutputError):
                    llm.parse_json_object(text)

    def test_brackets_inside_strings_do_not_count_toward_depth(self):
        text = '{"a": "' + "[" * 100 + '"}'
        self.assertEqual(llm.parse_json_object(text), {"a": "[" * 100})


class _FakeSequentialExecutor:
    pass


def fake_strands_with_executors():
    mods = fake_strands()
    tools_mod = types.ModuleType("strands.tools")
    executors = types.ModuleType("strands.tools.executors")
    executors.SequentialToolExecutor = _FakeSequentialExecutor
    tools_mod.executors = executors
    mods["strands"].tools = tools_mod
    mods.update({"strands.tools": tools_mod, "strands.tools.executors": executors})
    return mods


class ToolExecutorTest(unittest.TestCase):
    def build(self, modules, tools):
        with mock.patch.dict(sys.modules, modules):
            return llm.build_agent("orchestrator", "system", tools)

    def test_agent_with_tools_runs_them_sequentially(self):
        agent = self.build(fake_strands_with_executors(), [lambda: None])
        self.assertIsInstance(agent.kwargs["tool_executor"], _FakeSequentialExecutor)

    def test_agent_without_tools_keeps_default_executor(self):
        agent = self.build(fake_strands_with_executors(), None)
        self.assertNotIn("tool_executor", agent.kwargs)

    def test_older_library_without_executor_still_constructs(self):
        mods = fake_strands()
        # import of strands.tools.executors fails (also with the real library installed)
        mods["strands.tools"] = mods["strands.tools.executors"] = None
        with self.assertLogs("customer-update-agent", "WARNING") as logs:
            agent = self.build(mods, [lambda: None])
        self.assertEqual(len(logs.output), 1)
        self.assertIn("sequential tool executor unavailable", logs.output[0])
        self.assertNotIn("tool_executor", agent.kwargs)
        self.assertEqual(len(agent.kwargs["tools"]), 1)


class _CancelAgent(_FakeAgent):
    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        self.cancels = []

    def cancel(self, **kwargs):
        self.cancels.append(kwargs)


class StoppableAgentTest(unittest.TestCase):
    def test_agent_limit_cancels_after_current_tools_and_returns_refusal(self):
        mods = fake_strands_with_executors()
        mods["strands"].Agent = _CancelAgent
        mods["strands"].tool = lambda fn: fn

        def ok(x: str) -> str:
            """fine"""
            return "ok " + x

        def capped(x: str) -> str:
            """capped"""
            raise llm.AgentLimit("tool_limit")

        with mock.patch.dict(sys.modules, mods):
            agent = llm.build_stoppable_agent("orchestrator", "system", [ok, capped])
        first, second = agent.kwargs["tools"]
        self.assertEqual((first.__name__, first.__doc__), ("ok", "fine"))
        self.assertEqual(first("a"), "ok a")
        self.assertEqual(agent.cancels, [])
        self.assertEqual(json.loads(second("a")), {"refused": "tool_limit"})
        self.assertEqual(agent.cancels, [{"after_current_tools": True}])

    @unittest.skipUnless(importlib.util.find_spec("strands"), "strands-agents not installed")
    def test_real_strands_loop_ends_on_agent_limit(self):
        """Wiring check with the real library and an offline fake Model."""
        from strands.models.model import Model

        class LoopModel(Model):
            """Asks for one call of tool `t` every turn (plain text after 30 turns)."""

            def __init__(self):
                self.turns = 0

            def update_config(self, **kwargs):
                pass

            def get_config(self):
                return {}

            def structured_output(self, *args, **kwargs):
                raise NotImplementedError

            async def stream(self, messages, tool_specs=None, system_prompt=None, **kwargs):
                self.turns += 1
                yield {"messageStart": {"role": "assistant"}}
                if self.turns > 30:
                    yield {"contentBlockDelta": {"delta": {"text": "DONE"}}}
                    yield {"contentBlockStop": {}}
                    yield {"messageStop": {"stopReason": "end_turn"}}
                    return
                yield {"contentBlockStart": {"start": {"toolUse": {
                    "toolUseId": f"u{self.turns}", "name": "t"}}}}
                yield {"contentBlockDelta": {"delta": {"toolUse": {"input": "{}"}}}}
                yield {"contentBlockStop": {}}
                yield {"messageStop": {"stopReason": "tool_use"}}

        model, calls = LoopModel(), []

        def t() -> str:
            """Count calls; the third hits the cap."""
            calls.append(1)
            if len(calls) >= 3:
                raise llm.AgentLimit("tool_limit")
            return "ok"

        with mock.patch.object(llm, "_bedrock_model", lambda *a: model):
            agent = llm.build_stoppable_agent("orchestrator", "system", [t])
            result = agent("go")
        self.assertEqual((len(calls), model.turns, result.stop_reason), (3, 3, "cancelled"))


class BuildAgentTest(unittest.TestCase):
    def build(self, role, env=None, tools=None):
        with mock.patch.dict(sys.modules, fake_strands()), \
                mock.patch.dict(os.environ, env or {}, clear=False):
            return llm.build_agent(role, "system", tools)

    def test_role_selects_configured_model(self):
        env = {"LUNA_MODEL_ID": "luna-x", "WRITER_MODEL_ID": "writer-x",
               "ORCHESTRATOR_MODEL_ID": "orch-x"}
        expected = {"writer": "writer-x", "researcher": "writer-x", "orchestrator": "orch-x",
                    "reader": "luna-x", "gate": "luna-x", "reviewer": "luna-x"}
        for role, model_id in expected.items():
            with self.subTest(role):
                agent = self.build(role, env)
                self.assertEqual(agent.kwargs["model"].kwargs["model_id"], model_id)

    def test_defaults(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            for name in ("LUNA_MODEL_ID", "WRITER_MODEL_ID", "ORCHESTRATOR_MODEL_ID"):
                os.environ.pop(name, None)
            self.assertEqual(llm.model_id_for("writer"), "global.anthropic.claude-sonnet-5-5")
            self.assertEqual(llm.model_id_for("reader"), "global.openai.gpt-6-luna")
            self.assertEqual(llm.model_id_for("orchestrator"), "global.openai.gpt-6-luna")
            os.environ["LUNA_MODEL_ID"] = "luna-y"
            self.assertEqual(llm.model_id_for("orchestrator"), "luna-y")

    def test_luna_gets_low_reasoning_and_writer_does_not(self):
        reader = self.build("reader")
        self.assertEqual(reader.kwargs["model"].kwargs["additional_request_fields"],
                         {"reasoning": {"effort": "low"}})
        writer = self.build("writer")
        self.assertNotIn("additional_request_fields", writer.kwargs["model"].kwargs)

    def test_reasoning_fields_follow_role_and_resolved_luna_id(self):
        def fields(role, env):
            return self.build(role, env).kwargs["model"].kwargs.get("additional_request_fields")

        low = {"reasoning": {"effort": "low"}}
        # A luna-named writer model still gets no luna fields (decided by role).
        self.assertIsNone(fields("writer", {"WRITER_MODEL_ID": "global.openai.gpt-6-luna"}))
        # A non-luna orchestrator override gets none; a custom luna id does.
        self.assertIsNone(fields("orchestrator", {"ORCHESTRATOR_MODEL_ID": "global.anthropic.x"}))
        self.assertEqual(fields("orchestrator", {"LUNA_MODEL_ID": "custom-model",
                                                 "ORCHESTRATOR_MODEL_ID": ""}), low)
        self.assertEqual(fields("gate", {"LUNA_MODEL_ID": "custom-model"}), low)
        self.assertEqual(fields("reviewer", {"LUNA_MODEL_ID": "custom-model"}), low)

    def test_no_default_tools_and_no_stdout_streaming(self):
        agent = self.build("gate")
        self.assertEqual(agent.kwargs["tools"], [])
        self.assertIsNone(agent.kwargs["callback_handler"])
        self.assertEqual(agent.kwargs["system_prompt"], "system")
        marker = object()
        self.assertEqual(self.build("researcher", tools=[marker]).kwargs["tools"], [marker])

    def test_max_tokens_per_role(self):
        expected = {"reader": 1200, "gate": 1500, "researcher": 2000, "writer": 1500,
                    "reviewer": 600, "orchestrator": 1500}
        self.assertEqual(set(expected), llm.ROLES)
        for role, tokens in expected.items():
            with self.subTest(role):
                self.assertEqual(self.build(role).kwargs["model"].kwargs["max_tokens"], tokens)

    def test_region_from_environment(self):
        for env, region in (({"AWS_REGION": "us-west-2", "AWS_DEFAULT_REGION": "x"}, "us-west-2"),
                            ({"AWS_DEFAULT_REGION": "eu-west-1"}, "eu-west-1"),
                            ({}, "ap-northeast-2")):
            with self.subTest(region=region), mock.patch.dict(os.environ, env, clear=True), \
                    mock.patch.dict(sys.modules, fake_strands()):
                model = llm.build_agent("gate", "system").kwargs["model"]
                self.assertEqual(model.kwargs["region_name"], region)

    def test_unknown_role(self):
        with self.assertRaises(ValueError):
            self.build("admin")


class ScriptedAgent:
    def __init__(self, *replies):
        self.replies = list(replies)
        self.prompts = []
        self.messages = [{"role": "user", "content": "earlier"}]

    def __call__(self, prompt):
        self.prompts.append(prompt)
        reply = self.replies.pop(0)
        if isinstance(reply, Exception):
            raise reply
        return reply


class AskJsonTest(unittest.TestCase):
    def test_first_reply_valid(self):
        agent = ScriptedAgent('{"a": 1, "b": 2}')
        self.assertEqual(llm.ask_json(agent, "PROMPT", {"a"}), {"a": 1, "b": 2})
        self.assertEqual(agent.prompts, ["PROMPT"])

    def test_starts_from_empty_conversation(self):
        agent = ScriptedAgent('{"a": 1}')
        llm.ask_json(agent, "PROMPT", {"a"})
        self.assertEqual(agent.messages, [])

    def test_repairs_once_without_repeating_prompt(self):
        agent = ScriptedAgent("not json", '{"a": 1}')
        self.assertEqual(llm.ask_json(agent, "UNTRUSTED BLOCK", {"a"}), {"a": 1})
        self.assertEqual(len(agent.prompts), 2)
        self.assertNotIn("UNTRUSTED BLOCK", agent.prompts[1])
        self.assertIn('"a"', agent.prompts[1])

    def test_missing_key_is_repaired(self):
        agent = ScriptedAgent('{"b": 1}', '{"a": 1}')
        self.assertEqual(llm.ask_json(agent, "P", {"a"}), {"a": 1})

    def test_raises_after_one_repair(self):
        agent = ScriptedAgent("bad", '{"b": 1}', '{"a": 1}')
        with self.assertRaises(llm.ModelOutputError) as ctx:
            llm.ask_json(agent, "P", {"a"})
        self.assertEqual(ctx.exception.code, "missing_keys")
        self.assertEqual(len(agent.prompts), 2)

    def test_never_logs_prompt_or_output(self):
        agent = ScriptedAgent("SECRET-OUTPUT", "SECRET-OUTPUT")
        with self.assertLogs("customer-update-agent", level="DEBUG") as logs:
            with self.assertRaises(llm.ModelOutputError):
                llm.ask_json(agent, "SECRET-PROMPT", {"a"})
        joined = "\n".join(logs.output)
        self.assertNotIn("SECRET", joined)

    def test_model_transport_error_propagates(self):
        agent = ScriptedAgent(RuntimeError("throttled"))
        with self.assertRaises(RuntimeError):
            llm.ask_json(agent, "P", {"a"})


if __name__ == "__main__":
    unittest.main()
