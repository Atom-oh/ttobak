"""Spoken-question selection and automatic read-only QA policy."""
import json
import unittest
from unittest import mock

from live_assistant import parse_live_questions, automatic_tool_names


class LiveAssistantTests(unittest.TestCase):
    def test_only_explicit_spoken_answerable_questions_are_automatic(self):
        data = [
            {'q': '납기는 언제인가요?', 'spoken': True, 'answerable': True, 'search': False},
            {'q': '최신 지원 버전은?', 'spoken': True, 'answerable': True, 'search': True},
            {'q': '누구를 담당자로 정할까요?', 'spoken': True, 'answerable': False},
            {'q': '제안한 후속 질문', 'spoken': False, 'answerable': True, 'search': True},
            {'q': '레거시 검색 질문', 'search': True},
        ]
        questions, proactive, automatic = parse_live_questions(json.dumps(data))
        self.assertEqual(len(questions), 5)
        self.assertEqual(automatic, ['납기는 언제인가요?', '최신 지원 버전은?'])
        self.assertIn('제안한 후속 질문', proactive)

    def test_legacy_malformed_and_oversized_output_cannot_enable_automatic_answers(self):
        self.assertEqual(parse_live_questions('["질문"]'), (['질문'], [], []))
        self.assertEqual(parse_live_questions('not json'), ([], [], []))
        data = [
            {'q': 'x' * 501, 'spoken': True, 'answerable': True},
            {'q': '반복 질문', 'spoken': False},
            {'q': '반복 질문', 'spoken': True, 'answerable': True},
            {'q': '문자열 플래그', 'spoken': 'true', 'answerable': 'true'},
        ]
        questions, _, automatic = parse_live_questions(json.dumps(data))
        self.assertEqual(questions, ['반복 질문', '문자열 플래그'])
        self.assertEqual(automatic, [])

    def test_code_fenced_output_keeps_spoken_flags_and_legacy_safety(self):
        for prefix in ('```json\n', '```\n'):
            data = '[{"q":"납기는?","spoken":true,"answerable":true,"search":false}]'
            self.assertEqual(parse_live_questions(prefix + data + '\n```'), (['납기는?'], [], ['납기는?']))
            self.assertEqual(parse_live_questions(prefix + '["납기는?"]\n```'), (['납기는?'], [], []))

    def test_automatic_tools_are_read_only_and_external_search_requires_opt_in(self):
        default = automatic_tool_names(False)
        self.assertIn('search_transcript', default)
        self.assertIn('search_knowledge_base', default)
        for tool in ('start_research', 'search_web', 'search_aws_docs', 'get_aws_recommendation'):
            self.assertNotIn(tool, default)
        opted_in = automatic_tool_names(True)
        self.assertIn('search_web', opted_in)
        self.assertIn('search_aws_docs', opted_in)
        self.assertNotIn('start_research', opted_in)

    def test_dispatch_rejects_forbidden_tools_before_any_callback(self):
        from tools import execute_tool
        with mock.patch('tools.gateway_web_search') as web, mock.patch('tools.search_aws_docs') as docs:
            create = mock.Mock()
            context = {'allowed_tools': automatic_tool_names(False), 'create_research': create,
                       'user_id': 'alice', 'check_web_search_limit': lambda _: True}
            for name in ('search_web', 'search_aws_docs', 'start_research'):
                text, sources = execute_tool(name, {'query': 'private', 'topic': 'private'}, context)
                self.assertIn('자동', text)
                self.assertEqual(sources, [])
            web.assert_not_called()
            docs.assert_not_called()
            create.assert_not_called()

    def test_async_request_keeps_and_validates_automatic_policy(self):
        from async_jobs import request_data, JobError
        base = {'question': '납기는?', 'context': '미팅', 'automatic': True, 'allowWebSearch': False}
        request = request_data(base)
        self.assertTrue(request['automatic'])
        self.assertFalse(request['allowWebSearch'])
        for change in ({'automatic': 'true'}, {'allowWebSearch': 'false'}):
            with self.assertRaises(JobError):
                request_data(dict(base, **change))
        manual = request_data({'question': '질문'})
        self.assertNotIn('automatic', manual)


class LiveAssistantHandlerTests(unittest.TestCase):
    def test_detection_uses_meeting_background_and_returns_separate_automatic_candidates(self):
        import test_handler as helpers
        handler = helpers.handler
        output = json.dumps([
            {'q': '납기는 언제인가요?', 'spoken': True, 'answerable': True},
            {'q': '추가 확인할 질문은?', 'spoken': False, 'answerable': True},
        ])
        with mock.patch.object(handler.bedrock_runtime, 'converse', return_value={
                'output': {'message': {'content': [{'text': output}]}}}) as model:
            result = handler.handle_detect_questions({
                'transcript': '가' * 10000, 'summary': '요약' * 5000,
                'meetingTitle': '고객 제안', 'meetingContext': '예산 검토',
                'previousQuestions': ['이미 답한 질문'],
            })
        data = json.loads(result['body'])
        self.assertEqual(data['automatic'], ['납기는 언제인가요?'])
        input_data = json.loads(model.call_args.kwargs['messages'][0]['content'][0]['text'])
        self.assertEqual(input_data['meetingTitle'], '고객 제안')
        self.assertEqual(input_data['meetingContext'], '예산 검토')
        self.assertEqual(len(input_data['recentTranscript']), 6000)
        self.assertEqual(len(input_data['summary']), 3000)
        self.assertEqual(input_data['answeredQuestions'], ['이미 답한 질문'])
        self.assertNotIn('temperature', model.call_args.kwargs['inferenceConfig'])
        for body in ({'transcript': ['invalid']}, {'transcript': '질문', 'previousQuestions': 'invalid'}):
            self.assertEqual(handler.handle_detect_questions(body)['statusCode'], 400)

    def test_automatic_handler_policy_survives_history_loading_and_filters_model_tools(self):
        import test_handler as helpers
        from session_provenance import new_source_state
        from tool_history import ToolHistory
        handler = helpers.handler
        model = mock.Mock()
        model.converse.return_value = {
            'stopReason': 'end_turn',
            'output': {'message': {'role': 'assistant', 'content': [{'text': '확인된 납기는 다음 주입니다.'}]}},
        }
        state = new_source_state()

        def context(user, transcript, notes, source_state, details):
            self.assertTrue(source_state['automaticAnswer'])
            self.assertFalse(source_state['allowAutomaticWebSearch'])
            return {'tool_history': ToolHistory(user, {}),
                    'allowed_tools': automatic_tool_names(source_state['allowAutomaticWebSearch'])}

        def history(*args, **kwargs):
            # A prior manual turn must not transfer its policy.
            kwargs['source_state'].update(automaticAnswer=False, allowAutomaticWebSearch=True)
            return []

        with mock.patch.object(handler, '_request_meeting_context', return_value=('납기는 다음 주', None, None)), \
                mock.patch.object(handler, 'load_session', side_effect=history), \
                mock.patch.object(handler, '_agent_context', side_effect=context), \
                mock.patch.object(handler, '_validate_answer_sources'), \
                mock.patch.object(handler, 'save_session', return_value=True):
            result = handler.handle_ask('납기는?', user_id='reader', source_state=state,
                                        model_client=model, automatic=True, allow_web_search=False)
        self.assertEqual(result['statusCode'], 200, result)
        names = {tool['toolSpec']['name'] for tool in model.converse.call_args.kwargs['toolConfig']['tools']}
        self.assertIn('search_transcript', names)
        self.assertNotIn('start_research', names)
        self.assertNotIn('search_web', names)
        self.assertNotIn('search_aws_docs', names)
        system = model.converse.call_args.kwargs['system']
        self.assertTrue(any('자동으로 답하는 읽기 전용 턴' in entry['text'] for entry in system))


if __name__ == '__main__':
    unittest.main()
