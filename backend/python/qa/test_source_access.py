"""Current source access and tool contracts before public handler activation."""
import json
from pathlib import Path
import unittest
from unittest import mock

import test_handler as helpers
from test_source_contract import _SourceFixture
from test_attachment_context import _AttachmentFixture
from test_kb_fixtures import _KBFixture
from source_context import SourceReader
from attachment_context import AttachmentReader
from source_access import SourceAccess
from source_tools import execute_source_tool, format_source_results, SOURCE_TOOL_DEFINITIONS
from session_provenance import new_source_state
from manual_kb import binary_revision


def access():
    reader = SourceReader(helpers.handler.table, helpers.handler.s3_client, 'assets', 'knowledge',
                          helpers.handler._has_meeting_access)
    return SourceAccess(reader, AttachmentReader(reader, helpers.handler._query_all),
                        helpers.handler._list_shared_meetings)


class TestDocumentAccess(_SourceFixture, unittest.TestCase):
    def test_search_refreshes_after_a_miss_and_tracks_current_shared_content(self):
        current = SourceAccess(self.source_reader, AttachmentReader(self.source_reader, helpers.handler._query_all),
                               helpers.handler._list_shared_meetings, query_all=helpers.handler._query_all,
                               provider=self.runtime, kb_id='test-kb')
        self.assertEqual(current.retrieve_from_kb('NEW_TERM', user_id='reader'), [])
        row = self.doc(content='NEW_TERM')
        self.grant()
        found = current.retrieve_from_kb('NEW_TERM', user_id='reader')
        self.assertEqual(found[0]['document']['content'], 'NEW_TERM')
        row['content'] = 'EDITED_TERM'
        edited = current.retrieve_from_kb('EDITED_TERM', user_id='reader')
        self.assertEqual(edited[0]['document']['content'], 'EDITED_TERM')
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        self.assertEqual(current.retrieve_from_kb('EDITED_TERM', user_id='reader'), [])

    def test_document_grant_is_required_and_public_details_exclude_raw_fields(self):
        self.doc(content='CURRENT_NOTE', publicShareToken='NEVER_RETURN_THIS', notes='PRIVATE_EXTRA')
        state, details = new_source_state(), []
        text, error = access().load_document_context('reader', 'USER#owner', 'doc',
                                                     source_state=state, source_details=details)
        self.assertIsNone(text)
        self.assertEqual(error['status'], 404)
        self.assertEqual(details, [])
        self.grant()
        text, error = access().load_document_context('reader', 'USER#owner', 'doc',
                                                     source_state=state, source_details=details)
        self.assertIsNone(error)
        self.assertIn('CURRENT_NOTE', text)
        self.assertEqual(set(details[0]), {'resourceKind', 'resourceId', 'sourcePK', 'sourceSK',
                                          'uri', 'title', 'sourceRevision', 'contentSource'})
        self.assertNotIn('NEVER_RETURN_THIS', json.dumps(details))
        self.assertTrue(access()._source_is_current('reader', state['dependencies'][0]))
        del self.table.items[('USER#reader', 'SHAREDDOC#doc')]
        self.assertFalse(access()._source_is_current('reader', state['dependencies'][0]))

    def test_live_context_keeps_saved_notes_but_cannot_be_replayed(self):
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#m', 'userId': 'owner',
                                 'meetingId': 'm', 'notes': 'SAVED_CORRECTION', 'transcriptA': 'OLD_TRANSCRIPT'})
        state, details = new_source_state(), []
        transcript, notes, error = access()._request_meeting_context(
            'owner', 'm', 'LIVE_TRANSCRIPT', source_state=state, source_details=details)
        self.assertIsNone(error)
        self.assertEqual(transcript, 'LIVE_TRANSCRIPT')
        self.assertEqual(notes, 'SAVED_CORRECTION')
        self.assertFalse(state['replayable'])
        self.assertEqual(details[0]['resourceKind'], 'meeting')

    def test_optional_attachment_failure_preserves_valid_meeting_text(self):
        self.table.put_item(Item={'PK': 'USER#owner', 'SK': 'MEETING#m', 'userId': 'owner',
                                 'meetingId': 'm', 'notes': 'SAVED_NOTE', 'transcriptA': 'TRANSCRIPT'})
        current = access()
        current.attachments = mock.Mock()
        current.attachments.overview.side_effect = RuntimeError('synthetic unavailable attachment')
        state = new_source_state()
        text, error = current.load_meeting_context('owner', 'm', source_state=state)
        self.assertIsNone(error)
        self.assertIn('SAVED_NOTE', text)
        self.assertIn('ATTACHMENT_CONTEXT_UNAVAILABLE', text)
        self.assertFalse(state['replayable'])

    def test_document_tool_reads_multiple_pages_without_dropping_the_suffix(self):
        self.doc(content='a' * 7000 + 'DOCUMENT_END')
        context = {'user_id': 'owner', 'load_document_context': access().load_document_context}
        first, _ = execute_source_tool('get_document_detail', {'sourcePK': 'USER#owner', 'docId': 'doc'}, context)
        page = json.loads(first.split('\n', 1)[1])
        self.assertTrue(page['partial'])
        self.assertNotIn('DOCUMENT_END', page['text'])
        second, _ = execute_source_tool('get_document_detail', {
            'sourcePK': 'USER#owner', 'docId': 'doc', 'offset': page['nextOffset']}, context)
        self.assertIn('DOCUMENT_END', second)
        invalid, _ = execute_source_tool('get_document_detail', {
            'sourcePK': 'USER#owner', 'docId': 'doc', 'offset': True}, context)
        self.assertIn('정수', invalid)

    def test_document_tool_cannot_read_without_authenticated_context(self):
        self.doc(content='PRIVATE_NOTE')
        text, _ = execute_source_tool('get_document_detail', {
            'sourcePK': 'USER#owner', 'docId': 'doc'}, {})
        self.assertNotIn('PRIVATE_NOTE', text)
        self.assertEqual(self.table.reads, [])
        self.assertEqual({tool['toolSpec']['name'] for tool in SOURCE_TOOL_DEFINITIONS},
                         {'get_document_detail', 'get_meeting_attachments', 'get_attachment_text'})


class TestAttachmentAccess(_AttachmentFixture, unittest.TestCase):
    def test_retained_result_reports_current_failure_and_document_locations(self):
        self.state.update(status='failed', runId='retry-run', errorCode='TIMEOUT')
        state, details = new_source_state(), []
        current = access()
        context = {'user_id': 'reader', 'load_attachment_text':
                   lambda uid, mid, aid, unit, text, revision: current.load_attachment_text(
                       uid, mid, aid, unit, text, revision, source_state=state, source_details=details)}
        result, _ = execute_source_tool('get_attachment_text', {'meetingId': 'm', 'attachmentId': 'a'}, context)
        self.assertIn('현재 파일 사실', result)
        self.assertIn('"usingPreviousResult": true', result)
        self.assertIn('TIMEOUT', result)
        self.assertEqual(details[0]['locations'], [{'kind': 'page', 'page': 3}])
        dependency = state['dependencies'][0]
        self.assertTrue(current._source_is_current('reader', dependency))
        self.source_etag = '"replacement"'
        self.assertFalse(current._source_is_current('reader', dependency))

    def test_formatter_keeps_extraction_failure_distinct_from_empty_search(self):
        result = {'uri': 's3://knowledge/kb/owner/file.pdf', 'score': .9,
                  'manualFile': {'fileName': 'file.pdf', 'status': 'pending',
                                 'reason': 'VERIFIED_SNAPSHOT_UNAVAILABLE'},
                  'provenance': {'resourceKind': 'manualKbDocument', 'filePending': True}}
        text = format_source_results([result])
        self.assertIn('file.pdf', text)
        self.assertIn('VERIFIED_SNAPSHOT_UNAVAILABLE', text)
        self.assertIn('pending', text)
        self.assertNotIn('관련 문서를 찾지 못했습니다', text)


class TestManualSourceAccess(_KBFixture, unittest.TestCase):
    def test_private_and_shared_revisions_match_producer_vectors(self):
        path = Path(__file__).parent / 'testdata/knowledge-revisions.json'
        for vector in json.loads(path.read_text()):
            with self.subTest(source=vector['sourceKey']):
                actual = binary_revision(vector['indexSchema'], vector['sourceBucket'], vector['sourceKey'],
                                         vector['sourceETag'], vector['sourceVersionId'], vector['sourceSize'])
                self.assertEqual(actual, vector['sourceRevision'])

    def test_persisted_binary_dependency_requires_current_original_bytes(self):
        hit = self.snapshot_fixture('CURRENT_V1')
        dependency = {'manualKey': self.key, 'sourceRevision': hit['metadata']['sourceRevision']}
        current = access()
        self.assertTrue(current._source_is_current('owner', dependency))
        self.assertFalse(current._source_is_current('reader', dependency))
        self.version = 'version-2'
        self.assertFalse(current._source_is_current('owner', dependency))
        self.body = None
        self.assertFalse(current._source_is_current('owner', dependency))
