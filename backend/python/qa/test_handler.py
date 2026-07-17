"""Unit tests for handler.py's _list_shared_meetings origin/membership gate.

Verifies the fix for the PR #114 review MAJOR: an account-origin share row
must not grant access once the caller is no longer an account member, even
if RemoveMember's best-effort cleanup never deleted the row.
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
        handler._account_member_cache.clear()
        handler._account_member_cache_expiry.clear()

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
                return {'Item': {'accountId': 'acc-1'}}
            if Key['SK'].startswith('MEMBER#'):
                return {'Item': {'role': 'TAM'}}
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])

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
                return {'Item': {'accountId': 'acc-1'}}
            if Key['SK'].startswith('MEMBER#'):
                return {}  # membership removed
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('removed-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_account_membership_cache_expires(self, mock_table):
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1', 'origin': 'account'}],
        }
        is_still_member = {'value': True}

        def get_item(Key, **kwargs):
            if Key['SK'].startswith('MEETING#'):
                return {'Item': {'accountId': 'acc-1'}}
            if Key['SK'].startswith('MEMBER#'):
                return {'Item': {'role': 'TAM'}} if is_still_member['value'] else {}
            return {}

        mock_table.get_item.side_effect = get_item
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])
        self.assertEqual(mock_table.get_item.call_count, 2)  # meeting lookup + member check

        # Force both caches to have expired so the query path (and the
        # membership check, which has its own independent TTL) run again;
        # membership has since been revoked.
        handler._shared_meetings_cache_expiry['member-1'] = time.time() - 1
        handler._account_member_cache_expiry[('acc-1', 'member-1')] = time.time() - 1
        is_still_member['value'] = False
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [])


if __name__ == '__main__':
    unittest.main()
