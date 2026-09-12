"""Current legacy text evidence and continuation, with no live AWS calls."""
import io
import json
import unittest

from test_source_contract import _SourceFixture
from indexed_retrieval import hydrate_candidates
from source_tools import execute_source_tool, format_source_results
from source_access import SourceAccess
from attachment_context import AttachmentReader
import test_handler as helpers
from session_provenance import new_source_state


class LegacyTextTests(_SourceFixture, unittest.TestCase):
    uri = 's3://knowledge/kb/owner/reference.md'

    def source(self, text):
        body = text.encode()
        self.s3.head_object.return_value = {'ETag': '"one"', 'VersionId': 'v1', 'ContentLength': len(body)}
        self.s3.get_object.side_effect = lambda **kwargs: {
            'ETag': '"one"', 'VersionId': 'v1', 'Body': io.BytesIO(body),
        }

    def access(self):
        return SourceAccess(self.source_reader, AttachmentReader(self.source_reader, helpers.handler._query_all),
                            helpers.handler._list_shared_meetings)

    def hit(self, text, score=.9):
        return {'uri': self.uri, 'score': score, '_provider': {'content': {'text': text}}}

    def test_matching_fact_after_the_file_head_reaches_model_tool_text(self):
        fact = 'FINAL_APPROVED_BUDGET is 42, not 84.'
        self.source('introduction ' * 900 + fact + '\n' + 'appendix ' * 100)
        result = hydrate_candidates(self.source_reader, 'owner', 'FINAL_APPROVED_BUDGET',
                                    [self.hit(fact)], {}, 5)
        rendered = format_source_results(result)
        self.assertIn(fact, rendered)
        self.assertGreater(result[0]['coverage']['startCharacter'], 8000)
        self.assertTrue(result[0]['provenance']['matchedIndexedText'])
        self.assertIn('get_legacy_text_detail', rendered)

    def test_duplicate_uri_uses_the_best_current_matching_chunk(self):
        fact = 'CURRENT_TARGET_FACT'
        self.source('intro\n' + 'padding ' * 1500 + fact)
        result = hydrate_candidates(self.source_reader, 'owner', 'TARGET_FACT',
                                    [self.hit('intro', .5), self.hit(fact, .95)], {}, 5)
        self.assertEqual(len(result), 1)
        self.assertIn(fact, format_source_results(result))
        self.assertEqual(result[0]['score'], .95)

    def test_stale_indexed_chunk_is_never_replayed_as_current_text(self):
        self.source('padding ' * 1200 + 'CURRENT_TARGET_FACT')
        result = hydrate_candidates(self.source_reader, 'owner', 'CURRENT_TARGET_FACT',
                                    [self.hit('OLD_PRIVATE_FACT')], {}, 5)
        self.assertNotIn('OLD_PRIVATE_FACT', json.dumps(result))
        self.assertIn('CURRENT_TARGET_FACT', format_source_results(result))
        self.assertFalse(result[0]['provenance']['matchedIndexedText'])

    def test_continuation_reaches_suffix_and_changed_source_restarts(self):
        self.source('a' * 7000 + 'DOCUMENT_END')
        state, details = new_source_state(), []
        access = self.access()
        context = {'user_id': 'owner', 'load_legacy_text':
                   lambda uid, uri, offset, revision: access.load_legacy_text(
                       uid, uri, offset, revision, source_state=state, source_details=details)}
        first, _ = execute_source_tool('get_legacy_text_detail', {'uri': self.uri}, context)
        page = json.loads(first.split('\n', 1)[1])
        second, _ = execute_source_tool('get_legacy_text_detail', {
            'uri': self.uri, 'offset': page['nextOffset'], 'sourceRevision': page['sourceRevision']}, context)
        self.assertIn('DOCUMENT_END', second)
        self.assertEqual(details[0]['resourceKind'], 'legacyText')
        self.assertEqual(state['dependencies'][0]['legacyURI'], self.uri)
        self.s3.head_object.return_value['VersionId'] = 'v2'
        self.s3.get_object.reset_mock()
        with self.assertRaises(ValueError):
            access.load_legacy_text('owner', self.uri, page['nextOffset'], page['sourceRevision'])
        self.s3.get_object.assert_not_called()

    def test_foreign_uri_and_invalid_continuation_never_read_s3(self):
        access = self.access()
        self.assertIsNone(access.load_legacy_text('reader', self.uri))
        self.s3.head_object.assert_not_called()
        for offset, revision in [(True, None), (-1, None), (1, None), (1, 'bad')]:
            with self.subTest(offset=offset, revision=revision), self.assertRaises(ValueError):
                access.load_legacy_text('owner', self.uri, offset, revision)
        self.s3.head_object.assert_not_called()
