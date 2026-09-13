"""Authenticated input receipts and fresh proofs of successful empty searches."""
from decimal import Decimal
import hashlib

from source_revision import HEX_REVISION, IDENTIFIER
from tool_history import fingerprint
from manual_kb import selected_source_keys

MAX_EMPTY_SEARCHES = 8
MAX_QUERY_BYTES = 4096


def is_request_dependency(dependency):
    return isinstance(dependency, dict) and ('clientInput' in dependency or 'emptySearch' in dependency)


def _identifier(value, optional=False):
    if optional and value == '':
        return value
    if type(value) is not str or not IDENTIFIER.fullmatch(value):
        raise ValueError('Invalid request receipt identity')
    return value


def _search(user_id, query, count, source_keys=None):
    _identifier(user_id)
    if (type(query) is not str or not query.strip() or len(query) > MAX_QUERY_BYTES
            or len(query.encode('utf-8')) > MAX_QUERY_BYTES):
        raise ValueError('Query exceeds receipt budget')
    if type(count) is Decimal:
        if not count.is_finite() or count != count.to_integral_value():
            raise ValueError('Invalid search result limit')
        count = 10 if count >= 10 else int(count)
    if type(count) is not int or count < 1:
        raise ValueError('Invalid search result limit')
    value = {'userId': user_id, 'query': query, 'count': min(count, 10)}
    if source_keys is not None:
        value['sourceKeys'] = selected_source_keys(user_id, source_keys)
        if len(value['sourceKeys']) > value['count']:
            raise ValueError('Result limit must cover every selected source')
    return value


def _dependency(kind, value):
    return {kind: value, 'sourceRevision': fingerprint([kind + '-v1', value])}


def valid_request_dependency(dependency):
    try:
        kind = 'clientInput' if 'clientInput' in dependency else 'emptySearch'
        if set(dependency) != {kind, 'sourceRevision'}:
            return False
        value = dependency[kind]
        if type(value) is not dict:
            return False
        if kind == 'clientInput':
            if set(value) != {'userId', 'meetingId', 'contentHash'}:
                return False
            _identifier(value['userId'])
            _identifier(value['meetingId'], optional=True)
            if type(value['contentHash']) is not str or not HEX_REVISION.fullmatch(value['contentHash']):
                return False
        else:
            if set(value) not in ({'userId', 'query', 'count'}, {'userId', 'query', 'count', 'sourceKeys'}):
                return False
            if value != _search(value['userId'], value['query'], value['count'], value.get('sourceKeys')):
                return False
        return dependency == _dependency(kind, value)
    except (KeyError, TypeError, ValueError, UnicodeError):
        return False


def request_key(dependency):
    if 'clientInput' in dependency:
        value = dependency['clientInput']
        return ('client-input', value['userId'], value['meetingId'], value['contentHash'])
    return ('empty-search', dependency['sourceRevision'])


def request_is_current(user_id, dependency, search, request_meeting_id=None):
    if not valid_request_dependency(dependency):
        return False
    value = dependency.get('clientInput', dependency.get('emptySearch'))
    if value['userId'] != user_id:
        return False
    if 'clientInput' in dependency:
        # Historical user input, not a claim about a saved source's current bytes.
        return value['meetingId'] == ('' if request_meeting_id is None else request_meeting_id)
    selection = {'source_keys': value['sourceKeys']} if 'sourceKeys' in value else {}
    results = search(value['query'], int(value['count']), user_id=user_id, **selection)
    return type(results) is list and not results


def mark_untracked(state, tool, reason):
    state['replayable'] = False
    entry = {'tool': tool, 'complete': False, 'reason': reason}
    coverage = state.setdefault('toolHistoryCoverage', [])
    if entry not in coverage:
        coverage.append(entry)


def _remember(state, dependency, tool):
    from session_provenance import MAX_SESSION_DEPENDENCIES, remember_source
    key = request_key(dependency)
    known = any(is_request_dependency(dep) and request_key(dep) == key for dep in state['dependencies'])
    if not known and (len(state['dependencies']) >= MAX_SESSION_DEPENDENCIES
                      or 'emptySearch' in dependency and
                      sum('emptySearch' in dep for dep in state['dependencies']) >= MAX_EMPTY_SEARCHES):
        mark_untracked(state, tool, 'DEPENDENCY_LIMIT')
        return False
    return remember_source(state, dependency) is not False


def remember_client_input(state, user_id, meeting_id, text):
    """Record only a digest; callers still record and revalidate every saved source."""
    try:
        _identifier(user_id)
        meeting_id = _identifier('' if meeting_id is None else meeting_id, optional=True)
        if type(text) is not str or not text:
            raise ValueError('Client transcript must be nonempty text')
        value = {'userId': user_id, 'meetingId': meeting_id,
                 'contentHash': hashlib.sha256(text.encode('utf-8')).hexdigest()}
        state['clientInputReceived'] = True
        return _remember(state, _dependency('clientInput', value), 'search_transcript')
    except (TypeError, ValueError, UnicodeError):
        mark_untracked(state, 'search_transcript', 'RECEIPT_UNAVAILABLE')
        return False


def remember_empty_search(state, user_id, query, count, source_keys=None):
    """Call only after a successful empty read; unavailable reads are never empty."""
    try:
        return _remember(state, _dependency('emptySearch', _search(user_id, query, count, source_keys)),
                         'search_knowledge_base')
    except (TypeError, ValueError, UnicodeError):
        mark_untracked(state, 'search_knowledge_base', 'RECEIPT_UNAVAILABLE')
        return False
