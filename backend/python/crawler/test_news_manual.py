"""Manual collection starter: claim once, start one versioned run, nothing else."""

import json
import os
import unittest
from unittest import mock

from botocore.exceptions import ClientError

import news_manual

SOURCE = 'acct_acct-1_0123abcd'
REQUEST = '7d3f0c3e-1b2a-4c5d-8e9f-0a1b2c3d4e5f'
ENV = {
    'TABLE_NAME': 'synthetic-manual', 'NEWS_PIPELINE_MODE': 'all', 'NEWS_LEGACY_WRITES': 'off',
    'NEWS_CANARY_SOURCE_ID': '', 'NEWS_PRICE_VERIFIED_ON': '2026-11-10',
    'NEWS_INPUT_USD_PER_MTOK': '0.125', 'NEWS_OUTPUT_USD_PER_MTOK': '0.50',
    'NEWS_RUN_LIMIT_USD': '2', 'NEWS_MONTH_LIMIT_USD': '25',
    'PUBLICATION_STATE_MACHINE_ARN': 'arn:child', 'DAILY_STATE_MACHINE_ARN': 'arn:daily',
    'NEWS_DAILY_HOUR_UTC': '19', 'NEWS_DAILY_MINUTE_UTC': '10', 'NEWS_DAILY_LEAD_SECONDS': '2700',
}
NOW = 1794441600  # 2026-11-12T00:00:00Z


def event(**changes):
    return {'manualCollect': {'sourceId': SOURCE, 'accountId': 'acct-1',
                              'requestId': REQUEST, **changes}}


def client_error(code):
    return ClientError({'Error': {'Code': code, 'Message': 'synthetic'}}, 'Operation')


def row(**changes):
    return {'PK': f'CRAWLER#{SOURCE}', 'SK': 'CONFIG', 'sourceId': SOURCE,
            'accountId': 'acct-1', 'lastCollectRequestId': REQUEST, **changes}


class TestManualStarter(unittest.TestCase):
    def setUp(self):
        self.table = mock.Mock()
        self.table.update_item.return_value = {}
        self.sfn = mock.Mock()
        self.sfn.list_executions.return_value = {'executions': []}
        self.sfn.start_execution.return_value = {'executionArn': 'arn'}
        patches = [
            mock.patch.dict(os.environ, ENV, clear=True),
            mock.patch.object(news_manual, '_table', return_value=self.table),
            mock.patch.object(news_manual, '_sfn', return_value=self.sfn),
            mock.patch.object(news_manual.time, 'time', return_value=NOW),
        ]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def run_handler(self, value=None):
        return news_manual.handler(event() if value is None else value, None)

    def assert_no_work(self, claimed=False):
        self.sfn.start_execution.assert_not_called()
        if not claimed:
            self.table.update_item.assert_not_called()
        self.table.put_item.assert_not_called()
        self.table.delete_item.assert_not_called()

    def assert_released(self, code):
        """The API claim is released, a receipt written, and nothing started."""
        self.sfn.start_execution.assert_not_called()
        self.table.get_item.assert_not_called()
        self.table.update_item.assert_called_once()
        update = self.table.update_item.call_args.kwargs
        self.assertEqual(update['Key'], {'PK': f'CRAWLER#{SOURCE}', 'SK': 'CONFIG'})
        self.assertEqual(update['UpdateExpression'],
                         'REMOVE lastCollectRequestedAt, lastCollectRequestId')
        self.assertEqual(update['ConditionExpression'], news_manual.RELEASE_CONDITION)
        for clause in ('attribute_exists(PK)', 'accountId = :acc', 'lastCollectRequestId = :rid',
                       'lastCollectStartedRequestId <> :rid'):
            self.assertIn(clause, update['ConditionExpression'])
        self.assertEqual(update['ExpressionAttributeValues'], {':acc': 'acct-1', ':rid': REQUEST})
        receipt = self.table.put_item.call_args.kwargs
        self.assertEqual(receipt['Item'], {
            'PK': f'CRAWLER#{SOURCE}', 'SK': f'COLLECTREFUSAL#{REQUEST}', 'requestId': REQUEST,
            'code': code, 'refusedAt': '2026-11-12T00:00:00+00:00',
            'pendingShareExpiresAt': NOW + 400 * 86400})
        self.assertEqual(receipt['ConditionExpression'], 'attribute_not_exists(PK)')

    def test_happy_path_claims_and_starts_one_named_run(self):
        result = self.run_handler()
        self.assertEqual(result, {'status': 'STARTED', 'requestId': REQUEST})
        self.assertEqual(self.sfn.list_executions.call_args_list, [
            mock.call(stateMachineArn=ENV[name], statusFilter='RUNNING', maxResults=1)
            for name in ('DAILY_STATE_MACHINE_ARN', 'PUBLICATION_STATE_MACHINE_ARN')])
        update = self.table.update_item.call_args.kwargs
        self.assertEqual(update['Key'], {'PK': f'CRAWLER#{SOURCE}', 'SK': 'CONFIG'})
        self.assertEqual(update['ConditionExpression'], news_manual.CLAIM_CONDITION)
        self.assertEqual(update['UpdateExpression'],
                         'SET lastCollectStartedRequestId = :rid, lastCollectStartedAt = :now')
        values = update['ExpressionAttributeValues']
        self.assertEqual((values[':acc'], values[':rid'], values[':disabled']),
                         ('acct-1', REQUEST, 'disabled'))
        for clause in ('attribute_exists(PK)', 'accountId = :acc', '#status <> :disabled',
                       'attribute_not_exists(globalNewsSource)', 'lastCollectRequestId = :rid',
                       'lastCollectStartedRequestId <> :rid'):
            self.assertIn(clause, update['ConditionExpression'])
        self.sfn.start_execution.assert_called_once()
        start = self.sfn.start_execution.call_args.kwargs
        self.assertEqual(start['stateMachineArn'], ENV['PUBLICATION_STATE_MACHINE_ARN'])
        self.assertEqual(start['name'], f'manual-{REQUEST}')
        self.assertEqual(json.loads(start['input']),
                         {'sourceId': SOURCE, 'runId': f'manual-{REQUEST}'})
        self.table.get_item.assert_not_called()

    def test_duplicate_delivery_after_start_is_refused_without_new_run(self):
        self.table.update_item.side_effect = client_error('ConditionalCheckFailedException')
        self.table.get_item.return_value = {'Item': row(lastCollectStartedRequestId=REQUEST)}
        result = self.run_handler()
        self.assertEqual(result['code'], 'DUPLICATE_REQUEST')
        self.assert_no_work(claimed=True)
        self.table.get_item.assert_called_once()

    def test_existing_execution_is_idempotent_success(self):
        self.sfn.start_execution.side_effect = client_error('ExecutionAlreadyExists')
        result = self.run_handler()
        self.assertEqual(result, {'status': 'STARTED', 'code': 'ALREADY_STARTED',
                                  'requestId': REQUEST})
        self.sfn.start_execution.assert_called_once()

    def test_failed_claims_are_classified_and_start_nothing(self):
        cases = [
            (None, 'NOT_FOUND'),
            (row(lastCollectRequestId='other-request'), 'NOT_CLAIMED'),
            (row(lastCollectRequestId=None), 'NOT_CLAIMED'),
            (row(accountId='acct-2'), 'ACCOUNT_MISMATCH'),
            (row(status='disabled'), 'SOURCE_DISABLED'),
            (row(globalNewsSource=True), 'OPERATOR_SOURCE'),
            (row(), 'CLAIM_REJECTED'),
        ]
        for item, code in cases:
            with self.subTest(code=code):
                self.table.reset_mock()
                self.sfn.reset_mock()
                self.table.update_item.side_effect = client_error('ConditionalCheckFailedException')
                self.table.get_item.return_value = {'Item': item} if item else {}
                result = self.run_handler()
                self.assertEqual((result['status'], result['code']), ('REFUSED', code))
                self.assert_no_work(claimed=True)

    def test_claim_error_propagates_without_start(self):
        self.table.update_item.side_effect = client_error('ThrottlingException')
        with self.assertRaises(ClientError):
            self.run_handler()
        self.sfn.start_execution.assert_not_called()

    def test_bad_shapes_refused_before_any_sdk_call(self):
        bad = [
            None, [], {}, {'manualCollect': None},
            {'manualCollect': event()['manualCollect'], 'sourceId': SOURCE},
            event(extra='x'),
            event(sourceId='acct_acct-2_0123abcd'),
            event(sourceId='acct_acct-1_0123ABCD'),
            event(sourceId='wooribank'),
            event(accountId='acct/1'),
            event(requestId='x' * 65),
            event(requestId=''),
            event(requestId=7),
        ]
        for value in bad:
            with self.subTest(value=value), \
                    mock.patch.object(news_manual, '_sfn') as sfn, \
                    mock.patch.object(news_manual, '_table') as table:
                result = news_manual.handler(value, None)
                self.assertEqual(result['code'], 'INVALID_REQUEST')
                sfn.assert_not_called()
                table.assert_not_called()

    def test_pipeline_gates_release_the_claim_without_step_functions(self):
        cases = [
            ({'NEWS_PIPELINE_MODE': 'off'}, 'PIPELINE_DISABLED'),
            ({'NEWS_PIPELINE_MODE': 'typo'}, 'PIPELINE_DISABLED'),
            ({'NEWS_PIPELINE_MODE': 'canary', 'NEWS_CANARY_SOURCE_ID': 'acct_acct-1_ffffffff'},
             'PIPELINE_DISABLED'),
            ({'NEWS_LEGACY_WRITES': 'on'}, 'LEGACY_WRITER_ENABLED'),
            ({'NEWS_PRICE_VERIFIED_ON': '2026-01-01'}, 'INVALID_PRICING'),
            ({'NEWS_DAILY_HOUR_UTC': ''}, 'INVALID_CONFIGURATION'),
            ({'NEWS_DAILY_MINUTE_UTC': '60'}, 'INVALID_CONFIGURATION'),
            ({'NEWS_DAILY_LEAD_SECONDS': '0'}, 'INVALID_CONFIGURATION'),
            ({'NEWS_DAILY_LEAD_SECONDS': '-5'}, 'INVALID_CONFIGURATION'),
        ]
        for changes, code in cases:
            with self.subTest(code=code, changes=changes), \
                    mock.patch.dict(os.environ, changes), \
                    mock.patch.object(news_manual, '_sfn') as sfn:
                self.table.reset_mock()
                result = self.run_handler()
                self.assertEqual((result['status'], result['code']), ('REFUSED', code))
                sfn.assert_not_called()
                self.assert_released(code)
        with mock.patch.dict(os.environ, {'NEWS_DAILY_HOUR_UTC': ''}):
            del os.environ['NEWS_DAILY_HOUR_UTC']
            self.table.reset_mock()
            self.assertEqual(self.run_handler()['code'], 'INVALID_CONFIGURATION')

    def test_daily_window_boundaries(self):
        day = NOW  # 2026-11-12T00:00:00Z
        for clock, refused in (((18, 24, 59), False), ((18, 25, 0), True),
                               ((19, 9, 59), True), ((19, 10, 0), False)):
            hour, minute, second = clock
            with self.subTest(clock=clock), \
                    mock.patch.object(news_manual.time, 'time',
                                      return_value=day + hour * 3600 + minute * 60 + second):
                self.table.reset_mock()
                self.sfn.reset_mock()
                result = self.run_handler()
                if refused:
                    self.assertEqual(result['code'], 'DAILY_WINDOW')
                    self.sfn.list_executions.assert_not_called()
                    self.sfn.start_execution.assert_not_called()
                    self.table.put_item.assert_called_once()
                else:
                    self.assertEqual(result['status'], 'STARTED')
                    self.assertEqual(self.sfn.list_executions.call_count, 2)

    def test_release_failures_do_not_raise(self):
        with mock.patch.dict(os.environ, {'NEWS_PIPELINE_MODE': 'off'}):
            # A failed condition (already started, replaced or gone): no receipt.
            self.table.update_item.side_effect = client_error('ConditionalCheckFailedException')
            self.assertEqual(self.run_handler()['code'], 'PIPELINE_DISABLED')
            self.table.put_item.assert_not_called()
            # A release error still records the receipt; a receipt error is logged.
            self.table.update_item.side_effect = client_error('ThrottlingException')
            self.table.put_item.side_effect = client_error('ThrottlingException')
            self.assertEqual(self.run_handler()['code'], 'PIPELINE_DISABLED')
            self.table.put_item.assert_called_once()
            self.sfn.start_execution.assert_not_called()

    def test_canary_source_is_accepted(self):
        with mock.patch.dict(os.environ, {'NEWS_PIPELINE_MODE': 'canary',
                                          'NEWS_CANARY_SOURCE_ID': SOURCE}):
            self.assertEqual(self.run_handler()['status'], 'STARTED')
        self.sfn.start_execution.assert_called_once()

    def running(self, daily, child):
        def respond(stateMachineArn, **_):
            busy = daily if stateMachineArn == ENV['DAILY_STATE_MACHINE_ARN'] else child
            if isinstance(busy, Exception):
                raise busy
            return {'executions': [{'executionArn': 'arn'}] if busy else []}
        self.sfn.list_executions.side_effect = respond

    def test_running_daily_parent_or_child_refuses_before_claim(self):
        for daily, child, code in ((True, False, 'DAILY_RUNNING'), (True, True, 'DAILY_RUNNING'),
                                   (False, True, 'RUN_IN_PROGRESS')):
            with self.subTest(code=code, daily=daily, child=child):
                self.running(daily, child)
                self.table.reset_mock()
                result = self.run_handler()
                self.assertEqual((result['status'], result['code']), ('REFUSED', code))
                self.assert_released(code)

    def test_list_executions_error_on_either_machine_fails_closed(self):
        for daily, child in ((client_error('AccessDeniedException'), False),
                             (False, client_error('ThrottlingException'))):
            with self.subTest(daily=daily, child=child):
                self.running(daily, child)
                with self.assertRaises(ClientError):
                    self.run_handler()
                self.assert_no_work()

    def test_start_error_raises_and_keeps_started_marker(self):
        self.sfn.start_execution.side_effect = client_error('ServiceUnavailable')
        with self.assertRaises(ClientError):
            self.run_handler()
        self.table.update_item.assert_called_once()
        self.table.delete_item.assert_not_called()
        self.table.put_item.assert_not_called()
        # A redelivery after the failure is then refused as already started.
        self.table.update_item.side_effect = client_error('ConditionalCheckFailedException')
        self.table.get_item.return_value = {'Item': row(lastCollectStartedRequestId=REQUEST)}
        self.sfn.start_execution.reset_mock()
        self.assertEqual(self.run_handler()['code'], 'DUPLICATE_REQUEST')
        self.sfn.start_execution.assert_not_called()


if __name__ == '__main__':
    unittest.main()
