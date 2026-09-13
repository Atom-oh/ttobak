"""Actual REST/WebSocket tool continuity with synthetic storage and model replies."""
import json
import unittest
from unittest import mock

import test_handler
import test_account_reads as fixtures
from test_tool_history import conversation

handler = test_handler.handler


class RuntimeTable(fixtures.AccountTable):
    @staticmethod
    def _check_projection(kwargs):
        # Legacy meeting projections may use unreserved literal names.
        reserved = {'name', 'status', 'permission'}
        if any(field.strip() in reserved for field in kwargs.get('ProjectionExpression', '').split(',')):
            raise AssertionError('Reserved projection name is not aliased')


class TestRuntimeToolHistory(unittest.TestCase):
    account = fixtures.TestStrictAccountReads.account
    insight = fixtures.TestStrictAccountReads.insight
    meeting = fixtures.TestStrictAccountReads.meeting

    def setUp(self):
        self.table = RuntimeTable()
        for patch in (mock.patch.object(handler, 'table', self.table),
                      mock.patch('socket.socket', side_effect=AssertionError('live network forbidden')),
                      mock.patch.object(handler, '_apigw_client'),
                      mock.patch.object(handler, '_post_ws', return_value=True)):
            patch.start()
            self.addCleanup(patch.stop)
        patch = mock.patch.object(handler, 'bedrock_runtime')
        self.model = patch.start()
        self.addCleanup(patch.stop)

    def replies(self, transport, name, arguments):
        tool = {'toolUseId': 'tool1', 'name': name, 'input': arguments}
        messages = [[{'toolUse': tool}], [{'text': 'PRIVATE_CHOICE'}],
                    [{'text': 'stable followup'}], [{'text': 'fresh answer'}]]
        if transport == 'rest':
            self.model.converse.side_effect = [
                {'stopReason': 'tool_use' if i == 0 else 'end_turn',
                 'output': {'message': {'role': 'assistant', 'content': content}}}
                for i, content in enumerate(messages)]
            return self.model.converse
        streams = []
        for i, content in enumerate(messages):
            if i == 0:
                start, delta = {'toolUse': {k: tool[k] for k in ('toolUseId', 'name')}}, {
                    'toolUse': {'input': json.dumps(arguments)}}
            else:
                start, delta = {}, content[0]
            streams.append({'stream': [
                {'contentBlockStart': {'start': start}}, {'contentBlockDelta': {'delta': delta}},
                {'contentBlockStop': {}}, {'messageStop': {'stopReason': 'tool_use' if i == 0 else 'end_turn'}},
            ]})
        self.model.converse_stream.side_effect = streams
        return self.model.converse_stream

    def ask(self, transport, question, meeting_id=None):
        if transport == 'rest':
            result = handler.handle_ask(question, user_id='reader', session_id='chat-readonly', meeting_id=meeting_id)
            self.assertEqual(result['statusCode'], 200, result)
        else:
            result = handler.handle_ask_stream({
                'question': question, 'userId': 'reader', 'sessionId': 'chat-readonly',
                'meetingId': meeting_id,
                'connectionId': 'c', 'endpoint': 'https://synthetic.invalid',
            })
            self.assertEqual(result['status'], 'ok', result)
        return result

    def test_history_budget_reports_coverage_without_losing_current_answer(self):
        self.account()
        self.replies('rest', 'list_accounts', {})
        with mock.patch('tool_history.MAX_RESULT_BYTES', 64):
            result = self.ask('rest', 'list accounts')
        payload = json.loads(result['body'])
        self.assertEqual(payload['answer'], 'PRIVATE_CHOICE')
        self.assertEqual(payload['toolHistoryCoverage'], [
            {'tool': 'list_accounts', 'complete': False, 'reason': 'RESULT_LIMIT'}])
        stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
        self.assertFalse(stored['sourceReplayable'])
        self.assertIn('Account', stored['messages'])

    def test_readonly_followup_survives_then_edit_revoke_or_failure_clears_everything(self):
        for transport in ('rest', 'stream'):
            for name in ('list_meetings', 'list_accounts', 'get_account_insights', 'get_account_brief'):
                for change in ('edit', 'revoke', 'read_failure'):
                    with self.subTest(transport=transport, tool=name, change=change):
                        self.table.items.clear()
                        self.table.fail_query = False
                        self.account()
                        self.insight()
                        self.meeting()
                        args = {'account': 'a'} if name.startswith('get_') else {}
                        model = self.replies(transport, name, args)
                        self.ask(transport, 'list sources')
                        stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                        self.assertTrue(stored['sourceReplayable'])
                        self.ask(transport, 'the first one')
                        self.assertIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                        if change == 'revoke':
                            del self.table.items[('ACCOUNT#a', 'MEMBER#reader')]
                        elif change == 'read_failure':
                            self.table.fail_query = True
                        else:
                            key, field = (('USER#owner', 'MEETING#m'), 'title') if name == 'list_meetings' else (
                                (('ACCOUNT#a', 'META'), 'name') if name == 'list_accounts' else
                                (('ACCOUNT#a', 'INSIGHT#2026-09-12#i'), 'text'))
                            self.table.items[key][field] = 'EDITED'
                        self.ask(transport, 'continue')
                        sent = json.dumps(model.call_args.kwargs['messages'])
                        self.assertNotIn('PRIVATE_CHOICE', sent)
                        self.assertNotIn('toolResult', sent)

    def test_creation_receipt_replays_without_repeating_actual_creation(self):
        for transport in ('rest', 'stream'):
            with self.subTest(transport=transport):
                self.table.items.clear()
                database = mock.Mock()
                with mock.patch.object(handler.boto3, 'client', return_value=database), \
                        mock.patch.object(handler, 'RESEARCH_SFN_ARN', ''), \
                        mock.patch.object(handler, 'check_research_limit', return_value=True) as limit, \
                        mock.patch('secrets.token_hex', return_value='a' * 32) as new_id:
                    model = self.replies(transport, 'start_research', {'topic': '한국어' * 2000, 'mode': 'invalid'})
                    self.ask(transport, 'start research')
                    stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                    self.assertIn('a' * 32, stored['messages'])
                    self.assertNotIn('Tool error', stored['messages'])
                    self.ask(transport, 'what did you start')
                    self.assertIn('a' * 32, json.dumps(model.call_args.kwargs['messages']))
                    self.assertEqual(database.transact_write_items.call_count, 1)
                    new_id.assert_called_once_with(16)
                    limit.assert_called_once_with('reader')

    def test_untracked_core_or_unknown_tool_never_replays_private_paraphrase(self):
        for tool in ('get_meeting_detail', 'search_knowledge_base', 'future_tool'):
            self.table.put_item(Item={
                'PK': 'SESSION#reader#untracked', 'SK': 'MESSAGES',
                'sourceProvenanceVersion': 1, 'sourceReplayable': True, 'sourceDependencies': [],
                'messages': json.dumps(conversation('PRIVATE', tool, {})),
            })
            self.assertEqual(handler.load_session('untracked', user_id='reader'), [])

    def test_prior_source_cannot_cover_empty_failed_or_skipped_private_read(self):
        cases = [
            ('search_knowledge_base', {'query': 'empty'}, [], False),
            ('search_knowledge_base', {'query': 'failure'}, RuntimeError('unavailable'), False),
            ('get_document_detail', {'offset': -1}, [], False),
            ('get_meeting_detail', {'meetingId': 'missing'}, [], False),
            ('get_meeting_detail', {'meetingId': 'm'}, [], True),
        ]
        for transport in ('rest', 'stream'):
            for name, arguments, result, replayable in cases:
                with self.subTest(transport=transport, tool=name, arguments=arguments):
                    self.table.items.clear()
                    self.account()
                    self.meeting(transcriptA='CURRENT_BODY')
                    model = self.replies(transport, name, arguments)
                    if name == 'get_document_detail':
                        # A valid earlier tool call must not cover the skipped callback.
                        replies = list(model.side_effect)
                        first = {'toolUseId': 'first', 'name': 'get_meeting_detail', 'input': {'meetingId': 'm'}}
                        if transport == 'rest':
                            replies[0]['output']['message']['content'].insert(0, {'toolUse': first})
                        else:
                            replies[0]['stream'][:0] = [
                                {'contentBlockStart': {'start': {'toolUse': {k: first[k] for k in ('toolUseId', 'name')}}}},
                                {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"meetingId":"m"}'}}}},
                                {'contentBlockStop': {}},
                            ]
                        model.side_effect = replies
                    with mock.patch.object(handler, 'retrieve_from_kb',
                                           side_effect=result if isinstance(result, Exception) else None,
                                           return_value=result):
                        self.ask(transport, 'read source', meeting_id='m')
                    stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                    self.assertTrue(stored['sourceDependencies'], 'test needs an earlier unrelated source')
                    self.assertEqual(stored['sourceReplayable'], replayable)
                    self.assertEqual(bool(handler.load_session('chat-readonly', user_id='reader')), replayable)
