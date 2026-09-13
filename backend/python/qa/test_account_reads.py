"""Strict account reads with real SDK pagination/serialization and synthetic data."""
import copy
import importlib.util
from types import SimpleNamespace
import unittest
from unittest import mock

from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
from botocore.paginate import Paginator
from botocore.session import Session
from botocore.stub import ANY, Stubber

import test_handler as helpers
from session_provenance import new_source_state, restore_messages
from tool_history import CompleteRead, ToolHistory
from test_tool_history import conversation, session


def sdk_value(value):
    return TypeDeserializer().deserialize(TypeSerializer().serialize(value))


class AccountTable(helpers.RetrievalTable):
    name = 'synthetic-table'

    def __init__(self):
        super().__init__()
        self.after_read = None
        self.fail_prefix = None
        self.empty_first = False
        operation = Session().get_service_model('dynamodb').operation_model('Query')
        config = {'input_token': 'ExclusiveStartKey', 'output_token': 'LastEvaluatedKey',
                  'result_key': 'Items'}
        self.meta = SimpleNamespace(client=SimpleNamespace(
            get_paginator=lambda name: Paginator(self.query, config, operation)))

    def put_item(self, Item):
        super().put_item(Item=sdk_value(Item))

    def get_item(self, Key, **kwargs):
        result = super().get_item(Key=Key, **kwargs)
        if self.after_read:
            self.after_read(Key, kwargs)
        return sdk_value(result)

    def query(self, **kwargs):
        self._check_projection(kwargs)
        kwargs.pop('TableName', None)
        condition = kwargs['KeyConditionExpression']
        prefix = condition.get_expression()['values'][1].get_expression()['values'][1]
        if self.fail_prefix == prefix:
            raise RuntimeError('synthetic query failure')
        if self.empty_first and kwargs.get('IndexName') == 'GSI1' and not kwargs.get('ExclusiveStartKey'):
            self.empty_first = False
            rows = sorted(self.items.values(), key=lambda row: (row['PK'], row['SK']))
            first = next(row for row in rows if self.matches(condition, row))
            self.queries.append(kwargs)
            return {'Items': [], 'LastEvaluatedKey': {'PK': first['PK'], 'SK': first['SK']}}
        return sdk_value(super().query(**kwargs))

    @staticmethod
    def _check_projection(kwargs):
        for field in kwargs.get('ProjectionExpression', '').split(','):
            if field and not field.strip().startswith('#'):
                raise AssertionError('Projection names must all be aliased, including reserved names')


class TestStrictAccountReads(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch('socket.socket', side_effect=AssertionError('live AWS/network forbidden'))
        patcher.start()
        self.addCleanup(patcher.stop)
        self.table = AccountTable()

    def reader(self):
        self.assertIsNotNone(importlib.util.find_spec('account_reads'), 'strict reader helper is not implemented')
        from account_reads import StrictAccountReader
        return StrictAccountReader(self.table)

    def account(self, identifier='a', name='Account', user='reader', role='SA', **extra):
        self.table.put_item(Item={'PK': 'ACCOUNT#' + identifier, 'SK': 'META', 'accountId': identifier,
                                  'name': name, 'aliases': ['Alias ' + name], 'industry': 'Industry',
                                  'ownerUserId': 'owner', 'entityType': 'ACCOUNT', **extra})
        self.table.put_item(Item={'PK': 'ACCOUNT#' + identifier, 'SK': 'MEMBER#' + user,
                                  'accountId': identifier, 'userId': user, 'role': role,
                                  'GSI1PK': 'USER#' + user, 'GSI1SK': 'ACCOUNT#' + identifier,
                                  'entityType': 'ACCOUNT_MEMBER'})

    def insight(self, identifier='i', account='a', **extra):
        self.table.put_item(Item={'PK': 'ACCOUNT#' + account, 'SK': 'INSIGHT#2026-09-12#' + identifier,
                                  'insightId': identifier, 'type': 'risk', 'text': 'PRIVATE ' + identifier,
                                  'occurredAt': '2026-09-12T12:00:00Z',
                                  'sourceType': 'meeting', 'entities': ['Entity'], **extra})

    def meeting(self, identifier='m', **extra):
        self.table.put_item(Item={'PK': 'ACCOUNT#a', 'SK': 'MEETINGREF#2026-01-01#' + identifier,
                                  'accountId': 'a', 'meetingId': identifier, 'ownerUserId': 'owner',
                                  'title': 'STALE REF TITLE', 'date': 'OLD'})
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#' + identifier,
                                  'meetingId': identifier, 'userId': 'owner', 'accountId': 'a',
                                  'sharedToAccount': True, 'title': 'CURRENT ' + identifier,
                                  'date': '2026-09-12', 'content': 'NEVER_READ_BODY',
                                  'transcriptA': 's3://foreign/NEVER_READ', **extra})

    def research(self, identifier='r', **extra):
        self.table.put_item(Item={'PK': 'ACCOUNT#a', 'SK': 'RESEARCHREF#' + identifier,
                                  'accountId': 'a', 'researchId': identifier, 'ownerUserId': 'owner',
                                  'topic': 'STALE REF TOPIC'})
        self.table.put_item(Item={'PK': 'RESEARCH#' + identifier, 'SK': 'CONFIG',
                                  'researchId': identifier, 'userId': 'owner', 'accountIds': {'a'},
                                  'topic': 'CURRENT ' + identifier, 'summary': 'SUMMARY ' + identifier,
                                  'status': 'done', 's3Key': 'NEVER_READ_S3', **extra})

    def test_gsi_is_discovery_only_and_all_pages_use_exact_current_members(self):
        self.account('a', 'Alpha')
        self.account('b', 'Beta')
        self.account('c', 'Gamma')
        self.table.index_rows = copy.deepcopy(list(self.table.items.values()))
        del self.table.items[('ACCOUNT#b', 'MEMBER#reader')]
        self.table.items[('ACCOUNT#c', 'MEMBER#reader')]['role'] = 'TAM'
        result = self.reader().list_accounts('reader')
        self.assertIsInstance(result, CompleteRead)
        self.assertEqual(result.value, [
            {'accountId': 'a', 'name': 'Alpha', 'role': 'SA'},
            {'accountId': 'c', 'name': 'Gamma', 'role': 'TAM'},
        ])
        self.assertTrue(any(q.get('ExclusiveStartKey') for q in self.table.queries))
        self.assertNotIn(('ACCOUNT#b', 'META'), [key for key, _ in self.table.reads])
        self.assertTrue(all(args.get('ConsistentRead') for _, args in self.table.reads))

    def test_empty_discovery_page_does_not_terminate_pagination(self):
        self.account('a', 'Alpha')
        self.account('b', 'Beta')
        self.table.empty_first = True
        result = self.reader().list_accounts('reader')
        self.assertEqual([row['accountId'] for row in result.value], ['b'])
        self.assertTrue(any(q.get('ExclusiveStartKey') for q in self.table.queries))

    def test_parent_membership_does_not_grant_child_or_read_child_content(self):
        self.account('parent', 'Parent')
        self.account('a', 'Child', user='someone-else', parentAccountId='parent')
        reader = self.reader()
        with self.assertRaises(LookupError):
            reader.get_account_brief('reader', 'Child')
        self.assertNotIn(('ACCOUNT#a', 'META'), [key for key, _ in self.table.reads])
        self.assertFalse(any(not q.get('IndexName') for q in self.table.queries))

    def test_insights_filters_use_complete_strong_reads(self):
        self.account()
        self.insight('one')
        self.insight('two')
        self.insight('earlier', occurredAt='2026-08-01T00:00:00Z')
        self.insight('other-type', type='need')
        result = self.reader().get_account_insights(
            'reader', 'Alias Account', date_from='2026-09-01T00:00:00Z',
            date_to='2026-09-30T23:59:59Z', types=['risk'])
        self.assertEqual({row['insightId'] for row in result.value['insights']}, {'one', 'two'})
        self.assertTrue(all(q.get('ConsistentRead') for q in self.table.queries if not q.get('IndexName')))

    def test_brief_rehydrates_current_meetings_and_canonical_research(self):
        self.account()
        self.insight()
        self.meeting('m1')
        self.meeting('m2', sharedToAccount=False)
        self.meeting('m3', accountId='different')
        self.meeting('m4')
        del self.table.items[('USER#owner', 'MEETING#m4')]
        self.research('r1')
        self.research('r2', accountIds={'different'})
        self.research('r3', trashedAt='2026-09-12T00:00:00Z')
        self.research('r4')
        del self.table.items[('RESEARCH#r4', 'CONFIG')]
        result = self.reader().get_account_brief('reader', 'Account').value
        self.assertEqual([m['meetingId'] for m in result['meetings']], ['m1'])
        self.assertEqual(result['meetings'][0]['title'], 'CURRENT m1')
        self.assertEqual([r['researchId'] for r in result['research']], ['r1'])
        self.assertEqual(result['research'][0]['topic'], 'CURRENT r1')
        self.assertNotIn('STALE', str(result))
        for key, args in self.table.reads:
            self.table._check_projection(args)
            fields = set(args.get('ExpressionAttributeNames', {}).values())
            self.assertNotIn('content', fields)
            self.assertNotIn('transcriptA', fields)
            self.assertNotIn('s3Key', fields)
            if key in (('USER#owner', 'MEETING#m2'), ('USER#owner', 'MEETING#m3')):
                self.assertNotIn('title', fields, 'content read preceded current publication authorization')
            if key in (('RESEARCH#r2', 'CONFIG'), ('RESEARCH#r3', 'CONFIG')):
                self.assertNotIn('summary', fields, 'content read preceded canonical account/trashed checks')

    def test_read_failures_propagate_instead_of_complete_empty_or_partial_results(self):
        for target in [('ACCOUNT#a', 'MEMBER#reader'), ('ACCOUNT#a', 'META'),
                       ('USER#owner', 'MEETING#m'), ('RESEARCH#r', 'CONFIG')]:
            with self.subTest(target=target):
                self.table = AccountTable()
                self.account()
                self.insight()
                self.meeting()
                self.research()
                self.table.fail_key = target
                with self.assertRaises(RuntimeError):
                    self.reader().get_account_brief('reader', 'Account')
        self.table = AccountTable()
        self.account()
        self.table.fail_query = True
        with self.assertRaises(RuntimeError):
            self.reader().list_accounts('reader')
        for prefix in ('ACCOUNT#', 'INSIGHT#', 'MEETINGREF#', 'RESEARCHREF#'):
            with self.subTest(prefix=prefix):
                self.table = AccountTable()
                self.account()
                self.insight()
                self.meeting()
                self.research()
                self.table.fail_prefix = prefix
                with self.assertRaises(RuntimeError):
                    self.reader().get_account_brief('reader', 'Account')

    def test_revocation_after_aggregation_rejects_all_collected_content(self):
        self.account()
        self.meeting()
        self.research()
        def revoke(key, args):
            if key == {'PK': 'RESEARCH#r', 'SK': 'CONFIG'} and 'summary' in args.get('ExpressionAttributeNames', {}).values():
                self.table.items.pop(('ACCOUNT#a', 'MEMBER#reader'), None)
        self.table.after_read = revoke
        with self.assertRaises(PermissionError):
            self.reader().get_account_brief('reader', 'Account')

    def test_unpublish_or_unlink_during_aggregation_never_attests_stale_content(self):
        for change in ('unpublish', 'unlink'):
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.research()
                def change_scope(key, args):
                    if key == {'PK': 'RESEARCH#r', 'SK': 'CONFIG'} and 'summary' in args.get('ExpressionAttributeNames', {}).values():
                        if change == 'unpublish':
                            self.table.items[('USER#owner', 'MEETING#m')]['sharedToAccount'] = False
                        else:
                            self.table.items[('RESEARCH#r', 'CONFIG')]['accountIds'] = {'other'}
                self.table.after_read = change_scope
                with self.assertRaises(PermissionError):
                    self.reader().get_account_brief('reader', 'Account')

    def test_current_account_reader_and_history_preserve_then_revoke_followup(self):
        self.account()
        self.insight()
        reader = self.reader()
        history = ToolHistory('reader', {'get_account_brief': reader.get_account_brief})
        state = new_source_state()
        history.read(state, 'get_account_brief', {'account': 'Account'})
        saved = session(state, conversation('PRIVATE ANSWER', 'get_account_brief', {'account': 'Account'}))
        self.assertTrue(restore_messages(saved, new_source_state(), lambda _: False, tool_history=history))
        self.table.index_rows = copy.deepcopy(list(self.table.items.values()))
        del self.table.items[('ACCOUNT#a', 'MEMBER#reader')]
        self.assertEqual(restore_messages(saved, new_source_state(), lambda _: False, tool_history=history), [])

    def test_current_views_invalidate_history_after_each_displayed_source_changes(self):
        for change in ('account', 'insight', 'meeting', 'research', 'role'):
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.insight()
                self.meeting()
                self.research()
                reader = self.reader()
                name = 'list_accounts' if change == 'role' else 'get_account_brief'
                arguments = {} if change == 'role' else {'account': 'a'}
                history = ToolHistory('reader', {name: getattr(reader, name)})
                state = new_source_state()
                history.read(state, name, arguments)
                saved = session(state, conversation('PRIVATE ANSWER', name, arguments))
                self.assertTrue(restore_messages(saved, new_source_state(), lambda _: False, tool_history=history))
                key, field = {
                    'account': (('ACCOUNT#a', 'META'), 'industry'),
                    'insight': (('ACCOUNT#a', 'INSIGHT#2026-09-12#i'), 'text'),
                    'meeting': (('USER#owner', 'MEETING#m'), 'title'),
                    'research': (('RESEARCH#r', 'CONFIG'), 'summary'),
                    'role': (('ACCOUNT#a', 'MEMBER#reader'), 'role'),
                }[change]
                self.table.items[key][field] = 'CHANGED'
                self.assertEqual(restore_messages(
                    saved, new_source_state(), lambda _: False, tool_history=history), [])

    def test_late_query_page_failure_cannot_attest_partial_insights(self):
        self.account()
        self.insight('one')
        self.insight('two')
        read_page = self.table.query
        def fail_later(**kwargs):
            if not kwargs.get('IndexName') and kwargs.get('ExclusiveStartKey'):
                raise RuntimeError('synthetic second page failure')
            return read_page(**kwargs)
        self.table.query = fail_later
        with self.assertRaisesRegex(RuntimeError, 'second page'):
            self.reader().get_account_insights('reader', 'Account')

    def test_ambiguous_account_and_invalid_inputs_do_not_read_account_children(self):
        self.account('a', 'Same')
        self.account('b', 'Same')
        with self.assertRaises(LookupError):
            self.reader().get_account_brief('reader', 'Same')
        self.assertTrue(all(q.get('IndexName') for q in self.table.queries))
        self.table.queries.clear()
        self.table.reads.clear()
        with self.assertRaises(ValueError):
            self.reader().list_accounts('')
        with self.assertRaises(ValueError):
            self.reader().get_account_brief('reader', {'userId': 'owner'})
        self.assertEqual(self.table.queries, [])
        self.assertEqual(self.table.reads, [])

    def test_real_resource_paginator_and_strong_get_wire_shapes(self):
        from boto3.session import Session as BotoSession
        from account_reads import StrictAccountReader
        sdk = BotoSession(aws_access_key_id='synthetic-key', aws_secret_access_key='synthetic-secret',
                         region_name='us-east-1').resource('dynamodb')
        table = sdk.Table('synthetic-table')
        encode = TypeSerializer().serialize
        member = {'PK': 'ACCOUNT#a', 'SK': 'MEMBER#reader', 'accountId': 'a', 'userId': 'reader', 'role': 'SA'}
        discovery = dict(member, GSI1PK='USER#reader', GSI1SK='ACCOUNT#a')
        meta = {'PK': 'ACCOUNT#a', 'SK': 'META', 'accountId': 'a', 'name': 'Account', 'aliases': ['Alias']}
        query = {'TableName': 'synthetic-table', 'IndexName': 'GSI1', 'Limit': 100,
                 'KeyConditionExpression': ANY, 'ProjectionExpression': ANY, 'ExpressionAttributeNames': ANY}
        get = {'TableName': 'synthetic-table', 'Key': ANY, 'ConsistentRead': True,
               'ProjectionExpression': ANY, 'ExpressionAttributeNames': ANY}
        with Stubber(table.meta.client) as wire:
            token = {'PK': {'S': 'ACCOUNT#previous'}, 'SK': {'S': 'MEMBER#reader'}}
            wire.add_response('query', {'Items': [], 'LastEvaluatedKey': token}, query)
            wire.add_response('query', {'Items': [encode(discovery)['M']]},
                              dict(query, ExclusiveStartKey=ANY))
            wire.add_response('get_item', {'Item': encode(member)['M']}, get)
            wire.add_response('get_item', {'Item': encode(meta)['M']}, get)
            wire.add_response('get_item', {'Item': encode(member)['M']}, get)
            result = StrictAccountReader(table).list_accounts('reader')
            self.assertEqual(result.value, [{'accountId': 'a', 'name': 'Account', 'role': 'SA'}])
            wire.assert_no_pending_responses()
