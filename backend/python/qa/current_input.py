"""Per-turn input metadata; never a source authorization or saved-byte proof."""
import hashlib
import json
import re

PREFIX = 'Request input receipt (server-generated metadata):\n'
GUIDANCE = (
    'Each user question has a separate request input receipt. scope=this_user_turn describes '
    'the user turn containing that receipt. The latest receipt describes the current request, '
    'even when it is followed by tool-result messages. clientContextReceived=true means the '
    'client supplied meeting_context on that turn; do not say it was absent because the question '
    'does not repeat it, no tool ran, or earlier dialogue lacks the system context. '
    'The system meeting_context JSON is rebuilt for the latest request only; do not backdate '
    'its current text to earlier turns. Historical receipts describe only their own input snapshot. '
    'clientSnapshotChange=changed means input bytes changed, which may be growth, a rolling window, '
    'or a client correction; it does not by itself mean an earlier assistant answer was wrong. '
    'Attribute updated values to the current client input, not to a supposed earlier assistant error. '
    'Do not invent a correction history when prior input was unrecorded. '
    'Receipts and digests attest input presence, not content truth or saved-source verification. '
    'Keep client input, validated saved sources, and conversation labels distinct. '
    'Use receipts to interpret the request, without reciting bookkeeping unless asked.'
)


def _previous_receipt(history):
    for message in reversed(history):
        if not isinstance(message, dict) or message.get('role') != 'user':
            continue
        content = message.get('content', [])
        if type(content) is not list or not all(isinstance(block, dict) for block in content):
            return None
        if any('toolResult' in block for block in content):
            continue
        # The user's question occupies block zero. A lookalike inside it is
        # never parsed as server bookkeeping, including in legacy sessions.
        if len(content) != 2 or set(content[1]) != {'text'}:
            return None
        text = content[1]['text']
        try:
            if type(text) is not str or not text.startswith(PREFIX) or len(text.encode()) > 2048:
                return None
            value = json.loads(text[len(PREFIX):])
        except (ValueError, TypeError, UnicodeError):
            return None
        fields = {'version', 'scope', 'meetingId', 'contextKind', 'clientContextReceived',
                  'contextCharacters', 'contextSHA256', 'clientSnapshotChange'}
        if (type(value) is not dict or set(value) != fields or type(value['version']) is not int
                or value['version'] != 1 or value['scope'] != 'this_user_turn'
                or type(value['meetingId']) is not str or type(value['clientContextReceived']) is not bool
                or value['contextKind'] not in ('client_live', 'saved_meeting', 'none')
                or value['clientContextReceived'] != (value['contextKind'] == 'client_live')
                or type(value['contextCharacters']) is not int or value['contextCharacters'] < 0
                or type(value['contextSHA256']) is not str
                or (value['contextSHA256'] != '' if value['contextKind'] == 'none'
                    else re.fullmatch(r'[0-9a-f]{64}', value['contextSHA256']) is None)):
            return None
        return value
    return None


def request_user_message(question, history, transcript=None, meeting_id=None, *, client_input_received=False):
    """Keep earlier turns intact and identify the input attached to this one."""
    text = transcript if type(transcript) is str else ''
    received = client_input_received is True and bool(text)
    kind = 'client_live' if received else 'saved_meeting' if text else 'none'
    digest = hashlib.sha256(text.encode('utf-8')).hexdigest() if text else ''
    previous = _previous_receipt(history)
    change = 'not_client_input'
    if received:
        if previous is None:
            change = 'prior_unrecorded' if history else 'first_recorded'
        elif previous['contextKind'] == 'client_live' and previous['meetingId'] == (meeting_id or ''):
            change = 'unchanged' if previous['contextSHA256'] == digest else 'changed'
        else:
            change = 'newly_supplied'
    receipt = {
        'version': 1, 'scope': 'this_user_turn', 'meetingId': meeting_id or '',
        'contextKind': kind, 'clientContextReceived': received,
        'contextCharacters': len(text), 'contextSHA256': digest, 'clientSnapshotChange': change,
    }
    return {'role': 'user', 'content': [
        {'text': question},
        {'text': PREFIX + json.dumps(receipt, ensure_ascii=False, separators=(',', ':'))},
    ]}
