"""Manual KB consumer contract; worker migration must be tested independently."""
import hashlib
import io
import json
from pathlib import Path
import unittest
from unittest import mock
import zipfile

from botocore.exceptions import ClientError
import test_handler as helpers
from test_kb_fixtures import _KBFixture, binary_fixture

handler = helpers.handler


class TestManualKB(_KBFixture, unittest.TestCase):

    def test_existing_pdf_and_docx_remain_visible_pending_then_answerable_from_new_snapshot(self):
        import tools
        for extension in ('.pdf', '.docx'):
            with self.subTest(extension=extension):
                self.key = 'kb/owner/existing' + extension
                self.body, self.version, self.snapshots = binary_fixture(extension, 'CURRENT_V1'), 'version-1', []
                pending = handler.retrieve_from_kb('document facts', user_id='owner')
                self.assertEqual(len(pending), 1, 'existing binary disappeared')
                self.assertEqual(pending[0]['manualFile']['status'], 'pending')
                self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', json.dumps(pending))
                self.assertIn('마이그레이션', tools.format_kb_results(pending))
                self.snapshots.append(self.snapshot_fixture('CURRENT_V1'))
                ready = handler.retrieve_from_kb('document facts', user_id='owner')
                self.assertEqual(len(ready), 1)
                self.assertEqual(ready[0]['manualFile']['status'], 'ready')
                self.assertIn('CURRENT_V1', tools.format_kb_results(ready))
                self.assertNotIn('OLD_UNBOUND_PRIVATE_CHUNK', json.dumps(ready))
                self.body, self.version = binary_fixture(extension, 'CURRENT_V2'), 'version-2'
                changed = handler.retrieve_from_kb('document facts', user_id='owner')
                self.assertEqual(changed[0]['manualFile']['status'], 'pending')
                self.assertNotIn('CURRENT_V1', json.dumps(changed))
                self.snapshots.append(self.snapshot_fixture('CURRENT_V2', '00000000-0000-4000-8000-000000000002'))
                current = handler.retrieve_from_kb('document facts', user_id='owner')
                self.assertIn('CURRENT_V2', tools.format_kb_results(current))
                self.body = None
                self.assertEqual(handler.retrieve_from_kb('document facts', user_id='owner'), [])

    def test_new_metadata_on_old_uri_never_proves_old_chunk_current(self):
        forged = self.snapshot_fixture('CURRENT_V1')
        forged['location'] = self.old_hit()['location']
        forged['content']['text'] = 'FORGED_OLD_CHUNK'
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [forged]}
        results = handler.retrieve_from_kb('query', user_id='owner')
        self.assertEqual(results[0]['manualFile']['status'], 'pending')
        self.assertNotIn('FORGED_OLD_CHUNK', json.dumps(results))

    def test_foreign_owner_and_bucket_never_reach_source_head(self):
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [self.old_hit()]}
        self.assertEqual(handler.retrieve_from_kb('query', user_id='reader'), [])
        self.s3.head_object.assert_not_called()
        hit = self.old_hit()
        hit['location']['s3Location']['uri'] = 's3://foreign/' + self.key
        self.runtime.retrieve.side_effect = lambda **kwargs: {'retrievalResults': [hit]}
        self.assertEqual(handler.retrieve_from_kb('query', user_id='owner'), [])
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
        results = handler.retrieve_from_kb('query', user_id='owner')
        self.assertEqual(results[0]['manualFile']['status'], 'ready')
        self.assertEqual(results[0]['text'], 'CURRENT_V1')

    def test_manual_dependencies_invalidate_sessions_on_overwrite_and_pending_is_not_replayed(self):
        context_state, details = handler.new_source_state(), []
        context = handler._agent_context('owner', None, None, context_state, details)
        context['retrieve_from_kb']('query')
        messages = [{'role': 'user', 'content': [{'text': 'question'}]},
                    {'role': 'assistant', 'content': [{'text': 'pending notice'}]}]
        handler.save_session('manual', messages, user_id='owner', source_state=context_state)
        self.assertEqual(handler.load_session('manual', user_id='owner'), [])
        self.snapshots.append(self.snapshot_fixture('CURRENT_V1'))
        context_state, details = handler.new_source_state(), []
        context = handler._agent_context('owner', None, None, context_state, details)
        context['retrieve_from_kb']('query')
        messages[-1]['content'][0]['text'] = 'CURRENT_V1 derived answer'
        handler.save_session('manual', messages, user_id='owner', source_state=context_state)
        self.assertEqual(handler.load_session('manual', user_id='owner'), messages)
        self.body, self.version = binary_fixture('.pdf', 'CURRENT_V2'), 'version-2'
        self.assertEqual(handler.load_session('manual', user_id='owner'), [])

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
                self.assertEqual(handler.retrieve_from_kb('query', user_id='owner'), [])
                self.s3.head_object.assert_not_called()
        for suffix in ('?versionId=x', '#fragment', '/..'):
            broken = copy.deepcopy(ready)
            broken['location']['s3Location']['uri'] += suffix
            self.runtime.retrieve.side_effect = lambda **kw: {'retrievalResults': [broken]}
            self.s3.head_object.reset_mock()
            self.assertEqual(handler.retrieve_from_kb('query', user_id='owner'), [])
            self.s3.head_object.assert_not_called()
