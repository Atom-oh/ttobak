"""Actual QA retrieval/model/session integration tests, loaded by test_handler."""
import copy
import io
import json
import unittest
from unittest import mock

import test_handler as helpers
from test_source_contract import _SourceFixture
from test_attachment_context import _AttachmentFixture

handler = helpers.handler


class TestIndexedSourceIntegration(_SourceFixture, unittest.TestCase):


    def test_source_derived_assistant_and_tool_history_is_not_replayed_after_changes(self):
        for change in ('edit', 'delete', 'revoke'):
            with self.subTest(change=change):
                self.table.items.clear()
                row = self.doc(content='PRIVATE_TERM')
                self.grant()
                tool = {'role': 'assistant', 'content': [{'toolUse': {
                    'toolUseId': 'search1', 'name': 'search_knowledge_base',
                    'input': {'query': 'PRIVATE_TERM'},
                }}]}
                answer = {'role': 'assistant', 'content': [{'text': 'DERIVED_PRIVATE_ANSWER'}]}
                with mock.patch.object(handler, 'bedrock_runtime') as model:
                    model.converse.side_effect = [
                        {'stopReason': 'tool_use', 'output': {'message': tool}},
                        {'stopReason': 'end_turn', 'output': {'message': answer}},
                        {'stopReason': 'end_turn', 'output': {'message': {
                            'role': 'assistant', 'content': [{'text': 'fresh answer'}]}}},
                    ]
                    first = handler.handle_ask('find private', user_id='reader', session_id='chat-proof')
                    self.assertEqual(first['statusCode'], 200)
                    payload = json.loads(first['body'])
                    self.assertEqual(payload['sourceDetails'][0]['resourceKind'], 'personalDocument')
                    if change == 'edit':
                        row['content'] = 'EDITED_TERM'
                    elif change == 'delete':
                        del self.table.items[('USER#owner', 'DOC#doc')]
                    else:
                        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
                    second = handler.handle_ask('continue', user_id='reader', session_id='chat-proof')
                    self.assertEqual(second['statusCode'], 200)
                    sent = json.dumps(model.converse.call_args.kwargs['messages'])
                    self.assertNotIn('DERIVED_PRIVATE_ANSWER', sent)
                    self.assertNotIn('PRIVATE_TERM', sent)
                    self.assertNotIn('toolResult', sent)

    def test_unverifiable_legacy_session_does_not_replay_source_text(self):
        self.table.put_item(Item={
            'PK': 'SESSION#reader#legacy', 'SK': 'MESSAGES',
            'messages': json.dumps([
                {'role': 'user', 'content': [{'text': 'question'}]},
                {'role': 'assistant', 'content': [{'text': 'OLD_UNVERIFIABLE_PRIVATE_TEXT'}]},
            ]),
        })
        self.assertEqual(handler.load_session('legacy', user_id='reader'), [])

    def test_streaming_response_details_and_session_revoke_use_same_guard(self):
        self.doc(content='STREAM_PRIVATE_TERM')
        self.grant()
        tool_stream = [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': 'search', 'name': 'search_knowledge_base'}}}},
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"query":"STREAM_PRIVATE_TERM"}'}}}},
            {'contentBlockStop': {}}, {'messageStop': {'stopReason': 'tool_use'}},
        ]

        def text_stream(text):
            return [{'contentBlockStart': {'start': {}}},
                    {'contentBlockDelta': {'delta': {'text': text}}},
                    {'contentBlockStop': {}}, {'messageStop': {'stopReason': 'end_turn'}}]
        with mock.patch.object(handler, 'bedrock_runtime') as model, \
                mock.patch.object(handler, '_apigw_client'), \
                mock.patch.object(handler, '_post_ws', return_value=True) as send:
            model.converse_stream.side_effect = [
                {'stream': tool_stream}, {'stream': text_stream('DERIVED_STREAM_PRIVATE')},
                {'stream': text_stream('fresh answer')},
            ]
            event = {'connectionId': 'c', 'endpoint': 'https://synthetic.invalid',
                     'question': 'find stream', 'userId': 'reader', 'sessionId': 'chat-stream'}
            self.assertEqual(handler.handle_ask_stream(event)['status'], 'ok')
            complete = next(call.args[2] for call in send.call_args_list if call.args[2]['type'] == 'answer_complete')
            self.assertEqual(complete['sourceDetails'][0]['resourceKind'], 'personalDocument')
            del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
            self.s3.reset_mock()
            self.assertEqual(handler.handle_ask_stream(dict(event, question='continue'))['status'], 'ok')
            sent = json.dumps(model.converse_stream.call_args.kwargs['messages'])
            self.assertNotIn('DERIVED_STREAM_PRIVATE', sent)
            self.assertNotIn('STREAM_PRIVATE_TERM', sent)
            self.s3.head_object.assert_not_called()

    def test_unchanged_source_history_is_preserved_but_file_bytes_changes_invalidate_it(self):
        from session_provenance import new_source_state, remember_source
        self.doc(content='', fileKey='docs/owner/file.pdf')
        self.s3.head_object.return_value = {'ETag': '"old"', 'VersionId': 'v1', 'ContentLength': 5}
        snapshot = handler._source_reader().read('owner', 'USER#owner', 'DOC#doc')
        state = new_source_state()
        remember_source(state, {'sourcePK': 'USER#owner', 'sourceSK': 'DOC#doc',
                                'sourceRevision': snapshot['revision']})
        messages = [{'role': 'user', 'content': [{'text': 'question'}]},
                    {'role': 'assistant', 'content': [{'text': 'FILE_DERIVED_FACT'}]}]
        handler.save_session('file-session', messages, user_id='owner', source_state=state)
        self.assertEqual(handler.load_session('file-session', user_id='owner'), messages)
        self.s3.head_object.return_value = {'ETag': '"new"', 'VersionId': 'v2', 'ContentLength': 5}
        self.assertEqual(handler.load_session('file-session', user_id='owner'), [])


    def test_legacy_text_history_uses_current_object_binding_without_unbound_chunks(self):
        self.runtime.retrieve.return_value = {'retrievalResults': [{
            'score': 0.9, 'location': {'s3Location': {'uri': 's3://knowledge/kb/owner/note.md'}},
            'content': {'text': 'OLD_UNBOUND_CHUNK'},
        }]}
        body = b'current' * 450
        self.s3.head_object.return_value = {'ETag': '"v1"', 'VersionId': 'v1', 'ContentLength': len(body)}
        self.s3.get_object.side_effect = lambda **kw: {'ETag': '"v1"', 'VersionId': 'v1',
                                                       'Body': io.BytesIO(body)}
        state, details = handler.new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        result = context['retrieve_from_kb']('legacy')[0]
        self.assertIn('dependency', result)
        self.assertEqual(result['text'], body.decode())
        self.assertTrue(details[0]['partial'], 'public source detail claimed the full model context')
        page = context['load_legacy_text']('owner', result['uri'], 0, result['provenance']['sourceRevision'])
        self.assertEqual(page['text'], body.decode())
        messages = [{'role': 'user', 'content': [{'text': 'question'}]},
                    {'role': 'assistant', 'content': [{'text': 'LEGACY_DERIVED_FACT'}]}]
        handler.save_session('legacy-new', messages, user_id='owner', source_state=state)
        self.assertEqual(handler.load_session('legacy-new', user_id='owner'), messages)
        self.s3.head_object.return_value = {'ETag': '"v2"', 'VersionId': 'v2', 'ContentLength': len(body)}
        self.assertEqual(handler.load_session('legacy-new', user_id='owner'), [])
        self.assertEqual(self.s3.get_object.call_count, 2)


class AttachmentIntegrationTests(_AttachmentFixture, unittest.TestCase):
    def test_actual_meeting_answer_gets_file_context_and_replay_rechecks_attachment(self):
        with mock.patch.object(handler, 'bedrock_runtime') as model:
            model.converse.return_value = {'stopReason': 'end_turn', 'output': {'message': {
                'role': 'assistant', 'content': [{'text': 'DERIVED_ATTACHMENT_FACT'}]}}}
            first = handler.handle_ask('read attached file', meeting_id='m',
                                       user_id='reader', session_id='chat-attachment')
            self.assertEqual(first['statusCode'], 200)
            sent = json.dumps(model.converse.call_args.kwargs['system'], ensure_ascii=False)
            self.assertIn('현재 파일 사실', sent)
            self.assertIn('page', sent)
            self.assertIn('오디오 시각을 붙이지 마세요', sent)
            details = json.loads(first['body'])['sourceDetails']
            file_detail = next(d for d in details if d['resourceKind'] == 'meetingAttachment')
            self.assertEqual(file_detail['locations'], [{'kind': 'page', 'page': 3}])
            self.source_etag = '"overwritten-without-ddb-event"'
            model.converse.return_value = {'stopReason': 'end_turn', 'output': {'message': {
                'role': 'assistant', 'content': [{'text': 'fresh answer'}]}}}
            second = handler.handle_ask('continue', user_id='reader', session_id='chat-attachment')
            self.assertEqual(second['statusCode'], 200)
            sent = json.dumps(model.converse.call_args.kwargs['messages'], ensure_ascii=False)
            self.assertNotIn('DERIVED_ATTACHMENT_FACT', sent)
            self.assertNotIn('현재 파일 사실', sent)
