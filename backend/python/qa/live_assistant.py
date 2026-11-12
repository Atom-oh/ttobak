"""Bounded question detection and explicit automatic-answer tool policy."""
import json
import re

# An allowlist keeps future mutation/egress tools out of automatic turns.
READ_ONLY_TOOLS = frozenset({
    'search_transcript', 'search_knowledge_base', 'get_meeting_detail',
    'list_meetings', 'list_accounts', 'get_account_insights', 'get_account_brief',
    'get_legacy_text_detail', 'get_document_detail',
    'get_meeting_attachments', 'get_attachment_text',
})
EXTERNAL_TOOLS = frozenset({'search_web', 'search_aws_docs', 'get_aws_recommendation'})

AUTOMATIC_ANSWER_PROMPT = (
    '현재 미팅 발언에서 나온 질문에 자동으로 답하는 읽기 전용 턴입니다. '
    '미팅 제목, 배경, 최근 발언을 바탕으로 질문의 대상을 파악하고 핵심 답변을 먼저 짧게 제시하세요. '
    '현재 대화에서 이미 답한 내용을 반복하지 마세요. 참여자를 대신해 결정하거나 작업을 생성하지 마세요. '
    '근거가 부족하면 추측하지 말고 확인이 필요한 사항을 명확히 알려주세요. '
    '허용된 도구만 사용하고 외부 검색이 비활성화되어 있으면 최신 사실을 확인했다고 주장하지 마세요.'
)


def automatic_tool_names(allow_web_search=False):
    return READ_ONLY_TOOLS | (EXTERNAL_TOOLS if allow_web_search is True else frozenset())


def parse_live_questions(raw):
    if not isinstance(raw, str) or len(raw.encode('utf-8')) > 16000:
        return [], [], []
    try:
        text = raw.strip()
        fence = re.fullmatch(r'```[A-Za-z]*\s*(.*?)\s*```', text, re.S)
        parsed = json.loads(fence.group(1) if fence else text)
    except (ValueError, TypeError):
        return [], [], []
    if not isinstance(parsed, list):
        return [], [], []
    questions, proactive, automatic, seen = [], [], [], set()
    for item in parsed[:30]:
        q = item if isinstance(item, str) else item.get('q') if isinstance(item, dict) else None
        if not isinstance(q, str) or not q.strip() or len(q) > 500:
            continue
        q = q.strip()
        if q in seen:
            continue
        seen.add(q)
        questions.append(q)
        if isinstance(item, dict):
            if item.get('search') is True:
                proactive.append(q)
            if item.get('spoken') is True and item.get('answerable') is True:
                automatic.append(q)
        if len(questions) == 5:
            break
    return questions, proactive, automatic
