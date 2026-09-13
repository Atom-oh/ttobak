"""Current-answer authorization does not inherit conversation replay budgets."""
import json
import time
import unittest
from unittest import mock

import test_handler
from async_jobs import QAJobs
from delivery_proof import DeliveryProof, validate_delivery
from session_provenance import new_source_state, SourceValidationError, SourceUnavailable
from tool_history import ToolHistory, CompleteRead
from tool_context import build_tool_context, track_tool_history
from test_async_jobs import JobTable
from test_kb_fixtures import _QAConversationFixture
from test_source_contract import _SourceFixture

handler = test_handler.handler


class _DeliveryFixture(_SourceFixture, _QAConversationFixture):
    def setUp(self):
        _SourceFixture.setUp(self)
        self.set_up_transport(self.table)


    def state(self):
        state = new_source_state()
        state['_delivery'] = DeliveryProof()
        state['_delivery'].seed(state)
        return state



class TestDeliveryProof(_DeliveryFixture, unittest.TestCase):
    def test_ninth_successful_empty_search_is_deliverable_and_still_rechecked(self):
        state = self.state()
        context = handler._agent_context('reader', None, None, state, [])
        for index in range(9):
            context['sourceReadRecorded'] = context['deliveryReadRecorded'] = False
            text, sources = handler.execute_tool('search_knowledge_base', {'query': 'empty ' + str(index)}, context)
            track_tool_history(state, 'search_knowledge_base', context)
            self.assertNotIn('Tool error', text)
            self.assertEqual(sources, [])
        self.assertFalse(state['replayable'])
        self.assertEqual(len(state['dependencies']), 8)
        proof = state['_delivery'].finish(state)
        self.assertEqual(len(proof['dependencies']), 9)
        validate_delivery(proof, lambda dep: handler._source_is_current('reader', dep, proof), handler._tool_history('reader'))
        self.doc(pk='USER#reader', sourceUserId='reader', content='empty 8')
        with self.assertRaises(SourceValidationError):
            validate_delivery(proof, lambda dep: handler._source_is_current('reader', dep, proof), handler._tool_history('reader'))


    def test_large_readonly_and_seventeenth_query_keep_current_results_with_separate_proof(self):
        state = self.state()
        rows = [{'meetingId': str(index), 'title': '한글 제목 ' * 100, 'status': 'done'} for index in range(100)]
        reader = mock.Mock(return_value=CompleteRead(rows))
        history = ToolHistory('reader', {'list_meetings': reader})
        for index in range(17):
            result = history.read(state, 'list_meetings', {'keyword': str(index), 'limit': 100})
            self.assertEqual(result, rows)
        self.assertFalse(state['replayable'])
        proof = state['_delivery'].finish(state)
        self.assertEqual(len(proof['dependencies']), 17)
        validate_delivery(proof, lambda dep: True, history)
        self.assertTrue(all(call.args[0] == 'reader' for call in reader.call_args_list))
        rows[0]['title'] = 'CHANGED'
        with self.assertRaises(SourceValidationError):
            validate_delivery(proof, lambda dep: True, history)
        reader.side_effect = RuntimeError('synthetic authorization read outage')
        with self.assertRaises(SourceUnavailable):
            validate_delivery(proof, lambda dep: True, history)


    def test_seventeenth_small_readonly_result_exceeds_only_history_dependency_budget(self):
        state = self.state()
        reader = mock.Mock(return_value=CompleteRead([{'meetingId': 'm', 'title': 'Current'}]))
        history = ToolHistory('reader', {'list_meetings': reader})
        for index in range(17):
            result = history.read(state, 'list_meetings', {'keyword': str(index)})
            self.assertEqual(result[0]['meetingId'], 'm')
        self.assertEqual(len(state['dependencies']), 16)
        self.assertFalse(state['replayable'])
        proof = state['_delivery'].finish(state)
        self.assertEqual(len(proof['dependencies']), 17)
        validate_delivery(proof, lambda dep: True, history)


    def test_confirmed_mutation_receipt_does_not_replay_creation_at_poll_validation(self):
        state = self.state()
        history = ToolHistory('reader', {})
        create = mock.Mock(return_value={'researchId': 'b' * 32})
        context = build_tool_context('reader', '', state, [], source_access=handler._source_access(),
                                     history=history, create_research=create,
                                     check_research_limit=lambda uid: True, check_web_search_limit=lambda uid: True)
        context['create_research']('reader', 'topic', 'standard')
        proof = state['_delivery'].finish(state)
        self.assertEqual(len(proof['dependencies']), 1)
        validate_delivery(proof, lambda dep: True, history)
        create.assert_called_once()
