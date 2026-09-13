"""Shared KB migration keeps baseline authenticated visibility and byte proof."""
import copy
import json
import unittest
from unittest import mock

import test_handler as helpers
from test_kb_fixtures import _SharedKBFixture, binary_fixture

handler = helpers.handler


class TestSharedKB(_SharedKBFixture, unittest.TestCase):


    def test_shared_pdf_docx_pending_ready_overwrite_and_delete_for_authenticated_users(self):
        import tools
        for extension in ('.pdf', '.docx'):
            with self.subTest(extension=extension):
                self.key = 'shared/reference/existing' + extension
                self.body, self.version, self.snapshots = binary_fixture(extension, 'CURRENT_V1'), 'v1', []
                pending = handler.retrieve_from_kb('facts', user_id='reader')
                self.assertEqual(len(pending), 1, 'shared binary disappeared')
                self.assertEqual(pending[0]['manualFile']['status'], 'pending')
                self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', tools.format_kb_results(pending))
                self.snapshots.append(self.shared_snapshot())
                for user in ('reader', 'another-user'):
                    current = handler.retrieve_from_kb('facts', user_id=user)
                    self.assertEqual(current[0]['manualFile']['status'], 'ready')
                    detail = current[0]['provenance']
                    self.assertEqual(detail['resourceKind'], 'sharedKbDocument')
                    self.assertEqual(detail['visibility'], 'authenticated-shared')
                    self.assertNotIn('ownerId', detail)
                    self.assertIn('CURRENT_V1', tools.format_kb_results(current))
                # A new object version invalidates the snapshot even if bytes/ETag stay the same.
                self.version = 'v2'
                stale = handler.retrieve_from_kb('facts', user_id='reader')
                self.assertEqual(stale[0]['manualFile']['status'], 'pending')
                self.assertNotIn('CURRENT_V1', json.dumps(stale))
                self.body = binary_fixture(extension, 'CURRENT_V2')
                self.snapshots.append(self.shared_snapshot('CURRENT_V2', '00000000-0000-4000-8000-000000000002'))
                self.assertIn('CURRENT_V2', tools.format_kb_results(handler.retrieve_from_kb('facts', user_id='reader')))
                self.body = None
                self.assertEqual(handler.retrieve_from_kb('facts', user_id='reader'), [])


    def test_shared_exact_revision_lookup_and_session_revalidation(self):
        snapshot = self.shared_snapshot()

        def retrieve(**kwargs):
            query = kwargs['retrievalConfiguration']['vectorSearchConfiguration']['filter']
            text = json.dumps(query)
            if '"resourceId"' in text and '"sourceRevision"' in text:
                self.assertIn('"authenticated-shared"', text)
                self.assertNotIn('"ownerId"', text)
                return {'retrievalResults': [snapshot]}
            return {'retrievalResults': [self.old_hit()]}
        self.runtime.retrieve.side_effect = retrieve
        state, details = handler.new_source_state(), []
        result = handler._agent_context('reader', None, None, state, details)['retrieve_from_kb']('facts')
        self.assertEqual(result[0]['manualFile']['status'], 'ready')
        self.assertIn('sharedKey', result[0]['dependency'])
        messages = [{'role': 'user', 'content': [{'text': 'facts'}]},
                    {'role': 'assistant', 'content': [{'text': 'DERIVED_SHARED_FACT'}]}]
        handler.save_session('shared', messages, user_id='reader', source_state=state)
        self.assertEqual(handler.load_session('shared', user_id='reader'), messages)
        self.version = 'replacement'
        self.assertEqual(handler.load_session('shared', user_id='reader'), [])
        # A tampered shared dependency cannot gain private-key access.
        self.s3.head_object.reset_mock()
        self.assertFalse(handler._source_is_current('reader', {
            'sharedKey': 'kb/owner/private.pdf', 'sourceRevision': snapshot['metadata']['sourceRevision'],
        }))
        self.s3.head_object.assert_not_called()


    def test_actual_model_turns_use_shared_provenance_and_discard_changed_history(self):
        self.snapshots.append(self.shared_snapshot())

        def tool(identifier):
            return {'stopReason': 'tool_use', 'output': {'message': {
                'role': 'assistant', 'content': [{'toolUse': {
                    'toolUseId': identifier, 'name': 'search_knowledge_base', 'input': {'query': 'shared facts'},
                }}]}}}

        def answer(text):
            return {'stopReason': 'end_turn', 'output': {'message': {
                'role': 'assistant', 'content': [{'text': text}]}}}

        with mock.patch.object(handler, 'bedrock_runtime') as model:
            inputs = []
            responses = iter([tool('t1'), answer('DERIVED_SHARED_V1'), tool('t2'), answer('DERIVED_SHARED_V2')])

            def converse(**kwargs):
                inputs.append(copy.deepcopy(kwargs['messages']))
                return next(responses)
            model.converse.side_effect = converse
            first = handler.handle_ask('shared facts', user_id='reader', session_id='chat-shared')
            self.assertEqual(first['statusCode'], 200)
            detail = json.loads(first['body'])['sourceDetails'][0]
            self.assertEqual(detail['resourceKind'], 'sharedKbDocument')
            self.assertEqual(detail['visibility'], 'authenticated-shared')
            self.assertNotIn('ownerId', detail)
            self.body, self.version = binary_fixture('.pdf', 'CURRENT_V2'), 'v2'
            self.snapshots.append(self.shared_snapshot('CURRENT_V2', '00000000-0000-4000-8000-000000000002'))
            second = handler.handle_ask('shared facts again', user_id='reader', session_id='chat-shared')
            self.assertEqual(second['statusCode'], 200)
            self.assertNotIn('DERIVED_SHARED_V1', json.dumps(inputs[2]))
            self.assertNotIn('CURRENT_V1', json.dumps(inputs[2]))
            self.assertIn('CURRENT_V2', json.dumps(inputs[3]))
            self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', json.dumps(inputs))

    def test_shared_pending_does_not_replay_as_current_after_migration(self):
        state, details = handler.new_source_state(), []
        pending = handler._agent_context('reader', None, None, state, details)['retrieve_from_kb']('facts')
        self.assertEqual(pending[0]['manualFile']['status'], 'pending')
        messages = [{'role': 'user', 'content': [{'text': 'facts'}]},
                    {'role': 'assistant', 'content': [{'text': 'old pending notice'}]}]
        handler.save_session('pending-shared', messages, user_id='reader', source_state=state)
        self.snapshots.append(self.shared_snapshot())
        self.assertEqual(handler.load_session('pending-shared', user_id='reader'), [])
        current = handler.retrieve_from_kb('facts', user_id='reader')
        self.assertEqual(current[0]['manualFile']['status'], 'ready')
