"""Shared QA callbacks; current-user reads and per-call history coverage."""
from request_history import remember_empty_search, mark_untracked
from session_provenance import collect_detail, new_source_state, remember_source
from source_tools import SOURCE_TOOL_NAMES
from tool_history import READONLY_TOOLS, HistoryLimit
from manual_kb import selected_source_keys

SOURCE_HISTORY_TOOLS = SOURCE_TOOL_NAMES | {'search_knowledge_base', 'get_meeting_detail', 'search_transcript'}
PUBLIC_HISTORY_TOOLS = frozenset(('search_web', 'search_aws_docs', 'get_aws_recommendation'))
CLIENT_LIVE_NOTE = ('client_live: The meeting_context block is user-provided input for this request. '
                    'Use the latest request input when it corrects earlier live context. '
                    'It is not a verification of saved-source bytes.')


def build_tool_context(user_id, text, source_state, source_details, *, source_access, history,
                       create_research, check_research_limit, check_web_search_limit):
    if history.user_id != user_id:
        raise ValueError('Tool history differs from current user')
    context = {}

    def read_source(tool_name, callback, *args):
        current = new_source_state()
        try:
            value = callback(*args, source_state=current, source_details=source_details)
            recorded = True
            for dependency in current['dependencies']:
                try:
                    accepted = remember_source(source_state, dependency)
                except HistoryLimit:
                    mark_untracked(source_state, tool_name, 'DEPENDENCY_LIMIT')
                    accepted = False
                if accepted is False:
                    recorded = False
            if recorded and current['dependencies'] and current['replayable']:
                context['sourceReadRecorded'] = True
            else:
                source_state['replayable'] = False
            for coverage in current.get('toolHistoryCoverage', []):
                if coverage not in source_state.setdefault('toolHistoryCoverage', []):
                    source_state['toolHistoryCoverage'].append(coverage)
            return value
        except Exception:
            source_state['replayable'] = False
            raise

    def bound_read(tool_name, callback, uid, *args):
        if uid != user_id:
            source_state['replayable'] = False
            raise ValueError('Source caller differs from current user')
        return read_source(tool_name, callback, uid, *args)

    def create(uid, topic, mode):
        if uid != user_id:
            raise ValueError('Research caller differs from current user')
        created = create_research(uid, topic, mode)
        if not created.get('error'):
            history.research_receipt(source_state, {'topic': topic, 'mode': mode}, created)
        return created

    def retrieve(query, count=5, source_keys=None, *, source_state, source_details):
        selection = {'source_keys': source_keys} if source_keys is not None else {}
        results = source_access.retrieve_from_kb(query, count, user_id=user_id, **selection)
        if type(results) is not list:
            raise ValueError('Invalid source search response')
        if source_keys is not None:
            found = {result.get('provenance', {}).get('sourceKey') for result in results}
            for key in selected_source_keys(user_id, source_keys):
                if key not in found:
                    remember_empty_search(source_state, user_id, query, count, [key])
        elif not results:
            remember_empty_search(source_state, user_id, query, count, source_keys)
        for result in results:
            if result.get('dependency'):
                remember_source(source_state, result['dependency'])
            else:
                source_state['replayable'] = False
            for dependency in result.get('additionalDependencies', []):
                remember_source(source_state, dependency)
            if result.get('volatile'):
                source_state['replayable'] = False
            detail = dict(result.get('provenance') or {})
            if detail.get('resourceKind') == 'legacyText':
                detail['partial'] = detail.get('partial', False) or len(result.get('text', '')) > 2400
            collect_detail(source_details, detail)
            for detail in result.get('additionalSourceDetails', []):
                collect_detail(source_details, detail)
        return results

    context.update({
        'transcript': text,
        'retrieve_from_kb': lambda query, count=5, source_keys=None: read_source(
            'search_knowledge_base', retrieve, query, count, source_keys),
        **history.callbacks(source_state),
        'tool_history': history,
        'load_meeting_context': lambda uid, mid: bound_read('get_meeting_detail', source_access.load_meeting_context, uid, mid),
        'load_document_context': lambda uid, pk, did: bound_read('get_document_detail', source_access.load_document_context, uid, pk, did),
        'load_legacy_text': lambda uid, uri, offset=0, revision=None: bound_read(
            'get_legacy_text_detail', source_access.load_legacy_text, uid, uri, offset, revision),
        'load_meeting_attachments': lambda uid, mid, offset=0: bound_read(
            'get_meeting_attachments', source_access.load_meeting_attachments, uid, mid, offset),
        'load_attachment_text': lambda uid, mid, aid, unit=0, text=0, revision=None: bound_read(
            'get_attachment_text', source_access.load_attachment_text, uid, mid, aid, unit, text, revision),
        'create_research': create,
        'check_research_limit': check_research_limit,
        'check_web_search_limit': check_web_search_limit,
        'user_id': user_id,
    })
    return context


def track_tool_history(source_state, tool_name, context):
    """Caller resets sourceReadRecorded before each execution, including failed calls."""
    tracked = SOURCE_HISTORY_TOOLS | PUBLIC_HISTORY_TOOLS | READONLY_TOOLS | {'start_research'}
    if tool_name == 'search_transcript':
        covered = source_state.get('requestContextTracked', False) or source_state.get('clientInputReceived', False)
    elif tool_name in SOURCE_HISTORY_TOOLS:
        covered = context.get('sourceReadRecorded', False)
    else:
        covered = tool_name in tracked
    if not covered:
        source_state['replayable'] = False
