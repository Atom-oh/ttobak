"""Manual KB consumer contract; worker migration must be tested independently."""
import unittest

import test_handler as helpers
from test_kb_fixtures import _KBFixture, binary_fixture

handler = helpers.handler


class TestManualKB(_KBFixture, unittest.TestCase):


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
