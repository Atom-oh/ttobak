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
from async_jobs import QAJobs, JobError, JobDeadline, MutationGuard, deadline, RESULT_LIMIT
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


class _JobFixture:
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


    def event(self, method, path, body=None, user='reader'):
        return {'rawPath': path, 'requestContext': {'http': {'method': method},
                'authorizer': {'jwt': {'claims': {'sub': user}}}},
                'body': json.dumps(body) if body is not None else ''}



class TestAsyncJobs(_JobFixture, unittest.TestCase):
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

    def test_committed_claim_followed_by_sdk_conditional_retry_executes_once(self):
        self.jobs.submit('reader', self.body)
        def lost_then_retried(operation, item):
            if operation == 'update' and item.get('status') == 'RUNNING':
                self.table.after_write = None
                # The first update committed, then the SDK's retry saw RUNNING.
                raise ConditionalFailure()
        self.table.after_write = lost_then_retried
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.jobs.poll('reader', self.job_id, self.validate)['status'], 'succeeded')
        self.jobs.work('reader', self.job_id, self.execute, self.validate)
        self.assertEqual(len(self.calls), 1)


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
        self.assertEqual(failure['error']['code'], 'SOURCE_UNAVAILABLE')
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


    def test_combined_input_result_and_proof_use_separate_bounded_complete_items(self):
        from async_jobs import INPUT_LIMIT, PROOF_LIMIT, DDB_ITEM_LIMIT, item_size
        body = dict(self.body, context='한' * ((INPUT_LIMIT - 2048) // 3))
        self.jobs.submit('reader', body)
        def large(user, request, state):
            state['_delivery'].seed(state)
            for index in range(500):
                state['_delivery'].source({
                    'sourcePK': 'USER#reader', 'sourceSK': 'DOC#source-' + str(index),
                    'sourceRevision': 'a' * 64})
            return {'answer': '😀' * ((RESULT_LIMIT - 2048) // 4), 'sources': ['synthetic://source'],
                    'sourceDetails': [{'resourceId': 'source'}]}
        self.jobs.work('reader', self.job_id, large, self.validate)
        result = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertEqual(result['status'], 'succeeded', result)
        items = list(self.table.items.values())
        self.assertEqual(len(items), 3)
        self.assertGreater(sum(item_size(item) for item in items), DDB_ITEM_LIMIT)
        self.assertTrue(all(item_size(item) <= DDB_ITEM_LIMIT for item in items))
        proof = self.table.items[('USER#reader', 'QA_PROOF#' + self.job_id)]
        self.assertLessEqual(len(proof['payload'].encode()), PROOF_LIMIT)
        self.assertEqual(len(json.loads(proof['payload'])['dependencies']), 500)
        self.assertEqual(len(result['result']['answer']), (RESULT_LIMIT - 2048) // 4)

    def test_work_collects_capacity_overflow_and_late_direct_dependencies_without_executor_seeding(self):
        from delivery_proof import validate_delivery
        from tool_history import ToolHistory, CompleteRead
        reader = mock.Mock(return_value=CompleteRead([{'meetingId': 'm', 'title': 'Current'}]))
        history = ToolHistory('reader', {'list_meetings': reader})
        direct = {'sourcePK': 'USER#reader', 'sourceSK': 'DOC#direct', 'sourceRevision': 'b' * 64}
        def execute(user, request, state):
            for index in range(17):
                history.read(state, 'list_meetings', {'keyword': str(index)})
            self.assertFalse(state['replayable'])
            state['dependencies'].append(direct)  # Another trusted reader's final dependency.
            return {'answer': 'current valid answer', 'sources': ['synthetic://direct']}
        def validate(user, proof):
            self.assertEqual(len(proof['dependencies']), 18)
            self.assertIn(direct, proof['dependencies'])
            validate_delivery(proof, lambda dep: True, history)
        self.jobs.submit('reader', self.body)
        self.jobs.work('reader', self.job_id, execute, validate)
        result = self.jobs.poll('reader', self.job_id, validate)
        self.assertEqual(result['status'], 'succeeded', result)
        self.assertEqual(result['result']['answer'], 'current valid answer')
        self.table.reads.clear()
        with self.assertRaises(SourceValidationError):
            self.jobs.poll('reader', self.job_id, lambda user, proof: validate_delivery(proof, lambda dep: False, history))
        self.assertFalse(any(sk.startswith('QA_RESULT#') for _, sk in self.table.reads))


    def test_item_boundary_counts_names_utf8_and_metadata_before_any_write(self):
        from async_jobs import DDB_ITEM_LIMIT, item_size, bounded_item
        item = {'PK': 'USER#reader', 'SK': 'QA_RESULT#' + self.job_id, 'payload': '',
                'pendingShareExpiresAt': self.now + 3600, '메타데이터': '값'}
        remaining = DDB_ITEM_LIMIT - item_size(item)
        item['payload'] = '😀' * (remaining // 4) + 'x' * (remaining % 4)
        self.assertEqual(item_size(item), DDB_ITEM_LIMIT)
        bounded_item(JobTable.roundtrip(item))
        item['payload'] += 'x'
        with self.assertRaises(JobError) as large:
            bounded_item(item)
        self.assertEqual(large.exception.status, 413)
        self.jobs.submit('reader', self.body)
        job = dict(self.table.items[('USER#reader', 'QA_JOB#' + self.job_id)], runId='b' * 32)
        with self.assertRaises(JobError):
            self.jobs._artifact(job, 'RESULT', 'x' * DDB_ITEM_LIMIT)
        self.assertNotIn(('USER#reader', 'QA_RESULT#' + self.job_id), self.table.items)


    def test_control_updates_reserve_aggregate_space_and_bound_error_bytes(self):
        from async_jobs import CONTROL_RESERVE, CONTROL_STRING_LIMITS, item_size
        self.jobs.submit('reader', self.body)
        job = self.table.items[('USER#reader', 'QA_JOB#' + self.job_id)]
        self.jobs._fail(job, 'SYNTHETIC_ERROR', '한😀' * 10000)
        result = self.jobs.poll('reader', self.job_id, self.validate)
        self.assertLessEqual(len(result['error']['message'].encode()), 1024)
        maximum = {key: 'x' * limit for key, limit in CONTROL_STRING_LIMITS.items()}
        maximum.update(runUntil=10**15, dispatchAfter=10**15)
        self.assertLess(item_size(maximum), CONTROL_RESERVE)
        with self.assertRaises(ValueError):
            self.jobs._update({'PK': 'USER#reader', 'SK': 'QA_JOB#' + self.job_id},
                              {'requestJson': 'cannot replace immutable request'}, None)


    def test_deadline_bypasses_catch_all_legacy_fallbacks(self):
        with self.assertRaises(JobDeadline):
            with deadline(0.01):
                try:
                    time.sleep(0.05)
                except Exception:
                    self.fail('deadline was swallowed')


    def test_mutating_tool_retry_after_ambiguous_outcome_never_calls_creation_again(self):
        for outcome in ({'error': 'uncertain'}, TimeoutError('uncertain')):
            with self.subTest(outcome=type(outcome).__name__):
                guard = MutationGuard()
                create = mock.Mock(side_effect=outcome if isinstance(outcome, Exception) else None,
                                   return_value=outcome)
                try:
                    guard.call(create, 'reader', 'topic', 'standard')
                except TimeoutError:
                    pass
                self.assertIn('error', guard.call(create, 'reader', 'topic', 'standard'))
                self.assertIn('error', guard.call(create, 'reader', 'rephrased topic', 'deep'))
                self.assertEqual(create.call_count, 1)


    def test_confirmed_creation_receipt_is_reused_without_repeating_mutation(self):
        guard = MutationGuard()
        create = mock.Mock(return_value={'researchId': 'a' * 32})
        first = guard.call(create, 'reader', ' topic ', 'invalid')
        create.assert_called_once_with('reader', 'topic', 'standard')
        self.assertEqual(guard.call(create, 'reader', 'topic', 'standard'), first)
        self.assertEqual(create.call_count, 1)
        guard.call(create, 'reader', 'a separate explicit research task', 'standard')
        self.assertEqual(create.call_count, 2)


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




if __name__ == '__main__':
    unittest.main()
