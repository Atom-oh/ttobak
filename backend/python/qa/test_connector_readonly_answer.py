import json
import unittest
from unittest import mock

import test_handler
from session_provenance import SourceValidationError

handler = test_handler.handler


class ReadonlyAnswerTests(unittest.TestCase):
    def setUp(self):
        self.model = mock.Mock()
        self.model.converse.return_value = {
            'stopReason': 'end_turn', 'output': {'message': {'content': [{'text': 'synthetic answer'}]}}}
        self.stack = [
            mock.patch.object(handler, '_CONNECTOR_MODEL', self.model),
            mock.patch.object(handler, '_request_meeting_context', return_value=('source', 'notes', None)),
            mock.patch.object(handler, '_validate_answer_sources'),
            mock.patch.object(handler, 'load_session', side_effect=AssertionError('session read')),
            mock.patch.object(handler, 'save_session', side_effect=AssertionError('session write')),
            mock.patch.object(handler, 'agentic_converse', side_effect=AssertionError('tool loop')),
        ]
        self.patched = [patch.start() for patch in self.stack]
        for patch in self.stack:
            self.addCleanup(patch.stop)

    def test_single_read_only_model_call_revalidates_sources(self):
        result = handler._connector_readonly_answer({'question': 'q', 'meetingId': 'meeting'}, 'verified')
        self.assertEqual(result['statusCode'], 200)
        self.assertTrue(json.loads(result['body'])['readOnly'])
        self.assertEqual(self.patched[2].call_count, 2)
        self.assertNotIn('toolConfig', self.model.converse.call_args.kwargs)
        self.patched[1].assert_called_once()
        self.assertEqual(self.patched[1].call_args.args[:2], ('verified', 'meeting'))

    def test_revoked_source_never_releases_answer(self):
        self.patched[2].side_effect = [None, SourceValidationError()]
        with self.assertRaises(SourceValidationError):
            handler._connector_readonly_answer({'question': 'q', 'meetingId': 'meeting'}, 'verified')

    def test_incomplete_and_oversized_source_are_not_success(self):
        self.model.converse.return_value['stopReason'] = 'max_tokens'
        self.assertEqual(handler._connector_readonly_answer({'question': 'q'}, 'verified')['statusCode'], 502)
        self.model.reset_mock()
        self.patched[1].return_value = ('x' * 24_001, '', None)
        self.assertEqual(handler._connector_readonly_answer({'question': 'q'}, 'verified')['statusCode'], 413)
        self.model.converse.assert_not_called()

    def test_http_body_cannot_select_private_protocol_and_empty_config_denies(self):
        with mock.patch.object(handler, '_private_connector') as private, \
                mock.patch.object(handler._HTTP_ORIGIN, 'verify', return_value=False):
            result = handler.lambda_handler({
                'rawPath': '/api/qa/ask', 'body': '{"protocol":"ttobak.connector.invoke.v1"}'}, None)
            self.assertEqual(result['statusCode'], 403)
            private.assert_not_called()
        with mock.patch.dict(handler.os.environ, {'CONNECTOR_SIGNING_KEY_ARN': ''}):
            result = handler.lambda_handler({'protocol': 'ttobak.connector.invoke.v1'}, None)
            self.assertEqual(result['statusCode'], 503)


if __name__ == '__main__':
    unittest.main()
