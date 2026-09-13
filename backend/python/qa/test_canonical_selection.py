"""Exact canonical selectors preserve current authorization and file provenance."""
import copy
import json
import unittest
from unittest import mock

import test_handler as helpers
from test_source_contract import _SourceFixture
from session_provenance import new_source_state

handler = helpers.handler


class TestCanonicalSelection(_SourceFixture, unittest.TestCase):
    def search(self, ids, user='owner', count=5):
        return handler._source_access().retrieve_from_kb(
            'Allocation', count, user_id=user, resource_ids=ids)

    def file(self):
        self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"v1"', 'VersionId': 'one', 'ContentLength': 12}
        hit = self.indexed(filename='file.pdf', text='PRIVATE_BODY_CODE Allocation 17')
        hit['score'] = 0.05
        return hit

    def test_exact_low_score_file_uses_current_revision_and_excludes_foreign_candidates(self):
        hit = self.file()
        foreign = copy.deepcopy(hit)
        foreign['location']['s3Location']['uri'] = 's3://knowledge/shared/foreign.txt'
        foreign['content']['text'] = 'FOREIGN_SECRET'
        self.runtime.retrieve.return_value = {'retrievalResults': [foreign, hit]}
        result = self.search(['doc'], count=1)
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]['text'], 'PRIVATE_BODY_CODE Allocation 17')
        self.assertEqual(result[0]['provenance']['contentSource'], 'verified_indexed_file')
        filters = self.runtime.retrieve.call_args.kwargs['retrievalConfiguration']['vectorSearchConfiguration']['filter']
        self.assertIn(self.source_reader.read('owner', 'USER#owner', 'DOC#doc')['revision'], json.dumps(filters))
        self.assertNotIn('FOREIGN_SECRET', json.dumps(result))
        self.assertTrue(all(call.kwargs['Key'] == 'docs/owner/file.pdf'
                            for call in self.s3.head_object.call_args_list))

    def test_foreign_id_does_not_read_private_content_or_call_provider(self):
        self.file()
        self.s3.reset_mock()
        self.runtime.reset_mock()
        self.assertEqual(self.search(['doc'], user='reader'), [])
        self.s3.head_object.assert_not_called()
        self.runtime.retrieve.assert_not_called()

    def test_received_grant_is_rechecked_after_discovery(self):
        hit = self.file()
        self.grant()
        self.runtime.retrieve.return_value = {'retrievalResults': [hit]}
        self.assertEqual(self.search(['doc'], user='reader')[0]['text'], hit['content']['text'])
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        self.runtime.reset_mock()
        self.s3.reset_mock()
        self.assertEqual(self.search(['doc'], user='reader'), [])
        self.runtime.retrieve.assert_not_called()
        self.s3.head_object.assert_not_called()

    def test_account_selection_requires_current_membership(self):
        self.doc(pk='ACCOUNT#team', content='ACCOUNT_CODE')
        self.table.put_item(Item={'PK': 'ACCOUNT#team', 'SK': 'MEMBER#reader',
                                 'accountId': 'team', 'userId': 'reader',
                                 'GSI1PK': 'USER#reader', 'GSI1SK': 'ACCOUNT#team'})
        self.assertEqual(self.search(['doc'], user='reader')[0]['document']['content'], 'ACCOUNT_CODE')
        self.table.index_rows = copy.deepcopy(list(self.table.items.values()))
        del self.table.items[('ACCOUNT#team', 'MEMBER#reader')]
        self.assertEqual(self.search(['doc'], user='reader'), [])

    def test_wrong_revision_never_yields_stale_file_bytes(self):
        hit = self.file()
        self.s3.head_object.return_value = {'ETag': '"v2"', 'VersionId': 'two', 'ContentLength': 12}
        self.runtime.retrieve.return_value = {'retrievalResults': [hit]}
        result = self.search(['doc'])
        self.assertTrue(result[0]['provenance']['filePending'])
        self.assertNotIn('text', result[0])
        self.assertTrue(result[0]['volatile'])

    def test_provider_failure_is_not_an_empty_result(self):
        self.file()
        self.runtime.retrieve.side_effect = RuntimeError('synthetic private provider detail')
        with self.assertRaises(RuntimeError):
            self.search(['doc'])

    def test_change_during_provider_call_is_rejected(self):
        hit = self.file()
        def changed(**_):
            self.s3.head_object.return_value = {'ETag': '"v2"', 'VersionId': 'two', 'ContentLength': 12}
            return {'retrievalResults': [hit]}
        self.runtime.retrieve.side_effect = changed
        with self.assertRaises(RuntimeError):
            self.search(['doc'])

    def test_meeting_selection_does_not_expand_into_attachment_sources(self):
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#m',
                                 'meetingId': 'm', 'userId': 'owner', 'notes': 'MEETING_CODE'})
        result = self.search(['m'])
        self.assertEqual(result[0]['meeting']['notes'], 'MEETING_CODE')
        self.assertNotIn('attachments', result[0])
        self.assertNotIn('additionalSourceDetails', result[0])

    def test_ambiguous_authorized_id_is_not_arbitrarily_chosen(self):
        self.doc()
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#doc',
                                 'meetingId': 'doc', 'userId': 'owner', 'notes': 'OTHER'})
        with self.assertRaises(ValueError):
            self.search(['doc'])
        self.runtime.retrieve.assert_not_called()

    def test_invalid_or_mixed_selectors_fail_before_discovery(self):
        for ids in ([], ['../doc'], ['x'] * 6, 'doc', [True]):
            with self.subTest(ids=ids), self.assertRaises(ValueError):
                self.search(ids)
        with self.assertRaises(ValueError):
            handler._source_access().retrieve_from_kb(
                'q', user_id='owner', resource_ids=['doc'], source_keys=['kb/owner/a.pdf'])
        with self.assertRaises(ValueError):
            self.search(['a', 'b'], count=1)
        self.runtime.retrieve.assert_not_called()

    def test_missing_selected_id_is_revalidated_even_with_another_result(self):
        self.doc(content='CURRENT')
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        result = context['retrieve_from_kb']('Allocation', 2, resource_ids=['doc', 'later'])
        self.assertEqual(len(result), 1)
        empty = next(dep for dep in state['dependencies'] if 'emptySearch' in dep)
        self.assertEqual(empty['emptySearch']['resourceIds'], ['later'])
        access = handler._source_access()
        self.assertTrue(access._source_is_current('owner', empty))
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'DOC#later', 'docId': 'later',
                                 'sourceUserId': 'owner', 'entityType': 'USER_DOC',
                                 'title': 'Later', 'content': 'NOW_PRESENT'})
        self.assertFalse(access._source_is_current('owner', empty))

    def test_tool_schema_and_callback_expose_the_canonical_selector(self):
        import tools
        spec = next(x['toolSpec'] for x in tools.TOOL_DEFINITIONS
                    if x['toolSpec']['name'] == 'search_knowledge_base')
        self.assertIn('resource_ids', spec['inputSchema']['json']['properties'])
        callback = mock.Mock(return_value=[])
        tools.execute_tool('search_knowledge_base', {'query': 'q', 'resource_ids': ['doc']},
                           {'retrieve_from_kb': callback})
        callback.assert_called_once_with('q', 5, resource_ids=['doc'])
        from web_search import redact_tool_input_for_log
        self.assertNotIn('PRIVATE_RESOURCE', json.dumps(redact_tool_input_for_log(
            'search_knowledge_base', {'resource_ids': ['PRIVATE_RESOURCE']})))

    def test_default_semantic_threshold_and_manual_key_boundary_remain(self):
        hit = self.file()
        self.runtime.retrieve.return_value = {'retrievalResults': [hit]}
        self.assertEqual(handler._source_access().retrieve_from_kb(
            'Allocation', user_id='owner'), [])
        with self.assertRaises(ValueError):
            handler._source_access().retrieve_from_kb(
                'Allocation', user_id='owner', source_keys=['doc'])
        with self.assertRaises(ValueError):
            handler._source_access().retrieve_from_kb(
                'Allocation', user_id='owner', source_keys=['kb/another/file.pdf'])

    def test_selected_file_reaches_real_handler_and_revocation_invalidates_history(self):
        hit = self.file()
        self.grant()
        self.runtime.retrieve.return_value = {'retrievalResults': [hit]}
        tool = {'role': 'assistant', 'content': [{'toolUse': {
            'toolUseId': 'selected', 'name': 'search_knowledge_base',
            'input': {'query': 'Allocation', 'resource_ids': ['doc'], 'numberOfResults': 1}}}]}
        with mock.patch.object(handler, 'bedrock_runtime') as model:
            model.converse.side_effect = [
                {'stopReason': 'tool_use', 'output': {'message': tool}},
                {'stopReason': 'end_turn', 'output': {'message': {
                    'role': 'assistant', 'content': [{'text': 'PRIVATE_BODY_CODE Allocation 17'}]}}},
                {'stopReason': 'end_turn', 'output': {'message': {
                    'role': 'assistant', 'content': [{'text': 'Unavailable'}]}}},
            ]
            first = handler.handle_ask('Read selected doc', user_id='reader', session_id='selected-file')
            self.assertEqual(first['statusCode'], 200)
            result = json.loads(first['body'])
            self.assertTrue(result['usedKB'])
            self.assertEqual(result['sourceDetails'][0]['contentSource'], 'verified_indexed_file')
            self.assertEqual(result['sourceDetails'][0]['resourceId'], 'doc')
            del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
            second = handler.handle_ask('Continue', user_id='reader', session_id='selected-file')
            self.assertEqual(second['statusCode'], 200)
            self.assertNotIn('PRIVATE_BODY_CODE', json.dumps(model.converse.call_args.kwargs['messages']))

    def test_empty_selector_receipt_survives_sdk_number_serialization(self):
        from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
        from request_history import remember_empty_search, valid_request_dependency
        state = new_source_state()
        self.assertTrue(remember_empty_search(state, 'owner', 'Allocation', 1, resource_ids=['later']))
        restored = TypeDeserializer().deserialize(TypeSerializer().serialize(state['dependencies'][0]))
        self.assertTrue(valid_request_dependency(restored))
        forged = copy.deepcopy(restored)
        forged['emptySearch']['sourceKeys'] = ['kb/owner/a.pdf']
        self.assertFalse(valid_request_dependency(forged))
