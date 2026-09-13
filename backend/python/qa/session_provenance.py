"""Source dependencies live beside session messages, never in Converse blocks."""
from decimal import Decimal
import json

from source_revision import HEX_REVISION, IDENTIFIER, resource_identity
from manual_kb import shared_source_key
from tool_history import (
    MAX_TOOL_DEPENDENCIES, HistoryLimit, is_tool_dependency, valid_tool_dependency, tool_dependency_key, covers_tool_calls,
)
from request_history import MAX_EMPTY_SEARCHES, is_request_dependency, valid_request_dependency, request_key, mark_untracked

MAX_SESSION_DEPENDENCIES = 128
MAX_HISTORY_BYTES = 384 * 1024
MAX_PUBLIC_TITLE_CHARS = 256


class SourceValidationError(RuntimeError):
    code = 'SOURCE_CHANGED'
    status = 409
    message = 'Source access or content changed or is no longer verifiable. Ask again with current sources.'

    def __init__(self):
        super().__init__(self.message)


class SourceUnavailable(SourceValidationError):
    code = 'SOURCE_UNAVAILABLE'
    status = 503
    message = 'Current sources could not be verified. Try again later.'


def new_source_state():
    return {'dependencies': [], 'replayable': True}


def valid_dependency(dependency):
    if not isinstance(dependency, dict):
        return False
    if is_tool_dependency(dependency):
        return valid_tool_dependency(dependency)
    if is_request_dependency(dependency):
        return valid_request_dependency(dependency)
    try:
        revision = dependency.get('sourceRevision')
        if not isinstance(revision, str) or HEX_REVISION.fullmatch(revision) is None:
            return False
        if 'legacyURI' in dependency:
            return (set(dependency) == {'legacyURI', 'sourceRevision'} and
                    isinstance(dependency['legacyURI'], str) and len(dependency['legacyURI']) <= 2048)
        if 'manualKey' in dependency:
            return (set(dependency) == {'manualKey', 'sourceRevision'}
                    and isinstance(dependency['manualKey'], str) and len(dependency['manualKey'].encode()) <= 1024)
        if 'sharedKey' in dependency:
            return set(dependency) == {'sharedKey', 'sourceRevision'} and shared_source_key(dependency['sharedKey'])
        resource_identity(dependency.get('sourcePK'), dependency.get('sourceSK'))
        attachment_id = dependency.get('attachmentId')
        if attachment_id is not None and (not dependency['sourceSK'].startswith('MEETING#')
                or not isinstance(attachment_id, str)
                or attachment_id != '*' and not IDENTIFIER.fullmatch(attachment_id)):
            return False
        return True
    except ValueError:
        return False


def _dependency_key(dependency):
    if is_tool_dependency(dependency):
        return tool_dependency_key(dependency)
    if is_request_dependency(dependency):
        return request_key(dependency)
    if 'legacyURI' in dependency:
        return ('legacy', dependency['legacyURI'])
    if 'manualKey' in dependency:
        return ('manual', dependency['manualKey'])
    if 'sharedKey' in dependency:
        return ('shared', dependency['sharedKey'])
    return ('canonical', dependency['sourcePK'], dependency['sourceSK'], dependency.get('attachmentId'))


def remember_source(state, dependency):
    if not valid_dependency(dependency):
        raise ValueError('Invalid source dependency')
    key = _dependency_key(dependency)
    for saved in state['dependencies']:
        if _dependency_key(saved) == key:
            if saved != dependency:
                raise RuntimeError('Source changed during this answer; retry with current content.')
            return
    if 'emptySearch' in dependency and (
            sum('emptySearch' in dep for dep in state['dependencies']) >= MAX_EMPTY_SEARCHES
            or len(state['dependencies']) >= MAX_SESSION_DEPENDENCIES):
        mark_untracked(state, 'search_knowledge_base', 'DEPENDENCY_LIMIT')
        return False
    if (len(state['dependencies']) >= MAX_SESSION_DEPENDENCIES
            or is_tool_dependency(dependency) and
            sum(is_tool_dependency(dep) for dep in state['dependencies']) >= MAX_TOOL_DEPENDENCIES):
        state['replayable'] = False
        raise HistoryLimit('Source history dependency budget exceeded')
    state['dependencies'].append(dict(dependency))


def _current(dependency, is_current, tool_history):
    if is_tool_dependency(dependency):
        return tool_history is not None and tool_history.is_current(dependency)
    return is_current(dependency)


def _valid_dependencies(dependencies):
    return (type(dependencies) is list and len(dependencies) <= MAX_SESSION_DEPENDENCIES
            and sum(is_tool_dependency(dep) for dep in dependencies) <= MAX_TOOL_DEPENDENCIES
            and sum(isinstance(dep, dict) and 'emptySearch' in dep for dep in dependencies) <= MAX_EMPTY_SEARCHES
            and all(valid_dependency(dep) for dep in dependencies))


def restore_sources(item, state, is_current, *, tool_history=None):
    """Legacy/untracked histories cannot prove absence of private derived text."""
    dependencies = item.get('sourceDependencies')
    # DynamoDB resource reads deserialize all Number attributes as Decimal.
    version = item.get('sourceProvenanceVersion')
    valid_version = (type(version) is int or isinstance(version, Decimal) and version.is_finite())
    if (not valid_version or version != 1
            or item.get('sourceReplayable') is not True
            or not _valid_dependencies(dependencies)):
        return False
    try:
        if not all(_current(dep, is_current, tool_history) for dep in dependencies):
            return False
        candidate = {'dependencies': list(state['dependencies']), 'replayable': state['replayable']}
        for dependency in dependencies:
            if remember_source(candidate, dependency) is False:
                return False
    except Exception:
        return False
    state['dependencies'] = candidate['dependencies']
    return True


def validate_sources(state, is_current, *, tool_history=None):
    try:
        valid = (_valid_dependencies(state['dependencies'])
                 and all(_current(dep, is_current, tool_history) for dep in state['dependencies']))
    except Exception:
        state['replayable'] = False
        raise SourceUnavailable() from None
    if not valid:
        state['replayable'] = False
        raise SourceValidationError()


def restore_messages(item, state, is_current, *, tool_history=None,
                     source_covered_tools=(), public_tools=()):
    """All-or-nothing replay: never keep paraphrases after removing stale tools."""
    try:
        raw = item.get('messages')
        if type(raw) is not str or len(raw) > MAX_HISTORY_BYTES or len(raw.encode()) > MAX_HISTORY_BYTES:
            return []
        messages = json.loads(raw)
        if (type(messages) is not list or len(messages) > 100
                or any(type(msg) is not dict or msg.get('role') not in ('user', 'assistant')
                       or type(msg.get('content')) is not list for msg in messages)):
            return []
        dependencies = item.get('sourceDependencies')
        if not _valid_dependencies(dependencies) or not covers_tool_calls(
                messages, dependencies, source_covered_tools=source_covered_tools, public_tools=public_tools):
            return []
        if not restore_sources(item, state, is_current, tool_history=tool_history):
            return []
        return messages
    except (ValueError, TypeError, UnicodeError, RecursionError):
        return []


def collect_detail(details, detail):
    if not detail:
        return
    public = dict(detail)
    title = public.get('title')
    if isinstance(title, str) and len(title) > MAX_PUBLIC_TITLE_CHARS:
        public['title'] = title[:MAX_PUBLIC_TITLE_CHARS]
        public['titleTruncated'] = True
    evidence = {key: value for key, value in public.items() if key != 'provenanceScope'}
    for index, previous in enumerate(details):
        if {key: value for key, value in previous.items() if key != 'provenanceScope'} == evidence:
            if 'provenanceScope' not in public:
                details[index] = public
            return
    details.append(public)
