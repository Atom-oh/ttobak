"""Readonly history continuity; real DynamoDB serialization, no AWS clients."""
import copy
from decimal import Decimal
import json
import unittest
from unittest import mock

from boto3.dynamodb.types import TypeDeserializer, TypeSerializer

import session_provenance as provenance


def persisted(value):
    return TypeDeserializer().deserialize(TypeSerializer().serialize(value))


def conversation(text, tool='list_meetings', arguments=None):
    return [
        {'role': 'user', 'content': [{'text': 'Show my meetings'}]},
        {'role': 'assistant', 'content': [{'toolUse': {
            'toolUseId': 'list-1', 'name': tool, 'input': arguments or {}}}]},
        {'role': 'user', 'content': [{'toolResult': {
            'toolUseId': 'list-1', 'content': [{'text': text}]}}]},
        {'role': 'assistant', 'content': [{'text': 'The first meeting concerns PRIVATE_RELEASE_PLAN.'}]},
    ]


def session(state, messages):
    return persisted({'sourceProvenanceVersion': 1, 'sourceReplayable': state['replayable'],
                      'sourceDependencies': state['dependencies'],
                      'messages': json.dumps(messages, ensure_ascii=False)})


class TestToolHistory(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch('socket.socket', side_effect=AssertionError('live network forbidden'))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_readonly_dependency_is_an_explicit_typed_union(self):
        dependency = {'readOnlyTool': 'list_meetings', 'toolInput': {'limit': 20},
                      'userId': 'reader', 'sourceRevision': 'a' * 64}
        self.assertTrue(provenance.valid_dependency(dependency))
        for key, value in [('readOnlyTool', 'start_research'), ('userId', ''),
                           ('toolInput', {'limit': True}), ('toolInput', {'user_id': 'owner'})]:
            self.assertFalse(provenance.valid_dependency(dict(dependency, **{key: value})))

    def tracker(self, callback=None, user='reader'):
        from tool_history import CompleteRead, ToolHistory
        rows = [{'meetingId': 'm1', 'title': 'PRIVATE_RELEASE_PLAN', 'date': '2026-09-12',
                 'tags': [], 'status': 'done', 'isShared': True}]
        self.rows, self.calls = rows, []
        def read(user_id, **kwargs):
            self.calls.append((user_id, kwargs))
            return CompleteRead(copy.deepcopy(rows))
        return ToolHistory(user, {'list_meetings': callback or read})

    def test_stable_list_first_one_survives_sdk_roundtrip_without_mutation(self):
        history = self.tracker()
        state = provenance.new_source_state()
        value = history.read(state, 'list_meetings', {'limit': 20})
        messages = conversation(json.dumps(value))
        saved = session(state, messages)
        restored = provenance.restore_messages(saved, provenance.new_source_state(), lambda _: False,
                                               tool_history=history)
        self.assertEqual(restored, messages)
        restored.append({'role': 'user', 'content': [{'text': 'Tell me about the first one'}]})
        self.assertIn('m1', json.dumps(restored))
        self.assertEqual(len(self.calls), 2)
        self.assertTrue(all(user == 'reader' and type(args['limit']) is int for user, args in self.calls))
        self.assertIsInstance(saved['sourceDependencies'][0]['toolInput']['limit'], Decimal)
        self.assertNotIn('PRIVATE_RELEASE_PLAN', json.dumps(saved['sourceDependencies'], default=str))

    def test_edit_reorder_delete_or_revoke_discards_entire_history(self):
        for change in ('edit', 'order', 'delete', 'revoke'):
            with self.subTest(change=change):
                history = self.tracker()
                self.rows.append(dict(self.rows[0], meetingId='m2', title='SECOND'))
                state = provenance.new_source_state()
                value = history.read(state, 'list_meetings', {})
                saved = session(state, conversation(json.dumps(value)))
                if change == 'edit':
                    self.rows[0]['title'] = 'CORRECTED'
                elif change == 'order':
                    self.rows.reverse()
                else:
                    self.rows.clear()
                restored = provenance.restore_messages(saved, provenance.new_source_state(), lambda _: True,
                                                       tool_history=history)
                self.assertEqual(restored, [], 'assistant paraphrase survived source invalidation')

    def test_error_or_unattested_partial_result_cannot_be_replayed_as_empty(self):
        from tool_history import CompleteRead
        for broken in (RuntimeError('synthetic denied read'), [], CompleteRead({'error': 'unavailable'})):
            with self.subTest(broken=type(broken).__name__):
                def callback(user, **kwargs):
                    if isinstance(broken, Exception):
                        raise broken
                    return broken
                history = self.tracker()
                self.rows.clear()
                state = provenance.new_source_state()
                history.read(state, 'list_meetings', {})
                saved = session(state, conversation('no meetings'))
                denied = self.tracker(callback)
                self.assertEqual(provenance.restore_messages(
                    saved, provenance.new_source_state(), lambda _: True, tool_history=denied), [])

    def test_current_user_cannot_be_replaced_by_persisted_user_or_input(self):
        history = self.tracker()
        state = provenance.new_source_state()
        history.read(state, 'list_meetings', {})
        saved = session(state, conversation('PRIVATE_RELEASE_PLAN'))
        outsider = self.tracker(user='outsider')
        self.assertEqual(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: True, tool_history=outsider), [])
        self.assertEqual(self.calls, [])
        saved['sourceDependencies'][0]['toolInput']['userId'] = 'reader'
        self.assertEqual(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: True, tool_history=outsider), [])
        self.assertEqual(self.calls, [])

    def test_account_tools_revalidate_typed_current_user_callbacks(self):
        from tool_history import CompleteRead, ToolHistory
        examples = {
            'list_accounts': ({}, [{'accountId': 'a', 'name': 'Account', 'role': 'SA'}]),
            'get_account_insights': ({'account': 'Account', 'types': ['risk']},
                                     {'account': 'Account', 'insights': [{'type': 'risk', 'text': 'PRIVATE'}]}),
            'get_account_brief': ({'account': 'Account'}, {'account': 'Account', 'insightsByType': {},
                                                          'meetings': [], 'research': []}),
        }
        for name, (args, value) in examples.items():
            with self.subTest(tool=name):
                calls = []
                def read(user, **kwargs):
                    calls.append((user, kwargs))
                    return CompleteRead(copy.deepcopy(value))
                history = ToolHistory('reader', {name: read})
                state = provenance.new_source_state()
                self.assertEqual(history.read(state, name, args), value)
                saved = session(state, conversation('account detail', name, args))
                self.assertTrue(provenance.restore_messages(
                    saved, provenance.new_source_state(), lambda _: False, tool_history=history))
                self.assertEqual(len(calls), 2)
                self.assertTrue(all(user == 'reader' for user, _ in calls))
                if name != 'list_accounts':
                    self.assertEqual(calls[-1][1]['account_query'], 'Account')

    def test_research_receipt_never_reinvokes_creation(self):
        from tool_history import ToolHistory
        calls = []
        def create():
            calls.append('create')
            return {'researchId': 'new-research'}
        history = ToolHistory('reader', {})
        state = provenance.new_source_state()
        receipt = history.research_receipt(state, {'topic': 'Synthetic topic', 'mode': 'quick'}, create())
        self.assertEqual(receipt, {'topic': 'Synthetic topic', 'mode': 'quick', 'researchId': 'new-research'})
        saved = session(state, conversation(json.dumps(receipt), 'start_research',
                                            {'topic': 'Synthetic topic', 'mode': 'quick'}))
        for _ in range(3):
            self.assertTrue(provenance.restore_messages(
                saved, provenance.new_source_state(), lambda _: False, tool_history=history))
        self.assertEqual(calls, ['create'])
        with self.assertRaises(ValueError):
            ToolHistory('reader', {'start_research': create})
        for result in ({'error': 'failed'}, {'researchId': 'r', 'summary': 'PRIVATE_SOURCE_TEXT'}):
            with self.assertRaises(ValueError):
                history.research_receipt(provenance.new_source_state(), {'topic': 'test'}, result)

    def test_canonical_hash_is_typed_order_sensitive_and_sdk_stable(self):
        from tool_history import fingerprint
        a = {'count': 2, 'score': Decimal('1.2500'), 'label': '검증', 'ok': True}
        self.assertEqual(fingerprint(a), fingerprint(persisted(a)))
        self.assertEqual(fingerprint(a), fingerprint(dict(reversed(list(a.items())))))
        self.assertEqual(fingerprint(Decimal('1.0')), fingerprint(1))
        self.assertNotEqual(fingerprint(True), fingerprint(1))
        self.assertNotEqual(fingerprint(['m1', 'm2']), fingerprint(['m2', 'm1']))
        self.assertNotEqual(fingerprint({'x': None}), fingerprint({}))
        for bad in (float('nan'), 1.0, Decimal('NaN'), Decimal('Infinity'), Decimal('1e999999'), object()):
            with self.assertRaises(ValueError):
                fingerprint(bad)

    def test_input_and_dependency_budgets_fail_before_callback_dispatch(self):
        from tool_history import MAX_TOOL_DEPENDENCIES
        history = self.tracker()
        for args in ({'limit': False}, {'limit': 1.5}, {'limit': Decimal('1.1')},
                     {'limit': 101}, {'limit': 0}, {'limit': '20'}, {'keyword': []},
                     {'callback': 'start_research'}, {'keyword': 'x' * 5000}):
            with self.assertRaises(ValueError):
                history.read(provenance.new_source_state(), 'list_meetings', args)
        self.assertEqual(self.calls, [])
        state = provenance.new_source_state()
        for i in range(MAX_TOOL_DEPENDENCIES):
            history.read(state, 'list_meetings', {'keyword': str(i)})
        before = len(self.calls)
        result = history.read(state, 'list_meetings', {'keyword': 'one-too-many'})
        self.assertEqual(result[0]['meetingId'], 'm1')
        self.assertEqual(len(self.calls), before + 1)
        self.assertFalse(state['replayable'])
        self.assertEqual(state['toolHistoryCoverage'][0]['reason'], 'DEPENDENCY_LIMIT')

    def test_output_bounds_and_cycles_never_produce_a_trusted_hash(self):
        from tool_history import CompleteRead, MAX_RESULT_BYTES, fingerprint
        cyclic = []
        cyclic.append(cyclic)
        for value in ('x' * (MAX_RESULT_BYTES + 1), cyclic, {'a': [None] * 20000}):
            with self.assertRaises(ValueError):
                fingerprint(value)
        history = self.tracker(lambda user, **kwargs: CompleteRead([{'title': 'x' * (MAX_RESULT_BYTES + 1)}]))
        state = provenance.new_source_state()
        current = history.read(state, 'list_meetings', {})
        self.assertEqual(len(current[0]['title']), MAX_RESULT_BYTES + 1)
        self.assertFalse(state['replayable'])
        self.assertEqual(state['dependencies'], [])
        self.assertEqual(state['toolHistoryCoverage'][0]['reason'], 'RESULT_LIMIT')

    def test_changes_within_a_turn_fail_and_final_validation_rechecks(self):
        history = self.tracker()
        state = provenance.new_source_state()
        history.read(state, 'list_meetings', {})
        self.rows[0]['title'] = 'changed'
        with self.assertRaises(RuntimeError):
            provenance.validate_sources(state, lambda _: True, tool_history=history)
        with self.assertRaises(RuntimeError):
            history.read(state, 'list_meetings', {})

    def test_mixed_source_invalidation_never_restores_only_the_assistant(self):
        history = self.tracker()
        state = provenance.new_source_state()
        history.read(state, 'list_meetings', {})
        provenance.remember_source(state, {'sourcePK': 'USER#reader', 'sourceSK': 'DOC#d',
                                           'sourceRevision': 'b' * 64})
        saved = session(state, conversation('PRIVATE_RELEASE_PLAN'))
        target = provenance.new_source_state()
        self.assertEqual(provenance.restore_messages(saved, target, lambda _: False, tool_history=history), [])
        self.assertEqual(target, provenance.new_source_state())
        self.assertEqual(provenance.restore_messages(saved, target, lambda _: True), [],
                         'tool dependency accepted without current-user dispatcher')

    def test_untracked_read_or_creation_call_cannot_reuse_assistant_history(self):
        from tool_history import ToolHistory
        for name, arguments in [('list_meetings', {}), ('start_research', {'topic': 'test'})]:
            saved = session(provenance.new_source_state(), conversation('PRIVATE', name, arguments))
            self.assertEqual(provenance.restore_messages(
                saved, provenance.new_source_state(), lambda _: True,
                tool_history=ToolHistory('reader', {})), [])

    def test_account_access_revoke_and_read_failure_discard_prior_brief(self):
        from tool_history import CompleteRead, ToolHistory
        access = {'allowed': True}
        def brief(user, account_query):
            self.assertEqual(user, 'reader')
            if not access['allowed']:
                raise PermissionError('synthetic revoked membership')
            return CompleteRead({'account': account_query, 'insightsByType': {'risk': [{'text': 'PRIVATE'}]},
                                 'meetings': [], 'research': []})
        history = ToolHistory('reader', {'get_account_brief': brief})
        state = provenance.new_source_state()
        history.read(state, 'get_account_brief', {'account': 'Account'})
        saved = session(state, conversation('PRIVATE', 'get_account_brief', {'account': 'Account'}))
        access['allowed'] = False
        self.assertEqual(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: True, tool_history=history), [])

    def test_existing_tool_executor_uses_compact_current_user_callback_adapter(self):
        import test_handler  # Existing AWS-free import setup; production handler is unchanged.
        import tools
        history = self.tracker()
        self.assertTrue(hasattr(history, 'callbacks'))
        state = provenance.new_source_state()
        context = {'user_id': 'reader', **history.callbacks(state)}
        text, sources = tools.execute_tool('list_meetings', {'limit': 2}, context)
        self.assertIn('PRIVATE_RELEASE_PLAN', text)
        self.assertIn('m1', text)
        self.assertEqual(sources, [])
        self.assertEqual(len(state['dependencies']), 1)
        saved = session(state, conversation(text, arguments={'limit': 2}))
        self.assertTrue(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: False, tool_history=history))
        before = len(self.calls)
        with self.assertRaises(ValueError):
            context['list_meetings']('owner', limit=2)
        self.assertEqual(len(self.calls), before)

    def test_large_korean_limit100_list_keeps_answer_and_history(self):
        from tool_history import CompleteRead, ToolHistory
        import test_handler
        import tools
        rows = [{'meetingId': f'm{i}', 'title': '고객사 운영 검토 회의 ' * 20,
                 'date': '2026-09-12', 'status': 'done', 'isShared': False,
                 'tags': ['운영검토태그' + str(tag) for tag in range(40)]} for i in range(100)]
        self.assertGreater(len(json.dumps(rows, ensure_ascii=False).encode()), 65536)
        history = ToolHistory('reader', {'list_meetings': lambda user, **kw: CompleteRead(copy.deepcopy(rows))})
        state = provenance.new_source_state()
        view = history.read(state, 'list_meetings', {'limit': 100})
        self.assertEqual(len(view), 100)
        self.assertEqual(tools.format_meetings_results(view), tools.format_meetings_results(rows))
        self.assertTrue(state['replayable'])
        saved = session(state, conversation(tools.format_meetings_results(view), arguments={'limit': 100}))
        self.assertTrue(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: False, tool_history=history))

    def test_brief_hash_uses_actual_formatter_summary_extent(self):
        from tool_history import CompleteRead, ToolHistory
        import test_handler
        import tools
        value = {'account': '고객사', 'industry': '제조', 'insightsByType': {}, 'meetings': [],
                 'research': [{'topic': '보고서', 'status': 'done', 'summary': '공개 요약' * 100000}]}
        history = ToolHistory('reader', {'get_account_brief': lambda user, **kw: CompleteRead(copy.deepcopy(value))})
        state = provenance.new_source_state()
        view = history.read(state, 'get_account_brief', {'account': '고객사'})
        self.assertEqual(len(view['research'][0]['summary']), 200)
        self.assertEqual(tools.format_account_brief(view), tools.format_account_brief(value))
        saved = session(state, conversation('brief', 'get_account_brief', {'account': '고객사'}))
        value['research'][0]['summary'] = value['research'][0]['summary'][:200] + 'changed unshown suffix'
        self.assertTrue(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: False, tool_history=history))
        value['research'][0]['summary'] = 'Visible correction'
        self.assertEqual(provenance.restore_messages(
            saved, provenance.new_source_state(), lambda _: False, tool_history=history), [])

    def test_history_capacity_cannot_misreport_successful_research_creation(self):
        from tool_history import MAX_TOOL_DEPENDENCIES, ToolHistory
        history = ToolHistory('reader', {})
        state = provenance.new_source_state()
        for i in range(MAX_TOOL_DEPENDENCIES):
            history.research_receipt(state, {'topic': 'Synthetic'}, {'researchId': f'r{i}'})
        receipt = history.research_receipt(state, {'topic': 'New task'}, {'researchId': 'new-id'})
        self.assertEqual(receipt['researchId'], 'new-id')
        self.assertFalse(state['replayable'])
        self.assertEqual(state['toolHistoryCoverage'][0]['reason'], 'DEPENDENCY_LIMIT')
