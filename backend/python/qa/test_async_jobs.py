"""Durable async QA contracts; no AWS/model calls."""
import copy
import json
import time
import types
import unittest
from unittest import mock

from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
from boto3.session import Session
from botocore.awsrequest import AWSResponse

import test_handler
from async_jobs import QAJobs, JobError, JobDeadline, deadline, RESULT_LIMIT, PROOF_LIMIT
from session_provenance import SourceValidationError

handler = test_handler.handler


class ConditionalFailure(Exception):
    pass


def matches(condition, item):
    expression = condition.get_expression()
    op, values = expression['operator'], expression['values']
    if op == 'AND':
        return all(matches(value, item) for value in values)
    if op == 'OR':
        return any(matches(value, item) for value in values)
    name = values[0].name
    if op == 'attribute_not_exists':
        return name not in item
    if op == '=':
        return item.get(name) == values[1]
    if name not in item:
        return False
    return item[name] > values[1] if op == '>' else item[name] <= values[1]


class JobTable:
    def __init__(self):
        self.items, self.reads = {}, []
        self.meta = types.SimpleNamespace(client=types.SimpleNamespace(
            exceptions=types.SimpleNamespace(ConditionalCheckFailedException=ConditionalFailure)))
        self.after_write = None
        self.before_update = None

    @staticmethod
    def roundtrip(item):
        return TypeDeserializer().deserialize(TypeSerializer().serialize(item))

    def get_item(self, Key, ConsistentRead):
        assert ConsistentRead is True
        key = Key['PK'], Key['SK']
        self.reads.append(key)
        return {'Item': copy.deepcopy(self.items[key])} if key in self.items else {}

    def put_item(self, Item, ConditionExpression):
        key = Item['PK'], Item['SK']
        if not matches(ConditionExpression, self.items.get(key, {})):
            raise ConditionalFailure()
        self.items[key] = self.roundtrip(Item)
        if self.after_write:
            self.after_write('put', self.items[key])

    def update_item(self, Key, UpdateExpression, ExpressionAttributeNames,
                    ExpressionAttributeValues, ConditionExpression):
        if self.before_update:
            callback, self.before_update = self.before_update, None
            callback()
        key = Key['PK'], Key['SK']
        if not matches(ConditionExpression, self.items.get(key, {})):
            raise ConditionalFailure()
        for assignment in UpdateExpression.removeprefix('SET ').split(', '):
            name, value = assignment.split(' = ')
            self.items[key][ExpressionAttributeNames[name]] = self.roundtrip(ExpressionAttributeValues[value])
        if self.after_write:
            self.after_write('update', self.items[key])


class TestAsyncJobs(unittest.TestCase):
    def setUp(self):
        self.now = 1789272000
        self.table = JobTable()
        self.queue = mock.Mock()
        self.jobs = QAJobs(self.table, self.queue, 'https://sqs.invalid/queue', clock=lambda: self.now)
        self.job_id = str(self.now * 1000) + '-' + 'a' * 32
        self.body = {'requestId': self.job_id, 'question': 'synthetic', 'sessionId': 'chat-test'}
        self.calls = []

    def execute(self, user, request, state):
        self.calls.append((user, request))
        state['dependencies'] = [{'sourcePK': 'USER#owner', 'sourceSK': 'DOC#document',
                                  'sourceRevision': 'a' * 64}]
        return {'answer': 'synthetic complete answer', 'sources': ['synthetic://source'],
                'sourceDetails': [{'resourceId': 'document'}], 'toolsUsed': ['start_research'],
                'usedKB': False, 'usedDocs': False, 'toolHistoryCoverage': []}

    def validate(self, user, state):
        self.assertEqual(user, 'reader')
        self.assertIs(state['replayable'], True)

    def test_submit_is_user_bound_idempotent_and_conflicting_request_never_executes(self):
        first = self.jobs.submit('reader', self.body)
        self.assertEqual(first['status'], 'queued')
        self.assertEqual(self.jobs.submit('reader', self.body), first)
        self.assertEqual(self.queue.send_message.call_count, 1)
        with self.assertRaises(JobError) as conflict:
            self.jobs.submit('reader', dict(self.body, question='different'))
        self.assertEqual(conflict.exception.status, 409)
        with self.assertRaises(JobError) as denied:
            self.jobs.poll('other', self.job_id, self.validate)
        self.assertEqual(denied.exception.status, 404)
        self.assertEqual(self.calls, [])

    def test_uncertain_enqueue_is_repaired_only_while_queued_and_duplicate_events_do_not_repeat_actions(self):
        self.queue.send_message.side_effect = TimeoutError('private request body')
        self.jobs.submit('reader', self.body)
        self.now += 11
        self.queue.send_message.side_effect = None
        self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(self.queue.send_message.call_count, 2)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.now += 12
        result = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(result['result']['answer'], 'synthetic complete answer')
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.queue.send_message.call_count, 2)

    def test_concurrent_claim_loser_and_ambiguous_ack_cannot_reexecute(self):
        self.jobs.submit('reader', self.body)
        self.table.before_update = lambda: self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.assertEqual(len(self.calls), 1)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.assertEqual(len(self.calls), 1)

    def test_write_ack_loss_recovers_without_regenerating_answer(self):
        self.jobs.submit('reader', self.body)
        def lost_ack(operation, item):
            if item.get('status') in ('RUNNING', 'SUCCEEDED') or item.get('SK', '').startswith(('QA_RESULT#', 'QA_PROOF#')):
                raise TimeoutError('ambiguous write acknowledgement')
        self.table.after_write = lost_ack
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.table.after_write = None
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.jobs.poll('reader', self.job_id, self.validate)['status'], 'succeeded')

    def test_source_revocation_blocks_cached_body_read_and_never_replays_mutating_tools(self):
        self.jobs.submit('reader', self.body)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.table.reads.clear()
        with self.assertRaises(SourceValidationError):
            self.jobs.poll('reader', self.job_id, lambda *args: (_ for _ in ()).throw(SourceValidationError()))
        self.assertFalse(any(sk.startswith('QA_RESULT#') for _, sk in self.table.reads))
        self.assertEqual(len(self.calls), 1)

    def test_source_change_during_cached_body_read_is_checked_again(self):
        self.jobs.submit('reader', self.body)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        checked = []
        def current(*args):
            checked.append(True)
            if len(checked) == 2:
                raise SourceValidationError()
        with self.assertRaises(SourceValidationError):
            self.jobs.poll('reader', self.job_id, current)
        self.assertEqual(len(checked), 2)
        self.assertEqual(len(self.calls), 1)

    def test_untracked_proof_and_corrupt_cached_result_are_never_released(self):
        self.jobs.submit('reader', self.body)
        def untracked(user, request, state):
            result = self.execute(user, request, state)
            state['replayable'] = False
            return result
        self.jobs.work('reader', self.job_id, untracked, lambda *args: None)
        failure = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(failure['status'], 'failed')
        self.assertEqual(failure['error']['code'], 'QA_RESULT_UNVERIFIABLE')
        self.assertEqual(len(self.calls), 1)
        # A different request gets a separately scoped result.
        second = self.job_id[:-1] + 'b'
        self.jobs.submit('reader', dict(self.body, requestId=second))
        self.jobs.work('reader', second, self.execute, self.validate)
        self.table.items[('USER#reader', 'QA_RESULT#' + second)]['payload'] = '{"answer":"tampered"}'
        with self.assertRaises(JobError):
            self.jobs.poll('reader', second, self.validate)

    def test_running_expiry_and_ttl_are_not_new_execution_opportunities(self):
        self.jobs.submit('reader', self.body)
        def interrupted(*args):
            self.calls.append('side effect may have completed')
            raise JobDeadline()
        self.jobs.work('reader', self.job_id, interrupted, self.validate)
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        result = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(result['error']['code'], 'QA_JOB_INTERRUPTED')
        self.assertEqual(len(self.calls), 1)
        self.now += 3601
        with self.assertRaises(JobError) as expired:
            self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(expired.exception.status, 410)
        self.table.items.clear()  # Model DynamoDB's eventual TTL sweep.
        with self.assertRaises(JobError):
            self.jobs.submit('reader', self.body)
        self.assertEqual(len(self.calls), 1)

    def test_payload_limits_fail_explicitly_without_truncating_source_results(self):
        self.jobs.submit('reader', self.body)
        def huge(user, request, state):
            result = self.execute(user, request, state)
            result['answer'] = '한' * RESULT_LIMIT
            return result
        self.jobs.work('reader', self.job_id, huge, self.validate)
        result = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(result['status'], 'failed')
        self.assertNotIn('result', result)
        self.assertEqual(result['error']['code'], 'QA_PAYLOAD_TOO_LARGE')

    def test_deadline_bypasses_catch_all_legacy_fallbacks(self):
        with self.assertRaises(JobDeadline):
            with deadline(0.01):
                try:
                    time.sleep(0.05)
                except Exception:
                    self.fail('deadline was swallowed')

    def event(self, method, path, body=None, user='reader'):
        return {'rawPath': path, 'requestContext': {'http': {'method': method},
                'authorizer': {'jwt': {'claims': {'sub': user}}}},
                'body': json.dumps(body) if body is not None else ''}

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
                mock.patch.object(handler, '_validate_answer_sources', side_effect=self.validate):
            result = handler.lambda_handler({'Records': [record]}, None)
            self.assertEqual(result, {'batchItemFailures': []})
            handler.lambda_handler({'Records': [record]}, None)
            self.assertEqual(len(self.calls), 1)
            wrong = dict(record, eventSourceARN='arn:other')
            handler.lambda_handler({'Records': [wrong]}, None)
            self.assertEqual(len(self.calls), 1)

    def test_native_dynamodb_conditions_and_ttl_reach_the_sdk_wire(self):
        resource = Session(aws_access_key_id='synthetic', aws_secret_access_key='synthetic',
                           region_name='us-east-1').resource('dynamodb', endpoint_url='https://synthetic.invalid')
        table = resource.Table('synthetic-table')
        self.addCleanup(table.meta.client.close)
        calls = []
        def wire(model, params, **kwargs):
            calls.append((model.name, json.loads(params['body'])))
            return AWSResponse('https://synthetic.invalid', 200, {}, None), {}
        table.meta.client.meta.events.register('before-call.dynamodb', wire)
        jobs = QAJobs(table, self.queue, 'https://sqs.invalid/queue', clock=lambda: self.now)
        jobs.submit('reader', self.body)
        put = next(data for operation, data in calls if operation == 'PutItem')
        update = next(data for operation, data in calls if operation == 'UpdateItem')
        self.assertEqual(put['Item']['pendingShareExpiresAt'], {'N': str(self.now + 3600)})
        self.assertEqual(put['Item']['PK'], {'S': 'USER#reader'})
        self.assertIn('attribute_not_exists', put['ConditionExpression'])
        self.assertIn('status', update['ExpressionAttributeNames'].values())
        self.assertIn('deadlineAt', update['ExpressionAttributeNames'].values())
        self.assertTrue(all(data['ConsistentRead'] for operation, data in calls if operation == 'GetItem'))
        pointer = json.loads(self.queue.send_message.call_args.kwargs['MessageBody'])
        self.assertEqual(set(pointer), {'version', 'userId', 'jobId'})
        self.assertNotIn('synthetic', pointer.values())

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


if __name__ == '__main__':
    unittest.main()
