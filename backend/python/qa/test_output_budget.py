"""Actual handler requests and safe completion failure telemetry; no live calls."""
import copy
import json
import unittest
from unittest import mock

import test_handler
from test_async_jobs import _JobFixture
from test_stream_events import text_stream

handler = test_handler.handler


class TestOutputBudget(_JobFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.history = test_handler.RetrievalTable()
        self.model = mock.Mock()
        self.model.converse.return_value = {
            "stopReason": "end_turn", "output": {"message": {"role": "assistant",
                "content": [{"text": "Complete synthetic answer."}]}}}
        self.model.converse_stream.return_value = text_stream("Complete synthetic answer.")
        for patcher in (
            mock.patch.object(handler, "table", self.history),
            mock.patch.object(handler, "bedrock_runtime", self.model),
            mock.patch.object(handler, "_ASYNC_MODEL", self.model),
            mock.patch.object(handler, "_request_meeting_context", return_value=(None, None, None)),
            mock.patch.object(handler, "_apigw_client"),
            mock.patch.object(handler, "_post_ws", return_value=True),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.event = {"question": "PRIVATE_QUESTION", "userId": "PRIVATE_USER_ID", "sessionId": "PRIVATE_SESSION_ID",
                      "connectionId": "PRIVATE_CONNECTION_ID", "endpoint": "https://synthetic.invalid"}

    def test_same_8192_ceiling_reaches_sync_meeting_ws_and_async_calls(self):
        self.assertEqual(handler.handle_ask("synthetic", session_id="s", user_id="reader")["statusCode"], 200)
        self.assertEqual(handler.handle_meeting_ask("synthetic", "meeting", "reader", "m")["statusCode"], 200)
        self.assertEqual(handler.handle_ask_stream(self.event)["status"], "ok")
        self.jobs.submit("reader", self.body)
        self.jobs.work("reader", self.job_id, handler._execute_job_request, handler._validate_job_sources)
        self.assertEqual(self.jobs.poll("reader", self.job_id, handler._validate_job_sources)["status"], "succeeded")
        self.assertEqual(self.model.converse.call_count, 3)
        self.assertEqual(self.model.converse_stream.call_count, 1)
        for call in self.model.converse.call_args_list + self.model.converse_stream.call_args_list:
            self.assertEqual(call.kwargs["inferenceConfig"]["maxTokens"], 8192)

    def test_stream_failure_retains_safe_stop_usage_and_block_diagnostics_only(self):
        stream = text_stream("PRIVATE_RESPONSE_TEXT", "max_tokens")
        stream["stream"].append({"metadata": {"usage": {
            "inputTokens": 105, "outputTokens": 8192, "totalTokens": 8297,
            "PRIVATE_SOURCE_KEY": "PRIVATE_SECRET"}, "requestId": "PRIVATE_REQUEST_ID"}})
        self.model.converse_stream.return_value = stream
        with self.assertLogs(level="WARNING") as logs:
            result = handler.handle_ask_stream(self.event)
        self.assertEqual(result, {"status": "model_failed", "code": "MODEL_STREAM_INCOMPLETE"})
        records = [line.split("QA completion diagnostic ", 1)[1] for line in logs.output
                   if "QA completion diagnostic " in line]
        self.assertEqual(len(records), 1)
        record = json.loads(records[0])
        self.assertEqual(record["stopReason"], "max_tokens")
        self.assertEqual(record["failure"], "unsupported_stop")
        self.assertEqual(record["outputTokens"], 8192)
        self.assertEqual(record["inputTokens"], 105)
        self.assertEqual(record["openBlock"], "none")
        self.assertEqual(record["toolCount"], 0)
        self.assertLessEqual(len(records[0].encode()), 1024)
        self.assertNotIn("PRIVATE_", "\n".join(logs.output))
        self.assertNotIn(("SESSION#PRIVATE_USER_ID#PRIVATE_SESSION_ID", "MESSAGES"), self.history.items)
        self.model.converse_stream.assert_called_once()

    def test_all_stream_completion_rejections_stay_terminal_with_distinct_metadata(self):
        opened = {"stream": [{"contentBlockDelta": {"delta": {"text": "PRIVATE_PARTIAL"}}},
                             {"messageStop": {"stopReason": "end_turn"}}]}
        tool_open = {"stream": [{"contentBlockStart": {"start": {"toolUse": {
            "toolUseId": "PRIVATE_TOOL_ID", "name": "start_research"}}}},
            {"messageStop": {"stopReason": "tool_use"}}]}
        cases = [
            (text_stream("partial", "max_tokens"), "unsupported_stop", "none", "MODEL_STREAM_INCOMPLETE"),
            (text_stream("partial", None), "missing_stop", "none", "MODEL_STREAM_INCOMPLETE"),
            (opened, "open_block", "text", "MODEL_STREAM_INCOMPLETE"),
            (tool_open, "open_block", "tool_use", "MODEL_STREAM_INCOMPLETE"),
            (text_stream("partial", "tool_use"), "tool_stop_without_tool", "none", "MODEL_STREAM_INCOMPLETE"),
            (text_stream("   "), "empty_answer", "none", "MODEL_STREAM_EMPTY"),
            (text_stream("partial", "content_filtered"), "unsupported_stop", "none", "MODEL_STREAM_INCOMPLETE"),
        ]
        for stream, reason, block, code in cases:
            with self.subTest(reason=reason, block=block):
                self.history.items.clear()
                self.model.converse_stream.reset_mock()
                handler._post_ws.reset_mock()
                self.model.converse_stream.return_value = stream
                with self.assertLogs(level="WARNING") as logs:
                    result = handler.handle_ask_stream(self.event)
                self.assertEqual(result, {"status": "model_failed", "code": code})
                rows = [json.loads(line.split("QA completion diagnostic ", 1)[1])
                        for line in logs.output if "QA completion diagnostic " in line]
                self.assertEqual(len(rows), 1)
                self.assertEqual((rows[0]["failure"], rows[0]["openBlock"]), (reason, block))
                self.assertNotIn("PRIVATE_", "\n".join(logs.output))
                self.assertFalse(any(call.args[2]["type"] == "answer_complete"
                                     for call in handler._post_ws.call_args_list))
                self.assertNotIn(("SESSION#PRIVATE_USER_ID#PRIVATE_SESSION_ID", "MESSAGES"), self.history.items)
                self.model.converse_stream.assert_called_once()

    def test_async_incomplete_responses_remain_failed_without_continuation_or_result(self):
        for index, (stop, text) in enumerate((
                ("max_tokens", "PRIVATE_PARTIAL"), ("end_turn", ""), ("tool_use", "not a tool"),
                (None, "partial"))):
            with self.subTest(stop=stop):
                self.table.items.clear()
                self.history.items.clear()
                self.model.converse.reset_mock()
                self.model.converse.return_value = {
                    "stopReason": stop, "output": {"message": {"role": "assistant", "content": [{"text": text}]}},
                    "usage": {"inputTokens": 200, "outputTokens": 8192, "source": "PRIVATE_SOURCE"}}
                self.job_id = str(self.now * 1000) + "-" + f"{index:032x}"
                body = {**self.body, "requestId": self.job_id}
                self.jobs.submit("reader", body)
                with self.assertLogs(level="WARNING") as logs:
                    self.jobs.work("reader", self.job_id, handler._execute_job_request, handler._validate_job_sources)
                result = self.jobs.poll("reader", self.job_id, handler._validate_job_sources)
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["error"]["code"], "QA_MODEL_INCOMPLETE")
                self.assertNotIn("result", result)
                rows = [json.loads(line.split("QA completion diagnostic ", 1)[1])
                        for line in logs.output if "QA completion diagnostic " in line]
                self.assertEqual(len(rows), 1)
                self.assertEqual(rows[0]["outputTokens"], 8192)
                self.assertNotIn("PRIVATE_", "\n".join(logs.output))
                self.model.converse.assert_called_once()
                self.assertNotIn(("SESSION#reader#chat-test", "MESSAGES"), self.history.items)

    def test_malformed_metadata_does_not_leak_or_change_the_completion_guard(self):
        bad_values = ("PRIVATE_USAGE", [], True, 3.5, -1, float("nan"), float("inf"), 10**100)
        for value in bad_values:
            with self.subTest(type=type(value).__name__):
                stream = text_stream("PRIVATE_TEXT", "PRIVATE_STOP_REASON")
                stream["stream"].append({"metadata": {"usage": {
                    "inputTokens": value, "outputTokens": value, "totalTokens": value,
                    "requestId": "PRIVATE_REQUEST_ID"}, "trace": "PRIVATE_TRACE"}})
                self.model.converse_stream.return_value = stream
                with self.assertLogs(level="WARNING") as logs:
                    result = handler.handle_ask_stream(self.event)
                self.assertEqual(result["code"], "MODEL_STREAM_INCOMPLETE")
                row = next(json.loads(line.split("QA completion diagnostic ", 1)[1])
                           for line in logs.output if "QA completion diagnostic " in line)
                self.assertEqual(row["stopReason"], "other")
                self.assertIsNone(row["inputTokens"])
                self.assertIsNone(row["outputTokens"])
                self.assertIsNone(row["totalTokens"])
                self.assertNotIn("PRIVATE_", "\n".join(logs.output))

    def test_request_and_iterator_failure_do_not_log_exception_payloads(self):
        self.model.converse.side_effect = RuntimeError("PRIVATE_CONVERSE_EXCEPTION")
        with self.assertLogs(level="WARNING") as logs:
            handler.handle_ask("PRIVATE_QUESTION", session_id="s", user_id="reader")
        self.assertNotIn("PRIVATE_", "\n".join(logs.output))
        self.model.converse_stream.side_effect = RuntimeError("PRIVATE_STREAM_EXCEPTION")
        with self.assertLogs(level="WARNING") as logs:
            self.assertEqual(handler.handle_ask_stream(self.event)["code"], "MODEL_STREAM_UNAVAILABLE")
        self.assertNotIn("PRIVATE_", "\n".join(logs.output))
        self.model.converse_stream.side_effect = None
        def interrupted():
            yield {"contentBlockDelta": {"delta": {"text": "PRIVATE_PARTIAL"}}}
            raise RuntimeError("PRIVATE_ITERATOR_EXCEPTION")
        self.model.converse_stream.return_value = {"stream": interrupted()}
        with self.assertLogs(level="WARNING") as logs:
            self.assertEqual(handler.handle_ask_stream(self.event)["code"], "MODEL_STREAM_UNAVAILABLE")
        row = next(json.loads(line.split("QA completion diagnostic ", 1)[1])
                   for line in logs.output if "QA completion diagnostic " in line)
        self.assertEqual((row["failure"], row["openBlock"]), ("iteration_failed", "text"))
        self.assertNotIn("PRIVATE_", "\n".join(logs.output))

    def test_tool_round_limit_records_completed_tool_count_without_replaying_mutation(self):
        first = {"stream": [
            {"contentBlockStart": {"start": {"toolUse": {"toolUseId": "r", "name": "start_research"}}}},
            {"contentBlockDelta": {"delta": {"toolUse": {"input": '{"topic":"synthetic"}'}}}},
            {"contentBlockStop": {}}, {"messageStop": {"stopReason": "tool_use"}},
        ]}
        self.model.converse_stream.return_value = first
        with mock.patch.object(handler, "MAX_TOOL_ROUNDS", 1), \
                mock.patch.object(handler, "check_research_limit", return_value=True), \
                mock.patch.object(handler, "create_research_from_chat", return_value={"researchId": "a" * 32}) as create, \
                self.assertLogs(level="WARNING") as logs:
            result = handler.handle_ask_stream(self.event)
        self.assertEqual(result["code"], "MODEL_TOOL_ROUND_LIMIT")
        row = next(json.loads(line.split("QA completion diagnostic ", 1)[1])
                   for line in logs.output if "QA completion diagnostic " in line)
        self.assertEqual(row["toolCount"], 1)
        self.assertEqual(row["failure"], "tool_round_limit")
        create.assert_called_once()
        self.model.converse_stream.assert_called_once()


class TestClosedDiagnostics(unittest.TestCase):
    def test_closed_schema_and_numeric_caps_never_retain_raw_payloads(self):
        from completion_diagnostics import CompletionDiagnostics, MAX_COUNT, USAGE_FIELDS
        diagnostic = CompletionDiagnostics("PRIVATE_MODE", True)
        diagnostic.response({"stopReason": {"PRIVATE": "STOP"}, "usage": {
            **dict.fromkeys(USAGE_FIELDS, MAX_COUNT), "PRIVATE_KEY": "PRIVATE_SECRET"},
            "output": {"message": {"content": [{"text": "PRIVATE_CONTENT", "toolUse": {"id": "PRIVATE_ID"}}]}}})
        diagnostic.events = diagnostic.starts = diagnostic.stops = diagnostic.unknown_events = MAX_COUNT
        diagnostic.event({"PRIVATE_EVENT": "PRIVATE_SECRET"})
        logger = mock.Mock()
        diagnostic.emit(logger, "PRIVATE_REASON", open_block={"toolUse": {"id": "PRIVATE_ID"}})
        payload = logger.warning.call_args.args[1]
        self.assertLessEqual(len(payload.encode()), 1024)
        self.assertNotIn("PRIVATE", payload)
        self.assertNotIn("PRIVATE", repr(diagnostic.__dict__))
        value = json.loads(payload)
        self.assertEqual(value["eventCount"], MAX_COUNT)
        self.assertEqual(value["unknownEventCount"], MAX_COUNT)
        self.assertEqual(value["stopReason"], "other")
        self.assertEqual(value["failure"], "other")
        self.assertIsNone(value["round"])
        self.assertEqual(set(value), {"version", "mode", "round", "failure", "maxOutputTokens", "stopReason",
            "messageStopSeen", "metadataSeen", "eventCount", "unknownEventCount", "blockStartCount",
            "blockStopCount", "textCharacters", "openBlock", "toolCount", *USAGE_FIELDS})


if __name__ == "__main__":
    unittest.main()
