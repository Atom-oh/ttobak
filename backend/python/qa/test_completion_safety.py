"""Final-source and delivery boundaries; synthetic storage/model/SDK responses."""
import copy
import json
import unittest
from unittest import mock

from botocore.session import get_session
from botocore.stub import ANY, Stubber
from test_source_contract import _SourceFixture
from test_kb_fixtures import _QAConversationFixture
import test_handler
from session_provenance import collect_detail
from web_search import redact_tool_input_for_log

handler = test_handler.handler


class TestFinalSourceValidation(_SourceFixture, _QAConversationFixture, unittest.TestCase):
    def setUp(self):
        _SourceFixture.setUp(self)
        self.set_up_transport(self.table)

    def prepare_answer(self, transport, during_model=None):
        self.doc()
        self.grant()
        model = self.replies(transport, 'get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'})
        replies = iter(model.side_effect)
        count = 0
        def invoke(**kwargs):
            nonlocal count
            count += 1
            if count == 2 and during_model:
                during_model()
            return next(replies)
        model.side_effect = invoke

    def assert_source_error(self, transport, result, code):
        if transport == 'rest':
            self.assertEqual(result['statusCode'], 503 if code == 'SOURCE_UNAVAILABLE' else 409)
            payload = json.loads(result['body'])
            self.assertEqual(payload['error']['code'], code)
            self.assertNotIn('PRIVATE_CHOICE', result['body'])
        else:
            self.assertEqual(result['status'], 'error')
            frames = [call.args[2] for call in handler._post_ws.call_args_list]
            self.assertFalse(any(frame['type'] == 'answer_complete' for frame in frames))
            self.assertEqual(frames[-1]['type'], 'answer_error')
            self.assertEqual(frames[-1]['code'], code)
            # Already emitted deltas cannot be recalled by the final guard.
            self.assertTrue(any(frame['type'] == 'answer_delta' for frame in frames))

    def test_changes_revocation_and_read_failure_during_final_model_call_block_persistence(self):
        for transport in ('rest', 'stream'):
            for change in ('edit', 'revoke', 'unavailable'):
                with self.subTest(transport=transport, change=change):
                    self.table.items.clear()
                    self.table.fail_key = None
                    handler._post_ws.reset_mock()
                    def mutate():
                        if change == 'edit':
                            self.table.items[('USER#owner', 'DOC#doc')]['content'] = 'changed'
                        elif change == 'revoke':
                            del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
                        else:
                            self.table.fail_key = ('USER#owner', 'DOC#doc')
                    self.prepare_answer(transport, mutate)
                    result = self.send(transport, 'read the document')
                    self.assert_source_error(transport, result,
                                             'SOURCE_UNAVAILABLE' if change == 'unavailable' else 'SOURCE_CHANGED')
                    self.assertNotIn(('SESSION#reader#chat-readonly', 'MESSAGES'), self.table.items)

    def test_changes_after_persistence_are_checked_again_before_final_publication(self):
        original = handler.save_session
        for transport in ('rest', 'stream'):
            with self.subTest(transport=transport):
                self.table.items.clear()
                handler._post_ws.reset_mock()
                self.prepare_answer(transport)
                def save_then_change(*args, **kwargs):
                    original(*args, **kwargs)
                    self.table.items[('USER#owner', 'DOC#doc')]['content'] = 'new source'
                with mock.patch.object(handler, 'save_session', side_effect=save_then_change):
                    self.assert_source_error(transport, self.send(transport, 'read'), 'SOURCE_CHANGED')
                self.assertEqual(handler.load_session('chat-readonly', user_id='reader'), [])

    def test_uri_is_hashed_in_actual_tool_call_log_and_preserved_for_execution(self):
        uri = 's3://knowledge/shared/고객-비공개/개인정보@example.com.md'
        self.replies('rest', 'get_legacy_text_detail', {'uri': uri})
        with mock.patch.object(handler, 'execute_tool', return_value=('synthetic', [])) as execute, \
                self.assertLogs(level='INFO') as logs:
            self.ask('rest', 'read the source')
        self.assertEqual(execute.call_args.args[1]['uri'], uri)
        self.assertNotIn(uri, '\n'.join(logs.output))
        self.assertNotIn('개인정보', '\n'.join(logs.output))
        self.assertIn('q#', '\n'.join(logs.output))
        self.assertEqual(redact_tool_input_for_log('get_legacy_text_detail', {'uri': [uri]})['uri'],
                         '<redacted non-string>')

    def test_meeting_rest_route_checks_sources_after_persistence(self):
        self.prepare_answer('rest')
        original = handler.save_session
        def persist(*args, **kwargs):
            original(*args, **kwargs)
            del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        with mock.patch.object(handler, '_request_meeting_context', return_value=(None, None, None)), \
                mock.patch.object(handler, 'save_session', side_effect=persist):
            result = handler.handle_meeting_ask('read', 'm', 'reader', 'chat-readonly')
        self.assert_source_error('rest', result, 'SOURCE_CHANGED')


class TestCompletionDelivery(unittest.TestCase):
    def setUp(self):
        self.client = get_session().create_client(
            'apigatewaymanagementapi', region_name='us-east-1',
            endpoint_url='https://synthetic.invalid', aws_access_key_id='synthetic',
            aws_secret_access_key='synthetic')
        self.addCleanup(self.client.close)
        self.event = {'question': 'synthetic', 'userId': 'reader', 'sessionId': 's',
                      'connectionId': 'c', 'endpoint': 'https://synthetic.invalid'}

    def invoke(self, final=None):
        with mock.patch.object(handler, '_apigw_client', return_value=self.client), \
                mock.patch.object(handler, '_request_meeting_context', return_value=(None, None, None)), \
                mock.patch.object(handler, 'load_session', return_value=[]), \
                mock.patch.object(handler, 'agentic_converse_stream', side_effect=final or (lambda **kw: None)) as model:
            if final is None:
                model.side_effect = None
                model.return_value = ('answer', [], [])
            return handler.handle_ask_stream(self.event)

    def test_public_titles_are_bounded_without_mutating_or_dropping_sources(self):
        details = []
        original = {'resourceId': 'source', 'uri': 's3://synthetic/source', 'title': '한😀' * 1000}
        for number in range(12):
            collect_detail(details, dict(original, resourceId=str(number)))
        self.assertEqual(len(details), 12)
        self.assertEqual(original['title'], '한😀' * 1000)
        self.assertTrue(all(len(row['title'].encode()) <= 1024 and row['titleTruncated'] is True
                            for row in details))
        collect_detail(details, dict(original, resourceId='0'))
        self.assertEqual(len(details), 12)

    def test_sdk_payload_rejection_sends_safe_error_and_returns_delivery_failure(self):
        frames = []
        self.client.meta.events.register('before-parameter-build.apigatewaymanagementapi.PostToConnection',
                                        lambda params, **kwargs: frames.append(json.loads(params['Data'])))
        with Stubber(self.client) as stub:
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            stub.add_client_error('post_to_connection', service_error_code='PayloadTooLargeException',
                                  service_message='sensitive upstream detail', http_status_code=413,
                                  expected_params={'ConnectionId': 'c', 'Data': ANY})
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            result = self.invoke()
            stub.assert_no_pending_responses()
        self.assertEqual(result, {'status': 'delivery_failed', 'code': 'RESPONSE_TOO_LARGE'})
        self.assertEqual(frames[-1]['type'], 'answer_error')
        self.assertEqual(frames[-1]['code'], 'RESPONSE_TOO_LARGE')
        self.assertNotIn('sensitive', json.dumps(frames[-1]))

    def test_large_source_array_is_delivered_in_bounded_frames_without_truncation(self):
        self.event['sourceFramesVersion'] = 1
        full, frames = [], []
        def final(*args, source_details, **kwargs):
            for number in range(100):
                collect_detail(source_details, {'resourceId': str(number), 'title': '한😀' * 1000,
                                               'uri': 's3://synthetic/' + str(number)})
            full.extend(copy.deepcopy(source_details))
            return 'answer', [], []
        with mock.patch.object(self.client, 'post_to_connection',
                               side_effect=lambda **params: frames.append(json.loads(params['Data']))):
            result = self.invoke(final)
        self.assertEqual(len(full), 100)
        self.assertEqual(result['status'], 'ok')
        self.assertEqual(frames[0]['type'], 'answer_start')
        self.assertEqual(frames[-1]['type'], 'answer_complete')
        self.assertEqual([detail for row in frames[1:-1] for detail in row['sourceDetails']], full)
        self.assertTrue(all(len(json.dumps(row, ensure_ascii=False).encode()) <= handler.WS_FRAME_BUDGET_BYTES
                            for row in frames))
        self.assertEqual(frames[-1]['sourceBatchCount'], len(frames) - 2)

    def test_failed_error_notification_does_not_recurse_or_report_success(self):
        with Stubber(self.client) as stub:
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            for _ in range(2):
                stub.add_client_error('post_to_connection', service_error_code='PayloadTooLargeException',
                                      http_status_code=413,
                                      expected_params={'ConnectionId': 'c', 'Data': ANY})
            self.assertEqual(self.invoke()['status'], 'delivery_failed')
            stub.assert_no_pending_responses()

    def test_gone_on_completion_is_not_ok(self):
        with Stubber(self.client) as stub:
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            stub.add_client_error('post_to_connection', service_error_code='GoneException', http_status_code=410,
                                  expected_params={'ConnectionId': 'c', 'Data': ANY})
            self.assertEqual(self.invoke()['status'], 'gone')
            stub.assert_no_pending_responses()

    def test_terminal_delivery_throttle_is_not_acknowledged_as_success(self):
        with Stubber(self.client) as stub:
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            stub.add_client_error('post_to_connection', service_error_code='LimitExceededException',
                                  http_status_code=429, expected_params={'ConnectionId': 'c', 'Data': ANY})
            stub.add_response('post_to_connection', {}, {'ConnectionId': 'c', 'Data': ANY})
            self.assertEqual(self.invoke(), {'status': 'delivery_failed', 'code': 'DELIVERY_FAILED'})
            stub.assert_no_pending_responses()

    def test_utf8_size_guard_rejects_before_sdk_and_heartbeat_rejection_propagates(self):
        with Stubber(self.client):
            with self.assertRaises(handler.WebSocketDeliveryError):
                handler._post_ws(self.client, 'c', {'type': 'answer_delta', 'text': '한' * 11000})
        with Stubber(self.client) as stub, \
                mock.patch.object(handler, 'execute_tool', return_value=('valid result', [])) as execute:
            stub.add_client_error('post_to_connection', service_error_code='PayloadTooLargeException',
                                  http_status_code=413, expected_params={'ConnectionId': 'c', 'Data': ANY})
            with self.assertRaises(handler.WebSocketDeliveryError):
                handler._execute_tool_with_heartbeat('read', {}, {}, self.client, 'c', 's')
            self.assertEqual(execute.call_count, 1)
            stub.assert_no_pending_responses()
