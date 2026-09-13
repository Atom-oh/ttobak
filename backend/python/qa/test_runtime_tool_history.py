"""QA tool continuity through both transports; no live services."""
import json
import time
import unittest
from unittest import mock

import test_handler
import test_account_reads as fixtures
from async_jobs import QAJobs
from test_async_jobs import JobTable
from test_tool_history import conversation
from test_kb_fixtures import _QAConversationFixture

handler = test_handler.handler


class RuntimeTable(fixtures.AccountTable):
    @staticmethod
    def _check_projection(kwargs):
        # Legacy meeting projections may use unreserved literal names.
        reserved = {'name', 'status', 'permission'}
        if any(field.strip() in reserved for field in kwargs.get('ProjectionExpression', '').split(',')):
            raise AssertionError('Reserved projection name is not aliased')


class TestRuntimeToolHistory(_QAConversationFixture, unittest.TestCase):
    account = fixtures.TestStrictAccountReads.account
    insight = fixtures.TestStrictAccountReads.insight
    meeting = fixtures.TestStrictAccountReads.meeting

    def setUp(self):
        self.set_up_transport(RuntimeTable())


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
                changes = ('edit', 'revoke', 'read_failure')
                if name in ('get_account_insights', 'get_account_brief'):
                    changes += ('unpublish',)
                for change in changes:
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
                        elif change == 'unpublish':
                            self.table.items[('USER#owner', 'MEETING#m')]['sharedToAccount'] = False
                        else:
                            key, field = (('USER#owner', 'MEETING#m'), 'title') if name == 'list_meetings' else (
                                (('ACCOUNT#a', 'META'), 'name') if name == 'list_accounts' else
                                (('ACCOUNT#a', 'INSIGHT#2026-09-12T12:00:00Z#m#0'), 'text'))
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
            ('search_knowledge_base', {'query': 'empty'}, [], True),
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
                        self.prepend_tool(model, transport, 'get_meeting_detail', {'meetingId': 'm'})
                    with mock.patch.object(handler.bedrock_agent_runtime, 'retrieve',
                                           side_effect=result if isinstance(result, Exception) else None,
                                           return_value={'retrievalResults': []}):
                        self.ask(transport, 'read source', meeting_id='m')
                    stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                    self.assertTrue(stored['sourceDependencies'], 'test needs an earlier unrelated source')
                    self.assertEqual(stored['sourceReplayable'], replayable)
                    self.assertEqual(bool(handler.load_session('chat-readonly', user_id='reader')), replayable)

    def test_live_windows_preserve_both_transports_and_use_latest_growing_or_corrected_input(self):
        initial = '가' * 8000 + ' LIVE_FIRST'
        windows = (initial + ' LATEST_GROW', 'LATEST_CORRECTED', '나' * 8000 + ' LATEST_ROLL')
        for transport in ('rest', 'stream'):
            for latest in windows:
                with self.subTest(transport=transport, latest=latest[-20:]):
                    self.table.items.clear()
                    self.account()
                    self.meeting(transcriptA='SERVER_TRANSCRIPT', notes='SAVED_NOTE')
                    model = self.replies(transport, 'search_transcript', {'keywords': 'LIVE_FIRST'})
                    self.ask(transport, 'live question', meeting_id='m', context=initial)
                    stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                    self.assertTrue(stored['sourceReplayable'])
                    self.ask(transport, 'follow up', meeting_id='m', context=latest)
                    self.assertIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                    system = json.dumps(model.call_args.kwargs['system'], ensure_ascii=False)
                    self.assertIn(latest[-100:], system)
                    self.assertIn('client_live', system)
                    self.assertIn('SAVED_NOTE', system)
                    self.assertNotIn('SERVER_TRANSCRIPT', system)

    def test_latest_only_live_policy_reaches_each_transport_without_losing_conversation_label(self):
        # This checks real model requests, not a stub's ability to obey the prompt.
        # Runtime acceptance must still reject old values anywhere in real answers.
        for transport in ('rest', 'stream', 'async'):
            with self.subTest(transport=transport):
                self.table.items.clear()
                self.account()
                self.meeting(transcriptA='SERVER_TRANSCRIPT', notes='CURRENT_SAVED_NOTE')
                model = self.replies('rest' if transport == 'async' else transport,
                                     'search_transcript', {'keywords': 'LIVE_DRAFT_LOCAL'})
                model.reset_mock()
                replies = list(model.side_effect)
                for index, answer in enumerate((
                        'LABEL_LOCAL LIVE_DRAFT_LOCAL',
                        'LABEL_LOCAL LIVE_DRAFT_LOCAL',
                        'LABEL_LOCAL LIVE_CORRECTED_LOCAL'), 1):
                    if transport == 'stream':
                        next(event['contentBlockDelta'] for event in replies[index]['stream']
                             if 'contentBlockDelta' in event)['delta']['text'] = answer
                    else:
                        replies[index]['output']['message']['content'] = [{'text': answer}]
                model.side_effect = replies
                jobs = QAJobs(JobTable(), mock.Mock(), 'https://sqs.invalid/q')
                questions = (
                    'Remember conversation label LABEL_LOCAL. Read this live draft.',
                    'Repeat the conversation label and current draft.',
                    'Use the latest corrected client draft. Repeat the conversation label from my FIRST '
                    'question and current saved source codes with provenance. '
                    'Give only the current draft code; omit old drafts.',
                )
                contexts = ('LIVE_DRAFT_LOCAL', 'LIVE_DRAFT_LOCAL grew', 'LIVE_CORRECTED_LOCAL')
                for turn, (question, context) in enumerate(zip(questions, contexts), 1):
                    if transport == 'async':
                        jid = str(int(time.time() * 1000)) + '-' + f'{turn:032x}'
                        jobs.submit('reader', {'requestId': jid, 'mode': 'ask', 'question': question,
                                              'context': context, 'meetingId': 'm', 'sessionId': 'chat-readonly'})
                        with mock.patch.object(handler, '_ASYNC_MODEL', self.model):
                            jobs.work('reader', jid, handler._execute_job_request, handler._validate_job_sources)
                        result = jobs.poll('reader', jid, handler._validate_job_sources)
                        self.assertEqual(result['status'], 'succeeded', result)
                    else:
                        self.ask(transport, question, meeting_id='m', context=context)
                request = model.call_args.kwargs
                system = '\n'.join(block['text'] for block in request['system'])
                dialogue = json.dumps(request['messages'])
                self.assertIn('LIVE_CORRECTED_LOCAL', system)
                self.assertIn('CURRENT_SAVED_NOTE', system)
                self.assertNotIn('LIVE_DRAFT_LOCAL', system)
                # Keep real prior dialogue/labels. Erasing it would mask the
                # observed narration failure and break follow-up continuity.
                self.assertIn('LABEL_LOCAL', dialogue)
                self.assertIn('LIVE_DRAFT_LOCAL', dialogue)
                self.assertIn('Give only the current draft code; omit old drafts.', dialogue)
                self.assertIn('When the user asks for only current/latest values or to omit old drafts', system)
                self.assertIn('Do not repeat superseded raw values anywhere in the answer', system)
                self.assertIn('including correction narratives, quotes, comparisons', system)
                self.assertIn('Preserve requested conversation labels', system)
                self.assertEqual(model.call_count, 4)  # One tool round, then three replies; no repair model call.

    def test_live_input_never_preserves_changed_or_revoked_server_data(self):
        for transport in ('rest', 'stream'):
            for change in ('notes', 'revoke', 'delete', 'scope'):
                with self.subTest(transport=transport, change=change):
                    self.table.items.clear()
                    self.account()
                    self.meeting(transcriptA='SERVER_TRANSCRIPT', notes='OLD_SERVER_NOTE')
                    model = self.replies(transport, 'search_transcript', {'keywords': 'live'})
                    self.ask(transport, 'first', meeting_id='m', context='live first input')
                    if change == 'notes':
                        self.table.items[('USER#owner', 'MEETING#m')]['notes'] = 'NEW_SERVER_NOTE'
                        self.ask(transport, 'second', meeting_id='m', context='live corrected input')
                        self.assertNotIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                        self.assertIn('NEW_SERVER_NOTE', json.dumps(model.call_args.kwargs['system']))
                    elif change == 'scope':
                        self.meeting('other', transcriptA='OTHER_TRANSCRIPT')
                        self.ask(transport, 'second', meeting_id='other', context='other live input')
                        self.assertNotIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                    else:
                        key = ('ACCOUNT#a', 'MEMBER#reader') if change == 'revoke' else ('USER#owner', 'MEETING#m')
                        del self.table.items[key]
                        before = model.call_count
                        response = self.send(transport, 'second', meeting_id='m', context='live next')
                        self.assertEqual(response.get('statusCode') if transport == 'rest' else response.get('status'),
                                         404 if transport == 'rest' else 'error')
                        self.assertEqual(model.call_count, before)

    def test_empty_kb_success_preserves_live_followup_and_requeries(self):
        for transport in ('rest', 'stream'):
            with self.subTest(transport=transport):
                self.table.items.clear()
                self.account()
                self.meeting(transcriptA='SERVER_TRANSCRIPT')
                model = self.replies(transport, 'search_knowledge_base', {'query': 'NO_MATCH'})
                with mock.patch.object(handler.bedrock_agent_runtime, 'retrieve',
                                       return_value={'retrievalResults': []}) as provider:
                    self.ask(transport, 'first', meeting_id='m', context='live first')
                    first_calls = provider.call_count
                    self.ask(transport, 'follow up', meeting_id='m', context='live next')
                    self.assertGreater(provider.call_count, first_calls)
                    self.assertIn('PRIVATE_CHOICE', json.dumps(model.call_args.kwargs['messages']))
                stored = self.table.items[('SESSION#reader#chat-readonly', 'MESSAGES')]
                self.assertTrue(stored['sourceReplayable'])
                self.assertTrue(any('emptySearch' in dep for dep in stored['sourceDependencies']))
