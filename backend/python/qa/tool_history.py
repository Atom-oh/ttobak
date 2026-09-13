"""Bounded, current-user replay checks for an explicit readonly tool allowlist."""
from dataclasses import dataclass
from decimal import Decimal
import hashlib

from source_revision import HEX_REVISION, IDENTIFIER

READONLY_TOOLS = frozenset(('list_meetings', 'list_accounts', 'get_account_insights', 'get_account_brief'))
MAX_TOOL_DEPENDENCIES = 16
MAX_RESULT_BYTES = 1024 * 1024
MAX_NODES = 16384
MAX_DEPTH = 12
INSIGHT_TYPES = frozenset(('trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action'))


class HistoryLimit(ValueError):
    """Bookkeeping limits never turn a valid current read into missing data."""


@dataclass(frozen=True)
class CompleteRead:
    """Only strict, freshly authorized readers may attest a complete result."""
    value: object


def _snapshot(value):
    """Typed framing preserves list order and bool/number/null distinctions."""
    output, ancestors = bytearray(), set()
    nodes = 0

    def emit(data):
        if len(output) + len(data) > MAX_RESULT_BYTES:
            raise HistoryLimit('Tool result exceeds history byte limit')
        output.extend(data)

    def text(value):
        if len(value) > MAX_RESULT_BYTES:
            raise HistoryLimit('Tool text exceeds history byte limit')
        data = value.encode('utf-8', errors='strict')
        emit(str(len(data)).encode() + b':' + data)

    def visit(item, depth):
        nonlocal nodes
        nodes += 1
        if depth > MAX_DEPTH or nodes > MAX_NODES:
            raise HistoryLimit('Tool result exceeds history structure limit')
        if item is None:
            emit(b'n')
        elif type(item) is bool:
            emit(b't' if item else b'f')
        elif type(item) is str:
            emit(b's')
            text(item)
        elif type(item) is int or type(item) is Decimal:
            if type(item) is int and item.bit_length() > 127:
                raise ValueError('Tool number exceeds DynamoDB precision')
            number = Decimal(item)
            if not number.is_finite():
                raise ValueError('Tool number must be finite')
            sign, digits, exponent = number.as_tuple()
            if len(digits) > 38 or abs(exponent) > 165:
                raise ValueError('Tool number exceeds history precision/range')
            digits = ''.join(str(digit) for digit in digits)
            if not any(digit != '0' for digit in digits):
                normalized = '0'
            else:
                stripped = digits.rstrip('0')
                normalized = ('-' if sign else '') + stripped + 'e' + str(exponent + len(digits) - len(stripped))
            emit(b'd')
            text(normalized)
        elif type(item) in (dict, list):
            if id(item) in ancestors:
                raise ValueError('Invalid tool result structure')
            if len(item) > MAX_NODES:
                raise HistoryLimit('Tool result exceeds history structure limit')
            ancestors.add(id(item))
            try:
                if type(item) is dict:
                    if not all(type(key) is str for key in item):
                        raise ValueError('Tool result keys must be strings')
                    emit(b'm' + str(len(item)).encode() + b':')
                    result = {}
                    for key in sorted(item):
                        text(key)
                        result[key] = visit(item[key], depth + 1)
                else:
                    emit(b'l' + str(len(item)).encode() + b':')
                    result = [visit(child, depth + 1) for child in item]
                return result
            finally:
                ancestors.remove(id(item))
        else:
            raise ValueError('Unsupported tool result type')
        return item

    copied = visit(value, 0)
    return copied, bytes(output)


def fingerprint(value):
    return hashlib.sha256(b'tool-history-v1\0' + _snapshot(value)[1]).hexdigest()


def _user(user_id):
    if type(user_id) is not str or not IDENTIFIER.fullmatch(user_id):
        raise ValueError('Current authenticated user is required')
    return user_id


def _string(value, maximum, *, required=False):
    if type(value) is not str or len(value) > maximum or len(value.encode()) > maximum:
        raise ValueError('Invalid bounded tool string')
    if required and not value.strip():
        raise ValueError('Required tool string is empty')
    return value


def normalize_input(name, arguments):
    fields = {
        'list_meetings': {'dateFrom', 'dateTo', 'tag', 'keyword', 'limit'},
        'list_accounts': set(),
        'get_account_insights': {'account', 'from', 'to', 'types'},
        'get_account_brief': {'account'},
    }
    if name not in fields or type(arguments) is not dict or set(arguments) - fields[name]:
        raise ValueError('Unsupported readonly tool or input field')
    normalized = {}
    for key, value in arguments.items():
        if value is None:
            continue
        if key == 'limit':
            if type(value) is Decimal:
                if not value.is_finite() or value != value.to_integral_value() or not 1 <= value <= 100:
                    raise ValueError('Invalid meeting limit')
                value = int(value)
            if type(value) is not int or not 1 <= value <= 100:
                raise ValueError('Invalid meeting limit')
            normalized[key] = value
        elif key == 'types':
            if (type(value) is not list or len(value) > len(INSIGHT_TYPES)
                    or any(type(item) is not str or item not in INSIGHT_TYPES for item in value)):
                raise ValueError('Invalid insight types')
            normalized[key] = list(value)
        else:
            maximum = 800 if key == 'keyword' else 1024 if key == 'account' else 128
            normalized[key] = _string(value, maximum, required=key == 'account')
    if name == 'list_meetings':
        normalized.setdefault('limit', 20)
    if name in ('get_account_insights', 'get_account_brief') and 'account' not in normalized:
        raise ValueError('Account selector is required')
    return normalized


def is_tool_dependency(dependency):
    return isinstance(dependency, dict) and (
        'readOnlyTool' in dependency or 'researchReceipt' in dependency)


def valid_tool_dependency(dependency):
    try:
        if not isinstance(dependency, dict):
            return False
        _user(dependency.get('userId'))
        revision = dependency.get('sourceRevision')
        if type(revision) is not str or not HEX_REVISION.fullmatch(revision):
            return False
        if 'readOnlyTool' in dependency:
            return (set(dependency) == {'readOnlyTool', 'toolInput', 'userId', 'sourceRevision'}
                    and normalize_input(dependency['readOnlyTool'], dependency['toolInput']) == dependency['toolInput'])
        if set(dependency) != {'researchReceipt', 'userId', 'sourceRevision'}:
            return False
        receipt = dependency['researchReceipt']
        if type(receipt) is not dict or set(receipt) != {'topic', 'mode', 'researchId'}:
            return False
        _string(receipt['topic'], 4000, required=True)
        if receipt['mode'] not in ('quick', 'standard', 'deep'):
            return False
        _user(receipt['researchId'])
        return revision == fingerprint(['research-receipt-v1', dependency['userId'], receipt])
    except (ValueError, TypeError, KeyError, UnicodeError):
        return False


def tool_dependency_key(dependency):
    if 'readOnlyTool' in dependency:
        return ('readonly-tool', dependency['userId'], dependency['readOnlyTool'],
                fingerprint(dependency['toolInput']))
    return ('research-receipt', dependency['userId'], dependency['researchReceipt']['researchId'])


def _public_view(name, value):
    """Only fields consumed by existing formatters, plus stable identity fields."""
    def pick(item, fields):
        if type(item) is not dict:
            raise ValueError('Invalid readonly result row')
        return {field: list(item[field]) if type(item[field]) is list else item[field]
                for field in fields if field in item}

    def rows(items, fields):
        if type(items) is not list:
            raise ValueError('Invalid readonly result collection')
        return [pick(item, fields) for item in items]

    if name == 'list_meetings':
        return rows(value, ('meetingId', 'title', 'date', 'tags', 'status', 'isShared', 'sharedBy'))
    if name == 'list_accounts':
        return rows(value, ('accountId', 'name', 'role'))
    result = pick(value, ('account', 'accountId'))
    if name == 'get_account_insights':
        result['insights'] = rows(value['insights'], ('insightId', 'type', 'text', 'occurredAt', 'entities'))
        return result
    if 'industry' in value:
        result['industry'] = value['industry']
    result['insightsByType'] = {key: rows(items, ('insightId', 'text'))
                               for key, items in value['insightsByType'].items()}
    result['meetings'] = rows(value['meetings'], ('meetingId', 'title', 'date'))
    result['research'] = rows(value['research'], ('researchId', 'topic', 'summary', 'status'))
    for research in result['research']:
        if type(research.get('summary')) is str:
            research['summary'] = research['summary'][:200]  # format_account_brief's actual visible extent.
    return result


def _research_input(arguments):
    """Match create_research_from_chat's effective topic and mode."""
    if type(arguments) is not dict or set(arguments) - {'topic', 'mode'}:
        raise ValueError('Invalid research receipt input')
    topic = arguments.get('topic')
    if type(topic) is not str:
        raise ValueError('Research topic must be text')
    topic = _string(topic.strip()[:500], 2000, required=True)
    mode = arguments.get('mode', 'standard')
    if mode not in ('quick', 'standard', 'deep'):
        mode = 'standard'
    return {'topic': topic, 'mode': mode}


def covers_tool_calls(messages, dependencies, *, source_covered_tools=(), public_tools=()):
    """A replayable flag cannot substitute for a tracked read/creation receipt."""
    try:
        for names in (source_covered_tools, public_tools):
            if (type(names) not in (tuple, list, set, frozenset)
                    or any(type(name) is not str for name in names)):
                return False
        if set(source_covered_tools) & set(public_tools):
            return False
        for message in messages:
            for block in message['content']:
                if type(block) is not dict:
                    return False
                if 'toolUse' not in block:
                    continue
                tool = block['toolUse']
                if type(tool) is not dict or type(tool.get('name')) is not str:
                    return False
                name, arguments = tool['name'], tool.get('input')
                if name in READONLY_TOOLS:
                    arguments = normalize_input(name, arguments)
                    if not any(dep.get('readOnlyTool') == name and dep['toolInput'] == arguments for dep in dependencies):
                        return False
                elif name == 'start_research':
                    arguments = _research_input(arguments)
                    if not any('researchReceipt' in dep
                               and dep['researchReceipt']['topic'] == arguments['topic']
                               and dep['researchReceipt']['mode'] == arguments['mode']
                               for dep in dependencies):
                        return False
                elif name in source_covered_tools:
                    if not any(not is_tool_dependency(dep) for dep in dependencies):
                        return False
                elif name not in public_tools:
                    return False
        return True
    except (ValueError, TypeError, KeyError):
        return False


class ToolHistory:
    """Callbacks are trusted adapters; dependency data can never select a mutation."""
    def __init__(self, user_id, readers):
        self.user_id = _user(user_id)
        if type(readers) is not dict or set(readers) - READONLY_TOOLS or not all(callable(fn) for fn in readers.values()):
            raise ValueError('Only explicit readonly callbacks may be registered')
        self._readers = dict(readers)

    def callbacks(self, state):
        """One context.update() wires the existing tools' callback signatures."""
        def read(user_id, name, arguments):
            if user_id != self.user_id:
                state['replayable'] = False
                raise ValueError('Readonly callback user differs from current user')
            return self.read(state, name, arguments)
        return {
            'list_meetings': lambda user_id, date_from=None, date_to=None, tag=None, keyword=None, limit=None: read(
                user_id, 'list_meetings', {'dateFrom': date_from, 'dateTo': date_to, 'tag': tag,
                                         'keyword': keyword, 'limit': limit}),
            'list_accounts': lambda user_id: read(user_id, 'list_accounts', {}),
            'get_account_insights': lambda user_id, account_query, date_from=None, date_to=None, types=None: read(
                user_id, 'get_account_insights', {'account': account_query, 'from': date_from,
                                                'to': date_to, 'types': types}),
            'get_account_brief': lambda user_id, account_query: read(
                user_id, 'get_account_brief', {'account': account_query}),
        }

    def _value(self, name, arguments):
        callback = self._readers.get(name)
        if callback is None:
            raise ValueError('Readonly callback is unavailable')
        if name == 'list_meetings':
            kwargs = {target: arguments.get(source) for source, target in (
                ('dateFrom', 'date_from'), ('dateTo', 'date_to'), ('tag', 'tag'),
                ('keyword', 'keyword'), ('limit', 'limit'))}
        elif name == 'list_accounts':
            kwargs = {}
        else:
            kwargs = {'account_query': arguments['account']}
            if name == 'get_account_insights':
                kwargs.update(date_from=arguments.get('from'), date_to=arguments.get('to'),
                              types=copy_input(arguments.get('types')))
        result = callback(self.user_id, **kwargs)
        if not isinstance(result, CompleteRead):
            raise ValueError('Readonly callback did not attest a complete authorized read')
        value = result.value
        if name in ('list_meetings', 'list_accounts'):
            valid = type(value) is list
        else:
            valid = type(value) is dict and 'error' not in value and type(value.get('account')) is str
            if name == 'get_account_insights':
                valid = valid and type(value.get('insights')) is list
            else:
                valid = (valid and type(value.get('insightsByType')) is dict
                         and type(value.get('meetings')) is list and type(value.get('research')) is list)
        if not valid:
            raise ValueError('Readonly callback returned an invalid/error result')
        return _public_view(name, value)

    @staticmethod
    def _capacity(state, key):
        from session_provenance import MAX_SESSION_DEPENDENCIES
        dependencies = [dep for dep in state['dependencies'] if is_tool_dependency(dep)]
        if ((len(dependencies) >= MAX_TOOL_DEPENDENCIES or len(state['dependencies']) >= MAX_SESSION_DEPENDENCIES)
                and not any(tool_dependency_key(dep) == key for dep in dependencies)):
            raise HistoryLimit('Readonly history dependency budget exceeded')

    @staticmethod
    def _untracked(state, name, reason):
        state['replayable'] = False
        coverage = state.setdefault('toolHistoryCoverage', [])
        entry = {'tool': name, 'complete': False, 'reason': reason}
        if entry not in coverage:
            coverage.append(entry)

    def read(self, state, name, arguments):
        from session_provenance import remember_source
        try:
            arguments = normalize_input(name, arguments)
            dependency = {'readOnlyTool': name, 'toolInput': arguments, 'userId': self.user_id,
                          'sourceRevision': '0' * 64}
            value = self._value(name, arguments)
            try:
                self._capacity(state, tool_dependency_key(dependency))
            except HistoryLimit:
                self._untracked(state, name, 'DEPENDENCY_LIMIT')
                return value
            try:
                dependency['sourceRevision'] = fingerprint(['readonly-tool-v1', self.user_id, name, arguments, value])
            except HistoryLimit:
                self._untracked(state, name, 'RESULT_LIMIT')
                return value
            remember_source(state, dependency)
            return value
        except Exception:
            state['replayable'] = False
            raise

    def research_receipt(self, state, arguments, result):
        from session_provenance import remember_source
        # Establish success before any optional bookkeeping. Never request a retry
        # of a completed creation merely because its receipt cannot be retained.
        try:
            if type(result) is not dict or 'error' in result:
                raise ValueError('Only a successful creation ID can become a receipt')
            receipt = {'researchId': _user(result.get('researchId'))}
        except ValueError:
            state['replayable'] = False
            raise
        try:
            if set(result) != {'researchId'}:
                raise ValueError('Unexpected creation result fields')
            receipt.update(_research_input(arguments))
            dependency = {'researchReceipt': receipt, 'userId': self.user_id,
                          'sourceRevision': fingerprint(['research-receipt-v1', self.user_id, receipt])}
            if not valid_tool_dependency(dependency):
                raise ValueError('Invalid research receipt')
            self._capacity(state, tool_dependency_key(dependency))
            remember_source(state, dependency)
        except HistoryLimit:
            self._untracked(state, 'start_research', 'DEPENDENCY_LIMIT')
        except Exception:
            self._untracked(state, 'start_research', 'RECEIPT_UNAVAILABLE')
        return dict(receipt)

    def is_current(self, dependency):
        if not valid_tool_dependency(dependency) or dependency['userId'] != self.user_id:
            return False
        if 'researchReceipt' in dependency:
            return True  # Immutable creation receipt; never read mutable research or invoke creation.
        try:
            name = dependency['readOnlyTool']
            arguments = normalize_input(name, dependency['toolInput'])
            value = self._value(name, arguments)
            return dependency['sourceRevision'] == fingerprint(['readonly-tool-v1', self.user_id, name, arguments, value])
        except Exception:
            return False  # Read/auth errors invalidate the entire prior conversation.


def copy_input(value):
    return list(value) if value is not None else None
