"""Exact binary selection must preserve authorization and current-source proof."""
import unittest

from botocore.exceptions import ClientError
from test_kb_fixtures import _KBFixture, _SharedKBFixture, binary_fixture, handler
from request_history import remember_empty_search, request_is_current, valid_request_dependency
from session_provenance import new_source_state
from web_search import redact_tool_input_for_log


class TestNamedKnowledge(_KBFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        hit = self.snapshot_fixture('CURRENT_V1')
        hit['score'] = 0.49
        self.snapshots.append(hit)
        original = self.old_hit
        self.old_hit = lambda: dict(original(), score=0.4)

    def test_exact_selection_reads_low_scoring_current_snapshot_without_lowering_global_threshold(self):
        access = handler._source_access()
        self.assertEqual(access.retrieve_from_kb('opaque filename', user_id='owner'), [])
        rows = access.retrieve_from_kb('opaque filename', user_id='owner', source_keys=[self.key])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]['manualFile']['status'], 'ready')
        self.assertIn('CURRENT_V1', rows[0]['text'])
        self.assertNotIn('OLD_UNBOUND', rows[0]['text'])
        self.assertLess(rows[0]['score'], 0.5)

    def test_named_source_still_rejects_old_snapshot_bytes(self):
        self.body, self.version = binary_fixture('.pdf', 'CURRENT_V2'), 'version-2'
        rows = handler._source_access().retrieve_from_kb('file', user_id='owner', source_keys=[self.key])
        self.assertEqual(len(rows), 1)
        self.assertTrue(rows[0]['provenance']['filePending'])
        self.assertNotIn('CURRENT_V1', rows[0].get('text', ''))

    def test_foreign_malformed_and_oversized_selection_precedes_aws_reads(self):
        access = handler._source_access()
        for keys in (['kb/other/file.pdf'], ['shared/../file.pdf'], [],
                     'kb/owner/file.pdf', [self.key] * 6,
                     ['kb/owner/' + 'x' * 1024 + '.pdf']):
            with self.subTest(keys_type=type(keys).__name__):
                self.s3.reset_mock()
                self.runtime.reset_mock()
                with self.assertRaises(ValueError):
                    access.retrieve_from_kb('query', user_id='owner', source_keys=keys)
                self.s3.head_object.assert_not_called()
                self.runtime.retrieve.assert_not_called()

    def test_tool_callback_preserves_selection_and_logs_no_filename(self):
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        text, sources = handler.execute_tool('search_knowledge_base',
            {'query': 'file', 'source_keys': [self.key]}, context)
        self.assertIn('CURRENT_V1', text)
        self.assertEqual(len(sources), 1)
        self.assertTrue(context['sourceReadRecorded'])
        self.assertEqual(redact_tool_input_for_log('search_knowledge_base',
                         {'source_keys': [self.key]})['source_keys'], '<redacted non-string>')

    def test_explicit_null_selection_never_falls_back_to_global_search(self):
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        self.s3.reset_mock()
        self.runtime.reset_mock()
        _, sources = handler.execute_tool('search_knowledge_base',
            {'query': 'file', 'source_keys': None}, context)
        self.assertEqual(sources, [])
        self.s3.head_object.assert_not_called()
        self.runtime.retrieve.assert_not_called()

    def test_provider_failure_is_not_recorded_as_successful_empty_search(self):
        self.runtime.retrieve.side_effect = RuntimeError('synthetic outage')
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        with self.assertRaises(RuntimeError):
            context['retrieve_from_kb']('file', source_keys=[self.key])
        self.assertFalse(state['replayable'])
        self.assertFalse(any('emptySearch' in d for d in state['dependencies']))

    def test_missing_selection_has_replayable_scoped_empty_proof(self):
        self.body = None
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        self.assertEqual(context['retrieve_from_kb']('file', source_keys=[self.key]), [])
        self.assertTrue(state['replayable'])
        self.assertEqual(state['dependencies'][0]['emptySearch']['sourceKeys'], [self.key])

    def test_mixed_results_revalidate_the_missing_selected_file(self):
        missing = 'kb/owner/absent.pdf'
        appeared = [False]
        def head(**kwargs):
            if kwargs['Key'] == missing:
                if not appeared[0]:
                    raise ClientError({'Error': {'Code': '404'}}, 'HeadObject')
                return {'ETag': '"new"', 'VersionId': 'new-version', 'ContentLength': 100}
            return self.head(**kwargs)
        self.s3.head_object.side_effect = head
        state, details = new_source_state(), []
        context = handler._agent_context('owner', None, None, state, details)
        rows = context['retrieve_from_kb']('file', source_keys=[self.key, missing])
        self.assertEqual(len(rows), 1)
        empty = next(dep for dep in state['dependencies'] if 'emptySearch' in dep)
        self.assertEqual(empty['emptySearch']['sourceKeys'], [missing])
        access = handler._source_access()
        self.assertTrue(request_is_current('owner', empty, access.retrieve_from_kb))
        appeared[0] = True
        self.assertFalse(request_is_current('owner', empty, access.retrieve_from_kb))


class TestNamedSharedKnowledge(_SharedKBFixture, unittest.TestCase):
    def test_authenticated_shared_selection_keeps_current_visibility(self):
        hit = self.shared_snapshot()
        hit['score'] = 0.01
        self.snapshots.append(hit)
        rows = handler._source_access().retrieve_from_kb('file', user_id='reader', source_keys=[self.key])
        self.assertIn('CURRENT_V1', rows[0]['text'])
        self.assertEqual(rows[0]['provenance']['visibility'], 'authenticated-shared')
        self.assertNotIn('ownerId', rows[0]['provenance'])


class TestNamedEmptySearch(unittest.TestCase):
    def test_empty_receipt_revalidates_the_same_selection(self):
        state = new_source_state()
        keys = ['kb/owner/missing.pdf']
        self.assertTrue(remember_empty_search(state, 'owner', 'file', 5, source_keys=keys))
        dependency = state['dependencies'][0]
        self.assertTrue(valid_request_dependency(dependency))
        calls = []
        def search(query, count, **kwargs):
            calls.append(kwargs)
            return []
        self.assertTrue(request_is_current('owner', dependency, search))
        self.assertEqual(calls, [{'user_id': 'owner', 'source_keys': keys}])
        self.assertFalse(request_is_current('owner', dependency, lambda *a, **k: [{'pending': True}]))
