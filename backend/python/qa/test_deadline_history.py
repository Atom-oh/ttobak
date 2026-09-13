"""Confirmed tool receipts survive job deadlines without replaying mutations."""
import json
import signal
import time
import unittest
from unittest import mock

import test_handler
from test_async_jobs import _JobFixture
from async_jobs import JobDeadline
from deadline_history import DeadlineHistory, MAX_CHECKPOINT_BYTES
from delivery_proof import DeliveryProof
from session_provenance import new_source_state
from tool_history import CompleteRead, ToolHistory

handler = test_handler.handler


def research_response(*ids):
    return {"stopReason": "tool_use", "output": {"message": {
        "role": "assistant", "content": [{"toolUse": {
            "toolUseId": value, "name": "start_research",
            "input": {"topic": "synthetic " + value, "mode": "standard"},
        }} for value in ids]}}}


class TestJobDeadlineHistory(_JobFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.history = test_handler.RetrievalTable()
        self.model = mock.Mock()
        for patcher in (
            mock.patch.object(handler, "table", self.history),
            mock.patch.object(handler, "_ASYNC_MODEL", self.model),
            mock.patch.object(handler, "_request_meeting_context", return_value=(None, None, None)),
            mock.patch.object(handler, "check_research_limit", return_value=True),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_job(self):
        self.jobs.submit("reader", self.body)
        self.jobs.work("reader", self.job_id, handler._execute_job_request, handler._validate_job_sources)
        job = self.table.items[("USER#reader", "QA_JOB#" + self.job_id)]
        self.assertEqual(job["status"], "FAILED")
        self.assertEqual(job["errorCode"], "QA_JOB_INTERRUPTED")
        self.assertFalse(any(key[1].startswith(("QA_RESULT#", "QA_PROOF#")) for key in self.table.items))
        return job

    def test_model_deadline_after_confirmed_research_preserves_paired_receipt(self):
        self.model.converse.side_effect = [research_response("first"), JobDeadline()]
        with mock.patch.object(handler, "create_research_from_chat",
                               return_value={"researchId": "a" * 32}) as create:
            self.run_job()
            self.assertEqual(create.call_count, 1)
            stored = self.history.items.get(("SESSION#reader#chat-test", "MESSAGES"))
            self.assertIsNotNone(stored, "confirmed receipt must survive the model deadline")
            history = handler.load_session("chat-test", user_id="reader")
            self.assertIn("a" * 32, json.dumps(history))
            self.assertEqual(history[-1]["role"], "assistant")
            self.assertTrue(history[-1]["content"][0]["text"].strip())
            self.assertEqual(create.call_count, 1)
        self.assertEqual(self.model.converse.call_count, 2)

    def test_later_tool_deadline_retains_only_acknowledged_tool_pairs(self):
        self.model.converse.return_value = research_response("first", "unknown")
        with mock.patch.object(handler, "create_research_from_chat",
                               side_effect=[{"researchId": "b" * 32}, JobDeadline()]) as create:
            self.run_job()
        history = handler.load_session("chat-test", user_id="reader")
        self.assertIn("b" * 32, json.dumps(history))
        uses = [block["toolUse"]["toolUseId"] for message in history for block in message["content"]
                if "toolUse" in block]
        results = [block["toolResult"]["toolUseId"] for message in history for block in message["content"]
                   if "toolResult" in block]
        self.assertEqual(uses, ["first"])
        self.assertEqual(results, ["first"])
        self.assertEqual(create.call_count, 2)
        self.assertEqual(self.model.converse.call_count, 1)

    def test_deadline_before_any_receipt_never_creates_history(self):
        self.model.converse.side_effect = JobDeadline()
        self.run_job()
        self.assertNotIn(("SESSION#reader#chat-test", "MESSAGES"), self.history.items)

    def test_unacknowledged_history_write_never_makes_job_successful(self):
        self.model.converse.side_effect = [research_response("first"), JobDeadline()]
        original_put = self.history.put_item
        def lost_ack(**kwargs):
            original_put(**kwargs)  # The server may have committed despite a timeout.
            raise TimeoutError("synthetic lost acknowledgement")
        with mock.patch.object(handler, "create_research_from_chat", return_value={"researchId": "c" * 32}), \
                mock.patch.object(self.history, "put_item", side_effect=lost_ack):
            job = self.run_job()
        self.assertNotIn("sessionContinuable", job)
        self.assertNotIn("result", job)


class TestDeadlineCheckpoint(unittest.TestCase):
    def checkpoint(self):
        state = new_source_state()
        state["_delivery"] = DeliveryProof()
        state["_delivery"].seed(state)
        self.reader = mock.Mock(return_value=CompleteRead([{"meetingId": "m", "title": "PRIVATE"}]))
        self.history = ToolHistory("reader", {"list_meetings": self.reader})
        value = self.history.read(state, "list_meetings", {})
        messages = [
            {"role": "user", "content": [{"text": "list"}]},
            {"role": "assistant", "content": [
                {"text": "Unconfirmed model claim must not survive"},
                {"toolUse": {"toolUseId": "one", "name": "list_meetings", "input": {}}},
                {"toolUse": {"toolUseId": "pending", "name": "start_research", "input": {}}},
            ]},
        ]
        results = [{"toolResult": {"toolUseId": "one", "content": [{"text": json.dumps(value)}]}}]
        checkpoint = DeadlineHistory()
        self.assertTrue(checkpoint.capture(messages, results, state, []))
        return checkpoint, messages, results, state

    def test_current_read_proof_is_required_after_edit_revocation_or_outage(self):
        for change in ("edit", "revoke", "unavailable"):
            with self.subTest(change=change):
                checkpoint, _, _, _ = self.checkpoint()
                if change == "edit":
                    self.reader.return_value = CompleteRead([{"meetingId": "m", "title": "CHANGED"}])
                elif change == "revoke":
                    self.reader.return_value = CompleteRead([])
                else:
                    self.reader.side_effect = RuntimeError("synthetic read outage")
                save = mock.Mock(return_value=True)
                self.assertFalse(checkpoint.preserve(
                    lambda state: handler._validate_answer_sources("reader", state, self.history), save))
                save.assert_not_called()

    def test_immutable_checkpoint_excludes_pending_calls_and_model_claims(self):
        checkpoint, messages, results, state = self.checkpoint()
        messages.clear()
        results.clear()
        state["dependencies"].clear()
        state["replayable"] = False
        saved = []
        def save(messages, state, details):
            saved.append((messages, state, details))
            return True
        self.assertTrue(checkpoint.preserve(
            lambda state: handler._validate_answer_sources("reader", state, self.history), save))
        raw = json.dumps(saved[0][0])
        self.assertIn("PRIVATE", raw)
        self.assertNotIn("pending", raw)
        self.assertNotIn("Unconfirmed model claim", raw)
        self.assertTrue(saved[0][1]["dependencies"])

    def test_invalid_or_oversized_candidate_keeps_previous_valid_checkpoint(self):
        for mode in ("oversized", "untracked", "invalid_delivery", "unpaired"):
            with self.subTest(mode=mode):
                checkpoint, messages, results, state = self.checkpoint()
                if mode == "oversized":
                    results[0]["toolResult"]["content"][0]["text"] = "한" * MAX_CHECKPOINT_BYTES
                elif mode == "untracked":
                    state["replayable"] = False
                elif mode == "invalid_delivery":
                    state["_delivery"].reject()
                else:
                    results[0]["toolResult"]["toolUseId"] = "missing"
                self.assertFalse(checkpoint.capture(messages, results, state, []))
                save = mock.Mock(return_value=True)
                self.assertTrue(checkpoint.preserve(
                    lambda state: handler._validate_answer_sources("reader", state, self.history), save))
                self.assertIn("PRIVATE", json.dumps(save.call_args.args[0]))

    def test_no_acknowledgement_is_never_claimed_as_saved(self):
        for outcome in (False, None, 1, TimeoutError("synthetic ambiguous write")):
            with self.subTest(outcome=type(outcome).__name__):
                checkpoint, _, _, _ = self.checkpoint()
                save = mock.Mock(side_effect=outcome) if isinstance(outcome, Exception) else mock.Mock(return_value=outcome)
                self.assertFalse(checkpoint.preserve(lambda state: None, save))
                self.assertEqual(save.call_count, 1)

    def test_real_alarm_bounds_validation_and_save_and_restores_timer(self):
        for phase in ("validate", "save"):
            with self.subTest(phase=phase):
                checkpoint, _, _, _ = self.checkpoint()
                previous_handler = signal.getsignal(signal.SIGALRM)
                self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0, 0))
                def slow(*args):
                    time.sleep(5)
                    return True
                validate = slow if phase == "validate" else lambda state: None
                save = slow if phase == "save" else mock.Mock(return_value=True)
                started = time.monotonic()
                with mock.patch("deadline_history.CLEANUP_SECONDS", 0.05):
                    self.assertFalse(checkpoint.preserve(validate, save))
                self.assertLess(time.monotonic() - started, 1)
                if phase == "validate":
                    save.assert_not_called()
                self.assertEqual(signal.getsignal(signal.SIGALRM), previous_handler)
                self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0, 0))
