import json
import unittest
from unittest import mock

import test_handler
from test_attachment_context import _AttachmentFixture
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

    def test_attachment_serialization_counts_toward_combined_source_budget(self):
        def source(*args, source_state, **kwargs):
            source_state['attachmentContext'] = {'text': '한' * 8000}
            return '', '', None
        self.patched[1].side_effect = source
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


class ReadonlyAttachmentAnswerTests(_AttachmentFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.model = mock.Mock()
        self.model.converse.return_value = {
            'stopReason': 'end_turn', 'output': {'message': {'content': [{'text': 'answer'}]}}}
        self.result['units'][0]['text'] = 'ATTACHMENT_ONLY_FACT_739'
        self.result['metrics']['textBytes'] = len('ATTACHMENT_ONLY_FACT_739')
        for patch in (
            mock.patch.object(handler, '_CONNECTOR_MODEL', self.model),
            mock.patch.object(handler, 'save_session', side_effect=AssertionError('session write')),
            mock.patch.object(handler, 'agentic_converse', side_effect=AssertionError('tool loop')),
        ):
            patch.start()
            self.addCleanup(patch.stop)

    def ask(self):
        return handler._connector_readonly_answer({'question': '첨부문서의 사실은?', 'meetingId': 'm'}, 'reader')

    def test_real_attachment_content_supports_returned_page_citation(self):
        result = self.ask()
        self.assertEqual(result['statusCode'], 200)
        content = json.loads(result['body'])
        citations = [detail for detail in content['sourceDetails']
                     if detail.get('contentSource') == 'verified_attachment_text']
        self.assertEqual(len(citations), 1)
        self.assertIn({'kind': 'page', 'page': 3}, citations[0]['locations'])
        prompt = json.dumps(self.model.converse.call_args.kwargs, ensure_ascii=False)
        self.assertIn('ATTACHMENT_ONLY_FACT_739', prompt)
        self.assertNotIn('get_attachment_text', prompt)
        self.assertNotIn('toolConfig', self.model.converse.call_args.kwargs)
        self.assertGreater(self.s3.get_object.call_count, 0)

    def test_real_attachment_revision_change_during_model_discards_answer(self):
        for field in ('source_etag', 'resultKey'):
            with self.subTest(field=field):
                self.source_etag = '"source"'
                self.state['resultKey'] = 'files/editor/m/text/a/run-1.json'
                def change(**kwargs):
                    if field == 'source_etag':
                        self.source_etag = '"changed"'
                    else:
                        self.state['resultKey'] = 'files/editor/m/text/a/run-2.json'
                    return {'stopReason': 'end_turn', 'output': {'message': {'content': [{'text': 'stale'}]}}}
                self.model.converse.side_effect = change
                with self.assertRaises(SourceValidationError):
                    self.ask()

    def test_partial_retained_and_unavailable_status_reaches_model(self):
        self.result.update(status='partial', complete=False)
        self.state.update(status='failed', runId='new-run', errorCode='TIMEOUT')
        self.assertEqual(self.ask()['statusCode'], 200)
        prompt = json.dumps(self.model.converse.call_args.kwargs, ensure_ascii=False)
        self.assertIn('ATTACHMENT_ONLY_FACT_739', prompt)
        self.assertIn('usingPreviousResult', prompt)
        self.assertIn('partial', prompt)
        self.state.update(resultKey='', sourceETag='')
        self.assertEqual(self.ask()['statusCode'], 200)
        blocks = self.model.converse.call_args.kwargs['messages'][0]['content']
        evidence = '\n'.join(block['text'] for block in blocks)
        self.assertNotIn('ATTACHMENT_ONLY_FACT_739', evidence)
        self.assertIn('"available": false', evidence)


if __name__ == '__main__':
    unittest.main()
