"""Private/shared binary contracts against the current SourceAccess implementation."""
import copy
import json
import unittest
from botocore.exceptions import ClientError

import test_handler as helpers
from test_kb_fixtures import _KBFixture, _SharedKBFixture, binary_fixture
from source_context import SourceReader
from attachment_context import AttachmentReader
from source_access import SourceAccess
from source_tools import format_source_results


class _Consumer:
    def current_search(self, question, number_of_results=5, user_id=None):
        reader = SourceReader(self.table, self.s3, 'assets', 'knowledge', helpers.handler._has_meeting_access)
        access = SourceAccess(reader, AttachmentReader(reader, helpers.handler._query_all), lambda user: [],
                              query_all=helpers.handler._query_all, provider=self.runtime, kb_id='test-kb')
        return access.retrieve_from_kb(question, number_of_results, user_id)


class TestManualConsumer(_Consumer, _KBFixture, unittest.TestCase):

    def test_existing_pdf_and_docx_remain_visible_pending_then_answerable_from_new_snapshot(self):
        for extension in ('.pdf', '.docx'):
            with self.subTest(extension=extension):
                self.key = 'kb/owner/existing' + extension
                self.body, self.version, self.snapshots = binary_fixture(extension, 'CURRENT_V1'), 'version-1', []
                pending = self.current_search('document facts', user_id='owner')
                self.assertEqual(len(pending), 1, 'existing binary disappeared')
                self.assertEqual(pending[0]['manualFile']['status'], 'pending')
                self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', json.dumps(pending))
                self.assertIn('마이그레이션', format_source_results(pending))
                self.snapshots.append(self.snapshot_fixture('CURRENT_V1'))
                ready = self.current_search('document facts', user_id='owner')
                self.assertEqual(len(ready), 1)
                self.assertEqual(ready[0]['manualFile']['status'], 'ready')
                self.assertIn('CURRENT_V1', format_source_results(ready))
                self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', json.dumps(ready))
                self.body, self.version = binary_fixture(extension, 'CURRENT_V2'), 'version-2'
                changed = self.current_search('document facts', user_id='owner')
                self.assertEqual(changed[0]['manualFile']['status'], 'pending')
                self.assertNotIn('CURRENT_V1', json.dumps(changed))
                self.snapshots.append(self.snapshot_fixture('CURRENT_V2', '00000000-0000-4000-8000-000000000002'))
                current = self.current_search('document facts', user_id='owner')
                self.assertIn('CURRENT_V2', format_source_results(current))
                self.body = None
                self.assertEqual(self.current_search('document facts', user_id='owner'), [])

    def test_new_metadata_on_old_uri_never_proves_old_chunk_current(self):
        forged = self.snapshot_fixture('CURRENT_V1')
        forged['location'] = self.old_hit()['location']
        forged['content']['text'] = 'FORGED_OLD_CHUNK'
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [forged]}
        results = self.current_search('query', user_id='owner')
        self.assertEqual(results[0]['manualFile']['status'], 'pending')
        self.assertNotIn('FORGED_OLD_CHUNK', json.dumps(results))

    def test_foreign_owner_and_bucket_never_reach_source_head(self):
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [self.old_hit()]}
        self.assertEqual(self.current_search('query', user_id='reader'), [])
        self.s3.head_object.assert_not_called()
        hit = self.old_hit()
        hit['location']['s3Location']['uri'] = 's3://foreign/' + self.key
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [hit]}
        self.assertEqual(self.current_search('query', user_id='owner'), [])
        self.s3.head_object.assert_not_called()

    def test_ready_snapshot_is_found_when_old_hits_occupy_broad_search_results(self):
        ready = self.snapshot_fixture('CURRENT_V1')

        def retrieve(**kwargs):
            source_filter = kwargs['retrievalConfiguration']['vectorSearchConfiguration']['filter']
            encoded = json.dumps(source_filter)
            if '"sourceRevision"' in encoded and '"resourceId"' in encoded:
                self.assertIn(ready['metadata']['sourceRevision'], encoded)
                self.assertIn('"ownerId"', encoded)
                return {'retrievalResults': [ready]}
            return {'retrievalResults': [self.old_hit()]}
        self.runtime.retrieve.side_effect = retrieve
        results = self.current_search('query', user_id='owner')
        self.assertEqual(results[0]['manualFile']['status'], 'ready')
        self.assertEqual(results[0]['text'], 'CURRENT_V1')

    def test_invalid_snapshot_metadata_or_uri_is_not_byte_proof(self):
        import copy
        ready = self.snapshot_fixture('CURRENT_V1')
        for field, value in (
            ('ownerId', 'other'), ('sourceBucket', 'other'),
            ('sourceKey', 'kb/other/private.pdf'), ('sourceETag', '"wrong"'),
            ('sourceVersionId', 'wrong'), ('sourceSize', True), ('sourceSize', 1.5),
            ('sourceSize', 10 ** 1000), ('sourceRevision', '0' * 64),
            ('resourceId', '0' * 64), ('indexRunId', 'not-a-run'),
        ):
            with self.subTest(field=field, value=str(value)[:30]):
                broken = copy.deepcopy(ready)
                broken['metadata'][field] = value
                self.runtime.retrieve.side_effect = lambda **kw: {'retrievalResults': [broken]}
                self.s3.head_object.reset_mock()
                self.assertEqual(self.current_search('query', user_id='owner'), [])
                self.s3.head_object.assert_not_called()
        for suffix in ('?versionId=x', '#fragment', '/..'):
            broken = copy.deepcopy(ready)
            broken['location']['s3Location']['uri'] += suffix
            self.runtime.retrieve.side_effect = lambda **kw: {'retrievalResults': [broken]}
            self.s3.head_object.reset_mock()
            self.assertEqual(self.current_search('query', user_id='owner'), [])
            self.s3.head_object.assert_not_called()


class TestSharedConsumer(_Consumer, _SharedKBFixture, unittest.TestCase):

    def test_shared_is_not_anonymous_and_cannot_relabel_a_private_source(self):
        self.snapshots.append(self.shared_snapshot())
        self.s3.head_object.reset_mock()
        with self.assertRaises(ValueError):
            self.current_search('facts', user_id=None)
        self.s3.head_object.assert_not_called()
        self.key = 'kb/owner/private.pdf'
        forged = self.shared_snapshot()
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [forged]}
        self.s3.head_object.reset_mock()
        self.assertEqual(self.current_search('facts', user_id='reader'), [])
        self.s3.head_object.assert_not_called()

    def test_shared_visibility_and_uri_are_required_not_just_a_new_tag(self):
        original = self.shared_snapshot()
        for field, value in (('visibility', 'public'), ('visibility', None), ('ownerId', 'reader'),
                             ('sourceBucket', 'other'), ('resourceKind', 'manualKbDocument'),
                             ('indexSchema', 'manual-kb-v1'), ('sourceVersionId', 'old'),
                             ('sourceSize', True), ('resourceId', '0' * 64)):
            with self.subTest(field=field):
                hit = copy.deepcopy(original)
                hit['metadata'][field] = value
                self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [hit]}
                self.s3.head_object.reset_mock()
                self.assertEqual(self.current_search('facts', user_id='reader'), [])
                self.s3.head_object.assert_not_called()
        hit = copy.deepcopy(original)
        hit['location'] = self.old_hit()['location']
        hit['content']['text'] = 'FORGED_OLD_SHARED_TEXT'
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [hit]}
        result = self.current_search('facts', user_id='reader')
        self.assertEqual(result[0]['manualFile']['status'], 'pending')
        self.assertNotIn('FORGED_OLD_SHARED_TEXT', json.dumps(result))

    def test_mixed_private_and_shared_results_keep_private_owner_isolation(self):
        self.key, self.body = 'kb/owner/private.pdf', binary_fixture('.pdf', 'PRIVATE_FACT')
        private = self.snapshot_fixture('PRIVATE_FACT')
        private_head = self.head(Bucket='knowledge', Key=self.key)
        self.key, self.body = 'shared/reference/file.pdf', binary_fixture('.pdf', 'SHARED_FACT')
        shared = self.shared_snapshot('SHARED_FACT')
        shared_head = self.head(Bucket='knowledge', Key=self.key)
        objects = {'kb/owner/private.pdf': private_head, self.key: shared_head}

        def head(**kwargs):
            self.assertEqual(kwargs['Bucket'], 'knowledge')
            return objects[kwargs['Key']]
        self.s3.head_object.side_effect = head
        # Exercise the authorization boundary even if the provider ignores filters.
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [private, shared]}
        owner = self.current_search('facts', user_id='owner')
        self.assertEqual({result['provenance']['resourceKind'] for result in owner},
                         {'manualKbDocument', 'sharedKbDocument'})
        self.s3.head_object.reset_mock()
        other = self.current_search('facts', user_id='reader')
        self.assertEqual(len(other), 1)
        self.assertEqual(other[0]['provenance']['resourceKind'], 'sharedKbDocument')
        self.assertNotIn('PRIVATE_FACT', json.dumps(other))
        self.assertTrue(all(call.kwargs['Key'].startswith('shared/')
                            for call in self.s3.head_object.call_args_list))

    def test_access_denied_is_unavailable_not_deleted_or_stale_fallback(self):
        import tools
        self.snapshots.append(self.shared_snapshot())
        self.assertEqual(self.current_search('facts', user_id='reader')[0]['manualFile']['status'], 'ready')
        denied = ClientError({'Error': {'Code': 'AccessDenied', 'Message': 'synthetic denial'},
                              'ResponseMetadata': {'HTTPStatusCode': 403}}, 'HeadObject')
        self.s3.head_object.side_effect = denied
        with self.assertRaises(ClientError):
            self.current_search('facts', user_id='reader')
        text, sources = tools.execute_tool('search_knowledge_base', {'query': 'facts'}, {
            'retrieve_from_kb': lambda q, n: self.current_search(q, n, user_id='reader'),
        })
        self.assertIn('Tool error:', text)
        self.assertNotIn('CURRENT_V1', text)
        self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', text)
        self.assertNotIn('관련 문서를 찾지 못했습니다', text)
        self.assertEqual(sources, [])
