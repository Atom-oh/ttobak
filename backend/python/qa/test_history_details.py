import copy
import hashlib
import json
import unittest
from unittest import mock

from boto3.dynamodb.types import TypeDeserializer, TypeSerializer
import test_handler
from test_kb_fixtures import _QAConversationFixture, _SharedKBFixture, binary_fixture
from test_source_contract import _SourceFixture
import history_details

handler = test_handler.handler


class TestHistoryDetailContinuity(_SourceFixture, _QAConversationFixture, unittest.TestCase):
    def setUp(self):
        _SourceFixture.setUp(self)
        self.set_up_transport(self.table)

    def output(self, transport, question):
        result = self.ask(transport, question)
        if transport == 'rest':
            return json.loads(result['body'])
        return next(call.args[2] for call in reversed(handler._post_ws.call_args_list)
                    if call.args[2]['type'] == 'answer_complete')

    def test_no_tool_followup_retains_details_and_edit_or_revoke_clears_history_and_details(self):
        for transport in ('rest', 'stream'):
            for change in ('edit', 'revoke'):
                with self.subTest(transport=transport, change=change):
                    self.table.items.clear()
                    self.doc(content='DOCUMENT_PRIVATE')
                    self.grant()
                    model = self.replies(transport, 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
                    first = self.output(transport, 'read the document')
                    second = self.output(transport, 'repeat the previous answer')
                    self.assertEqual(second['toolsUsed'], [])
                    self.assertEqual(second['sources'], first['sources'])
                    self.assertEqual(second['sourceDetails'][0]['resourceId'], 'doc')
                    self.assertEqual(second['sourceDetails'][0]['provenanceScope'], 'validated_history')
                    self.assertIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                    if change == 'edit':
                        self.table.items[('USER#owner', 'DOC#doc')]['content'] = 'NEW_CONTENT'
                    else:
                        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
                    third = self.output(transport, 'continue')
                    self.assertEqual(third['sourceDetails'], [])
                    self.assertEqual(third['sources'], [])
                    self.assertNotIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))

    def test_legacy_or_mismatched_metadata_keeps_valid_history_with_identity_scope(self):
        self.doc()
        self.grant()
        self.replies('rest', 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        self.output('rest', 'read')
        key = ('SESSION#reader#chat-readonly', 'MESSAGES')
        metadata_key = (key[0], 'SOURCE_DETAILS')
        original = copy.deepcopy(self.table.items[metadata_key])
        for mode in ('missing', 'mismatch', 'expired'):
            self.table.items[metadata_key] = copy.deepcopy(original)
            if mode == 'missing':
                del self.table.items[metadata_key]
            elif mode == 'expired':
                self.table.items[metadata_key]['pendingShareExpiresAt'] = 1
            else:
                self.table.items[metadata_key]['binding'] = 'f' * 64
            details = []
            history = handler.load_session('chat-readonly', user_id='reader', source_details=details)
            self.assertTrue(history)
            self.assertEqual(details[0]['resourceId'], 'doc')
            self.assertEqual(details[0]['provenanceScope'], 'legacy_identity')

    def test_metadata_failure_preserves_dialogue_and_uses_identity_fallback(self):
        self.doc()
        self.grant()
        self.replies('rest', 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        put = self.table.put_item
        def unavailable_write(**kwargs):
            if kwargs['Item']['SK'] == 'SOURCE_DETAILS':
                raise RuntimeError('synthetic metadata outage')
            return put(**kwargs)
        with mock.patch.object(self.table, 'put_item', side_effect=unavailable_write):
            self.output('rest', 'read')
        get = self.table.get_item
        def unavailable_read(**kwargs):
            if kwargs['Key']['SK'] == 'SOURCE_DETAILS':
                raise RuntimeError('synthetic metadata outage')
            return get(**kwargs)
        with mock.patch.object(self.table, 'get_item', side_effect=unavailable_read):
            result = self.output('rest', 'repeat')
        self.assertEqual(result['toolsUsed'], [])
        self.assertEqual(result['sourceDetails'][0]['provenanceScope'], 'legacy_identity')
        self.assertIn('PRIVATE_CHOICE', json.dumps(self.model.converse.call_args.kwargs['messages']))

    def test_metadata_separate_item_does_not_grow_parent_and_sdk_roundtrip_restores(self):
        self.doc()
        self.grant()
        self.replies('rest', 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        self.output('rest', 'read')
        pk = 'SESSION#reader#chat-readonly'
        parent = self.table.items[(pk, 'MESSAGES')]
        self.assertEqual(set(parent), {'PK', 'SK', 'messages', 'sourceProvenanceVersion',
                                      'sourceDependencies', 'sourceReplayable', 'TTL'})
        metadata = self.table.items[(pk, 'SOURCE_DETAILS')]
        self.assertLess(len(json.dumps(metadata).encode()), 66 * 1024)
        serialize, deserialize = TypeSerializer().serialize, TypeDeserializer().deserialize
        for sk in ('MESSAGES', 'SOURCE_DETAILS'):
            self.table.items[(pk, sk)] = deserialize(serialize(self.table.items[(pk, sk)]))
        details = []
        self.assertTrue(handler.load_session('chat-readonly', user_id='reader', source_details=details))
        self.assertEqual(details[0]['provenanceScope'], 'validated_history')
        # Payload cannot move to a different user/session or a later conversation.
        parent = self.table.items[(pk, 'MESSAGES')]
        parent['messages'] = parent['messages'].replace('PRIVATE_CHOICE', 'NEW_ANSWER')
        details = []
        self.assertTrue(handler.load_session('chat-readonly', user_id='reader', source_details=details))
        self.assertEqual(details[0]['provenanceScope'], 'legacy_identity')
        self.assertNotEqual(history_details.binding('reader', 'a', '[]', [], '[]'),
                            history_details.binding('other', 'a', '[]', [], '[]'))

    def test_source_change_during_metadata_read_never_returns_history_or_details(self):
        self.doc()
        self.grant()
        self.replies('rest', 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        self.output('rest', 'read')
        get = self.table.get_item
        def revoke_during_read(**kwargs):
            result = get(**kwargs)
            if kwargs['Key']['SK'] == 'SOURCE_DETAILS':
                del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
            return result
        details = []
        with mock.patch.object(self.table, 'get_item', side_effect=revoke_during_read):
            self.assertEqual(handler.load_session('chat-readonly', user_id='reader', source_details=details), [])
        self.assertEqual(details, [])

    def test_oversized_metadata_keeps_all_valid_source_identities(self):
        deps, details = [], []
        for index in range(64):
            dep = {'sourcePK': 'USER#owner', 'sourceSK': 'DOC#doc' + str(index), 'sourceRevision': 'a' * 64}
            identity = history_details.identity_detail(dep, 'knowledge')
            deps.append(dep)
            details.append(dict(identity, title='😀' * 256, contentSource='current_saved'))
        payload = history_details.pack(details, deps, 'knowledge', 'assets')
        self.assertIsNone(payload)
        restored = history_details.restore(deps, payload, 'knowledge', 'assets')
        self.assertEqual(len(restored), len(deps))
        self.assertTrue(all(item['provenanceScope'] == 'legacy_identity' for item in restored))
        self.assertLess(len(history_details.encoded(restored).encode()), history_details.MAX_RESTORED_BYTES)

    def test_unrelated_cached_fields_are_not_replayed_and_titles_are_bounded(self):
        dep = {'sourcePK': 'USER#owner', 'sourceSK': 'DOC#doc', 'sourceRevision': 'a' * 64}
        rid = hashlib.sha256(b'USER#owner\0DOC#doc').hexdigest()
        detail = dict(dep, resourceKind='personalDocument', resourceId='doc',
                      uri='ttobak://source/' + rid, title='한😀' * 1000,
                      contentSource='current_saved', privateBody='must never persist')
        payload = history_details.pack([detail], [dep], 'knowledge', 'assets')
        self.assertNotIn('privateBody', payload)
        restored = history_details.restore([dep], payload, 'knowledge', 'assets')
        self.assertLessEqual(len(restored[0]['title'].encode()), 1024)
        self.assertTrue(restored[0]['titleTruncated'])
        detail['uri'] = 's3://foreign/private/body'
        restored = history_details.restore([dep], json.dumps([detail]), 'knowledge', 'assets')
        self.assertEqual(restored[0]['provenanceScope'], 'legacy_identity')
        self.assertNotIn('foreign', json.dumps(restored))

    def test_attachment_metadata_keeps_pages_but_never_nested_body_fields(self):
        dep = {'sourcePK': 'USER#owner', 'sourceSK': 'MEETING#m', 'attachmentId': 'a',
               'sourceRevision': 'a' * 64}
        detail = dict(dep, resourceKind='meetingAttachment', resourceId='a', meetingId='m',
                      uri='s3://assets/files/uploader/m/report.pdf', title='report.pdf',
                      attempt={'status': 'succeeded', 'runId': 'run', 'body': 'PRIVATE_BODY'},
                      result={'status': 'succeeded', 'complete': True, 'format': 'pdf', 'scope': 'native',
                              'runId': 'run', 'body': 'PRIVATE_BODY'},
                      locations=[{'kind': 'page', 'page': 2, 'body': 'PRIVATE_BODY'}])
        payload = history_details.pack([detail], [dep], 'knowledge', 'assets')
        self.assertNotIn('PRIVATE_BODY', payload)
        result = history_details.restore([dep], payload, 'knowledge', 'assets')
        self.assertEqual(result[0]['locations'], [{'kind': 'page', 'page': 2}])
        self.assertTrue(result[0]['result']['complete'])
        detail['result']['scope'] = {'secret': 'PRIVATE_BODY'}
        result = history_details.restore([dep], json.dumps([detail]), 'knowledge', 'assets')
        self.assertEqual(result[0]['provenanceScope'], 'legacy_identity')
        # Malformed payload is optional metadata, not a reason to discard history.
        self.assertEqual(history_details.restore([dep], '[{"uri":"\\ud800"}]', 'knowledge', 'assets')[0]
                         ['provenanceScope'], 'legacy_identity')

    def test_private_and_shared_binary_details_bind_original_key_revision_and_scope(self):
        for shared in (False, True):
            key = 'shared/test/file.docx' if shared else 'kb/reader/file.pdf'
            rid = hashlib.sha256(key.encode()).hexdigest()
            dep = {'sharedKey' if shared else 'manualKey': key, 'sourceRevision': 'a' * 64}
            prefix = 'shared-kb/v1/' if shared else 'manual-kb/v1/reader/'
            uri = 's3://knowledge/' + prefix + rid + '/' + 'a' * 64 + '/00000000-0000-4000-8000-000000000001/document.' + key.rsplit('.', 1)[-1]
            detail = {'resourceKind': 'sharedKbDocument' if shared else 'manualKbDocument',
                      'resourceId': rid, 'sourceKey': key, 'sourceBucket': 'knowledge',
                      'sourceRevision': 'a' * 64, 'uri': uri, 'partial': True,
                      'contentSource': 'verified_shared_kb_file' if shared else 'verified_manual_kb_file'}
            detail.update({'visibility': 'authenticated-shared'} if shared else {'ownerId': 'reader'})
            payload = history_details.pack([detail], [dep], 'knowledge', 'assets')
            result = history_details.restore([dep], payload, 'knowledge', 'assets')
            self.assertEqual(result[0]['uri'], uri)
            self.assertEqual(result[0]['provenanceScope'], 'validated_history')
            self.assertEqual(history_details.restore([dep], None, 'knowledge', 'assets')[0]['uri'],
                             's3://knowledge/' + key)


class TestBinaryHistoryContinuity(_SharedKBFixture, _QAConversationFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.set_up_transport(self.table)

    def test_actual_private_and_shared_search_followup_then_overwrite_or_delete(self):
        for transport in ('rest', 'stream'):
            for shared in (False, True):
                for change in ('overwrite', 'delete'):
                    with self.subTest(transport=transport, shared=shared, change=change):
                        self.table.items.clear()
                        self.key = 'shared/reference/file.pdf' if shared else 'kb/owner/file.pdf'
                        self.body, self.version = binary_fixture('.pdf', 'CURRENT_V1'), 'version-1'
                        hit = self.shared_snapshot() if shared else self.snapshot_fixture('CURRENT_V1')
                        self.snapshots = [hit]
                        model = self.replies(transport, 'search_knowledge_base',
                                             {'query': 'facts', 'source_keys': [self.key]})
                        def ask(question):
                            if transport == 'rest':
                                response = handler.handle_ask(question, user_id='owner', session_id='chat-binary')
                                self.assertEqual(response['statusCode'], 200, response)
                                return json.loads(response['body'])
                            response = handler.handle_ask_stream({
                                'question': question, 'userId': 'owner', 'sessionId': 'chat-binary',
                                'connectionId': 'c', 'endpoint': 'https://synthetic.invalid'})
                            self.assertEqual(response['status'], 'ok', response)
                            return next(call.args[2] for call in reversed(handler._post_ws.call_args_list)
                                        if call.args[2]['type'] == 'answer_complete')
                        first = ask('read')
                        self.runtime.reset_mock()
                        followup = ask('repeat')
                        self.assertEqual(first['sources'], [hit['location']['s3Location']['uri']])
                        self.assertEqual(followup['sources'], first['sources'])
                        self.assertEqual(followup['toolsUsed'], [])
                        self.assertEqual(followup['sourceDetails'][0]['sourceKey'], self.key)
                        self.assertEqual(followup['sourceDetails'][0]['provenanceScope'], 'validated_history')
                        self.runtime.retrieve.assert_not_called()
                        if change == 'overwrite':
                            self.body, self.version = binary_fixture('.pdf', 'CURRENT_V2'), 'version-2'
                        else:
                            self.body = None
                        current = ask('continue')
                        self.assertEqual(current['sources'], [])
                        self.assertEqual(current['sourceDetails'], [])
                        self.assertNotIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))


if __name__ == '__main__':
    unittest.main()
