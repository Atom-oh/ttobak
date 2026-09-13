"""ConverseStream text starts with deltas; block-start is only required for tools."""
import json
import unittest
from unittest import mock

import test_handler

handler = test_handler.handler


def text_stream(text, stop='end_turn'):
    events = [{'messageStart': {'role': 'assistant'}}]
    for part in (text[:2], text[2:]):
        events.append({'contentBlockDelta': {'contentBlockIndex': 0, 'delta': {'text': part}}})
    events.append({'contentBlockStop': {'contentBlockIndex': 0}})
    if stop is not None:
        events.append({'messageStop': {'stopReason': stop}})
    return {'stream': events}


class TestRealStreamEvents(unittest.TestCase):
    def setUp(self):
        self.table = test_handler.RetrievalTable()
        for patcher in (
            mock.patch.object(handler, 'table', self.table),
            mock.patch.object(handler, '_request_meeting_context', return_value=(None, None, None)),
            mock.patch.object(handler, '_apigw_client'),
            mock.patch.object(handler, '_post_ws', return_value=True),
        ):
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = mock.patch.object(handler, 'bedrock_runtime')
        self.model = patcher.start()
        self.addCleanup(patcher.stop)
        self.event = {'question': 'synthetic question', 'userId': 'reader', 'sessionId': 'stream-fixture',
                      'connectionId': 'c', 'endpoint': 'https://synthetic.invalid'}

    def frames(self):
        return [call.args[2] for call in handler._post_ws.call_args_list]

    def test_text_without_block_start_streams_persists_and_replays_exact_text(self):
        answer = '현재 답변과 출처를 유지합니다.'
        self.model.converse_stream.return_value = text_stream(answer)
        self.assertEqual(handler.handle_ask_stream(self.event), {'status': 'ok'})
        frames = self.frames()
        self.assertEqual(''.join(frame['text'] for frame in frames if frame['type'] == 'answer_delta'), answer)
        self.assertEqual(frames[-1]['answer'], answer)
        stored = self.table.items[('SESSION#reader#stream-fixture', 'MESSAGES')]
        self.assertEqual(json.loads(stored['messages'])[-1]['content'], [{'text': answer}])
        history = handler.load_session('stream-fixture', user_id='reader')
        self.assertEqual(history[-1]['content'], [{'text': answer}])

    def test_empty_truncated_or_unterminated_stream_is_not_a_success_or_saved_history(self):
        for stream in (text_stream(''), text_stream('partial', None), text_stream('partial', 'max_tokens')):
            with self.subTest(stream=stream):
                self.table.items.clear()
                handler._post_ws.reset_mock()
                self.model.converse_stream.return_value = stream
                result = handler.handle_ask_stream(self.event)
                self.assertNotEqual(result.get('status'), 'ok')
                self.assertEqual(self.frames()[-1]['type'], 'answer_error')
                self.assertFalse(any(frame['type'] == 'answer_complete' for frame in self.frames()))
                self.assertNotIn(('SESSION#reader#stream-fixture', 'MESSAGES'), self.table.items)

    def test_failure_after_completed_mutation_preserves_receipt_without_repeating_it(self):
        tool = {'toolUseId': 'research-1', 'name': 'start_research'}
        first = {'stream': [
            {'messageStart': {'role': 'assistant'}},
            {'contentBlockStart': {'contentBlockIndex': 0, 'start': {'toolUse': tool}}},
            {'contentBlockDelta': {'contentBlockIndex': 0, 'delta': {
                'toolUse': {'input': '{"topic":"synthetic","mode":"standard"}'}}}},
            {'contentBlockStop': {'contentBlockIndex': 0}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}
        self.model.converse_stream.side_effect = [first, text_stream('')]
        with mock.patch.object(handler, 'check_research_limit', return_value=True), \
                mock.patch.object(handler, 'create_research_from_chat',
                                  return_value={'researchId': 'a' * 32}) as create:
            result = handler.handle_ask_stream(self.event)
            self.assertEqual(result['status'], 'model_failed')
            create.assert_called_once()
            history = handler.load_session('stream-fixture', user_id='reader')
            self.assertIn('a' * 32, json.dumps(history))
            self.assertIn('이전 도구 실행 결과', history[-1]['content'][0]['text'])
            self.assertTrue(all(message['content'] for message in history))
            create.assert_called_once()
        self.assertIs(self.frames()[-1].get('sessionContinuable'), True)
        self.assertEqual(self.frames()[-1]['code'], 'MODEL_STREAM_EMPTY')
        self.assertFalse(any(frame['type'] == 'answer_complete' for frame in self.frames()))

    def test_unconfirmed_receipt_write_never_claims_session_can_continue(self):
        first = {'stream': [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': 'r', 'name': 'start_research'}}}},
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"topic":"synthetic"}'}}}},
            {'contentBlockStop': {}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}
        self.model.converse_stream.side_effect = [first, text_stream('')]
        put = self.table.put_item
        def reject_messages(**kwargs):
            if kwargs['Item']['SK'] == 'MESSAGES':
                raise TimeoutError('synthetic unconfirmed write')
            return put(**kwargs)
        with mock.patch.object(handler, 'check_research_limit', return_value=True), \
                mock.patch.object(handler, 'create_research_from_chat', return_value={'researchId': 'e' * 32}), \
                mock.patch.object(self.table, 'put_item', side_effect=reject_messages):
            self.assertEqual(handler.handle_ask_stream(self.event)['status'], 'model_failed')
        self.assertIs(self.frames()[-1].get('sessionContinuable'), False)

    def test_stream_request_failure_is_explicit_without_saving_an_empty_answer(self):
        self.model.converse_stream.side_effect = RuntimeError('synthetic unavailable stream')
        result = handler.handle_ask_stream(self.event)
        self.assertEqual(result, {'status': 'model_failed', 'code': 'MODEL_STREAM_UNAVAILABLE'})
        self.assertEqual(self.frames()[-1]['type'], 'answer_error')
        self.assertNotIn(('SESSION#reader#stream-fixture', 'MESSAGES'), self.table.items)

    def test_iterator_failure_after_mutation_preserves_receipt_and_closes_stream(self):
        first = {'stream': [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': 'r', 'name': 'start_research'}}}},
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"topic":"synthetic"}'}}}},
            {'contentBlockStop': {}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}
        class InterruptedStream:
            closed = False
            def __iter__(self):
                yield {'messageStart': {'role': 'assistant'}}
                yield {'contentBlockDelta': {'contentBlockIndex': 0, 'delta': {'text': 'PARTIAL_UNSAVED'}}}
                raise RuntimeError('synthetic upstream detail must stay private')
            def close(self):
                self.closed = True
        stream = InterruptedStream()
        self.model.converse_stream.side_effect = [first, {'stream': stream}]
        with mock.patch.object(handler, 'check_research_limit', return_value=True), \
                mock.patch.object(handler, 'create_research_from_chat',
                                  return_value={'researchId': 'd' * 32}) as create:
            result = handler.handle_ask_stream(self.event)
            self.assertEqual(result, {'status': 'model_failed', 'code': 'MODEL_STREAM_UNAVAILABLE'})
            create.assert_called_once()
            history = handler.load_session('stream-fixture', user_id='reader')
            self.assertIn('d' * 32, json.dumps(history))
            self.assertNotIn('PARTIAL_UNSAVED', json.dumps(history))
        self.assertTrue(stream.closed)
        self.assertEqual(self.frames()[-1]['type'], 'answer_error')
        self.assertNotIn('upstream detail', self.frames()[-1]['error'])
        self.assertFalse(any(frame['type'] == 'answer_complete' for frame in self.frames()))

    def test_empty_text_prefix_before_tool_does_not_poison_the_next_model_request(self):
        first = {'stream': [
            {'messageStart': {'role': 'assistant'}},
            {'contentBlockDelta': {'contentBlockIndex': 0, 'delta': {'text': ''}}},
            {'contentBlockStop': {'contentBlockIndex': 0}},
            {'contentBlockStart': {'contentBlockIndex': 1, 'start': {
                'toolUse': {'toolUseId': 'r', 'name': 'start_research'}}}},
            {'contentBlockDelta': {'contentBlockIndex': 1, 'delta': {
                'toolUse': {'input': '{"topic":"synthetic"}'}}}},
            {'contentBlockStop': {'contentBlockIndex': 1}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}
        self.model.converse_stream.side_effect = [first, text_stream('Created the synthetic request.')]
        with mock.patch.object(handler, 'check_research_limit', return_value=True), \
                mock.patch.object(handler, 'create_research_from_chat', return_value={'researchId': 'c' * 32}):
            self.assertEqual(handler.handle_ask_stream(self.event), {'status': 'ok'})
        messages = self.model.converse_stream.call_args.kwargs['messages']
        self.assertTrue(all(block['text'].strip() for message in messages
                            for block in message['content'] if 'text' in block))

    def test_exhausted_tool_budget_is_not_reported_as_a_completed_answer(self):
        first = {'stream': [
            {'contentBlockStart': {'start': {'toolUse': {'toolUseId': 'r', 'name': 'start_research'}}}},
            {'contentBlockDelta': {'delta': {'toolUse': {'input': '{"topic":"synthetic"}'}}}},
            {'contentBlockStop': {}},
            {'messageStop': {'stopReason': 'tool_use'}},
        ]}
        self.model.converse_stream.return_value = first
        with mock.patch.object(handler, 'MAX_TOOL_ROUNDS', 1), \
                mock.patch.object(handler, 'check_research_limit', return_value=True), \
                mock.patch.object(handler, 'create_research_from_chat',
                                  return_value={'researchId': 'b' * 32}) as create:
            self.assertEqual(handler.handle_ask_stream(self.event),
                             {'status': 'model_failed', 'code': 'MODEL_TOOL_ROUND_LIMIT'})
            create.assert_called_once()
            self.model.converse_stream.assert_called_once()
            self.assertIn('b' * 32, json.dumps(handler.load_session('stream-fixture', user_id='reader')))
        self.assertFalse(any(frame['type'] == 'answer_complete' for frame in self.frames()))


if __name__ == '__main__':
    unittest.main()
