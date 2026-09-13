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


class TestDeliveryProof(_SourceFixture, _QAConversationFixture, unittest.TestCase):
    def setUp(self):
        _SourceFixture.setUp(self)
        self.set_up_transport(self.table)

    def state(self):
        state = new_source_state()
        state['_delivery'] = DeliveryProof()
        state['_delivery'].seed(state)
        return state

    def test_worker_delivers_129th_current_source_and_poll_rechecks_it_before_body(self):
        old = []
        for index in range(128):
            did = 'old-' + str(index)
            self.table.put_item(Item={
                'PK': 'USER#reader', 'SK': 'DOC#' + did, 'docId': did, 'entityType': 'USER_DOC',
                'sourceUserId': 'reader', 'title': 'old', 'content': 'old text'})
            snapshot = self.source_reader.read('reader', 'USER#reader', 'DOC#' + did)
            old.append({'sourcePK': 'USER#reader', 'sourceSK': 'DOC#' + did,
                        'sourceRevision': snapshot['revision']})
        self.doc(content='CURRENT_129')
        self.grant()
        self.replies('rest', 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        def history(*args, source_state, **kwargs):
            source_state['dependencies'] = old.copy()
            return []
        job_table = JobTable()
        jobs = QAJobs(job_table, mock.Mock(), 'https://sqs.invalid/q')
        jid = str(int(time.time() * 1000)) + '-' + 'a' * 32
        jobs.submit('reader', {'requestId': jid, 'question': 'read', 'sessionId': 'chat-capacity'})
        with mock.patch.object(handler, 'load_session', side_effect=history), \
                mock.patch.object(handler, 'save_session') as saved, \
                mock.patch.object(handler, '_ASYNC_MODEL', self.model):
            jobs.work('reader', jid, handler._execute_job_request, handler._validate_job_sources)
        result = jobs.poll('reader', jid, handler._validate_job_sources)
        self.assertEqual(result['status'], 'succeeded', result)
        self.assertEqual(result['result']['answer'], 'PRIVATE_CHOICE')
        self.assertEqual(result['result']['sourceDetails'][0]['resourceId'], 'doc')
        self.assertFalse(saved.call_args.kwargs['source_state']['replayable'])
        proof = json.loads(job_table.items[('USER#reader', 'QA_PROOF#' + jid)]['payload'])
        self.assertEqual(len(proof['dependencies']), 129)
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        job_table.reads.clear()
        with self.assertRaises(SourceValidationError):
            jobs.poll('reader', jid, handler._validate_job_sources)
        self.assertFalse(any(sk.startswith('QA_RESULT#') for _, sk in job_table.reads))

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
        handler._validate_job_sources('reader', proof)
        self.doc(pk='USER#reader', sourceUserId='reader', content='empty 8')
        with self.assertRaises(SourceValidationError):
            handler._validate_job_sources('reader', proof)

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

    def test_uncovered_failed_and_changed_private_reads_cannot_pass_delivery(self):
        self.doc()
        self.grant()
        for fault in ('skipped', 'failed', 'changed'):
            with self.subTest(fault=fault):
                state = self.state()
                context = handler._agent_context('reader', None, None, state, [])
                context['load_document_context']('reader', 'USER#owner', 'doc')
                context['sourceReadRecorded'] = context['deliveryReadRecorded'] = False
                if fault == 'failed':
                    with mock.patch.object(handler.table, 'get_item', side_effect=RuntimeError('read unavailable')):
                        handler.execute_tool('get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'}, context)
                elif fault == 'changed':
                    self.table.items[('USER#owner', 'DOC#doc')]['content'] = 'changed'
                    handler.execute_tool('get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'}, context)
                else:
                    handler.execute_tool('get_document_detail', {'offset': -1}, context)
                track_tool_history(state, 'get_document_detail', context)
                with self.assertRaises(SourceValidationError):
                    handler._validate_answer_sources('reader', state)

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
