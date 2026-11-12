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

    def test_happy_path_claims_and_starts_one_named_run(self):
        result = self.run_handler()
        self.assertEqual(result, {'status': 'STARTED', 'requestId': REQUEST})
        self.sfn.list_executions.assert_called_once_with(
            stateMachineArn=ENV['DAILY_STATE_MACHINE_ARN'], statusFilter='RUNNING', maxResults=1)
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

    def test_pipeline_gates_refuse_before_any_sdk_call(self):
        cases = [
            ({'NEWS_PIPELINE_MODE': 'off'}, 'PIPELINE_DISABLED'),
            ({'NEWS_PIPELINE_MODE': 'typo'}, 'PIPELINE_DISABLED'),
            ({'NEWS_PIPELINE_MODE': 'canary', 'NEWS_CANARY_SOURCE_ID': 'acct_acct-1_ffffffff'},
             'PIPELINE_DISABLED'),
            ({'NEWS_LEGACY_WRITES': 'on'}, 'LEGACY_WRITER_ENABLED'),
            ({'NEWS_PRICE_VERIFIED_ON': '2026-01-01'}, 'INVALID_PRICING'),
        ]
        for changes, code in cases:
            with self.subTest(code=code, changes=changes), \
                    mock.patch.dict(os.environ, changes), \
                    mock.patch.object(news_manual, '_sfn') as sfn, \
                    mock.patch.object(news_manual, '_table') as table:
                result = self.run_handler()
                self.assertEqual((result['status'], result['code']), ('REFUSED', code))
                sfn.assert_not_called()
                table.assert_not_called()

    def test_canary_source_is_accepted(self):
        with mock.patch.dict(os.environ, {'NEWS_PIPELINE_MODE': 'canary',
                                          'NEWS_CANARY_SOURCE_ID': SOURCE}):
            self.assertEqual(self.run_handler()['status'], 'STARTED')
        self.sfn.start_execution.assert_called_once()

    def test_running_daily_parent_refuses_before_claim(self):
        self.sfn.list_executions.return_value = {'executions': [{'executionArn': 'arn'}]}
        result = self.run_handler()
        self.assertEqual((result['status'], result['code']), ('REFUSED', 'DAILY_RUNNING'))
        self.assert_no_work()
        self.table.get_item.assert_not_called()

    def test_list_executions_error_fails_closed(self):
        self.sfn.list_executions.side_effect = client_error('AccessDeniedException')
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
