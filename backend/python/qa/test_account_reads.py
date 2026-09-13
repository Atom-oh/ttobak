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
        occurred = extra.get('occurredAt', '2026-09-12T12:00:00Z')
        source = extra.get('sourceId', 'm')
        index = sum(pk == 'ACCOUNT#' + account and sk.startswith('INSIGHT#')
                    for pk, sk in self.table.items)
        self.table.put_item(Item={'PK': 'ACCOUNT#' + account,
                                  'SK': f'INSIGHT#{occurred}#{source}#{index}',
                                  'accountId': account, 'entityType': 'ACCOUNT_INSIGHT',
                                  'insightId': identifier, 'type': 'risk', 'text': 'PRIVATE ' + identifier,
                                  'occurredAt': occurred, 'sourceId': source, 'sourceUserId': 'owner',
                                  'sourceType': 'meeting', 'entities': ['Entity'], **extra})

    def meeting(self, identifier='m', **extra):
        self.table.put_item(Item={'PK': 'ACCOUNT#a', 'SK': 'MEETINGREF#2026-01-01#' + identifier,
                                  'accountId': 'a', 'meetingId': identifier, 'ownerUserId': 'owner',
                                  'title': 'STALE REF TITLE', 'date': 'OLD'})
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#' + identifier,
                                  'meetingId': identifier, 'userId': 'owner', 'accountId': 'a',
                                  'entityType': 'MEETING',
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
        self.meeting()
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
        self.insight(sourceId='m1')
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
        self.meeting()
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
                    'insight': (('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0'), 'text'),
                    'meeting': (('USER#owner', 'MEETING#m'), 'title'),
                    'research': (('RESEARCH#r', 'CONFIG'), 'summary'),
                    'role': (('ACCOUNT#a', 'MEMBER#reader'), 'role'),
                }[change]
                self.table.items[key][field] = 'CHANGED'
                self.assertEqual(restore_messages(
                    saved, new_source_state(), lambda _: False, tool_history=history), [])

    def test_late_query_page_failure_cannot_attest_partial_insights(self):
        self.account()
        self.meeting()
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

    def test_retained_meeting_insights_require_current_publication_before_body_reads(self):
        changes = {
            'unpublished': {'sharedToAccount': False},
            'repointed': {'accountId': 'other'},
            'deleted': None,
            'wrong owner': {'userId': 'other'},
            'wrong meeting': {'meetingId': 'other'},
            'wrong entity': {'entityType': 'DOCUMENT'},
            'missing entity': {'entityType': None},
        }
        for name, change in changes.items():
            with self.subTest(change=name):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight('m_0')
                source_key = ('USER#owner', 'MEETING#m')
                if change is None:
                    del self.table.items[source_key]
                else:
                    self.table.items[source_key].update(change)
                # A direct share cannot replace publication to the account.
                self.table.put_item(Item={'PK': 'USER#reader', 'SK': 'SHARED#m',
                                          'ownerId': 'owner', 'permission': 'edit'})
                reader = self.reader()
                self.assertEqual(reader.get_account_insights('reader', 'Account').value['insights'], [])
                brief = reader.get_account_brief('reader', 'Account').value
                self.assertEqual(brief['insightsByType'], {})
                self.assertEqual(brief['meetings'], [])
                for query in self.table.queries:
                    fields = set(query.get('ExpressionAttributeNames', {}).values())
                    self.assertNotIn('text', fields)
                    self.assertNotIn('entities', fields)
                self.assertFalse(any(key[1].startswith('INSIGHT#') for key, _ in self.table.reads))
                self.assertFalse(any(key[1].startswith('SHARED#') for key, _ in self.table.reads))

    def test_insight_body_follows_strong_metadata_authorization_and_keeps_output(self):
        self.account()
        self.meeting()
        self.insight('m_0')
        result = self.reader().get_account_insights('reader', 'Account').value
        self.assertEqual(result, {'accountId': 'a', 'account': 'Account', 'insights': [{
            'insightId': 'm_0', 'type': 'risk', 'text': 'PRIVATE m_0',
            'occurredAt': '2026-09-12T12:00:00Z', 'sourceType': 'meeting', 'entities': ['Entity'],
        }]})
        discovery = next(q for q in self.table.queries if not q.get('IndexName'))
        fields = set(discovery['ExpressionAttributeNames'].values())
        self.assertTrue({'PK', 'SK', 'accountId', 'entityType', 'sourceType',
                         'sourceId', 'sourceUserId', 'occurredAt'} <= fields)
        self.assertNotIn('text', fields)
        self.assertNotIn('entities', fields)
        source_key = ('USER#owner', 'MEETING#m')
        body_key = ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0')
        keys = [key for key, _ in self.table.reads]
        self.assertLess(keys.index(source_key), keys.index(body_key))
        self.assertIn(source_key, keys[keys.index(body_key) + 1:])
        for key, args in self.table.reads:
            self.assertTrue(args.get('ConsistentRead'))
            fields = set(args['ExpressionAttributeNames'].values())
            self.assertFalse({'content', 'transcriptA', 'transcriptB', 's3Key', 'evidence'} & fields)
            if key == source_key:
                self.assertEqual(fields, {'PK', 'SK', 'meetingId', 'userId', 'entityType',
                                          'accountId', 'sharedToAccount'})

    def test_mismatched_canonical_primary_key_cannot_load_insight_body(self):
        for field, value in (('PK', 'USER#other'), ('SK', 'MEETING#other')):
            with self.subTest(field=field):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                self.table.items[('USER#owner', 'MEETING#m')][field] = value
                with self.assertRaises(ValueError):
                    self.reader().get_account_insights('reader', 'Account')
                self.assertFalse(any(key[1].startswith('INSIGHT#') for key, _ in self.table.reads))

    def test_unusable_insight_projection_identities_are_omitted_before_body_reads(self):
        changes = [
            {'accountId': 'other'}, {'accountId': None}, {'entityType': 'OTHER'},
            {'entityType': None}, {'sourceType': ''}, {'sourceType': 'unknown'},
            {'sourceType': 'research'}, {'sourceUserId': ''}, {'sourceUserId': 'bad/owner'},
            {'sourceId': ''}, {'sourceId': 'other'},
            {'occurredAt': '2026-09-13T12:00:00Z'}, {'occurredAt': 'invalid'},
        ]
        for change in changes:
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                key = ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0')
                self.table.items[key].update(change)
                self.assertEqual(self.reader().get_account_insights('reader', 'Account').value['insights'], [])
                self.assertNotIn(key, [read_key for read_key, _ in self.table.reads])

    def test_insight_metadata_and_body_type_errors_propagate(self):
        changes = [
            {'accountId': 1}, {'entityType': []}, {'sourceType': 1},
            {'sourceId': {}}, {'sourceUserId': []}, {'occurredAt': 1},
            {'insightId': []}, {'type': {}}, {'text': 1}, {'entities': 'not-a-list'},
        ]
        for change in changes:
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                key = ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0')
                self.table.items[key].update(change)
                with self.assertRaises(ValueError):
                    self.reader().get_account_insights('reader', 'Account')

    def test_canonical_publication_type_errors_propagate_before_insight_body(self):
        for change in ({'sharedToAccount': 'true'}, {'entityType': 1}, {'userId': []}):
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                self.table.items[('USER#owner', 'MEETING#m')].update(change)
                with self.assertRaises(ValueError):
                    self.reader().get_account_insights('reader', 'Account')
                self.assertFalse(any(key[1].startswith('INSIGHT#') for key, _ in self.table.reads))

    def test_meeting_insight_key_binds_utc_seconds_and_ascii_index(self):
        self.account()
        self.meeting()
        self.insight(occurredAt='2026-09-12T21:00:00.123456789+09:00',
                     SK='INSIGHT#2026-09-12T12:00:00Z#m#0')
        self.assertEqual(len(self.reader().get_account_insights('reader', 'Account').value['insights']), 1)
        row = self.table.items.pop(('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0'))
        for suffix in ('', 'bad', '0#extra', '１'):
            with self.subTest(suffix=suffix):
                bad = dict(row, SK='INSIGHT#2026-09-12T12:00:00Z#m#' + suffix)
                self.table.put_item(Item=bad)
                self.assertEqual(self.reader().get_account_insights('reader', 'Account').value['insights'], [])
                del self.table.items[(bad['PK'], bad['SK'])]

    def test_explicit_account_owned_insights_need_account_identity_but_no_meeting(self):
        for source_type in ('news', 'ingest'):
            with self.subTest(source_type=source_type):
                self.table = AccountTable()
                self.account()
                self.insight(sourceType=source_type, sourceId='feed-item', sourceUserId='')
                reader = self.reader()
                self.assertEqual(reader.get_account_insights('reader', 'Account').value['insights'][0]['text'], 'PRIVATE i')
                self.assertEqual(reader.get_account_brief('reader', 'Account').value['insightsByType']['risk'][0]['sourceType'], source_type)
                self.assertFalse(any(key[0].startswith('USER#') for key, _ in self.table.reads))
                key = ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#feed-item#0')
                self.table.items[key]['accountId'] = 'other'
                self.assertEqual(reader.get_account_insights('reader', 'Account').value['insights'], [])

    def test_insight_source_change_between_discovery_and_body_never_attests(self):
        for change in ({'sourceId': 'other'}, {'sourceUserId': 'other'}, {'sourceType': 'ingest'},
                       {'accountId': 'other'}, {'entityType': 'OTHER'}, None):
            with self.subTest(change=change):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                key = ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0')
                def replace_source(read_key, args):
                    if read_key == {'PK': 'USER#owner', 'SK': 'MEETING#m'}:
                        self.table.after_read = None
                        if change is None:
                            self.table.items.pop(key)
                        else:
                            self.table.items[key].update(change)
                self.table.after_read = replace_source
                with self.assertRaises(ValueError):
                    self.reader().get_account_insights('reader', 'Account')

    def test_insight_publication_is_rechecked_without_a_meeting_ref(self):
        for callback in ('get_account_insights', 'get_account_brief'):
            with self.subTest(callback=callback):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                self.table.items.pop(('ACCOUNT#a', 'MEETINGREF#2026-01-01#m'))
                def revoke_after_body(key, args):
                    if key['SK'].startswith('INSIGHT#') and 'text' in args['ExpressionAttributeNames'].values():
                        self.table.items[('USER#owner', 'MEETING#m')]['sharedToAccount'] = False
                self.table.after_read = revoke_after_body
                with self.assertRaises(PermissionError):
                    getattr(self.reader(), callback)('reader', 'Account')

    def test_brief_rechecks_insight_publication_after_research_aggregation(self):
        self.account()
        self.meeting()
        self.insight()
        self.research()
        self.table.items.pop(('ACCOUNT#a', 'MEETINGREF#2026-01-01#m'))
        def revoke_after_research(key, args):
            if key == {'PK': 'RESEARCH#r', 'SK': 'CONFIG'} and 'summary' in args['ExpressionAttributeNames'].values():
                self.table.items[('USER#owner', 'MEETING#m')]['accountId'] = 'other'
        self.table.after_read = revoke_after_research
        with self.assertRaises(PermissionError):
            self.reader().get_account_brief('reader', 'Account')

    def test_insight_authorization_and_body_read_failures_never_attest_partial_results(self):
        for target in (('USER#owner', 'MEETING#m'),
                       ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0'),
                       ('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#1')):
            for callback in ('get_account_insights', 'get_account_brief'):
                with self.subTest(target=target, callback=callback):
                    self.table = AccountTable()
                    self.account()
                    self.meeting()
                    self.insight()
                    self.insight('second')
                    self.table.fail_key = target
                    with self.assertRaises(RuntimeError):
                        getattr(self.reader(), callback)('reader', 'Account')

    def test_final_insight_publication_read_failure_aborts_attestation(self):
        for callback in ('get_account_insights', 'get_account_brief'):
            with self.subTest(callback=callback):
                self.table = AccountTable()
                self.account()
                self.meeting()
                self.insight()
                self.table.items.pop(('ACCOUNT#a', 'MEETINGREF#2026-01-01#m'))
                def fail_after_body(key, args):
                    if key['SK'].startswith('INSIGHT#') and 'text' in args['ExpressionAttributeNames'].values():
                        self.table.fail_key = ('USER#owner', 'MEETING#m')
                self.table.after_read = fail_after_body
                with self.assertRaises(RuntimeError):
                    getattr(self.reader(), callback)('reader', 'Account')

    def test_history_revalidates_insight_publication_without_meeting_refs(self):
        for callback in ('get_account_insights', 'get_account_brief'):
            for change in ('unpublish', 'repoint', 'delete', 'mismatch', 'read failure'):
                with self.subTest(callback=callback, change=change):
                    self.table = AccountTable()
                    self.account()
                    self.meeting()
                    self.insight()
                    self.table.items.pop(('ACCOUNT#a', 'MEETINGREF#2026-01-01#m'))
                    reader = self.reader()
                    history = ToolHistory('reader', {callback: getattr(reader, callback)})
                    state = new_source_state()
                    history.read(state, callback, {'account': 'Account'})
                    saved = session(state, conversation('PRIVATE ANSWER', callback, {'account': 'Account'}))
                    self.assertTrue(restore_messages(saved, new_source_state(), lambda _: False, tool_history=history))
                    key = ('USER#owner', 'MEETING#m')
                    if change == 'delete':
                        del self.table.items[key]
                    elif change == 'read failure':
                        self.table.fail_key = key
                    else:
                        field, value = {'unpublish': ('sharedToAccount', False),
                                        'repoint': ('accountId', 'other'),
                                        'mismatch': ('entityType', 'OTHER')}[change]
                        self.table.items[key][field] = value
                    self.assertEqual(restore_messages(saved, new_source_state(), lambda _: False, tool_history=history), [])

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
