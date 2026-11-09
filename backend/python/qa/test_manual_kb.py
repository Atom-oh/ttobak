"""Manual KB consumer contract; worker migration must be tested independently."""
import hashlib
import unittest

import manual_kb
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


class TestManualKBPresentationSnapshots(unittest.TestCase):
    """PPTX snapshots are Go-generated Markdown; legacy PPT stays an explicit failure."""

    def _metadata(self, key):
        revision = manual_kb.manual_revision('knowledge', key, '"etag"', 'v1', 10)
        resource = hashlib.sha256(key.encode()).hexdigest()
        run = '00000000-0000-4000-8000-000000000000'
        return revision, resource, run, {
            'indexSchema': 'manual-kb-v1', 'resourceKind': 'manualKbDocument', 'resourceId': resource,
            'ownerId': 'owner', 'sourceBucket': 'knowledge', 'sourceKey': key, 'sourceETag': '"etag"',
            'sourceVersionId': 'v1', 'sourceSize': 10, 'sourceRevision': revision, 'indexRunId': run,
            'sourceProjection': 'pptx-markdown-v1'}

    def test_snapshot_names(self):
        for key, name in (('kb/owner/1_deck.pptx', 'document.md'), ('kb/owner/deck.PPTX', 'document.md'),
                          ('kb/owner/a.pdf', 'document.pdf'), ('kb/owner/a.xlsx', 'document.xlsx')):
            self.assertEqual(manual_kb.snapshot_document_name(key), name)

    def test_pptx_identity_accepts_only_markdown_projection(self):
        key = 'kb/owner/1_deck.pptx'
        revision, resource, run, metadata = self._metadata(key)
        base = f's3://knowledge/manual-kb/v1/owner/{resource}/{revision}/{run}/'
        identity = manual_kb.snapshot_identity(base + 'document.md', metadata, 'knowledge', 'owner')
        self.assertEqual(identity and identity['revision'], revision)
        self.assertIsNone(manual_kb.snapshot_identity(base + 'document.pptx', metadata, 'knowledge', 'owner'))
        self.assertIsNone(manual_kb.snapshot_identity(base + 'document.md', metadata, 'knowledge', 'other'))

    def test_legacy_ppt_reason_is_precise(self):
        reader = type('Reader', (), {'kb_bucket': 'knowledge'})()
        for key, status, reason in (('kb/owner/old.ppt', 'failed', 'LEGACY_PPT_UNSUPPORTED'),
                                    ('kb/owner/new.pptx', 'pending', 'VERIFIED_SNAPSHOT_UNAVAILABLE')):
            current = {'key': key, 'object': {'size': 10}, 'revision': 'r', 'shared': False, 'resourceId': 'id'}
            result = manual_kb._result(reader, 'owner', current, 's3://knowledge/' + key, 0)
            self.assertEqual((result['manualFile']['status'], result['manualFile']['reason']), (status, reason))
