"""Active HTTP/worker wiring tests, installed only with the runtime routes."""
import json
import time
import unittest
from unittest import mock

import test_handler
from async_jobs import QAJobs
from session_provenance import SourceValidationError
from tool_context import track_tool_history
from test_async_jobs import _JobFixture, JobTable
from test_delivery_proof import _DeliveryFixture

handler = test_handler.handler


class TestAsyncRuntime(_JobFixture, unittest.TestCase):
    def test_nonstream_exhaustion_or_model_failure_keeps_completed_receipt_without_success(self):
        tool = {'stopReason': 'tool_use', 'output': {'message': {'role': 'assistant', 'content': [
            {'text': ''},
            {'toolUse': {'toolUseId': 'one', 'name': 'start_research',
                         'input': {'topic': 'synthetic', 'mode': 'standard'}}},
        ]}}}
        for mode in ('budget', 'model-failure'):
            with self.subTest(mode=mode):
                self.setUp()
                self.jobs.submit('reader', self.body)
                model = mock.Mock()
                model.converse.side_effect = [tool, RuntimeError('synthetic model failure')]
                with mock.patch.object(handler, 'table', test_handler.RetrievalTable()), \
                        mock.patch.object(handler, '_ASYNC_MODEL', model), \
                        mock.patch.object(handler, 'MAX_TOOL_ROUNDS', 1 if mode == 'budget' else 5), \
                        mock.patch.object(handler, '_request_meeting_context', return_value=(None, None, None)), \
                        mock.patch.object(handler, 'check_research_limit', return_value=True), \
                        mock.patch.object(handler, 'create_research_from_chat', return_value={'researchId': 'b' * 32}) as create:
                    self.jobs.work('reader', self.job_id, handler._execute_job_request, handler._validate_job_sources)
                    result = self.jobs.poll('reader', self.job_id, handler._validate_job_sources)
                    self.assertEqual(result['status'], 'failed', result)
                    self.assertEqual(result['error']['code'], 'QA_MODEL_INCOMPLETE')
                    self.assertNotIn('result', result)
                    history = handler.load_session(self.body['sessionId'], user_id='reader')
                    self.assertIn('b' * 32, json.dumps(history))
                    self.assertIn('이전 도구 실행 결과', history[-1]['content'][0]['text'])
                    self.assertTrue(all('text' not in block or block['text'].strip()
                                        for message in history for block in message['content']))
                    self.jobs.work('reader', self.job_id, handler._execute_job_request, handler._validate_job_sources)
                    create.assert_called_once()
                    self.assertEqual(model.converse.call_count, 1 if mode == 'budget' else 2)

    def test_empty_truncated_or_invalid_nonstream_completion_never_publishes(self):
        for stop, text in (('end_turn', ''), ('max_tokens', 'partial'), ('tool_use', 'no tool')):
            with self.subTest(stop=stop):
                self.setUp()
                self.jobs.submit('reader', self.body)
                model = mock.Mock()
                model.converse.return_value = {'stopReason': stop, 'output': {
                    'message': {'role': 'assistant', 'content': [{'text': text}]}}}
                with mock.patch.object(handler, 'table', test_handler.RetrievalTable()), \
                        mock.patch.object(handler, '_ASYNC_MODEL', model), \
                        mock.patch.object(handler, '_request_meeting_context', return_value=(None, None, None)):
                    self.jobs.work('reader', self.job_id, handler._execute_job_request, handler._validate_job_sources)
                result = self.jobs.poll('reader', self.job_id, self.validate)
                self.assertEqual(result['status'], 'failed', result)
                self.assertEqual(result['error']['code'], 'QA_MODEL_INCOMPLETE')
                self.assertNotIn('result', result)
                model.converse.assert_called_once()

    def test_authenticated_async_routes_preserve_sync_contract_and_reject_body_identity(self):
        with mock.patch.object(handler, '_job_service', return_value=self.jobs, create=True):
            result = handler.lambda_handler(self.event('POST', '/api/qa/jobs', self.body), None)
            self.assertEqual(result['statusCode'], 202)
            self.assertEqual(json.loads(result['body'])['jobId'], self.job_id)
            result = handler.lambda_handler(self.event('GET', '/api/qa/jobs/' + self.job_id, user='other'), None)
            self.assertEqual(result['statusCode'], 404)
            forged = self.event('POST', '/api/qa/jobs', dict(self.body, userId='victim'))
            forged['requestContext']['authorizer'] = {}
            self.assertEqual(handler.lambda_handler(forged, None)['statusCode'], 401)
        with mock.patch.object(handler, 'extract_user_id', return_value='reader'), \
                mock.patch.object(handler, 'handle_ask', return_value={'statusCode': 200, 'body': 'sync'}) as sync:
            self.assertEqual(handler.lambda_handler(self.event('POST', '/api/qa/ask', {'question': 'old client'}), None)['body'], 'sync')
            sync.assert_called_once()


    def test_queue_event_uses_saved_request_and_rejects_other_queue(self):
        self.jobs.submit('reader', self.body)
        record = {'messageId': 'message', 'eventSource': 'aws:sqs', 'eventSourceARN': 'arn:expected',
                  'body': json.dumps({'version': 1, 'userId': 'reader', 'jobId': self.job_id})}
        with mock.patch.object(handler, '_job_service', return_value=self.jobs, create=True), \
                mock.patch.object(handler, 'QA_JOBS_QUEUE_ARN', 'arn:expected', create=True), \
                mock.patch.object(handler, '_execute_job_request', side_effect=self.execute, create=True), \
                mock.patch.object(handler, '_validate_job_sources', side_effect=self.validate):
            result = handler.lambda_handler({'Records': [record]}, None)
            self.assertEqual(result, {'batchItemFailures': []})
            handler.lambda_handler({'Records': [record]}, None)
            self.assertEqual(len(self.calls), 1)
            wrong = dict(record, eventSourceARN='arn:other')
            handler.lambda_handler({'Records': [wrong]}, None)
            self.assertEqual(len(self.calls), 1)


    def test_async_executor_keeps_the_actual_handler_model_and_source_path(self):
        self.jobs.submit('reader', dict(self.body, meetingId='meeting', context='caller supplied context'))
        model = mock.Mock()
        model.converse.return_value = {'stopReason': 'end_turn',
                                      'output': {'message': {'role': 'assistant', 'content': [{'text': 'whole answer'}]}}}
        def load(user, mid, text=None, *, source_state, source_details):
            self.assertEqual((user, mid, text), ('reader', 'meeting', 'caller supplied context'))
            source_state['requestMeetingId'] = mid
            source_state['dependencies'] = [{'sourcePK': 'USER#reader', 'sourceSK': 'MEETING#meeting',
                                            'sourceRevision': 'a' * 64}]
            return 'current execution-time transcript', 'current notes', None
        with mock.patch.object(handler, '_ASYNC_MODEL', model), \
                mock.patch.object(handler, '_request_meeting_context', side_effect=load), \
                mock.patch.object(handler, 'load_session', return_value=[]), \
                mock.patch.object(handler, 'save_session'), \
                mock.patch.object(handler, '_source_is_current', return_value=True):
            self.jobs.work('reader', self.job_id, handler._execute_job_request, self.validate)
        self.assertEqual(model.converse.call_count, 1)
        model_input = json.dumps(model.converse.call_args.kwargs)
        self.assertIn('current execution-time transcript', model_input)
        self.assertIn('current notes', model_input)
        self.assertEqual(self.jobs.poll('reader', self.job_id, self.validate)['result']['answer'], 'whole answer')



class TestAsyncDelivery(_DeliveryFixture, unittest.TestCase):
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
