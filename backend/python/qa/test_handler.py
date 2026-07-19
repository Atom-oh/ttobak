"""Unit tests for handler.py's _list_shared_meetings origin/membership gate.

Verifies the fix for the PR #114 review MAJORs: an account-origin share row
must not grant access once the caller is no longer an account member, even
if RemoveMember's best-effort cleanup never deleted the row -- and that
membership revocation is visible immediately (checked per-call, uncached),
not bounded by the raw share-list's SHARED_MEETINGS_CACHE_TTL_SECONDS.
"""

import os
import sys
import time
import unittest
from unittest import mock

os.environ.setdefault('TABLE_NAME', 'test-table')
os.environ.setdefault('AWS_DEFAULT_REGION', 'us-east-1')

sys.path.insert(0, os.path.dirname(__file__))

import handler  # noqa: E402


class TestListSharedMeetings(unittest.TestCase):
    def setUp(self):
        # Each test gets a clean cache -- module-level dicts persist across
        # tests otherwise (mirrors the real warm-Lambda cache behavior this
        # code is designed for, but would make tests order-dependent).
        handler._shared_meetings_cache.clear()
        handler._shared_meetings_cache_expiry.clear()

    @mock.patch.object(handler, 'table')
    def test_direct_share_included_unconditionally(self, mock_table):
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],  # no origin => direct
        }
        result = handler._list_shared_meetings('reader-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])
        mock_table.get_item.assert_not_called()

    @mock.patch.object(handler, 'table')
    def test_account_share_included_when_still_member(self, mock_table):
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1', 'origin': 'account'}],
        }

        def get_item(Key, **kwargs):
            if Key['SK'].startswith('MEETING#'):
                return {'Item': {'accountId': 'acc-1', 'sharedToAccount': True}}
            if Key['SK'].startswith('MEMBER#'):
                return {'Item': {'role': 'TAM'}}
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])
        member_check_call = [c for c in mock_table.get_item.call_args_list if c.kwargs['Key']['SK'].startswith('MEMBER#')][0]
        self.assertTrue(member_check_call.kwargs.get('ConsistentRead'), "membership check must use ConsistentRead -- an eventual read could return stale removed=False")

    @mock.patch.object(handler, 'table')
    def test_account_share_excluded_when_membership_removed(self, mock_table):
        # The exact permanent-access gap this fix closes: the Share row is
        # still present (RemoveMember's cleanup delete never ran), but the
        # AccountMember row is gone.
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1', 'origin': 'account'}],
        }

        def get_item(Key, **kwargs):
            if Key['SK'].startswith('MEETING#'):
                return {'Item': {'accountId': 'acc-1', 'sharedToAccount': True}}
            if Key['SK'].startswith('MEMBER#'):
                return {}  # membership removed
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('removed-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_account_share_excluded_when_unshared_from_account(self, mock_table):
        # Mirrors the Go backend's resolveSharedAccess predicate: a meeting
        # the owner un-shared from the account (or that was only ever
        # Link-only), sharedToAccount=False, must not leak here even with a
        # lingering account-origin Share row and valid membership.
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1', 'origin': 'account'}],
        }

        def get_item(Key, **kwargs):
            if Key['SK'].startswith('MEETING#'):
                return {'Item': {'accountId': 'acc-1', 'sharedToAccount': False}}
            if Key['SK'].startswith('MEMBER#'):
                return {'Item': {'role': 'TAM'}}
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_membership_revocation_seen_immediately_despite_warm_raw_cache(self, mock_table):
        # The exact MAJOR this round's fix closes: _list_shared_meetings_raw's
        # cache (query + per-item meeting GetItem) stays warm across calls,
        # but membership itself is re-checked on every _list_shared_meetings
        # call with no cache of its own -- so revocation is visible on the
        # very next call, NOT bounded by SHARED_MEETINGS_CACHE_TTL_SECONDS.
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1', 'origin': 'account'}],
        }
        is_still_member = {'value': True}

        def get_item(Key, **kwargs):
            if Key['SK'].startswith('MEETING#'):
                return {'Item': {'accountId': 'acc-1', 'sharedToAccount': True}}
            if Key['SK'].startswith('MEMBER#'):
                return {'Item': {'role': 'TAM'}} if is_still_member['value'] else {}
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])
        self.assertEqual(mock_table.query.call_count, 1)

        # Membership revoked -- raw cache is still warm (not expired), yet
        # the very next call must reflect the revocation immediately.
        is_still_member['value'] = False
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [])
        self.assertEqual(mock_table.query.call_count, 1, "raw share-list cache should still be warm -- no re-query needed")

    @mock.patch.object(handler, 'table')
    def test_raw_share_list_cache_expires_and_requeries(self, mock_table):
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],  # direct share
        }
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 1)
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 1, "second call within TTL should hit the raw cache, not re-query")

        handler._shared_meetings_cache_expiry['reader-1'] = time.time() - 1
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 2, "expired cache should trigger a fresh query")


if __name__ == '__main__':
    unittest.main()
