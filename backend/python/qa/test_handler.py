"""Unit tests for the QA Lambda's handler.py.

Run: cd backend/python/qa && python3 -m unittest test_handler -v
Same stdlib-unittest pattern as backend/python/crawler/test_crawlers.py.

Covers:
- load_session's trailing-user-message trim (poisoning guard: a stored
  history ending in a user-role message would otherwise make Bedrock reject
  every subsequent call in that meeting with a role-alternation error).
- _list_shared_meetings' live origin/membership/sharedToAccount re-check
  (PR #114 review MAJORs: revocation must be visible immediately, not
  bounded by the raw share-list cache TTL).
- retrieve_from_kb's access-signature-gated cache (a cached KB answer must
  not be served once the caller's access has changed).
"""
import json
import os
import sys
import time
import unittest
from unittest import mock

# Set env vars BEFORE importing handler (it reads env at import time)
os.environ.setdefault('TABLE_NAME', 'test-table')
os.environ.setdefault('KB_ID', 'test-kb')
os.environ.setdefault('BEDROCK_MODEL_ID', 'test-model')
os.environ.setdefault('AWS_DEFAULT_REGION', 'us-east-1')

sys.path.insert(0, os.path.dirname(__file__))

# ---------------------------------------------------------------------------
# Patch boto3 at module level so `import handler` doesn't hit real AWS
# ---------------------------------------------------------------------------
_boto3_resource_patcher = mock.patch('boto3.resource', return_value=mock.MagicMock())
_boto3_client_patcher = mock.patch('boto3.client', return_value=mock.MagicMock())
_boto3_resource_patcher.start()
_boto3_client_patcher.start()

import handler  # noqa: E402


def _stored(messages):
    """DynamoDB get_item response holding the given conversation history."""
    return {'Item': {'PK': 'SESSION#u1#s1', 'SK': 'MESSAGES',
                     'messages': json.dumps(messages, ensure_ascii=False)}}


class TestLoadSessionTrimsTrailingUser(unittest.TestCase):
    """A stored history ending in user-role messages must be trimmed on load,
    or Bedrock rejects every subsequent call with a role-alternation error."""

    def setUp(self):
        self.get_item = mock.MagicMock()
        patcher = mock.patch.object(handler, 'table', mock.MagicMock(get_item=self.get_item))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_missing_item_returns_empty(self):
        self.get_item.return_value = {}
        self.assertEqual(handler.load_session('s1', user_id='u1'), [])

    def test_trims_single_trailing_user_message(self):
        self.get_item.return_value = _stored([
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'text': 'a1'}]},
            {'role': 'user', 'content': [{'text': 'q2 (round failed)'}]},
        ])
        result = handler.load_session('s1', user_id='u1')
        self.assertEqual(len(result), 2)
        self.assertEqual(result[-1]['role'], 'assistant')

    def test_trims_multiple_trailing_user_messages(self):
        self.get_item.return_value = _stored([
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'text': 'a1'}]},
            {'role': 'user', 'content': [{'toolResult': {'toolUseId': 't1', 'content': [{'text': 'r'}]}}]},
            {'role': 'user', 'content': [{'text': 'q2'}]},
        ])
        result = handler.load_session('s1', user_id='u1')
        self.assertEqual(len(result), 2)
        self.assertEqual(result[-1]['role'], 'assistant')

    def test_preserves_history_ending_in_assistant(self):
        history = [
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'text': 'a1'}]},
        ]
        self.get_item.return_value = _stored(history)
        self.assertEqual(handler.load_session('s1', user_id='u1'), history)

    def test_trims_dangling_tooluse_left_by_max_rounds_exhaustion(self):
        # MAX_TOOL_ROUNDS exhaustion can leave the round's toolResult unsaved
        # -- history ends in user(toolResult) whose matching assistant(toolUse)
        # is now dangling once the trailing user message is popped.
        self.get_item.return_value = _stored([
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'text': 'a1'}]},
            {'role': 'user', 'content': [{'text': 'q2'}]},
            {'role': 'assistant', 'content': [{'toolUse': {'toolUseId': 't1', 'name': 'x', 'input': {}}}]},
            {'role': 'user', 'content': [{'toolResult': {'toolUseId': 't1', 'content': [{'text': 'r'}]}}]},
        ])
        result = handler.load_session('s1', user_id='u1')
        self.assertEqual(len(result), 2)
        self.assertEqual(result[-1]['content'], [{'text': 'a1'}])

    def test_trims_dangling_tooluse_left_by_client_gone_break(self):
        # client_gone can break between appending assistant(toolUse) and
        # producing its toolResult -- history ends directly in the
        # unresolved toolUse with no trailing user message to pop first.
        self.get_item.return_value = _stored([
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'text': 'a1'}]},
            {'role': 'user', 'content': [{'text': 'q2'}]},
            {'role': 'assistant', 'content': [{'toolUse': {'toolUseId': 't1', 'name': 'x', 'input': {}}}]},
        ])
        result = handler.load_session('s1', user_id='u1')
        self.assertEqual(len(result), 2)
        self.assertEqual(result[-1]['content'], [{'text': 'a1'}])

    def test_preserves_assistant_with_mixed_text_and_resolved_toolresult_pairing(self):
        # A resolved tool round (toolUse followed by its toolResult, then a
        # final assistant text) must NOT be trimmed -- only an unresolved
        # trailing toolUse is dangling.
        history = [
            {'role': 'user', 'content': [{'text': 'q1'}]},
            {'role': 'assistant', 'content': [{'toolUse': {'toolUseId': 't1', 'name': 'x', 'input': {}}}]},
            {'role': 'user', 'content': [{'toolResult': {'toolUseId': 't1', 'content': [{'text': 'r'}]}}]},
            {'role': 'assistant', 'content': [{'text': 'final answer'}]},
        ]
        self.get_item.return_value = _stored(history)
        self.assertEqual(handler.load_session('s1', user_id='u1'), history)

    def test_all_user_history_trims_to_empty(self):
        self.get_item.return_value = _stored([
            {'role': 'user', 'content': [{'text': 'q1'}]},
        ])
        self.assertEqual(handler.load_session('s1', user_id='u1'), [])


class TestExecuteToolWithHeartbeat(unittest.TestCase):
    """A single heartbeat sent only before the call starts can't cover a
    tool that itself runs past the client's stall watchdog -- heartbeats
    must keep firing for the whole duration of a slow tool call."""

    @mock.patch.object(handler, 'execute_tool')
    def test_sends_periodic_heartbeats_for_a_slow_tool(self, mock_execute_tool):
        def slow_tool(name, tool_input, context):
            time.sleep(0.05)
            return 'result', []
        mock_execute_tool.side_effect = slow_tool

        apigw = mock.MagicMock()
        apigw.post_to_connection.return_value = None

        result, sources, client_gone = handler._execute_tool_with_heartbeat(
            'some_tool', {}, {}, apigw, 'c1', 's1', interval=0.01,
        )

        self.assertEqual(result, 'result')
        self.assertFalse(client_gone)
        # At 0.01s interval over a 0.05s tool call, multiple heartbeats must
        # have fired -- not just the one sent before the call started.
        self.assertGreaterEqual(apigw.post_to_connection.call_count, 2)
        for call in apigw.post_to_connection.call_args_list:
            payload = json.loads(call.kwargs['Data'])
            self.assertEqual(payload['type'], 'tool_progress')

    @mock.patch.object(handler, 'execute_tool')
    def test_client_gone_detected_mid_run_even_though_tool_call_itself_succeeds(self, mock_execute_tool):
        def slow_tool(name, tool_input, context):
            time.sleep(0.05)
            return 'result', []
        mock_execute_tool.side_effect = slow_tool

        class FakeGoneException(Exception):
            pass

        apigw = mock.MagicMock()
        apigw.exceptions.GoneException = FakeGoneException
        apigw.post_to_connection.side_effect = FakeGoneException()

        result, sources, client_gone = handler._execute_tool_with_heartbeat(
            'some_tool', {}, {}, apigw, 'c1', 's1', interval=0.01,
        )

        self.assertEqual(result, 'result')
        self.assertTrue(client_gone)


class TestAgenticConverseStreamClientGone(unittest.TestCase):
    """A client that disconnects mid-tool-round must stop the loop before the
    next (wasted) Bedrock round -- not just rearm-then-ignore the signal."""

    def _tool_use_stream(self, tool_use_id='t1'):
        return {'stream': [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': tool_use_id, 'name': 'some_tool'}}}},
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{}'}}}},
            {'contentBlockStop': {}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}

    @mock.patch.object(handler, 'execute_tool')
    @mock.patch.object(handler, 'bedrock_runtime')
    @mock.patch.object(handler, 'table')
    def test_stops_before_next_bedrock_round_when_client_gone_during_tool_round(
        self, mock_table, mock_bedrock, mock_execute_tool,
    ):
        mock_bedrock.converse_stream.return_value = self._tool_use_stream()
        mock_execute_tool.return_value = ('tool result', [])

        apigw = mock.MagicMock()

        class FakeGoneException(Exception):
            pass
        apigw.exceptions.GoneException = FakeGoneException
        # tool_progress heartbeat is the very first post_to_connection call
        # inside the tool branch -- fail it to simulate a mid-tool-round
        # disconnect.
        apigw.post_to_connection.side_effect = FakeGoneException()

        handler.agentic_converse_stream(
            messages=[{'role': 'user', 'content': [{'text': 'q'}]}],
            transcript='',
            session_id='s1',
            user_id='u1',
            apigw=apigw,
            connection_id='c1',
        )

        self.assertEqual(
            mock_bedrock.converse_stream.call_count, 1,
            "client_gone during the tool round must stop the loop before a second "
            "(wasted) Bedrock round -- the signal must not be silently reset/ignored",
        )
        saved = json.loads(mock_table.put_item.call_args.kwargs['Item']['messages'])
        self.assertEqual(saved[-1]['role'], 'user')
        self.assertIn('toolResult', saved[-1]['content'][0])


def make_get_item(share_origin='', member_exists=True, shared_to_account=True, account_id='acc-1', share_exists=True):
    """Build a get_item side_effect covering all three live re-checks
    _list_shared_meetings performs: the Share row itself (SHARED#), the
    meeting's account linkage (MEETING#), and account membership (MEMBER#)."""
    def get_item(Key, **kwargs):
        sk = Key['SK']
        if sk.startswith('SHARED#'):
            if not share_exists:
                return {}
            item = {'origin': share_origin} if share_origin else {}
            return {'Item': item}
        if sk.startswith('MEETING#'):
            return {'Item': {'accountId': account_id, 'sharedToAccount': shared_to_account}}
        if sk.startswith('MEMBER#'):
            return {'Item': {'role': 'TAM'}} if member_exists else {}
        return {}
    return get_item


class TestListSharedMeetings(unittest.TestCase):
    def setUp(self):
        # Each test gets a clean cache -- module-level dicts persist across
        # tests otherwise (mirrors the real warm-Lambda cache behavior this
        # code is designed for, but would make tests order-dependent).
        handler._shared_meetings_cache.clear()
        handler._shared_meetings_cache_expiry.clear()

    @mock.patch.object(handler, 'table')
    def test_direct_share_included_when_still_present(self, mock_table):
        mock_table.query.return_value = {'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}]}
        mock_table.get_item.side_effect = make_get_item(share_origin='')
        result = handler._list_shared_meetings('reader-1')
        self.assertEqual(result, [{'meetingId': 'm-1', 'ownerId': 'owner-1'}])

    @mock.patch.object(handler, 'table')
    def test_direct_share_excluded_once_revoked(self, mock_table):
        # The raw list is cached, but the Share row itself is re-checked live
        # -- a revoked direct share (owner called RevokeShare) must not leak
        # just because it was still present when the raw list was cached.
        mock_table.query.return_value = {'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}]}
        mock_table.get_item.side_effect = make_get_item(share_exists=False)
        result = handler._list_shared_meetings('reader-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_account_share_included_when_still_member(self, mock_table):
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],
        }
        mock_table.get_item.side_effect = make_get_item(share_origin='account', member_exists=True)
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
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],
        }
        mock_table.get_item.side_effect = make_get_item(share_origin='account', member_exists=False)
        result = handler._list_shared_meetings('removed-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_account_share_excluded_when_unshared_from_account(self, mock_table):
        # Mirrors the Go backend's resolveSharedAccess predicate: a meeting
        # the owner un-shared from the account (or that was only ever
        # Link-only), sharedToAccount=False, must not leak here even with a
        # lingering account-origin Share row and valid membership.
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],
        }
        mock_table.get_item.side_effect = make_get_item(share_origin='account', member_exists=True, shared_to_account=False)
        result = handler._list_shared_meetings('member-1')
        self.assertEqual(result, [])

    @mock.patch.object(handler, 'table')
    def test_membership_revocation_seen_immediately_despite_warm_raw_cache(self, mock_table):
        # The raw list (meetingId/ownerId identity only) stays cached across
        # calls, but membership/origin/sharedToAccount are all re-checked
        # live on every call -- so revocation is visible on the very next
        # call, NOT bounded by SHARED_MEETINGS_CACHE_TTL_SECONDS.
        mock_table.query.return_value = {
            'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}],
        }
        is_still_member = {'value': True}

        def get_item(Key, **kwargs):
            return make_get_item(share_origin='account', member_exists=is_still_member['value'])(Key, **kwargs)

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
        mock_table.get_item.side_effect = make_get_item(share_origin='')
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 1)
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 1, "second call within TTL should hit the raw cache, not re-query")

        handler._shared_meetings_cache_expiry['reader-1'] = time.time() - 1
        handler._list_shared_meetings('reader-1')
        self.assertEqual(mock_table.query.call_count, 2, "expired cache should trigger a fresh query")


class TestKBCacheAccessSignature(unittest.TestCase):
    def setUp(self):
        handler._shared_meetings_cache.clear()
        handler._shared_meetings_cache_expiry.clear()

    @mock.patch.object(handler, 'bedrock_agent_runtime')
    @mock.patch.object(handler, 'table')
    def test_kb_cache_miss_when_access_changed_since_cached(self, mock_table, mock_bedrock):
        # First call: user has access to m-1, result gets cached under that
        # access signature.
        mock_table.query.return_value = {'Items': [{'meetingId': 'm-1', 'ownerId': 'owner-1'}]}
        mock_table.get_item.side_effect = make_get_item(share_origin='')
        mock_bedrock.retrieve.return_value = {'retrievalResults': [
            {'score': 0.9, 'content': {'text': 'secret transcript excerpt'}, 'location': {'s3Location': {'uri': 's3://x'}}}
        ]}

        # Disable the real DynamoDB-backed KB cache reads/writes by making
        # get_item/put_item behave like an empty cache initially.
        cache_store = {}

        def get_item(Key, **kwargs):
            pk = Key['PK']
            if pk.startswith('CACHE#KB#'):
                item = cache_store.get(pk)
                return {'Item': item} if item else {}
            return make_get_item(share_origin='')(Key, **kwargs)

        def put_item(Item):
            cache_store[Item['PK']] = Item

        mock_table.get_item.side_effect = get_item
        mock_table.put_item.side_effect = put_item

        results1 = handler.retrieve_from_kb('what was discussed', user_id='reader-1')
        self.assertEqual(len(results1), 1)
        self.assertEqual(mock_bedrock.retrieve.call_count, 1)

        # Second call, same question/user, access UNCHANGED -- must hit the
        # KB cache (no second Bedrock call).
        results2 = handler.retrieve_from_kb('what was discussed', user_id='reader-1')
        self.assertEqual(results2, results1)
        self.assertEqual(mock_bedrock.retrieve.call_count, 1, "unchanged access should still hit the KB cache")

        # Access revoked (the share is gone) -- even though the KB cache
        # entry is still within TTL, the access signature no longer matches,
        # so it must NOT be served; Bedrock must be called again (and the
        # live filter now grants nothing).
        handler._shared_meetings_cache_expiry.clear()  # force the raw list to re-query too
        mock_table.query.return_value = {'Items': []}
        results3 = handler.retrieve_from_kb('what was discussed', user_id='reader-1')
        self.assertEqual(mock_bedrock.retrieve.call_count, 2, "revoked access must bypass the stale KB cache entry")


class TestHandleAskStreamIdentityAndOwnership(unittest.TestCase):
    """handle_ask_stream must never trust the WebSocket client for identity
    or for meeting context -- userId comes only from the server-set field
    (populated by the Go websocket Lambda from the $connect authorizer
    context), and a meetingId must be resolved server-side (with ownership/
    sharing verified) rather than trusting a client-supplied transcript."""

    def _base_event(self, **overrides):
        event = {
            'streamMode': 'ask_live',
            'connectionId': 'c1',
            'endpoint': 'https://example.com/prod',
            'question': 'what happened?',
            'userId': 'verified-user',
        }
        event.update(overrides)
        return event

    def setUp(self):
        self.apigw = mock.MagicMock()
        self._apigw_patcher = mock.patch.object(handler, '_apigw_client', return_value=self.apigw)
        self._apigw_patcher.start()
        self.addCleanup(self._apigw_patcher.stop)

    def _posted_types(self):
        return [json.loads(c.kwargs['Data'])['type'] for c in self.apigw.post_to_connection.call_args_list]

    def test_missing_user_id_is_rejected_without_calling_bedrock(self):
        with mock.patch.object(handler, 'agentic_converse_stream') as mock_converse:
            result = handler.handle_ask_stream(self._base_event(userId=None))

        self.assertEqual(result['status'], 'unauthorized')
        mock_converse.assert_not_called()
        self.assertIn('answer_error', self._posted_types())

    def test_missing_session_id_is_generated_server_side(self):
        with mock.patch.object(handler, 'agentic_converse_stream', return_value=('ok', [], [])) as mock_converse, \
             mock.patch.object(handler, 'load_session', return_value=[]):
            handler.handle_ask_stream(self._base_event(sessionId=None))

        # session_id passed into agentic_converse_stream must be a
        # non-empty string, not None.
        _, kwargs = mock_converse.call_args
        self.assertTrue(kwargs['session_id'])
        self.assertIsInstance(kwargs['session_id'], str)

    def test_meeting_id_fetches_context_server_side_ignoring_client_context(self):
        with mock.patch.object(handler, 'load_meeting_context', return_value=('real transcript', None)) as mock_load, \
             mock.patch.object(handler, 'agentic_converse_stream', return_value=('ok', [], [])) as mock_converse, \
             mock.patch.object(handler, 'load_session', return_value=[]):
            handler.handle_ask_stream(self._base_event(
                meetingId='m1',
                context='attacker-supplied transcript claiming to be m1',
            ))

        mock_load.assert_called_once_with('verified-user', 'm1')
        _, kwargs = mock_converse.call_args
        self.assertEqual(kwargs['transcript'], 'real transcript')

    def test_meeting_not_found_or_not_owned_surfaces_answer_error_without_calling_bedrock(self):
        with mock.patch.object(handler, 'load_meeting_context',
                                return_value=(None, {'code': 'NOT_FOUND', 'message': 'Meeting not found', 'status': 404})), \
             mock.patch.object(handler, 'agentic_converse_stream') as mock_converse:
            result = handler.handle_ask_stream(self._base_event(meetingId='not-mine'))

        self.assertEqual(result['status'], 'error')
        mock_converse.assert_not_called()
        self.assertIn('answer_error', self._posted_types())


class TestKBCacheTTLExpiry(unittest.TestCase):
    """A cache entry past its TTL must be treated as a miss even though the
    DynamoDB item itself is still physically present (TTL deletion lags)."""

    def setUp(self):
        handler._shared_meetings_cache.clear()
        handler._shared_meetings_cache_expiry.clear()

    def test_expired_ttl_is_a_cache_miss(self):
        expired_item = {
            'PK': handler._kb_cache_key('q', 5, None),
            'SK': 'V1',
            'results': json.dumps([{'text': 'stale', 'uri': '', 'score': 0.9}]),
            'accessSignature': None,
            'TTL': int(time.time()) - 10,  # already expired
        }
        with mock.patch.object(handler, 'table') as mock_table:
            mock_table.get_item.return_value = {'Item': expired_item}
            result = handler._kb_cache_get('q', 5, user_id=None, access_signature=None)
        self.assertIsNone(result, "an expired TTL must not be served even if the item still exists")

    def test_unexpired_ttl_is_a_cache_hit(self):
        fresh_item = {
            'PK': handler._kb_cache_key('q', 5, None),
            'SK': 'V1',
            'results': json.dumps([{'text': 'fresh', 'uri': '', 'score': 0.9}]),
            'accessSignature': None,
            'TTL': int(time.time()) + 600,
        }
        with mock.patch.object(handler, 'table') as mock_table:
            mock_table.get_item.return_value = {'Item': fresh_item}
            result = handler._kb_cache_get('q', 5, user_id=None, access_signature=None)
        self.assertEqual(result, [{'text': 'fresh', 'uri': '', 'score': 0.9}])


class TestAgenticConverseStreamMalformedToolInput(unittest.TestCase):
    """A tool-use content block whose streamed `input` JSON fails to parse
    must not crash the loop -- it should fall back to an empty dict input
    (and, since it's fed straight to execute_tool, the tool itself decides
    how to handle missing args) rather than propagating the exception."""

    def _malformed_tool_use_stream(self):
        return {'stream': [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': 't1', 'name': 'some_tool'}}}},
            # Deliberately invalid JSON fragment (unterminated object).
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"a": '}}}},
            {'contentBlockStop': {}},
            {'messageStop': {'stopReason': 'end_turn'}},
        ]}

    @mock.patch.object(handler, 'execute_tool')
    @mock.patch.object(handler, 'bedrock_runtime')
    @mock.patch.object(handler, 'table')
    def test_malformed_tool_input_json_falls_back_to_empty_dict(
        self, mock_table, mock_bedrock, mock_execute_tool,
    ):
        mock_bedrock.converse_stream.return_value = self._malformed_tool_use_stream()
        apigw = mock.MagicMock()

        # stop_reason is end_turn here, so execute_tool is never actually
        # invoked -- this test only asserts the loop doesn't raise on the
        # bad JSON while assembling the message content.
        handler.agentic_converse_stream(
            messages=[{'role': 'user', 'content': [{'text': 'q'}]}],
            transcript='',
            session_id='s1',
            user_id='u1',
            apigw=apigw,
            connection_id='c1',
        )

        saved = json.loads(mock_table.put_item.call_args.kwargs['Item']['messages'])
        tool_use_block = saved[-1]['content'][0]
        self.assertEqual(tool_use_block['toolUse']['input'], {})


if __name__ == '__main__':
    unittest.main()
