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


class TestParseDetectedQuestions(unittest.TestCase):
    """The detect-questions model output must parse in BOTH shapes: the
    current [{"q", "search"}] object format and the legacy plain-string
    array (older prompt, or a model that ignores the schema) — and the
    proactive list must always be a subset of the returned questions."""

    def test_object_format_splits_proactive(self):
        raw = json.dumps([
            {'q': 'EKS 1.31 지원 종료일은?', 'search': True},
            {'q': '어느 팀이 마이그레이션을 맡을까요?', 'search': False},
        ], ensure_ascii=False)
        questions, proactive = handler.parse_detected_questions(raw)
        self.assertEqual(len(questions), 2)
        self.assertEqual(proactive, ['EKS 1.31 지원 종료일은?'])

    def test_legacy_string_format_has_no_proactive(self):
        questions, proactive = handler.parse_detected_questions('["질문1", "질문2"]')
        self.assertEqual(questions, ['질문1', '질문2'])
        self.assertEqual(proactive, [])

    def test_mixed_and_malformed_items_are_filtered(self):
        raw = json.dumps(['질문1', {'q': '질문2', 'search': True}, {'search': True}, 42, {'q': '   '}], ensure_ascii=False)
        questions, proactive = handler.parse_detected_questions(raw)
        self.assertEqual(questions, ['질문1', '질문2'])
        self.assertEqual(proactive, ['질문2'])

    def test_bad_json_and_non_list_return_empty(self):
        self.assertEqual(handler.parse_detected_questions('not json'), ([], []))
        self.assertEqual(handler.parse_detected_questions('{"q": "x"}'), ([], []))

    def test_caps_at_five_and_proactive_stays_subset(self):
        items = [{'q': f'질문{i}', 'search': True} for i in range(8)]
        questions, proactive = handler.parse_detected_questions(json.dumps(items, ensure_ascii=False))
        self.assertEqual(len(questions), 5)
        self.assertEqual(proactive, questions)

    def test_duplicates_dropped_first_occurrence_wins(self):
        raw = json.dumps([
            {'q': '같은 질문', 'search': True},
            {'q': '같은 질문', 'search': False},
            '같은 질문',
            {'q': '다른 질문', 'search': False},
        ], ensure_ascii=False)
        questions, proactive = handler.parse_detected_questions(raw)
        self.assertEqual(questions, ['같은 질문', '다른 질문'])
        self.assertEqual(proactive, ['같은 질문'])  # first occurrence's flag wins


class TestWebSearchTool(unittest.TestCase):
    """format_web_results must keep a transport/config failure distinguishable
    from a genuine zero-hit search, and execute_tool must route search_web."""

    def test_error_is_not_no_results(self):
        import web_search
        text, sources = web_search.format_web_results([], 'gateway error')
        self.assertIn('웹 검색을 수행하지 못했습니다', text)
        self.assertEqual(sources, [])
        text_empty, _ = web_search.format_web_results([], None)
        self.assertIn('관련 결과를 찾지 못했습니다', text_empty)
        self.assertNotEqual(text, text_empty)

    def test_results_format_and_sources(self):
        import web_search
        text, sources = web_search.format_web_results([
            {'title': 'EKS 1.31 EOL', 'url': 'https://example.com/a', 'text': 'snippet', 'publishedDate': '2026-07-01T00:00:00Z'},
        ], None)
        self.assertIn('[EKS 1.31 EOL](https://example.com/a)', text)
        self.assertIn('2026-07-01', text)
        self.assertEqual(sources, ['https://example.com/a'])

    def test_execute_tool_routes_search_web(self):
        import tools
        with mock.patch.object(tools, 'gateway_web_search', return_value=([
            {'title': 'T', 'url': 'https://example.com/x', 'text': 's'},
        ], None)) as mocked:
            text, sources = tools.execute_tool('search_web', {'query': 'q', 'maxResults': 3}, {})
        mocked.assert_called_once_with('q', 3)
        self.assertIn('https://example.com/x', text)
        self.assertEqual(sources, ['https://example.com/x'])

    def test_unconfigured_gateway_returns_error_not_empty(self):
        import web_search
        with mock.patch.object(web_search, 'WEB_SEARCH_GATEWAY_URL', ''):
            results, error = web_search.gateway_web_search('anything')
        self.assertEqual(results, [])
        self.assertEqual(error, 'web search not configured')

    def test_max_results_clamped_to_at_least_one(self):
        # A model-supplied 0 (or junk) must not slice to [] with error=None —
        # that would masquerade as a genuine zero-hit search.
        import tools
        for bad in (0, -3, 'x'):
            with mock.patch.object(tools, 'gateway_web_search', return_value=([], None)) as mocked:
                tools.execute_tool('search_web', {'query': 'q', 'maxResults': bad}, {})
            self.assertGreaterEqual(mocked.call_args[0][1], 1, f'maxResults={bad!r} not clamped')

    def test_non_http_urls_filtered_from_results(self):
        import web_search
        gateway_payload = json.dumps({
            'result': {
                'content': [{'type': 'text', 'text': json.dumps({'results': [
                    {'title': 'ok', 'url': 'https://example.com/a', 'text': 's'},
                    {'title': 'js', 'url': 'javascript:alert(1)', 'text': 's'},
                    {'title': 'plain-http', 'url': 'http://example.com/b', 'text': 's'},
                    {'title': 'no-url', 'text': 's'},
                ]})}],
            },
        })
        with mock.patch.object(web_search, 'WEB_SEARCH_GATEWAY_URL', 'https://gw.example/mcp'), \
             mock.patch.object(web_search, '_sigv4_post', return_value=gateway_payload):
            results, error = web_search.gateway_web_search('q')
        self.assertIsNone(error)
        self.assertEqual([r['url'] for r in results], ['https://example.com/a'])

    def test_title_markdown_metachars_escaped(self):
        import web_search
        text, _ = web_search.format_web_results([
            {'title': 'evil](https://phish.example) [x', 'url': 'https://example.com/a', 'text': 's'},
        ], None)
        self.assertNotIn('evil](https://phish.example)', text)
        self.assertIn('\\]', text)

    def test_url_parens_percent_encoded_in_markdown_link(self):
        import web_search
        text, _ = web_search.format_web_results([
            {'title': 't', 'url': 'https://en.example.com/wiki/Foo_(bar)', 'text': 's'},
        ], None)
        self.assertIn('https://en.example.com/wiki/Foo_%28bar%29', text)
        self.assertNotIn('Foo_(bar)', text)

    def test_redact_tool_input_masks_free_text_keys_for_any_tool(self):
        # The agentic loop logs every tool call's input — free-text keys are
        # conversation-derived regardless of which tool carries them, so the
        # mask is key-based (a fixed blocklist of known free-text keys),
        # not search_web-specific. Identifier keys stay loggable.
        import web_search
        redacted = web_search.redact_tool_input_for_log('search_web', {'query': '민감한 고객사 키워드', 'maxResults': 3})
        self.assertNotIn('민감한', json.dumps(redacted, ensure_ascii=False))
        self.assertTrue(redacted['query'].startswith('q#'))
        self.assertEqual(redacted['maxResults'], 3)
        # search_transcript/list_meetings carry the same conversation text
        # under different key names — masked too.
        st = web_search.redact_tool_input_for_log('search_transcript', {'keywords': '고객사 이전 계획'})
        self.assertTrue(st['keywords'].startswith('q#'))
        lm = web_search.redact_tool_input_for_log('list_meetings', {'keyword': '고객사', 'limit': 5})
        self.assertTrue(lm['keyword'].startswith('q#'))
        self.assertEqual(lm['limit'], 5)
        # 'account' is a customer NAME/alias per the tool schema (예: 하나은행),
        # not an opaque id — the top sensitivity class in ADR-028's threat
        # model must never appear in logs.
        ai = web_search.redact_tool_input_for_log('get_account_insights', {'account': '하나은행', 'from': '2026-08-01T00:00:00Z'})
        self.assertTrue(ai['account'].startswith('q#'))
        self.assertEqual(ai['from'], '2026-08-01T00:00:00Z')
        # Identifier-shaped inputs pass through untouched.
        gm = web_search.redact_tool_input_for_log('get_meeting_detail', {'meetingId': 'm-123'})
        self.assertEqual(gm, {'meetingId': 'm-123'})
        # A schema-defying non-string value under a free-text key must be
        # fully masked, not passed through (it could embed the conversation
        # text in a list/dict).
        weird = web_search.redact_tool_input_for_log('search_web', {'query': ['민감', '키워드']})
        self.assertEqual(weird['query'], '<redacted non-string>')

    def test_sigv4_post_refuses_non_https_gateway(self):
        import web_search
        with mock.patch.object(web_search, 'WEB_SEARCH_GATEWAY_URL', 'http://gw.example/mcp'):
            with self.assertRaises(RuntimeError):
                web_search._sigv4_post('{}')


class TestWebSearchRateLimit(unittest.TestCase):
    """Server-side per-user hourly cap on search_web (ADR-028 follow-up):
    atomic counter with over-limit compensation, fail-open on DynamoDB
    errors, and a tool-level denial message distinct from no-results and
    gateway errors so a capped user consumes no external quota."""

    def setUp(self):
        self.mock_table = mock.MagicMock()
        patcher = mock.patch.object(handler, 'table', self.mock_table)
        patcher.start()
        self.addCleanup(patcher.stop)

    def _counter_response(self, count):
        return {'Attributes': {'count': count}}

    def test_under_limit_allows(self):
        self.mock_table.update_item.return_value = self._counter_response(1)
        with mock.patch.object(handler, 'WEB_SEARCH_HOURLY_LIMIT', 30):
            self.assertTrue(handler.check_web_search_limit('u1'))
        self.assertEqual(self.mock_table.update_item.call_count, 1)

    def test_over_limit_denies_and_compensates(self):
        self.mock_table.update_item.return_value = self._counter_response(31)
        with mock.patch.object(handler, 'WEB_SEARCH_HOURLY_LIMIT', 30):
            self.assertFalse(handler.check_web_search_limit('u1'))
        # Second update_item is the compensating decrement, so a burst of
        # denied calls can't inflate the counter.
        self.assertEqual(self.mock_table.update_item.call_count, 2)

    def test_dynamodb_failure_fails_open(self):
        self.mock_table.update_item.side_effect = RuntimeError('ddb down')
        with mock.patch.object(handler, 'WEB_SEARCH_HOURLY_LIMIT', 30):
            self.assertTrue(handler.check_web_search_limit('u1'))

    def test_zero_limit_disables_check(self):
        with mock.patch.object(handler, 'WEB_SEARCH_HOURLY_LIMIT', 0):
            self.assertTrue(handler.check_web_search_limit('u1'))
        self.mock_table.update_item.assert_not_called()

    def test_capped_user_never_reaches_gateway(self):
        import tools
        with mock.patch.object(tools, 'gateway_web_search') as mocked_gw:
            text, sources = tools.execute_tool(
                'search_web', {'query': 'q'},
                {'user_id': 'u1', 'check_web_search_limit': lambda uid: False},
            )
        mocked_gw.assert_not_called()
        self.assertIn('한도', text)
        self.assertNotIn('관련 결과를 찾지 못했습니다', text)
        self.assertEqual(sources, [])

    def test_missing_limit_fn_still_searches(self):
        # Contexts without the checker (older callers) keep working.
        import tools
        with mock.patch.object(tools, 'gateway_web_search', return_value=([], None)) as mocked_gw:
            tools.execute_tool('search_web', {'query': 'q'}, {'user_id': 'u1'})
        mocked_gw.assert_called_once()


if __name__ == '__main__':
    unittest.main()
