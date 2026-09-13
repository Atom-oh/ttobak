"""Request-input receipts and revalidated empty searches; no AWS/model calls."""
import copy
from decimal import Decimal
import importlib.util
import json
import unittest

from boto3.dynamodb.types import TypeDeserializer, TypeSerializer

import test_handler as helpers
from test_source_contract import _SourceFixture
from source_access import SourceAccess
from attachment_context import AttachmentReader
from session_provenance import new_source_state, restore_messages, valid_dependency


def persisted(state, messages):
    item = {'sourceProvenanceVersion': 1, 'sourceReplayable': state['replayable'],
            'sourceDependencies': state['dependencies'], 'messages': json.dumps(messages)}
    return TypeDeserializer().deserialize(TypeSerializer().serialize(item))


class TestRequestHistory(_SourceFixture, unittest.TestCase):
    def access(self):
        return SourceAccess(
            self.source_reader, AttachmentReader(self.source_reader, helpers.handler._query_all),
            lambda user: [{'ownerId': 'owner', 'meetingId': 'm'}],
            query_all=helpers.handler._query_all, provider=self.runtime, kb_id='test-kb')

    def meeting(self):
        self.row = {'PK': 'USER#owner', 'SK': 'MEETING#m', 'userId': 'owner', 'meetingId': 'm',
                    'notes': 'SAVED_NOTE', 'transcriptA': 'STORED_TRANSCRIPT'}
        self.table.items[('USER#owner', 'MEETING#m')] = self.row
        self.table.put_item(Item={'PK': 'USER#reader', 'SK': 'SHARED#m',
                                  'ownerId': 'owner', 'meetingId': 'm'})

    def messages(self):
        return [
            {'role': 'user', 'content': [{'text': 'continue the live discussion'}]},
            {'role': 'assistant', 'content': [{'toolUse': {
                'toolUseId': 't', 'name': 'search_transcript', 'input': {'keywords': 'live'}}}]},
            {'role': 'user', 'content': [{'toolResult': {
                'toolUseId': 't', 'content': [{'text': 'live excerpt'}]}}]},
            {'role': 'assistant', 'content': [{'text': 'PRIVATE_DERIVED_ANSWER'}]},
        ]

    def restore(self, saved, state, user='reader', meeting_id=None):
        return restore_messages(saved, state, lambda dep: self.access()._source_is_current(
            user, dep, request_meeting_id=meeting_id),
                                source_covered_tools={'search_transcript'})

    def test_rolling_client_windows_preserve_followup_with_sdk_serialization(self):
        self.meeting()
        state = new_source_state()
        first = 'old window ' + '가' * 9000
        text, notes, error = self.access()._request_meeting_context('reader', 'm', first, source_state=state)
        self.assertIsNone(error)
        self.assertEqual((text, notes), (first, 'SAVED_NOTE'))
        self.assertTrue(state['replayable'], 'client input is not an unverifiable stored-source result')
        saved = persisted(state, self.messages())
        current = new_source_state()
        second = '나' * 9000 + ' new tail'
        self.access()._request_meeting_context('reader', 'm', second, source_state=current)
        self.assertEqual(self.restore(saved, current, meeting_id='m'), self.messages())
        self.assertEqual(self.restore(saved, new_source_state(), meeting_id='other'), [])
        self.assertEqual(self.restore(saved, new_source_state()), [])
        self.assertNotIn(first, json.dumps(saved['sourceDependencies'], default=str))
        self.assertTrue(any('clientInput' in dep for dep in saved['sourceDependencies']))
        self.assertTrue(any(dep.get('sourceSK') == 'MEETING#m' for dep in saved['sourceDependencies']))

    def test_client_input_cannot_bypass_saved_source_edit_delete_or_revoke(self):
        for change in ('notes', 'delete', 'revoke'):
            with self.subTest(change=change):
                self.table.items.clear()
                self.meeting()
                state = new_source_state()
                self.access()._request_meeting_context('reader', 'm', 'live input', source_state=state)
                saved = persisted(state, self.messages())
                self.assertEqual(self.restore(saved, new_source_state(), meeting_id='m'), self.messages())
                if change == 'notes':
                    self.row['notes'] = 'CORRECTED'
                elif change == 'delete':
                    del self.table.items[('USER#owner', 'MEETING#m')]
                else:
                    del self.table.items[('USER#reader', 'SHARED#m')]
                self.assertEqual(self.restore(saved, new_source_state(), meeting_id='m'), [])

    def test_unscoped_client_input_is_bound_to_current_user_and_never_s3(self):
        state = new_source_state()
        self.access()._request_meeting_context('reader', None, 'own supplied input', source_state=state)
        saved = persisted(state, self.messages())
        self.assertEqual(self.restore(saved, new_source_state()), self.messages())
        self.assertEqual(self.restore(saved, new_source_state(), 'outsider'), [])
        self.s3.head_object.assert_not_called()
        self.s3.get_object.assert_not_called()

    def request_module(self):
        self.assertIsNotNone(importlib.util.find_spec('request_history'), 'request receipts are not implemented')
        import request_history
        return request_history

    def test_empty_search_reexecutes_and_new_content_or_grants_invalidate_history(self):
        request = self.request_module()
        for user in ('owner', 'reader'):
            with self.subTest(user=user):
                self.table.items.clear()
                state = new_source_state()
                self.assertEqual(self.access().retrieve_from_kb('NEW_TERM', user_id=user), [])
                request.remember_empty_search(state, user, 'NEW_TERM', 5)
                saved = persisted(state, [{'role': 'assistant', 'content': [{'text': 'No matches yet'}]}])
                before = self.runtime.retrieve.call_count
                self.assertTrue(self.restore(saved, new_source_state(), user))
                self.assertGreater(self.runtime.retrieve.call_count, before)
                self.doc(content='NEW_TERM')
                if user == 'reader':
                    self.grant()
                self.assertEqual(self.restore(saved, new_source_state(), user), [])

    def test_empty_search_failures_are_not_empty_success(self):
        request = self.request_module()
        state = new_source_state()
        request.remember_empty_search(state, 'reader', 'query', 5)
        saved = persisted(state, [{'role': 'assistant', 'content': [{'text': 'No matches'}]}])
        self.runtime.retrieve.side_effect = RuntimeError('synthetic provider unavailable')
        self.assertEqual(self.restore(saved, new_source_state()), [])
        self.assertEqual(self.restore(saved, new_source_state(), 'outsider'), [])

    def test_receipt_schema_and_tracking_bounds_fail_closed_without_tool_failure(self):
        request = self.request_module()
        state = new_source_state()
        request.remember_client_input(state, 'reader', None, '제공한 문장')
        dependency = state['dependencies'][0]
        self.assertTrue(valid_dependency(dependency))
        for field, value in [('sourceRevision', '0' * 64), ('extra', True)]:
            self.assertFalse(valid_dependency(dict(dependency, **{field: value})))
        forged = copy.deepcopy(dependency)
        forged['clientInput']['userId'] = 'outsider'
        self.assertFalse(valid_dependency(forged))
        limit = new_source_state()
        request.remember_empty_search(limit, 'reader', '가' * 5000, 5)
        self.assertFalse(limit['replayable'])
        self.assertEqual(limit['dependencies'], [])
        self.assertEqual(limit['toolHistoryCoverage'][0]['tool'], 'search_knowledge_base')

    def test_empty_search_replay_budget_is_enforced_before_provider_reads(self):
        request = self.request_module()
        dependencies = []
        for index in range(request.MAX_EMPTY_SEARCHES + 1):
            state = new_source_state()
            request.remember_empty_search(state, 'reader', f'query {index}', 5)
            dependencies.extend(state['dependencies'])
        saved = persisted({'dependencies': dependencies, 'replayable': True},
                          [{'role': 'assistant', 'content': [{'text': 'empty results'}]}])
        before = self.runtime.retrieve.call_count
        self.assertEqual(self.restore(saved, new_source_state()), [])
        self.assertEqual(self.runtime.retrieve.call_count, before)

    def test_sdk_counts_are_typed_and_same_query_deduplicates(self):
        request = self.request_module()
        state = new_source_state()
        request.remember_empty_search(state, 'reader', 'query', 20)
        request.remember_empty_search(state, 'reader', 'query', 10)
        self.assertEqual(len(state['dependencies']), 1)
        saved = persisted(state, [{'role': 'assistant', 'content': [{'text': 'No results'}]}])
        self.assertIsInstance(saved['sourceDependencies'][0]['emptySearch']['count'], Decimal)
        self.assertTrue(self.restore(saved, new_source_state()))
        for bad in (True, Decimal('NaN'), Decimal('1.5'), Decimal('1e99999')):
            dependency = copy.deepcopy(state['dependencies'][0])
            dependency['emptySearch']['count'] = bad
            self.assertFalse(valid_dependency(dependency))
