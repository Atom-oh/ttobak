import hashlib
import unittest

from summary_privacy import needs_summary_guard, project_meeting, safe_summary
from test_source_contract import _SourceFixture
from source_revision import resource_identity
from indexed_retrieval import hydrate_candidates


class SummaryPrivacyPolicyTests(unittest.TestCase):
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

    def test_new_membership_projects_raw_source_and_invalidates_prior_revision(self):
        member = self.account_meeting()
        before = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        member['summaryPrivacyVersion'] = 1
        self.table.reads.clear()
        after = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        self.assertNotEqual(before['revision'], after['revision'])
        text = self.source_reader.meeting_text(after)
        self.assertIn('raw note', text)
        self.assertIn('raw transcript', text)
        self.assertNotIn('PRIVATE_PREDECESSOR_CANARY', text)
        self.assertNotIn('PRIVATE_TASK_CANARY', text)
        self.assertIn('summaryReadRestriction', after['fields'])
        for key, request in self.table.reads:
            if key == ('USER#owner', 'MEETING#meeting'):
                names = request.get('ExpressionAttributeNames', {})
                fields = {names.get(v.strip(), v.strip()) for v in request.get('ProjectionExpression', '').split(',')}
                self.assertTrue(request.get('ProjectionExpression'), 'restricted source used a whole-item read')
                self.assertFalse({'content', 'actionItems', 'insights', 'tags'} & fields)
        self.s3.get_object.assert_not_called()

    def test_vector_score_on_withheld_text_cannot_reintroduce_identity(self):
        member = self.account_meeting()
        candidate = self.indexed(sk='MEETING#meeting', filename='meeting.md', text='PRIVATE_PREDECESSOR_CANARY')
        member['summaryPrivacyVersion'] = 1
        identity = resource_identity('USER#owner', 'MEETING#meeting')
        identities = {('USER#owner', 'MEETING#meeting'): identity}
        candidates = [{'uri': candidate['location']['s3Location']['uri'],
                       'score': 0.9, 'metadata': candidate['metadata']}]
        self.assertEqual(hydrate_candidates(self.source_reader, 'reader', 'PRIVATE_PREDECESSOR_CANARY',
                                            candidates, identities, 3), [])
        raw = hydrate_candidates(self.source_reader, 'reader', 'raw note', candidates, identities, 3)
        self.assertEqual(len(raw), 1)
        self.assertIn('summaryReadRestriction', raw[0]['meeting'])
        self.assertNotIn('PRIVATE_PREDECESSOR_CANARY', str(raw))


if __name__ == '__main__':
    unittest.main()
