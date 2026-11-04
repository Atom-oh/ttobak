"""Owner disclosure permits only the current canonical Account summary."""
import copy
import hashlib
import unittest
from unittest import mock

import summary_privacy
from session_provenance import new_source_state
import test_account_read_access as descendants
import test_account_reads as account_fixtures
import test_summary_privacy as source_fixtures
from test_source_contract import _SourceFixture
from tool_history import HistoryLimit, ToolHistory

handler = account_fixtures.helpers.handler


def approval(record):
    privacy = record.get('summaryPrivacy') or {}
    return {'version': 1, 'ownerUserId': record['userId'], 'accountId': record['accountId'],
            'summaryGeneration': privacy.get('generationId', ''),
            'contentSha256': hashlib.sha256(record['content'].encode('utf-8')).hexdigest(),
            'approvedAt': '2026-11-04T09:30:00.123456789Z'}


class AccountApprovalPolicyTests(unittest.TestCase):
    def shareable(self, record):
        # The fallback makes the pre-implementation failure a behavior failure.
        return getattr(summary_privacy, 'account_summary_shareable', summary_privacy.safe_summary)(record)

    def record(self, privacy=None):
        record = {'userId': 'owner', 'accountId': 'account', 'sharedToAccount': True,
                  'content': '현재 완료된 회의록'}
        if privacy is not None:
            record['summaryPrivacy'] = privacy
        record['summaryAccountApproval'] = approval(record)
        return record

    def test_approval_allows_private_and_legacy_metadata_without_certifying_provenance(self):
        for privacy in (None, {'version': 1, 'state': 'private', 'generationId': 'current'},
                        {'version': 1, 'state': 'unknown', 'generationId': 'current'}):
            with self.subTest(privacy=privacy):
                record = self.record(privacy)
                self.assertTrue(self.shareable(record))
                self.assertFalse(summary_privacy.safe_summary(record))
                del record['content']
                self.assertTrue(self.shareable(record))

    def test_approval_rejects_wrong_owner_audience_generation_and_full_bytes(self):
        original = self.record({'version': 1, 'state': 'private', 'generationId': 'current'})
        changes = ({'sharedToAccount': False}, {'sharedToAccount': 1}, {'accountId': 'other'},
                   {'accountId': ''}, {'userId': 'other'}, {'userId': ''},
                   {'content': 'replacement'}, {'content': None},
                   {'summaryPrivacy': {'generationId': 'new'}}, {'summaryPrivacy': None},
                   {'summaryPrivacy': []})
        for change in changes:
            with self.subTest(change=change):
                record = copy.deepcopy(original)
                record.update(change)
                self.assertFalse(self.shareable(record))

    def test_approval_rejects_malformed_stamp_fields(self):
        changes = ({'version': True}, {'version': '1'}, {'version': 2},
                   {'ownerUserId': ''}, {'accountId': ''}, {'summaryGeneration': None},
                   {'contentSha256': 'A' * 64}, {'contentSha256': '0' * 63},
                   {'approvedAt': '2026-11-04T09:30:00+00:00'},
                   {'approvedAt': '2026-02-30T09:30:00Z'}, {'approvedAt': True})
        for change in changes:
            with self.subTest(change=change):
                record = self.record()
                record['summaryAccountApproval'].update(change)
                self.assertFalse(self.shareable(record))
        for stamp in (None, [], 'approval'):
            record = self.record()
            record['summaryAccountApproval'] = stamp
            self.assertFalse(self.shareable(record))


class AccountApprovalSourceTests(_SourceFixture, unittest.TestCase):
    account_meeting = source_fixtures.SummaryPrivacySourceTests.account_meeting

    def approved_meeting(self):
        member = self.account_meeting()
        member['summaryPrivacyVersion'] = 1
        record = self.table.items[('USER#owner', 'MEETING#meeting')]
        record['summaryAccountApproval'] = approval(record)
        return record

    def test_protected_reader_gets_approved_current_bytes_and_revision_binds_stamp(self):
        record = self.approved_meeting()
        before = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        self.assertEqual(before['fields'].get('content'), 'PRIVATE_PREDECESSOR_CANARY')
        self.assertNotIn('summaryReadRestriction', before['fields'])
        self.assertNotIn('summaryAccountApproval', before['fields'])
        record['summaryAccountApproval']['approvedAt'] = '2026-11-04T09:30:01Z'
        after = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        self.assertNotEqual(before['revision'], after['revision'])
        self.assertEqual(before['indexRevision'], after['indexRevision'])

    def test_content_change_after_approval_withholds_derivation(self):
        record = self.approved_meeting()
        record['content'] = 'replacement'
        snapshot = self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting')
        self.assertNotIn('content', snapshot['fields'])
        self.assertNotIn('actionItems', snapshot['fields'])
        self.assertEqual(snapshot['fields']['notes'], 'raw note')
        self.assertEqual(snapshot['fields']['summaryReadRestriction'], 'SHARING_REGEN_REQUIRED')

    def test_stamp_rotation_between_metadata_and_body_rejects_snapshot(self):
        self.approved_meeting()
        original_get = self.table.get_item

        def rotate_after_metadata(Key, **request):
            result = original_get(Key=Key, **request)
            if Key == {'PK': 'USER#owner', 'SK': 'MEETING#meeting'} and request.get('ProjectionExpression'):
                self.table.items[('USER#owner', 'MEETING#meeting')]['summaryAccountApproval'][
                    'approvedAt'] = '2026-11-04T09:30:01Z'
            return result

        self.table.get_item = rotate_after_metadata
        self.assertIsNone(self.source_reader.read('reader', 'USER#owner', 'MEETING#meeting'))

    def test_approval_does_not_grant_unrelated_email_recipient(self):
        self.approved_meeting()
        self.assertIsNone(self.source_reader.read('email-recipient', 'USER#owner', 'MEETING#meeting'))


class AccountApprovalInsightTests(unittest.TestCase):
    setUp = account_fixtures.TestStrictAccountReads.setUp
    account = account_fixtures.TestStrictAccountReads.account
    meeting = account_fixtures.TestStrictAccountReads.meeting
    insight = account_fixtures.TestStrictAccountReads.insight
    reader = account_fixtures.TestStrictAccountReads.reader

    def approved_insight(self):
        self.account()
        self.meeting(summaryPrivacy={'version': 1, 'state': 'private', 'generationId': 'current'},
                     insightsRevision='insights')
        record = self.table.items[('USER#owner', 'MEETING#m')]
        record['summaryAccountApproval'] = approval(record)
        self.insight(summaryGeneration='current', insightsRevision='insights')
        self.table.items[('ACCOUNT#a', 'MEMBER#reader')]['summaryPrivacyVersion'] = 1
        return record

    def test_approved_private_insight_and_brief_keep_existing_derivatives(self):
        self.approved_insight()
        reader = self.reader()
        result = reader.get_account_insights('reader', 'Account').value
        self.assertEqual([row['text'] for row in result['insights']], ['PRIVATE i'])
        self.assertNotIn('summaryReadRestriction', result)
        self.assertEqual(reader.get_account_brief('reader', 'Account').value[
            'insightsByType']['risk'][0]['text'], 'PRIVATE i')

    def test_rotating_approval_after_insight_body_rejects_complete_read(self):
        record = self.approved_insight()

        def rotate(key, request):
            if key['SK'].startswith('INSIGHT#') and 'text' in request.get('ExpressionAttributeNames', {}).values():
                record['summaryAccountApproval']['approvedAt'] = '2026-11-04T09:30:01Z'

        self.table.after_read = rotate
        with self.assertRaises(PermissionError):
            self.reader().get_account_insights('reader', 'Account')

    def test_readonly_history_rejects_new_approval_with_identical_insight_text(self):
        record = self.approved_insight()
        reader = self.reader()
        history = ToolHistory('reader', {'get_account_insights': reader.get_account_insights})
        state = new_source_state()
        history.read(state, 'get_account_insights', {'account': 'Account'})
        dependency = state['dependencies'][0]
        self.assertTrue(history.is_current(dependency))
        record['summaryAccountApproval']['approvedAt'] = '2026-11-04T09:30:01Z'
        self.assertFalse(history.is_current(dependency))

    def test_approval_fingerprints_do_not_turn_valid_large_reads_into_history_errors(self):
        self.approved_insight()
        history = ToolHistory('reader', {'get_account_insights': self.reader().get_account_insights})
        state = new_source_state()
        with mock.patch('tool_history.MAX_RESULT_BYTES', 64):
            try:
                result = history.read(state, 'get_account_insights', {'account': 'Account'})
            except HistoryLimit:
                self.fail('Approval bookkeeping prevented a valid current read')
        self.assertEqual([row['text'] for row in result['insights']], ['PRIVATE i'])
        self.assertFalse(state['replayable'])

    def legacy_approval(self):
        self.account()
        self.meeting(insightsRevision='disclosure-revision')
        record = self.table.items[('USER#owner', 'MEETING#m')]
        record['summaryAccountApproval'] = approval(record)
        self.table.items[('ACCOUNT#a', 'MEMBER#reader')]['summaryPrivacyVersion'] = 1
        return record

    def test_legacy_approval_accepts_only_insights_matching_current_disclosure_revision(self):
        self.legacy_approval()
        self.insight('old')
        self.insight('stale', summaryGeneration='', insightsRevision='old-revision')
        self.insight('current', summaryGeneration='', insightsRevision='disclosure-revision')
        result = self.reader().get_account_insights('reader', 'Account').value
        self.assertEqual([row['text'] for row in result['insights']], ['PRIVATE current'])
        bodies = [key[1] for key, request in self.table.reads
                  if key[1].startswith('INSIGHT#') and 'text' in request[
                      'ExpressionAttributeNames'].values()]
        self.assertEqual(bodies, ['INSIGHT#2026-09-12T12:00:00Z#m#2'])

    def test_failed_legacy_insight_refresh_withholds_old_account_rows(self):
        self.legacy_approval()
        self.insight('old')
        result = self.reader().get_account_brief('reader', 'Account').value
        self.assertEqual(result['insightsByType'], {})
        self.assertFalse(any(key[1].startswith('INSIGHT#') for key, _ in self.table.reads))

    def test_legacy_revision_rotation_after_current_body_rejects_complete_read(self):
        record = self.legacy_approval()
        self.insight('current', summaryGeneration='', insightsRevision='disclosure-revision')

        def rotate(key, request):
            if key['SK'].startswith('INSIGHT#') and 'text' in request.get('ExpressionAttributeNames', {}).values():
                record['insightsRevision'] = 'replacement-revision'

        self.table.after_read = rotate
        with self.assertRaises(PermissionError):
            self.reader().get_account_insights('reader', 'Account')

    def test_nonempty_generation_without_canonical_insight_revision_stays_unreadable(self):
        record = self.approved_insight()
        record.pop('insightsRevision')
        for row in self.table.items.values():
            if row['SK'].startswith('INSIGHT#'):
                row.pop('insightsRevision')
        result = self.reader().get_account_insights('reader', 'Account').value
        self.assertEqual(result['insights'], [])
        self.assertFalse(any(key[1].startswith('INSIGHT#') for key, _ in self.table.reads))


class AccountApprovalInheritedTests(unittest.TestCase):
    setUp = descendants.DescendantReadsTests.setUp
    put = descendants.DescendantReadsTests.put
    account = descendants.DescendantReadsTests.account
    member = descendants.DescendantReadsTests.member
    meeting = descendants.DescendantReadsTests.meeting
    document = descendants.DescendantReadsTests.document
    insight = account_fixtures.TestStrictAccountReads.insight

    def test_inherited_approval_keeps_completed_notes_and_current_source_fence(self):
        record = self.table.items[('USER#owner', 'MEETING#m')]
        record['summaryAccountApproval'] = approval(record)
        access = handler._source_access()
        state = new_source_state()
        text, error = access.load_meeting_context('reader', 'm', source_state=state)
        self.assertIsNone(error)
        self.assertIn('Unsafe generated context', text)
        dependency = next(dep for dep in state['dependencies'] if 'attachmentId' not in dep)
        self.assertTrue(access._source_is_current('reader', dependency))
        record['summaryAccountApproval']['approvedAt'] = '2026-11-04T09:30:01Z'
        self.assertFalse(access._source_is_current('reader', dependency))
        record['accountId'] = 'child'
        snapshot = handler._source_reader().read('reader', 'USER#owner', 'MEETING#m')
        self.assertEqual(snapshot['fields']['summaryReadRestriction'], 'SHARING_REGEN_REQUIRED')

    def test_inherited_legacy_insights_require_current_disclosure_revision(self):
        from account_reads import StrictAccountReader
        record = self.table.items[('USER#owner', 'MEETING#m')]
        record['summaryAccountApproval'] = approval(record)
        record['insightsRevision'] = 'disclosure-revision'
        self.insight('old', account='grandchild')
        self.insight('current', account='grandchild', summaryGeneration='',
                     insightsRevision='disclosure-revision')
        result = StrictAccountReader(self.table).get_account_insights('reader', 'grandchild').value
        self.assertEqual([row['text'] for row in result['insights']], ['PRIVATE current'])
        self.assertEqual(result['inheritedFromAccountId'], 'root')
        record['summaryAccountApproval']['accountId'] = 'root'
        restricted = StrictAccountReader(self.table).get_account_insights('reader', 'grandchild').value
        self.assertEqual(restricted['insights'], [])
        self.assertEqual(restricted['summaryReadRestriction'], 'SHARING_REGEN_REQUIRED')
