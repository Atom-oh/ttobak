"""The shared tool context runs current readers and the existing tool executors."""
import importlib.util
import unittest
from unittest import mock

import test_handler as helpers
from test_source_contract import _SourceFixture
from source_access import SourceAccess
from attachment_context import AttachmentReader
from source_tools import execute_source_tool
from session_provenance import new_source_state, validate_sources, restore_sources
from request_history import remember_empty_search
from tool_history import ToolHistory
import tools


class TestToolContext(_SourceFixture, unittest.TestCase):
    def module(self):
        self.assertIsNotNone(importlib.util.find_spec('tool_context'), 'shared context is not implemented')
        import tool_context
        return tool_context

    def context(self, state, create=None):
        access = SourceAccess(
            self.source_reader, AttachmentReader(self.source_reader, helpers.handler._query_all),
            helpers.handler._list_shared_meetings, query_all=helpers.handler._query_all,
            provider=self.runtime, kb_id='test-kb')
        self.access = access
        return self.module().build_tool_context(
            'reader', 'live text', state, [], source_access=access, history=ToolHistory('reader', {}),
            create_research=create or (lambda *args: {'researchId': 'created'}),
            check_research_limit=lambda user: True, check_web_search_limit=lambda user: True)

    def test_successful_empty_search_is_tracked_but_failed_search_is_not(self):
        state = new_source_state()
        context = self.context(state)
        text, _ = tools.execute_tool('search_knowledge_base', {'query': 'NEW_TERM'}, context)
        self.module().track_tool_history(state, 'search_knowledge_base', context)
        self.assertNotIn('Tool error', text)
        self.assertTrue(state['replayable'])
        self.assertTrue(any('emptySearch' in dep for dep in state['dependencies']))
        self.runtime.retrieve.side_effect = RuntimeError('synthetic read failure')
        failed = new_source_state()
        context = self.context(failed)
        text, _ = tools.execute_tool('search_knowledge_base', {'query': 'NEW_TERM'}, context)
        self.module().track_tool_history(failed, 'search_knowledge_base', context)
        self.assertIn('Tool error', text)
        self.assertFalse(failed['replayable'])
        self.assertFalse(failed['dependencies'])

    def test_private_callbacks_pin_current_user_before_any_source_read(self):
        state = new_source_state()
        context = self.context(state)
        before = len(self.table.reads)
        with self.assertRaises(ValueError):
            context['load_document_context']('owner', 'USER#owner', 'doc')
        self.assertEqual(len(self.table.reads), before)
        self.assertFalse(state['replayable'])

    def test_skipped_private_callback_cannot_reuse_earlier_recorded_source(self):
        self.doc(content='PRIVATE')
        self.grant()
        state = new_source_state()
        context = self.context(state)
        context['sourceReadRecorded'] = False
        text, _ = execute_source_tool('get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'}, context)
        self.module().track_tool_history(state, 'get_document_detail', context)
        self.assertIn('PRIVATE', text)
        self.assertTrue(state['replayable'])
        context['sourceReadRecorded'] = False
        execute_source_tool('get_document_detail', {'offset': -1}, context)
        self.module().track_tool_history(state, 'get_document_detail', context)
        self.assertFalse(state['replayable'])

    def test_creation_is_invoked_once_and_only_successful_receipt_is_recorded(self):
        create = mock.Mock(return_value={'researchId': 'created'})
        state = new_source_state()
        context = self.context(state, create)
        text, _ = tools.execute_tool('start_research', {'topic': '연구' * 3000, 'mode': 'invalid'}, context)
        self.assertIn('created', text)
        self.assertNotIn('Tool error', text)
        create.assert_called_once()
        self.assertEqual(state['dependencies'][0]['researchReceipt']['mode'], 'standard')
        with self.assertRaises(ValueError):
            context['create_research']('owner', 'topic', 'quick')
        self.assertEqual(create.call_count, 1)

    def test_ninth_empty_tool_search_keeps_result_and_eight_cumulative_proofs(self):
        state = new_source_state()
        context = self.context(state)
        for index in range(9):
            context['sourceReadRecorded'] = False
            text, sources = tools.execute_tool('search_knowledge_base', {'query': f'query {index}'}, context)
            self.module().track_tool_history(state, 'search_knowledge_base', context)
            self.assertNotIn('Tool error', text)
            self.assertEqual(sources, [])
            self.assertEqual(len(state['dependencies']), min(index + 1, 8))
            validate_sources(state, lambda dep: self.access._source_is_current('reader', dep))
        self.assertFalse(state['replayable'])
        self.assertNotIn('query 8', [dep['emptySearch']['query'] for dep in state['dependencies']])
        self.assertEqual(state['toolHistoryCoverage'], [
            {'tool': 'search_knowledge_base', 'complete': False, 'reason': 'DEPENDENCY_LIMIT'}])

    def test_restore_does_not_partially_merge_over_budget_empty_proofs(self):
        saved = new_source_state()
        for index in range(8):
            remember_empty_search(saved, 'reader', f'old {index}', 5)
        current = new_source_state()
        remember_empty_search(current, 'reader', 'current', 5)
        before = list(current['dependencies'])
        self.assertFalse(restore_sources({
            'sourceProvenanceVersion': 1, 'sourceReplayable': True,
            'sourceDependencies': saved['dependencies'],
        }, current, lambda dep: True))
        self.assertEqual(current['dependencies'], before)
        self.assertTrue(current['replayable'])
