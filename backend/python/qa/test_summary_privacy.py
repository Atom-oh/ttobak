import hashlib
import json
import unittest

from summary_privacy import enforce_summary_guard, needs_summary_guard, project_meeting, safe_summary
from test_source_contract import _SourceFixture
from source_revision import resource_identity
from indexed_retrieval import hydrate_candidates


class SummaryPrivacyPolicyTests(unittest.TestCase):
    def test_account_member_guard_is_off_for_every_member_shape(self):
        for member in (None, {}, {'summaryPrivacyVersion': 1}, {'summaryPrivacyVersion': True},
                       {'addedAt': '2026-10-11T12:00:00Z'}, {'addedAt': 'not-a-date'}):
            self.assertFalse(enforce_summary_guard(member))

    def test_absence_is_not_membership_eligibility(self):
        cutoff = '2026-10-12T12:00:00Z'
        self.assertFalse(needs_summary_guard({'addedAt': '2026-10-11T12:00:00Z'}, cutoff))
        for member in (None, {}, {'addedAt': cutoff},
                       {'addedAt': '2026-10-11T12:00:00Z', 'summaryPrivacyVersion': 1},
                       {'addedAt': '2026-10-11T12:00:00Z', 'summaryPrivacyVersion': True},
                       {'addedAt': 'not-a-date'}):
            self.assertTrue(needs_summary_guard(member, cutoff))
        self.assertTrue(needs_summary_guard({'addedAt': '2026-10-11T12:00:00Z'}, ''))

    def test_clearance_binds_exact_content_and_raw_projection_keeps_notes(self):
        record = {'content': 'PRIVATE_PREDECESSOR_CANARY', 'actionItems': 'PRIVATE_TASK_CANARY',
                  'notes': 'raw user notes', 'transcriptA': 'raw speech', 'tags': ['PRIVATE_TAG'],
                  'summaryPrivacy': {'version': 1, 'state': 'safe', 'generationId': 'generation',
                                     'contentSha256': hashlib.sha256(b'old content').hexdigest()}}
        self.assertFalse(safe_summary(record))
        view = project_meeting(record, True)
        self.assertNotIn('content', view)
        self.assertNotIn('actionItems', view)
        self.assertNotIn('tags', view)
        self.assertEqual(view['notes'], 'raw user notes')
        self.assertEqual(view['transcriptA'], 'raw speech')
        self.assertIn('summaryReadRestriction', view)
        self.assertEqual(record['content'], 'PRIVATE_PREDECESSOR_CANARY')
        record['content'] = 'old content'
        self.assertTrue(safe_summary(record))

    def test_nanosecond_cutoff_matches_go_boundary_without_rounding(self):
        cutoff = '2026-10-12T12:00:00.000000002Z'
        self.assertFalse(needs_summary_guard({'addedAt': '2026-10-12T12:00:00.000000001Z'}, cutoff))
        for added in (cutoff, '2026-10-12T12:00:00.000000003Z'):
            self.assertTrue(needs_summary_guard({'addedAt': added}, cutoff))
        self.assertFalse(safe_summary(None))


class SummaryPrivacySourceTests(_SourceFixture, unittest.TestCase):
    def account_meeting(self):
        row = {
            'PK': 'USER#owner', 'SK': 'MEETING#meeting', 'meetingId': 'meeting', 'userId': 'owner',
            'accountId': 'account', 'sharedToAccount': True, 'title': 'parent title', 'status': 'done',
            'notes': 'raw note', 'transcriptA': 'raw transcript',
            'content': 'PRIVATE_PREDECESSOR_CANARY', 'actionItems': '[{"text":"PRIVATE_TASK_CANARY"}]',
        }
        self.table.put_item(Item=row)
        self.table.put_item(Item={'PK': 'ACCOUNT#account', 'SK': 'MEMBER#reader',
                                  'accountId': 'account', 'userId': 'reader', 'role': 'SA'})
        return self.table.items[('ACCOUNT#account', 'MEMBER#reader')]

    def test_stamped_membership_reads_published_summary_without_revision_change(self):
        # Account publication shares current derived content with every member
        # (ADR-029 source provenance, 2026-11-06 amendment); the privacy marker no longer projects.
        member = self.account_meeting()
        before = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        member['summaryPrivacyVersion'] = 1
        after = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        self.assertEqual(before['revision'], after['revision'])
        text = self.source_reader.meeting_text(after)
        for value in ('raw note', 'raw transcript', 'PRIVATE_PREDECESSOR_CANARY', 'PRIVATE_TASK_CANARY'):
            self.assertIn(value, text)
        self.assertNotIn('summaryReadRestriction', after['fields'])
        self.s3.get_object.assert_not_called()

    def test_vector_score_on_published_summary_hydrates_member_view(self):
        member = self.account_meeting()
        candidate = self.indexed(sk='MEETING#meeting', filename='meeting.md', text='PRIVATE_PREDECESSOR_CANARY')
        member['summaryPrivacyVersion'] = 1
        identity = resource_identity('USER#owner', 'MEETING#meeting')
        identities = {('USER#owner', 'MEETING#meeting'): identity}
        candidates = [{'uri': candidate['location']['s3Location']['uri'],
                       'score': 0.9, 'metadata': candidate['metadata']}]
        results = hydrate_candidates(self.source_reader, 'reader', 'PRIVATE_PREDECESSOR_CANARY',
                                     candidates, identities, 3)
        self.assertEqual(len(results), 1)
        self.assertNotIn('summaryReadRestriction', results[0]['meeting'])
        self.assertIn('PRIVATE_PREDECESSOR_CANARY', str(results))

    def test_kb_tool_shows_published_summary_fields_to_members(self):
        import tools
        member = self.account_meeting()
        candidate = self.indexed(sk='MEETING#meeting', filename='meeting.md', text='historical text')
        member['summaryPrivacyVersion'] = 1
        identity = resource_identity('USER#owner', 'MEETING#meeting')
        rows = [{'uri': candidate['location']['s3Location']['uri'],
                 'score': 0.9, 'metadata': candidate['metadata']}]
        results = hydrate_candidates(self.source_reader, 'reader', 'raw note', rows,
                                     {('USER#owner', 'MEETING#meeting'): identity}, 3)
        self.assertEqual(len(results), 1)
        text, sources = tools.execute_tool('search_knowledge_base', {'query': 'raw note'},
                                          {'retrieve_from_kb': lambda *a, **kw: results})
        snapshot = json.loads(text.split('\n')[-1])
        self.assertNotIn('summaryReadRestriction', snapshot)
        self.assertEqual(sources, [results[0]['uri']])
        self.assertEqual(snapshot['notes']['text'], 'raw note')
        self.assertIn('PRIVATE_PREDECESSOR_CANARY', snapshot['content']['text'])
        self.assertIn('PRIVATE_TASK_CANARY', snapshot['actionItems']['text'])
        self.assertNotIn('SHARING_REGEN_REQUIRED', text)
        self.s3.get_object.assert_not_called()
        self.runtime.retrieve.assert_not_called()

    def test_unrestricted_kb_empty_fields_keep_complete_empty_coverage(self):
        import tools
        result = {'uri': 'ttobak://source/synthetic', 'score': 0,
                  'meeting': {'meetingId': 'meeting', 'updatedAt': None, 'notes': 'raw note'},
                  'provenance': {'sourceRevision': 'a' * 64}}
        text = tools.format_kb_results([result])
        snapshot = json.loads(text.split('\n')[-1])
        self.assertNotIn('summaryReadRestriction', snapshot)
        for field in ('content', 'actionItems'):
            self.assertEqual(snapshot[field], {'text': '', 'includedCharacters': 0,
                                               'totalCharacters': 0, 'partial': False})
        self.assertEqual(snapshot['notes']['text'], 'raw note')
        self.assertNotIn('SHARING_REGEN_REQUIRED', text)


if __name__ == '__main__':
    unittest.main()
