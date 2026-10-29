"""Inherited reads use current canonical hierarchy grants, never index authority."""
import copy
import hashlib
import json
import unittest
from unittest import mock

from test_account_reads import AccountTable
import handler
from document_context import read_current_document
from indexed_retrieval import discover_sources
from source_access import SourceAccess
from account_reads import StrictAccountReader
from session_provenance import new_source_state
from tool_history import ToolHistory
from test_kb_fixtures import _QAConversationFixture


class DescendantTable(AccountTable):
    @staticmethod
    def _check_projection(kwargs):
        # The handler also has existing unaliased nonreserved projections.
        pass


class DescendantReadsTests(unittest.TestCase):
    def setUp(self):
        self.table = DescendantTable()
        for patcher in (
            mock.patch.dict('os.environ', {'ACCOUNT_DESCENDANT_READS_ENABLED': '1',
                                          'SUMMARY_PRIVACY_READER_ENABLED': '0'}),
            mock.patch.object(handler, 'table', self.table),
            mock.patch.object(handler, 'BUCKET_NAME', 'synthetic'),
            mock.patch.object(handler, 'KB_BUCKET_NAME', 'kb'),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.account('root')
        self.member('root')
        self.account('child', 'root')
        self.account('grandchild', 'child')
        self.account('outside')
        self.meeting()
        self.document()

    def put(self, **row):
        self.table.put_item(Item=row)

    def account(self, account_id, parent=''):
        self.put(PK='ACCOUNT#' + account_id, SK='META', accountId=account_id,
                 entityType='ACCOUNT', name=account_id, parentAccountId=parent)
        if parent:
            self.put(PK='ACCOUNT#' + parent, SK='CHILDREF#' + account_id,
                     accountId=account_id, parentAccountId=parent, entityType='ACCOUNT_CHILD_REF')

    def member(self, account_id, user='reader', **extra):
        self.put(PK='ACCOUNT#' + account_id, SK='MEMBER#' + user,
                 accountId=account_id, userId=user, role='owner', entityType='ACCOUNT_MEMBER',
                 GSI1PK='USER#' + user, GSI1SK='ACCOUNT#' + account_id,
                 addedAt='2020-01-01T00:00:00Z', summaryPrivacyVersion=0, **extra)

    def meeting(self):
        self.put(PK='USER#owner', SK='MEETING#m', meetingId='m', userId='owner',
                 entityType='MEETING', accountId='grandchild', sharedToAccount=True,
                 title='Current published meeting', notes='User note', transcriptA='Raw source',
                 content='Unsafe generated context', actionItems='[]', updatedAt='v1')
        self.put(PK='ACCOUNT#grandchild', SK='MEETINGREF#2026-10-29#m',
                 accountId='grandchild', meetingId='m', ownerUserId='owner')

    def document(self):
        self.put(PK='ACCOUNT#grandchild', SK='DOC#doc', docId='doc', accountId='grandchild',
                 entityType='ACCOUNT_DOC', title='Current document', content='Document text')

    def resolver(self):
        import account_read_access
        return account_read_access.AccountReadAccess(self.table)

    def test_parent_and_grandparent_grants_are_read_only_and_flag_defaults_off(self):
        grant = self.resolver().resolve('grandchild', 'reader')
        self.assertTrue(grant.inherited)
        self.assertEqual(grant.granting_account_id, 'root')
        self.assertEqual(grant.permission, 'read')
        self.assertEqual(grant.privacy_member()['summaryPrivacyVersion'], 1)
        with mock.patch.dict('os.environ', {'ACCOUNT_DESCENDANT_READS_ENABLED': '0'}):
            self.assertIsNone(self.resolver().resolve('grandchild', 'reader'))

    def test_malformed_nearer_membership_denies_instead_of_using_valid_higher_grant(self):
        # A missing child membership may use root, but an existing malformed
        # canonical row must deny the chain even while root remains valid.
        self.assertEqual(self.resolver().resolve('grandchild', 'reader').granting_account_id, 'root')
        self.member('child')
        key = ('ACCOUNT#child', 'MEMBER#reader')
        canonical = copy.deepcopy(self.table.items[key])
        for field, bad in (
            ('entityType', 'OTHER'), ('accountId', 'wrong'), ('userId', 'wrong'),
            ('PK', 'ACCOUNT#wrong'), ('SK', 'MEMBER#wrong'), ('role', ''),
            ('role', None), ('role', []),
        ):
            with self.subTest(field=field, bad=bad):
                self.table.items[key] = dict(canonical, **{field: bad})
                self.assertIn(('ACCOUNT#root', 'MEMBER#reader'), self.table.items)
                self.assertIsNone(self.resolver().resolve('grandchild', 'reader'))
                self.assertIsNone(read_current_document(self.table, 'reader', 'ACCOUNT#grandchild', 'doc'))
        self.table.fail_key = key
        with self.assertRaises(RuntimeError):
            self.resolver().resolve('grandchild', 'reader')

    def test_full_chain_identity_depth_cycle_and_storage_failure(self):
        self.member('child')
        baseline = copy.deepcopy(self.table.items)
        for key, field, bad in (
            (('ACCOUNT#root', 'META'), 'entityType', 'OTHER'),
            (('ACCOUNT#root', 'META'), 'parentAccountId', 'grandchild'),
            (('ACCOUNT#grandchild', 'META'), 'accountId', 'wrong'),
            (('ACCOUNT#child', 'MEMBER#reader'), 'entityType', 'OTHER'),
        ):
            with self.subTest(field=field, bad=bad):
                self.table.items = copy.deepcopy(baseline)
                self.table.items[key][field] = bad
                self.assertIsNone(self.resolver().resolve('grandchild', 'reader'))
        self.table.items = baseline
        self.table.fail_key = ('ACCOUNT#root', 'META')
        with self.assertRaises(RuntimeError):
            self.resolver().resolve('grandchild', 'reader')
        self.table.fail_key = None
        for index in range(65):
            self.account('deep' + str(index), 'root' if index == 0 else 'deep' + str(index - 1))
        self.assertIsNotNone(self.resolver().resolve('deep63', 'reader'))
        self.assertIsNone(self.resolver().resolve('deep64', 'reader'))

    def test_direct_grants_keep_precedence_and_no_upward_or_sibling_access(self):
        self.member('grandchild')
        self.assertFalse(self.resolver().resolve('grandchild', 'reader').inherited)
        self.member('grandchild', user='child-reader')
        self.assertIsNone(self.resolver().resolve('root', 'child-reader'))
        self.assertIsNone(self.resolver().resolve('child', 'child-reader'))
        self.assertIsNone(self.resolver().resolve('outside', 'reader'))

    def test_document_reads_reauthorize_current_path_before_returning_bytes(self):
        document = read_current_document(self.table, 'reader', 'ACCOUNT#grandchild', 'doc')
        self.assertEqual(document['content'], 'Document text')
        self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'outside'
        self.assertIsNone(read_current_document(self.table, 'reader', 'ACCOUNT#grandchild', 'doc'))

    def test_reparent_during_document_body_read_rejects_even_still_accessible_source(self):
        self.account('alternate', 'root')

        def move(key, request):
            if key == {'PK': 'ACCOUNT#grandchild', 'SK': 'DOC#doc'}:
                self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'

        self.table.after_read = move
        self.assertIsNone(read_current_document(self.table, 'reader', 'ACCOUNT#grandchild', 'doc'))

    def test_inherited_insights_withhold_unsafe_derivations_and_private_meeting_titles(self):
        self.put(PK='ACCOUNT#grandchild', SK='INSIGHT#2026-10-29T00:00:00Z#m#0',
                 accountId='grandchild', entityType='ACCOUNT_INSIGHT', sourceType='meeting',
                 sourceId='m', sourceUserId='owner', occurredAt='2026-10-29T00:00:00Z',
                 insightId='insight', type='risk', text='Unsafe copied insight')
        reader = StrictAccountReader(self.table)
        value = reader.get_account_insights('reader', 'grandchild').value
        self.assertEqual(value['insights'], [])
        self.assertEqual(value['summaryReadRestriction'], 'SHARING_REGEN_REQUIRED')
        self.assertNotIn('Unsafe copied insight', json.dumps(value))
        self.table.items[('USER#owner', 'MEETING#m')]['sharedToAccount'] = False
        self.assertEqual(reader.get_account_brief('reader', 'grandchild').value['meetings'], [])

    def test_attachment_inventory_replay_and_continuation_bind_current_grant(self):
        attachments = handler._attachment_reader()
        empty = attachments.inventory('reader', 'USER#owner', 'm')['dependency']
        self.put(PK='MEETING#m', SK='ATTACH#attachment', attachmentId='attachment', meetingId='m',
                 userId='owner', originalKey='files/owner/m/upload.pdf', fileName='upload.pdf')
        inventory = attachments.inventory('reader', 'USER#owner', 'm')['dependency']
        page = attachments.read('reader', 'USER#owner', 'm', 'attachment')
        self.account('alternate', 'root')
        self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'
        self.assertFalse(attachments.is_current('reader', inventory))
        self.assertFalse(attachments.is_current('reader', page['dependency']))
        with self.assertRaises(ValueError):
            attachments.read('reader', 'USER#owner', 'm', 'attachment',
                             expected_revision=page['sourceRevision'])
        del self.table.items[('MEETING#m', 'ATTACH#attachment')]
        self.assertFalse(attachments.is_current('reader', empty))

    def test_binary_document_uses_canonical_index_revision_and_grant_bound_replay(self):
        from source_revision import normalize_bindings
        from canonical_selection import retrieve_selected
        document = self.table.items[('ACCOUNT#grandchild', 'DOC#doc')]
        document.update(fileKey='docs/owner/original.pdf', fileName='original.pdf')
        with mock.patch.object(handler.s3_client, 'head_object',
                               return_value={'ETag': '"source"', 'ContentLength': 100}):
            reader = handler._source_reader()
            snapshot = reader.read('reader', 'ACCOUNT#grandchild', 'DOC#doc')
            identity = snapshot['identity']
            self.assertNotEqual(snapshot['revision'], snapshot['indexRevision'])
            run = '00000000-0000-4000-8000-000000000001'
            uri = f"s3://kb/canonical/v1/accountDocument/{identity['resourceHash']}/{run}/file.pdf"
            candidate = {
                'uri': uri, 'score': 0.9, '_provider': {'content': {'text': 'Verified binary source'}},
                'metadata': dict(identity, indexSchema='canonical-v1',
                                 sourceRevision=snapshot['indexRevision'], indexRunId=run,
                                 sourceObjects=json.dumps(normalize_bindings(snapshot['objects']))),
            }
            filters = []

            def lookup(source_filter):
                filters.append(source_filter)
                return [candidate]

            result = retrieve_selected(reader, 'reader', 'binary', ['doc'],
                                       {('ACCOUNT#grandchild', 'DOC#doc'): identity}, lookup)[0]
            self.assertEqual(result['text'], 'Verified binary source')
            self.assertIn(snapshot['indexRevision'], json.dumps(filters))
            self.assertEqual(result['dependency']['sourceRevision'], snapshot['revision'])
            self.account('alternate', 'root')
            self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'
            self.assertFalse(handler._source_access()._source_is_current('reader', result['dependency']))

    def test_inherited_meeting_rejects_malformed_canonical_identity(self):
        for field, bad in (('PK', 'USER#wrong'), ('SK', 'MEETING#wrong'), ('entityType', 'OTHER')):
            with self.subTest(field=field):
                original = self.table.items[('USER#owner', 'MEETING#m')][field]
                self.table.items[('USER#owner', 'MEETING#m')][field] = bad
                self.assertEqual(handler._list_shared_meetings('reader'), [])
                self.table.items[('USER#owner', 'MEETING#m')][field] = original

    def test_meeting_discovery_context_and_privacy_work_without_share_fanout(self):
        self.assertEqual(handler._list_shared_meetings('reader'), [{'meetingId': 'm', 'ownerId': 'owner'}])
        snapshot = handler._source_reader().read('reader', 'USER#owner', 'MEETING#m')
        self.assertIsNotNone(snapshot)
        self.assertEqual(snapshot['fields']['notes'], 'User note')
        self.assertNotIn('content', snapshot['fields'])
        self.assertEqual(snapshot['fields']['summaryReadRestriction'], 'SHARING_REGEN_REQUIRED')
        text, error = handler._source_access().load_meeting_context('reader', 'm')
        self.assertIsNone(error)
        self.assertIn('Raw source', text)
        self.assertNotIn('Unsafe generated context', text)
        self.table.items[('USER#owner', 'MEETING#m')]['sharedToAccount'] = False
        self.assertEqual(handler._list_shared_meetings('reader'), [])

    def test_safe_summary_still_replays_only_under_the_same_grant_identity(self):
        content = 'Safe current generated context'
        self.table.items[('USER#owner', 'MEETING#m')].update(
            content=content, summaryPrivacy={'version': 1, 'state': 'safe', 'generationId': 'generation',
                                            'contentSha256': hashlib.sha256(content.encode()).hexdigest()})
        access = handler._source_access()
        state = new_source_state()
        text, error = access.load_meeting_context('reader', 'm', source_state=state)
        self.assertIsNone(error)
        self.assertIn(content, text)
        dependency = next(dep for dep in state['dependencies'] if 'attachmentId' not in dep)
        self.assertTrue(access._source_is_current('reader', dependency))
        self.account('alternate', 'root')
        self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'
        self.assertFalse(access._source_is_current('reader', dependency))

    def test_indexed_discovery_and_account_callbacks_include_only_current_descendants(self):
        self.table.items[('ACCOUNT#outside', 'META')]['parentAccountId'] = ''
        self.put(PK='ACCOUNT#root', SK='CHILDREF#outside', accountId='outside',
                 parentAccountId='root', entityType='ACCOUNT_CHILD_REF')
        identities, accounts = discover_sources(handler._source_reader(), 'reader', handler._query_all, [])
        self.assertEqual(accounts, {'ACCOUNT#root', 'ACCOUNT#child', 'ACCOUNT#grandchild'})
        self.assertIn(('ACCOUNT#grandchild', 'DOC#doc'), identities)
        rows = StrictAccountReader(self.table).list_accounts('reader').value
        child = next(row for row in rows if row['accountId'] == 'grandchild')
        self.assertEqual(child['accessPermission'], 'read')
        self.assertEqual(child['inheritedFromAccountId'], 'root')
        self.assertNotEqual(child.get('role'), 'owner')
        brief = StrictAccountReader(self.table).get_account_brief('reader', 'grandchild').value
        self.assertEqual(brief['meetings'][0]['meetingId'], 'm')

    def test_legacy_exported_account_callbacks_use_the_same_strict_read_grants(self):
        self.assertIn('grandchild', [row['accountId'] for row in handler.list_accounts_for_user('reader')])
        brief = handler.get_account_brief_for_chat('reader', 'grandchild')
        self.assertEqual(brief['meetings'][0]['meetingId'], 'm')
        insights = handler.get_account_insights_for_chat('reader', 'grandchild')
        self.assertEqual(insights['accessPermission'], 'read')

    def test_account_tool_history_rejects_reparenting_even_with_identical_visible_text(self):
        reader = StrictAccountReader(self.table)
        history = ToolHistory('reader', {'get_account_brief': reader.get_account_brief})
        state = new_source_state()
        before = history.read(state, 'get_account_brief', {'account': 'grandchild'})
        dependency = state['dependencies'][0]
        self.assertTrue(history.is_current(dependency))
        self.account('alternate', 'root')
        self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'
        self.put(PK='ACCOUNT#alternate', SK='CHILDREF#child', accountId='child',
                 parentAccountId='alternate', entityType='ACCOUNT_CHILD_REF')
        after = reader.get_account_brief('reader', 'grandchild').value
        self.assertEqual(before['meetings'], after['meetings'])
        self.assertEqual(before['account'], after['account'])
        self.assertFalse(history.is_current(dependency))
        del self.table.items[('ACCOUNT#root', 'MEMBER#reader')]
        self.assertFalse(history.is_current(dependency))

    def test_child_discovery_consumes_empty_advancing_pages_and_has_explicit_bound(self):
        ordinary_query = self.table.query
        seen = []

        def empty_page(**kwargs):
            condition = kwargs['KeyConditionExpression'].get_expression()
            prefix = condition['values'][1].get_expression()['values'][1]
            pk = condition['values'][0].get_expression()['values'][1]
            if pk == 'ACCOUNT#root' and prefix == 'CHILDREF#' and not kwargs.get('ExclusiveStartKey'):
                seen.append(True)
                return {'Items': [], 'LastEvaluatedKey': {'PK': pk, 'SK': 'CHILDREF#before'}}
            if kwargs.get('ExclusiveStartKey', {}).get('SK') == 'CHILDREF#before':
                kwargs = dict(kwargs)
                kwargs.pop('ExclusiveStartKey')
            return ordinary_query(**kwargs)

        self.table.meta.client.get_paginator = lambda name: __import__('botocore.paginate', fromlist=['Paginator']).Paginator(
            empty_page, {'input_token': 'ExclusiveStartKey', 'output_token': 'LastEvaluatedKey', 'result_key': 'Items'},
            __import__('botocore.session', fromlist=['Session']).Session().get_service_model('dynamodb').operation_model('Query'))
        ids = self.resolver().discover('reader', ['root'])
        self.assertIn('grandchild', ids)
        self.assertTrue(seen)
        with mock.patch('account_read_access.MAX_READABLE_ACCOUNTS', 2):
            with self.assertRaisesRegex(ValueError, 'limit'):
                self.resolver().discover('reader', ['root'])

    def test_repeated_query_cursor_fails_instead_of_looping(self):
        pages = [
            {'Items': [], 'LastEvaluatedKey': {'PK': 'ACCOUNT#root', 'SK': 'one'}},
            {'Items': [], 'LastEvaluatedKey': {'PK': 'ACCOUNT#root', 'SK': 'two'}},
            {'Items': [], 'LastEvaluatedKey': {'PK': 'ACCOUNT#root', 'SK': 'one'}},
        ]
        with mock.patch.object(self.table, 'query', side_effect=pages):
            with self.assertRaisesRegex(RuntimeError, 'advance'):
                handler._query_all(KeyConditionExpression=mock.Mock())


class DescendantTransportTests(_QAConversationFixture, unittest.TestCase):
    put = DescendantReadsTests.put
    account = DescendantReadsTests.account
    member = DescendantReadsTests.member
    meeting = DescendantReadsTests.meeting

    def setUp(self):
        self.set_up_transport(DescendantTable())
        patch = mock.patch.dict('os.environ', {'ACCOUNT_DESCENDANT_READS_ENABLED': '1',
                                             'SUMMARY_PRIVACY_READER_ENABLED': '0'})
        patch.start()
        self.addCleanup(patch.stop)

    def test_both_transports_discover_child_and_discard_history_after_reparent(self):
        for transport in ('rest', 'stream'):
            for tool in ('get_account_brief', 'list_meetings'):
                with self.subTest(transport=transport, tool=tool):
                    self.table.items.clear()
                    self.account('root')
                    self.member('root')
                    self.account('child', 'root')
                    self.account('grandchild', 'child')
                    self.meeting()
                    model = self.replies(transport, tool, {'account': 'grandchild'} if tool.startswith('get_') else {})
                    self.ask(transport, 'Read descendant account')
                    saved = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                    self.assertTrue(saved['sourceReplayable'])
                    self.assertIn('Current published meeting', saved['messages'])
                    self.ask(transport, 'Continue')
                    self.assertIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                    self.account('alternate', 'root')
                    self.table.items[('ACCOUNT#child', 'META')]['parentAccountId'] = 'alternate'
                    self.put(PK='ACCOUNT#alternate', SK='CHILDREF#child', accountId='child',
                             parentAccountId='alternate', entityType='ACCOUNT_CHILD_REF')
                    self.ask(transport, 'Continue with current sources')
                    self.assertNotIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))


if __name__ == '__main__':
    unittest.main()
